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
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/telegram"
)

const testSecret = "s3cret"

type fakeScenario struct {
	starts       []funnel.StartCommand
	alternatives []funnel.AlternativeCommand
	stages       []funnel.StageCommand
	waitlists    []funnel.JoinWaitlistCommand
	blocks       []funnel.BlockCommand
	reply        funnel.Reply
	err          error
}

func (f *fakeScenario) SetBlocked(_ context.Context, cmd funnel.BlockCommand) error {
	if f.err != nil {
		return f.err
	}
	f.blocks = append(f.blocks, cmd)
	return nil
}

func (f *fakeScenario) AnswerStage(_ context.Context, cmd funnel.StageCommand) (funnel.Reply, error) {
	f.stages = append(f.stages, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) JoinWaitlist(_ context.Context, cmd funnel.JoinWaitlistCommand) (funnel.Reply, error) {
	f.waitlists = append(f.waitlists, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) Start(_ context.Context, cmd funnel.StartCommand) (funnel.Reply, error) {
	f.starts = append(f.starts, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) Alternative(_ context.Context, cmd funnel.AlternativeCommand) (funnel.Reply, error) {
	f.alternatives = append(f.alternatives, cmd)
	return f.reply, f.err
}

func (f *fakeScenario) calls() int {
	return len(f.starts) + len(f.alternatives) + len(f.stages) + len(f.waitlists) + len(f.blocks)
}

type sentMessage struct {
	chatID int64
	reply  funnel.Reply
}

type editedMessage struct {
	chatID    int64
	messageID int64
	reply     funnel.Reply
}

type fakeSender struct {
	sent     []sentMessage
	edited   []editedMessage
	answered []string
	editErr  error
}

func (f *fakeSender) EditMessage(_ context.Context, chatID, messageID int64, reply funnel.Reply) error {
	if f.editErr != nil {
		return f.editErr
	}
	f.edited = append(f.edited, editedMessage{chatID: chatID, messageID: messageID, reply: reply})
	return nil
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, reply funnel.Reply) error {
	f.sent = append(f.sent, sentMessage{chatID: chatID, reply: reply})
	return nil
}

func (f *fakeSender) AnswerCallback(_ context.Context, callbackID string) error {
	f.answered = append(f.answered, callbackID)
	return nil
}

// fakeMembers — приёмник событий канала.
type fakeMembers struct {
	applied []channel.MemberUpdate
	access  []string
	err     error
}

func (f *fakeMembers) Apply(_ context.Context, u channel.MemberUpdate) error {
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, u)
	return nil
}

func (f *fakeMembers) BotAccessChanged(_ channel.Chat, status string) {
	f.access = append(f.access, status)
}

func newHandler(t *testing.T, scenario telegram.Scenario, sender telegram.Sender, opts ...telegram.HandlerOption) http.Handler {
	t.Helper()

	// Логи обработчика в выводе тестов только мешают.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	h, err := telegram.NewHandler(scenario, sender, testSecret, quiet, opts...)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// Подписка на канал приходит отдельным типом update и не должна ни
// задевать сценарий, ни отправлять человеку сообщение: он подписался, а
// не написал боту.
func TestChannelJoinReachesMembers(t *testing.T) {
	scenario, sender, members := &fakeScenario{}, &fakeSender{}, &fakeMembers{}
	h := newHandler(t, scenario, sender, telegram.WithMembers(members))

	body := `{"update_id":20,"chat_member":{
		"chat":{"id":-1001234567890,"username":"alexeymuzykablog"},
		"date":1787000000,
		"old_chat_member":{"status":"left","user":{"id":55}},
		"new_chat_member":{"status":"member","user":{"id":55,"username":"akhmadullintf","first_name":"Тимур"}},
		"invite_link":{"invite_link":"https://t.me/+abc","name":"reel_razbor"}}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(members.applied) != 1 {
		t.Fatalf("подписка не доехала: %+v", members.applied)
	}
	got := members.applied[0]
	if got.UpdateID != 20 || got.Member.TelegramID != 55 || got.NewStatus != "member" {
		t.Errorf("событие собрано неверно: %+v", got)
	}
	if got.Member.Username != "akhmadullintf" || got.Member.FirstName != "Тимур" {
		t.Errorf("человек собран не полностью: %+v", got.Member)
	}
	if got.Chat.Username != "alexeymuzykablog" || got.Chat.ID != -1001234567890 {
		t.Errorf("канал собран неверно: %+v", got.Chat)
	}
	// Имя ссылки полезнее адреса: в отчёте нужна метка, а не t.me/+abc.
	if got.InviteLink != "reel_razbor" {
		t.Errorf("ссылка = %q, хочу имя ссылки", got.InviteLink)
	}
	if !got.At.Equal(time.Unix(1787000000, 0).UTC()) {
		t.Errorf("время = %v, хочу время из update", got.At)
	}
	if scenario.calls() != 0 || len(sender.sent) != 0 {
		t.Error("подписка на канал не должна ни трогать сценарий, ни писать человеку")
	}
}

// Человек в chat_member берётся из new_chat_member, а не из from: подписку
// может снять админ канала, и тогда from — это он.
func TestKickedPersonIsTheOneInNewChatMember(t *testing.T) {
	members := &fakeMembers{}
	h := newHandler(t, &fakeScenario{}, &fakeSender{}, telegram.WithMembers(members))

	body := `{"update_id":21,"chat_member":{
		"chat":{"id":-1001234567890},
		"from":{"id":1,"username":"lesha"},
		"old_chat_member":{"status":"member","user":{"id":77}},
		"new_chat_member":{"status":"kicked","user":{"id":77}}}}`
	post(t, h, testSecret, body)

	if len(members.applied) != 1 {
		t.Fatalf("событие потеряно")
	}
	if got := members.applied[0].Member.TelegramID; got != 77 {
		t.Errorf("человек = %d, хочу того, кого исключили", got)
	}
}

// Недоступная база на подписке — это 5xx: Telegram повторит доставку, а
// повтор отсекается по update_id. Молчаливый 200 потерял бы подписчика.
func TestChannelFailureAsksTelegramToRetry(t *testing.T) {
	members := &fakeMembers{err: errors.New("database is down")}
	h := newHandler(t, &fakeScenario{}, &fakeSender{}, telegram.WithMembers(members))

	body := `{"update_id":22,"chat_member":{"chat":{"id":-100},"new_chat_member":{"status":"member","user":{"id":55}}}}`
	if rec := post(t, h, testSecret, body); rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

// Блокировка приходит тем же типом update, что и снятие бота с админов
// канала. Развилка по типу чата — единственное, что их различает, и без
// неё блокировки молча уезжали в фильтр канала.
func TestBlockInPrivateChatIsRecorded(t *testing.T) {
	scenario, members := &fakeScenario{}, &fakeMembers{}
	h := newHandler(t, scenario, &fakeSender{}, telegram.WithMembers(members))

	body := `{"update_id":30,"my_chat_member":{
		"chat":{"id":55,"type":"private"},
		"from":{"id":55,"username":"akhmadullintf"},
		"old_chat_member":{"status":"member","user":{"id":9}},
		"new_chat_member":{"status":"kicked","user":{"id":9}}}}`
	if rec := post(t, h, testSecret, body); rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	if len(scenario.blocks) != 1 {
		t.Fatalf("блокировка потеряна: %+v", scenario.blocks)
	}
	got := scenario.blocks[0]
	if !got.Blocked || got.User.TelegramID != 55 {
		t.Errorf("блокировка собрана неверно: %+v", got)
	}
	if len(members.access) != 0 {
		t.Error("личный чат уехал в замер канала")
	}
}

func TestUnblockIsRecorded(t *testing.T) {
	scenario := &fakeScenario{}
	h := newHandler(t, scenario, &fakeSender{})

	body := `{"update_id":31,"my_chat_member":{
		"chat":{"id":55,"type":"private"},
		"from":{"id":55},
		"old_chat_member":{"status":"kicked","user":{"id":9}},
		"new_chat_member":{"status":"member","user":{"id":9}}}}`
	post(t, h, testSecret, body)

	if len(scenario.blocks) != 1 || scenario.blocks[0].Blocked {
		t.Errorf("возврат собран неверно: %+v", scenario.blocks)
	}
}

// Потерянная блокировка ничем не видна, поэтому сбой базы здесь тоже
// должен превращаться в повторную доставку.
func TestBlockFailureAsksTelegramToRetry(t *testing.T) {
	scenario := &fakeScenario{err: errors.New("database is down")}
	h := newHandler(t, scenario, &fakeSender{})

	body := `{"update_id":32,"my_chat_member":{"chat":{"id":55,"type":"private"},
		"from":{"id":55},"new_chat_member":{"status":"kicked","user":{"id":9}}}}`
	if rec := post(t, h, testSecret, body); rec.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestBotStatusChangeIsReported(t *testing.T) {
	members := &fakeMembers{}
	scenario := &fakeScenario{}
	h := newHandler(t, scenario, &fakeSender{}, telegram.WithMembers(members))

	body := `{"update_id":23,"my_chat_member":{"chat":{"id":-1001234567890,"type":"channel"},
		"old_chat_member":{"status":"administrator","user":{"id":9}},
		"new_chat_member":{"status":"left","user":{"id":9}}}}`
	post(t, h, testSecret, body)

	if len(scenario.blocks) != 0 {
		t.Error("канал уехал в блокировки")
	}

	if len(members.access) != 1 || members.access[0] != "left" {
		t.Errorf("статус бота не доехал: %v", members.access)
	}
	// Сам бот подписчиком не считается ни при каких обстоятельствах.
	if len(members.applied) != 0 {
		t.Errorf("бот записан в подписчики: %+v", members.applied)
	}
}

// Без настроенного канала chat_member — просто ненужный тип update.
func TestChannelUpdateWithoutAWatcherIsIgnored(t *testing.T) {
	h := newHandler(t, &fakeScenario{}, &fakeSender{})

	body := `{"update_id":24,"chat_member":{"chat":{"id":-100},"new_chat_member":{"status":"member","user":{"id":55}}}}`
	if rec := post(t, h, testSecret, body); rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
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

func TestOtherCallbackReachesFunnel(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":11,"callback_query":{"id":"cb-1","from":{"id":55},"data":"other:metod-6x5","message":{"message_id":500,"chat":{"id":99}}}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if len(scenario.alternatives) != 1 {
		t.Fatalf("Alternative calls = %d, want 1", len(scenario.alternatives))
	}
	if got := scenario.alternatives[0].CurrentMaterialID; got != funnel.MaterialMethod6x5 {
		t.Errorf("material = %q, want %q", got, funnel.MaterialMethod6x5)
	}
	if len(sender.answered) != 1 {
		t.Errorf("spinner must be stopped, answered = %v", sender.answered)
	}
}

// Ответ на нажатие заменяет сообщение, а не добавляет новое: иначе
// старые кнопки живут вечно и по воронке можно прыгать бесконечно.
func TestCallbackReplacesTheMessage(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":12,"callback_query":{"id":"cb-2","from":{"id":55},"data":"stage:not_shipping","message":{"message_id":777,"chat":{"id":99}}}}`
	post(t, h, testSecret, body)

	if len(sender.edited) != 1 {
		t.Fatalf("want the message edited once, got %d", len(sender.edited))
	}
	if sender.edited[0].messageID != 777 || sender.edited[0].chatID != 99 {
		t.Errorf("edited the wrong message: %+v", sender.edited[0])
	}
	if len(sender.sent) != 0 {
		t.Error("nothing new must be sent when the old message can be replaced")
	}
}

// Сообщение старше двух суток Telegram править не даёт. Человек не
// должен остаться без ответа из-за этого.
func TestFailedEditFallsBackToNewMessage(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
	sender := &fakeSender{editErr: errors.New("message is too old")}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":13,"callback_query":{"id":"cb-3","from":{"id":55},"data":"stage:no_signal","message":{"message_id":777,"chat":{"id":99}}}}`
	rec := post(t, h, testSecret, body)

	if rec.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Errorf("want a new message after a failed edit, got %d", len(sender.sent))
	}
}

func TestCallbackWithoutMessageFallsBackToPrivateChat(t *testing.T) {
	scenario := &fakeScenario{reply: funnel.Reply{Text: "ответ"}}
	sender := &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":14,"callback_query":{"id":"cb-4","from":{"id":55},"data":"stage:not_shipping"}}`
	post(t, h, testSecret, body)

	if len(sender.sent) != 1 || sender.sent[0].chatID != 55 {
		t.Errorf("want fallback to the user's private chat, got %+v", sender.sent)
	}
}

func TestMalformedCallbackIsAnsweredAndDropped(t *testing.T) {
	for _, data := range []string{"", "garbage", "other:", "unknown:metod-6x5"} {
		t.Run(data, func(t *testing.T) {
			scenario, sender := &fakeScenario{}, &fakeSender{}
			h := newHandler(t, scenario, sender)

			body := `{"update_id":15,"callback_query":{"id":"cb-5","from":{"id":55},"data":"` + data +
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

	body := `{"update_id":17,"callback_query":{"id":"cb-6","from":{"id":55},"data":"other:lesson-gone","message":{"message_id":1,"chat":{"id":99}}}}`
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

// Ответ про состояние доезжает до сценария и гасит индикатор.
func TestStageCallbackReachesFunnel(t *testing.T) {
	tests := map[string]struct {
		data string
		want funnel.Stage
	}{
		"не выпускает": {data: "stage:not_shipping", want: funnel.StageNotShipping},
		"нет сигнала":  {data: "stage:no_signal", want: funnel.StageNoSignal},
		"другая":       {data: "stage:other", want: funnel.StageOther},
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
			if len(scenario.stages) != 1 {
				t.Fatalf("AnswerStage calls = %d, want 1", len(scenario.stages))
			}
			if got := scenario.stages[0].Stage; got != tc.want {
				t.Errorf("stage = %v, want %v", got, tc.want)
			}
			if len(sender.answered) != 1 || len(sender.sent) != 1 {
				t.Errorf("want one answer and one message, got %d и %d", len(sender.answered), len(sender.sent))
			}
		})
	}
}

// Чужое состояние в callback — устаревшая или подделанная кнопка.
func TestUnknownStageIsDropped(t *testing.T) {
	scenario, sender := &fakeScenario{}, &fakeSender{}
	h := newHandler(t, scenario, sender)

	body := `{"update_id":22,"callback_query":{"id":"cb-10","from":{"id":55},"data":"stage:boss","message":{"chat":{"id":99}}}}`
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
