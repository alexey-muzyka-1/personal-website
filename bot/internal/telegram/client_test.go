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
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
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
	updates, ok := rec.body["allowed_updates"].([]any)
	if !ok || len(updates) != 2 {
		t.Fatalf("want message and callback_query only, got %v", rec.body["allowed_updates"])
	}
}

func TestNewClientRequiresToken(t *testing.T) {
	if _, err := telegram.NewClient(""); err == nil {
		t.Error("want error for an empty token")
	}
}
