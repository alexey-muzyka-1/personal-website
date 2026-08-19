package telegram_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/telegram"
)

const testSecret = "s3cret"

type fakeScenario struct {
	starts       []funnel.StartCommand
	chooses      []funnel.ChooseCommand
	alternatives []funnel.AlternativeCommand
	qualifies    []funnel.QualifyCommand
	reply        funnel.Reply
	err          error
}

func (f *fakeScenario) Qualify(_ context.Context, cmd funnel.QualifyCommand) (funnel.Reply, error) {
	f.qualifies = append(f.qualifies, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) Start(_ context.Context, cmd funnel.StartCommand) (funnel.Reply, error) {
	f.starts = append(f.starts, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) Choose(_ context.Context, cmd funnel.ChooseCommand) (funnel.Reply, error) {
	f.chooses = append(f.chooses, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) Alternative(_ context.Context, cmd funnel.AlternativeCommand) (funnel.Reply, error) {
	f.alternatives = append(f.alternatives, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) calls() int {
	return len(f.starts) + len(f.chooses) + len(f.alternatives) + len(f.qualifies)
}

// routedMaterial — материал, до которого доехала команда, независимо от
// того, в какую ветку она ушла.
func routedMaterial(f *fakeScenario) string {
	switch {
	case len(f.chooses) > 0:
		return f.chooses[0].MaterialID
	case len(f.alternatives) > 0:
		return f.alternatives[0].CurrentMaterialID
	default:
		return ""
	}
}

type sentMessage struct {
	chatID int64
	reply  funnel.Reply
}

type fakeSender struct {
	sent     []sentMessage
	answered []string
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, reply funnel.Reply) error {
	f.sent = append(f.sent, sentMessage{chatID: chatID, reply: reply})
	return nil
}

func (f *fakeSender) AnswerCallback(_ context.Context, callbackID string) error {
	f.answered = append(f.answered, callbackID)
	return nil
}

func newHandler(t *testing.T, scenario telegram.Scenario, sender telegram.Sender) http.Handler {
	t.Helper()

	// Логи обработчика в выводе тестов только мешают.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	h, err := telegram.NewHandler(scenario, sender, testSecret, quiet)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func post(t *testing.T, h http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/telegram/webhook", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRejectsWrongSecret(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":1,"message":{"chat":{"id":10},"from":{"id":10},"text":"/start"}}`
	rec := post(t, h, "wrong", body)

	if rec.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", rec.Code)
	}
	if scenario.calls() != 0 {
		t.Error("scenario must not run for an unauthenticated request")
	}
}

func TestWebhookRejectsBrokenJSON(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	rec := post(t, h, testSecret, `{"update_id":`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", rec.Code)
	}
	if scenario.calls() != 0 {
		t.Error("scenario must not run for a broken update")
	}
}

func TestStartCommandReachesFunnel(t *testing.T) {
	tests := map[string]struct {
		text        string
		wantPayload string
	}{
		"с источником":   {text: "/start reel_42", wantPayload: "reel_42"},
		"без источника":  {text: "/start", wantPayload: ""},
		"с именем бота":  {text: "/start@leshabot reel_42", wantPayload: "reel_42"},
		"лишние пробелы": {text: "  /start   reel_42  ", wantPayload: "reel_42"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scenario := &fakeScenario{reply: funnel.Reply{Text: "привет"}}
			sender := &fakeSender{}
			h := newHandler(t, scenario, sender)

			body := `{"update_id":7,"message":{"chat":{"id":99},"from":{"id":55,"username":"anya","first_name":"Аня"},"text":"` + tc.text + `"}}`
			rec := post(t, h, testSecret, body)

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			if len(scenario.starts) != 1 {
				t.Fatalf("Start calls = %d, want 1", len(scenario.starts))
			}

			got := scenario.starts[0]
			if got.Payload != tc.wantPayload {
				t.Errorf("payload = %q, want %q", got.Payload, tc.wantPayload)
			}
			if got.UpdateID != 7 {
				t.Errorf("update id = %d, want 7", got.UpdateID)
			}
			if got.User.TelegramID != 55 || got.User.Username != "anya" || got.User.FirstName != "Аня" {
				t.Errorf("user lost in transport: %+v", got.User)
			}
			if len(sender.sent) != 1 || sender.sent[0].chatID != 99 {
				t.Errorf("reply must go to the chat, got %+v", sender.sent)
			}
		})
	}
}

func TestNonStartMessageIsIgnored(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":8,"message":{"chat":{"id":99},"from":{"id":55},"text":"привет"}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if scenario.calls() != 0 || len(sender.sent) != 0 {
		t.Error("free text is not a funnel step yet")
	}
}

func TestMessageFromBotIsIgnored(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":9,"message":{"chat":{"id":99},"from":{"id":55,"is_bot":true},"text":"/start"}}`
	post(t, h, testSecret, body)

	if scenario.calls() != 0 {
		t.Error("bots must not enter the funnel")
	}
}

func TestCallbackRoutesToChooseAndAlternative(t *testing.T) {
	tests := map[string]struct {
		data           string
		wantChooses    int
		wantAlternat   int
		wantMaterialID string
	}{
		"забрать":     {data: "take:metod-6x5", wantChooses: 1, wantMaterialID: funnel.MaterialMethod6x5},
		"не подходит": {data: "other:metod-6x5", wantAlternat: 1, wantMaterialID: funnel.MaterialMethod6x5},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
			sender := &fakeSender{}
			h := newHandler(t, scenario, sender)

			body := `{"update_id":11,"callback_query":{"id":"cb-1","from":{"id":55},"data":"` + tc.data +
				`","message":{"chat":{"id":99}}}}`
			rec := post(t, h, testSecret, body)

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			if len(scenario.chooses) != tc.wantChooses {
				t.Errorf("Choose calls = %d, want %d", len(scenario.chooses), tc.wantChooses)
			}
			if len(scenario.alternatives) != tc.wantAlternat {
				t.Errorf("Alternative calls = %d, want %d", len(scenario.alternatives), tc.wantAlternat)
			}
			if got := routedMaterial(scenario); got != tc.wantMaterialID {
				t.Errorf("material = %q, want %q", got, tc.wantMaterialID)
			}
			if len(sender.answered) != 1 || sender.answered[0] != "cb-1" {
				t.Errorf("spinner must be stopped, answered = %v", sender.answered)
			}
			if len(sender.sent) != 1 || sender.sent[0].chatID != 99 {
				t.Errorf("reply must go to the chat, got %+v", sender.sent)
			}
		})
	}
}

