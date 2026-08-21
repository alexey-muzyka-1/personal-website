package telegram_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/telegram"
)

const testToken = "123456:super-secret-token"

// apiRecorder — поддельный Bot API: запоминает путь и тело запроса.
type apiRecorder struct {
	path string
	body map[string]any
	ok   bool
	// result — сырое тело ответа. Пустое отдаёт `{}`: методам отправки
	// сообщений результат не нужен, а вот вопросам про канал — нужен.
	result string
}

func newAPI(t *testing.T, rec *apiRecorder) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.URL.Path

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request: %v", err)
		}
		if err := json.Unmarshal(raw, &rec.body); err != nil {
			t.Errorf("decoding request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if !rec.ok {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"chat not found"}`)
			return
		}
		result := rec.result
		if result == "" {
			result = `{}`
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":`+result+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newClient(t *testing.T, apiBase string) *telegram.Client {
	t.Helper()

	c, err := telegram.NewClient(testToken, telegram.WithAPIBase(apiBase))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestSendMessageBuildsInlineKeyboard(t *testing.T) {
	rec := &apiRecorder{ok: true}
	client := newClient(t, newAPI(t, rec).URL)

	reply := funnel.Reply{
		Text: "текст",
		Buttons: []funnel.Button{
			{Label: "Открыть разбор", URL: "https://bot.test/r/abc"},
			{Label: "Мне это не подходит", Action: funnel.Action{Kind: funnel.ActionOther, MaterialID: "metod-6x5"}},
		},
	}
	if err := client.SendMessage(context.Background(), 42, reply); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.HasSuffix(rec.path, "/sendMessage") {
		t.Errorf("path = %q, want .../sendMessage", rec.path)
	}
	if got := rec.body["chat_id"]; got != float64(42) {
		t.Errorf("chat_id = %v, want 42", got)
	}
	if got := rec.body["disable_web_page_preview"]; got != true {
		t.Errorf("preview must be disabled, got %v", got)
	}

	rows, ok := rec.body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("want two keyboard rows, got %#v", rec.body["reply_markup"])
	}

	first := rows[0].([]any)[0].(map[string]any)
	if first["url"] != "https://bot.test/r/abc" {
		t.Errorf("url = %v", first["url"])
	}
	if _, leaked := first["callback_data"]; leaked {
		t.Error("link button must not carry callback data")
	}
	second := rows[1].([]any)[0].(map[string]any)
	if second["callback_data"] != "other:metod-6x5" {
		t.Errorf("callback_data = %v, want other:metod-6x5", second["callback_data"])
	}
	// Разметка обязана уезжать в Telegram, иначе теги приедут текстом.
	if got := rec.body["parse_mode"]; got != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", got)
	}
}

func TestSendMessageReportsAPIError(t *testing.T) {
	rec := &apiRecorder{ok: false}
	client := newClient(t, newAPI(t, rec).URL)

	err := client.SendMessage(context.Background(), 42, funnel.Reply{Text: "текст"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error must carry the api description, got %v", err)
	}
}

// Токен не должен всплывать в ошибке: она уедет в логи целиком.
func TestTransportErrorHidesToken(t *testing.T) {
	srv := newAPI(t, &apiRecorder{ok: true})
	client := newClient(t, srv.URL)
	srv.Close()

	err := client.SendMessage(context.Background(), 42, funnel.Reply{Text: "текст"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("token leaked into the error: %v", err)
	}
}

func TestAnswerCallback(t *testing.T) {
	rec := &apiRecorder{ok: true}
	client := newClient(t, newAPI(t, rec).URL)

	if err := client.AnswerCallback(context.Background(), "cb-1"); err != nil {
		t.Fatalf("AnswerCallback: %v", err)
	}
	if !strings.HasSuffix(rec.path, "/answerCallbackQuery") {
		t.Errorf("path = %q", rec.path)
	}
	if got := rec.body["callback_query_id"]; got != "cb-1" {
		t.Errorf("callback_query_id = %v", got)
	}
}

func TestSetWebhookCarriesSecret(t *testing.T) {
	rec := &apiRecorder{ok: true}
	client := newClient(t, newAPI(t, rec).URL)

	err := client.SetWebhook(context.Background(), "https://bot.test/telegram/webhook", "s3cret")
	if err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if got := rec.body["secret_token"]; got != "s3cret" {
		t.Errorf("secret_token = %v", got)
	}
	if got := rec.body["url"]; got != "https://bot.test/telegram/webhook" {
		t.Errorf("url = %v", got)
	}
	// Список типов передаётся целиком и обязан содержать chat_member:
	// Telegram заменяет прежний набор, а без явного упоминания подписки на
	// канал не приходят вовсе — этот тип по умолчанию выключен.
	updates, ok := rec.body["allowed_updates"].([]any)
	if !ok {
		t.Fatalf("allowed_updates = %v", rec.body["allowed_updates"])
	}
	want := map[string]bool{"message": false, "callback_query": false, "chat_member": false, "my_chat_member": false}
	for _, u := range updates {
		name, _ := u.(string)
		if _, expected := want[name]; !expected {
			t.Errorf("лишний тип update: %q", name)
			continue
		}
		want[name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("бот не просит %q", name)
		}
	}

	// Очередь за время простоя — это люди, которые нажали кнопку, пока бот
	// лежал. Сброс её на каждом старте стоил бы лида на каждый деплой.
	if drop, ok := rec.body["drop_pending_updates"].(bool); ok && drop {
		t.Error("накопившиеся update сбрасываются при регистрации webhook")
	}
}

func TestChatMemberCountReadsTheResult(t *testing.T) {
	rec := &apiRecorder{ok: true, result: `658`}
	client := newClient(t, newAPI(t, rec).URL)

	count, err := client.ChatMemberCount(context.Background(), "@alexeymuzykablog")
	if err != nil {
		t.Fatalf("ChatMemberCount: %v", err)
	}
	if count != 658 {
		t.Errorf("подписчиков = %d, хочу 658", count)
	}
	if got := rec.body["chat_id"]; got != "@alexeymuzykablog" {
		t.Errorf("chat_id = %v", got)
	}
}

func TestChatMemberReadsStatusAndUser(t *testing.T) {
	rec := &apiRecorder{ok: true, result: `{"status":"member","user":{"id":763464443,"username":"akhmadullintf","first_name":"Тимур"}}`}
	client := newClient(t, newAPI(t, rec).URL)

	member, status, err := client.ChatMember(context.Background(), "@alexeymuzykablog", 763464443)
	if err != nil {
		t.Fatalf("ChatMember: %v", err)
	}
	if status != "member" {
		t.Errorf("статус = %q", status)
	}
	if member.TelegramID != 763464443 || member.Username != "akhmadullintf" || member.FirstName != "Тимур" {
		t.Errorf("человек собран не полностью: %+v", member)
	}
}

// Ответ без result — это не пустой ответ, а сломанный. Молча вернуть ноль
// подписчиков значит нарисовать обвал канала на ровном месте.
func TestChatMemberCountFailsWithoutAResult(t *testing.T) {
	rec := &apiRecorder{ok: true}
	client := newClient(t, newAPI(t, rec).URL)

	if _, err := client.ChatMemberCount(context.Background(), "@channel"); err == nil {
		t.Error("want error for a response without result")
	}
}

func TestNewClientRequiresToken(t *testing.T) {
	if _, err := telegram.NewClient(""); err == nil {
		t.Error("want error for an empty token")
	}
}