func TestCallbackWithoutMessageFallsBackToPrivateChat(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":12,"callback_query":{"id":"cb-2","from":{"id":55},"data":"take:metod-6x5"}}`
	post(t, h, testSecret, body)

	if len(sender.sent) != 1 || sender.sent[0].chatID != 55 {
		t.Errorf("want fallback to the user's private chat, got %+v", sender.sent)
	}
}

func TestMalformedCallbackIsAnsweredAndDropped(t *testing.T) {
	for _, data := range []string{"", "garbage", "take:", "unknown:metod-6x5"} {
		t.Run(data, func(t *testing.T) {
			scenario, sender := &fakeScenario{}, &fakeSender{}
			h := newHandler(t, scenario, sender)

			body := `{"update_id":13,"callback_query":{"id":"cb-3","from":{"id":55},"data":"` + data +
				`","message":{"chat":{"id":99}}}}`
			rec := post(t, h, testSecret, body)

			if rec.Code != http.StatusOK {
				t.Errorf("code = %d, want 200", rec.Code)
			}
			if scenario.calls() != 0 {
				t.Error("malformed callback must not reach the funnel")
			}
			if len(sender.answered) != 1 {
				t.Error("spinner must be stopped even for a stale button")
			}
			if len(sender.sent) != 0 {
				t.Error("nothing to say about a stale button")
			}
		})
	}
}

// Ошибка сценария должна возвращаться как 5xx: Telegram повторит доставку,
// а повтор безопасен.
func TestScenarioFailureAsksTelegramToRetry(t *testing.T) {
	scenario := &fakeScenario{err: errors.New("database is down")}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":14,"message":{"chat":{"id":99},"from":{"id":55},"text":"/start reel_1"}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("nothing must be sent when the step was not recorded")
	}
}

func TestSkippedReplySendsNothing(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Skip: true}}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":15,"message":{"chat":{"id":99},"from":{"id":55},"text":"/start"}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("duplicate update must not produce a second message")
	}
}

func TestUnknownUpdateTypeIsIgnored(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	rec := post(t, h, testSecret, `{"update_id":16,"my_chat_member":{"chat":{"id":99}}}`)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if scenario.calls() != 0 {
		t.Error("unrelated update types must be ignored")
	}
}

func TestNewHandlerRequiresSecret(t *testing.T) {
	if _, err := telegram.NewHandler(&fakeScenario{}, &fakeSender{}, "", nil); err == nil {
		t.Error("an open webhook must not be allowed")
	}
}

// Кнопка из старого сообщения указывает на снятый материал. Повторять
// такой update бессмысленно — Telegram должен получить 200.
func TestCallbackToRemovedMaterialIsNotRetried(t *testing.T) {
	scenario := &fakeScenario{err: fmt.Errorf("choose: %w", funnel.ErrUnknownMaterial)}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":17,"callback_query":{"id":"cb-4","from":{"id":55},"data":"take:lesson-gone","message":{"chat":{"id":99}}}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if len(sender.answered) != 1 {
		t.Error("spinner must be stopped")
	}
	if len(sender.sent) != 0 {
		t.Error("nothing to say about a removed material")
	}
}

// Ответ на вопрос про команду доезжает до сценария и гасит индикатор.
func TestRoleCallbackReachesFunnel(t *testing.T) {
	tests := map[string]struct {
		data string
		want funnel.Role
	}{
		"сам":        {data: "role:solo", want: funnel.RoleSolo},
		"с командой": {data: "role:team", want: funnel.RoleTeam},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
			sender := &fakeSender{}
			h := newHandler(t, scenario, sender)

			body := `{"update_id":21,"callback_query":{"id":"cb-9","from":{"id":55},"data":"` + tc.data +
				`","message":{"chat":{"id":99}}}}`
			rec := post(t, h, testSecret, body)

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			if len(scenario.qualifies) != 1 {
				t.Fatalf("Qualify calls = %d, want 1", len(scenario.qualifies))
			}
			if got := scenario.qualifies[0].Role; got != tc.want {
				t.Errorf("role = %v, want %v", got, tc.want)
			}
			if len(sender.answered) != 1 || len(sender.sent) != 1 {
				t.Errorf("want one answer and one message, got %d и %d", len(sender.answered), len(sender.sent))
			}
		})
	}
}

// Чужая роль в callback — устаревшая или подделанная кнопка.
func TestUnknownRoleIsDropped(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":22,"callback_query":{"id":"cb-10","from":{"id":55},"data":"role:boss","message":{"chat":{"id":99}}}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if scenario.calls() != 0 {
		t.Error("unknown role must not reach the funnel")
	}
	if len(sender.answered) != 1 {
		t.Error("spinner must be stopped")
	}
}
