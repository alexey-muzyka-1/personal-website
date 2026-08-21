package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// maxBodyBytes — потолок на тело webhook-запроса. Update от Telegram
// заведомо меньше; всё крупнее — не от Telegram.
const maxBodyBytes = 1 << 20

// Scenario — то, что webhook умеет запускать. Интерфейс на стороне
// потребителя: боевая реализация — *funnel.Funnel, тестовая — фейк рядом.
type Scenario interface {
	Start(ctx context.Context, cmd funnel.StartCommand) (funnel.Reply, error)
	Alternative(ctx context.Context, cmd funnel.AlternativeCommand) (funnel.Reply, error)
	AnswerStage(ctx context.Context, cmd funnel.StageCommand) (funnel.Reply, error)
	JoinWaitlist(ctx context.Context, cmd funnel.JoinWaitlistCommand) (funnel.Reply, error)
	// SetBlocked ничего не отвечает: заблокировавшему нельзя написать, а
	// разблокировавшему нечего сказать, пока он сам не напишет.
	SetBlocked(ctx context.Context, cmd funnel.BlockCommand) error
}

// Members — приём того, что происходит в канале. Интерфейс на стороне
// потребителя: боевая реализация — *channel.Watcher.
type Members interface {
	Apply(ctx context.Context, u channel.MemberUpdate) error
	// BotAccessChanged — сменился статус самого бота в канале.
	BotAccessChanged(chat channel.Chat, status string)
}

// Sender — исходящая часть Bot API.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, reply funnel.Reply) error
	// EditMessage заменяет уже отправленное сообщение вместе с кнопками.
	EditMessage(ctx context.Context, chatID, messageID int64, reply funnel.Reply) error
	AnswerCallback(ctx context.Context, callbackID string) error
}

// Handler — HTTP-обработчик webhook.
type Handler struct {
	scenario Scenario
	sender   Sender
	members  Members
	secret   string
	log      *slog.Logger
}

// HandlerOption — подключаемая часть обработчика.
type HandlerOption func(*Handler)

// WithMembers включает приём событий канала. Не задан — chat_member
// просто игнорируется, как любой ненужный тип update.
func WithMembers(m Members) HandlerOption {
	return func(h *Handler) { h.members = m }
}

// NewHandler. secret — значение, которое Telegram кладёт в заголовок
// X-Telegram-Bot-Api-Secret-Token при setWebhook. Пустой секрет запрещён:
// открытый webhook позволяет кому угодно писать события в базу лидов.
func NewHandler(scenario Scenario, sender Sender, secret string, log *slog.Logger, opts ...HandlerOption) (*Handler, error) {
	if scenario == nil || sender == nil {
		return nil, errors.New("scenario and sender are required")
	}
	if secret == "" {
		return nil, errors.New("webhook secret is required")
	}
	if log == nil {
		log = slog.Default()
	}

	h := &Handler{scenario: scenario, sender: sender, secret: secret, log: log}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != h.secret {
		h.log.Warn("webhook rejected: bad secret token", "remote", r.RemoteAddr)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var update Update
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(body).Decode(&update); err != nil {
		h.log.Warn("webhook rejected: bad json", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Ошибка возвращается как 5xx намеренно: Telegram повторит доставку,
	// а повтор безопасен — единица работы атомарна и дедуплицируется по
	// update_id.
	if err := h.handle(r.Context(), update); err != nil {
		h.log.Error("update failed", "update_id", update.UpdateID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handle(ctx context.Context, update Update) error {
	switch {
	case update.CallbackQuery != nil:
		return h.handleCallback(ctx, update.UpdateID, *update.CallbackQuery)
	case update.Message != nil:
		return h.handleMessage(ctx, update.UpdateID, *update.Message)
	case update.ChatMember != nil:
		return h.handleChatMember(ctx, update.UpdateID, *update.ChatMember)
	case update.MyChatMember != nil:
		return h.handleMyChatMember(ctx, update.UpdateID, *update.MyChatMember)
	default:
		// Прочие типы update (edited_message, channel_post и так далее)
		// воронке пока не нужны.
		return nil
	}
}

// handleChatMember — человек вошёл в канал или вышел из него.
//
// Ошибка возвращается наверх и превращается в 5xx: подписка это шаг
// воронки не хуже нажатой кнопки, и терять её из-за недоступной базы
// нельзя. Telegram повторит доставку, а повтор отсекается по update_id.
func (h *Handler) handleChatMember(ctx context.Context, updateID int64, m ChatMemberUpdated) error {
	if h.members == nil {
		return nil
	}
	if err := h.members.Apply(ctx, m.toChannel(updateID)); err != nil {
		return fmt.Errorf("chat member: %w", err)
	}
	return nil
}

// handleMyChatMember — сменился статус самого бота. Смысл зависит от
// того, где это случилось, и развилка здесь не косметическая: в личке это
// человек, который заблокировал бота, а в канале — бот, которого сняли с
// админов. Без развилки первое молча уезжало в фильтр канала и терялось.
func (h *Handler) handleMyChatMember(ctx context.Context, updateID int64, m ChatMemberUpdated) error {
	if m.Chat.private() {
		return h.handleBlock(ctx, updateID, m)
	}
	if h.members != nil {
		h.members.BotAccessChanged(m.Chat.toChannel(), m.NewChatMember.Status)
	}
	return nil
}

// handleBlock — человек заблокировал бота или вернулся.
//
// Ошибка возвращается наверх: потерянная блокировка не видна никак, а
// стоит она непосланного сообщения тому, кто ждал ответа.
func (h *Handler) handleBlock(ctx context.Context, updateID int64, m ChatMemberUpdated) error {
	if m.From == nil {
		return nil
	}
	// В личке бот может быть только участником или изгнанным. Всё
	// остальное — не про блокировку, и гадать не надо.
	blocked := m.NewChatMember.Status == "kicked"
	if !blocked && m.NewChatMember.Status != "member" {
		return nil
	}

	cmd := funnel.BlockCommand{
		UpdateID: updateID,
		User:     m.From.toFunnel(),
		Blocked:  blocked,
	}
	if err := h.scenario.SetBlocked(ctx, cmd); err != nil {
		return fmt.Errorf("block: %w", err)
	}
	return nil
}

func (h *Handler) handleMessage(ctx context.Context, updateID int64, msg Message) error {
	if msg.From == nil || msg.From.IsBot {
		return nil
	}
	payload, ok := startPayload(msg.Text)
	if !ok {
		// Свободный текст станет входом в анализ Reel на тикете 09.
		// Пока он не является шагом воронки и не пишется в события.
		h.log.Debug("message ignored", "update_id", updateID)
		return nil
	}

	reply, err := h.scenario.Start(ctx, funnel.StartCommand{
		UpdateID: updateID,
		User:     msg.From.toFunnel(),
		Payload:  payload,
	})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return h.send(ctx, msg.Chat.ID, reply)
}

func (h *Handler) handleCallback(ctx context.Context, updateID int64, cb CallbackQuery) error {
	if cb.From == nil {
		return nil
	}
	action, err := decodeAction(cb.Data)
	if err != nil {
		return h.dropStaleButton(ctx, cb, updateID, err)
	}

	user := cb.From.toFunnel()

	var reply funnel.Reply
	switch action.Kind {
	case funnel.ActionOther:
		reply, err = h.scenario.Alternative(ctx, funnel.AlternativeCommand{
			UpdateID:          updateID,
			User:              user,
			CurrentMaterialID: action.MaterialID,
		})
	case funnel.ActionStage:
		reply, err = h.scenario.AnswerStage(ctx, funnel.StageCommand{
			UpdateID: updateID,
			User:     user,
			Stage:    action.Stage,
		})
	case funnel.ActionWaitlist:
		reply, err = h.scenario.JoinWaitlist(ctx, funnel.JoinWaitlistCommand{
			UpdateID: updateID,
			User:     user,
		})
	default:
		return fmt.Errorf("unsupported action kind %d", action.Kind)
	}
	switch {
	case errors.Is(err, funnel.ErrUnknownMaterial):
		// Материал сняли, а кнопка осталась в старом сообщении. Это не
		// сбой: повторять такой update бессмысленно, Telegram должен
		// получить 200 и забыть о нём.
		return h.dropStaleButton(ctx, cb, updateID, err)
	case err != nil:
		return fmt.Errorf("callback %q: %w", cb.Data, err)
	}

	if err := h.sender.AnswerCallback(ctx, cb.ID); err != nil {
		return fmt.Errorf("answering callback: %w", err)
	}
	return h.replace(ctx, cb, reply)
}

// replace — ответ на нажатие заменяет то сообщение, на котором нажали.
//
// Иначе старые кнопки остаются живыми, и по воронке можно прыгать
// бесконечно, плодя сообщения. Правило транспортное: сценарий не знает
// про идентификаторы сообщений.
func (h *Handler) replace(ctx context.Context, cb CallbackQuery, reply funnel.Reply) error {
	if reply.Skip {
		return nil
	}
	chatID := callbackChatID(cb)

	if cb.Message == nil || cb.Message.MessageID == 0 {
		return h.send(ctx, chatID, reply)
	}
	if err := h.sender.EditMessage(ctx, chatID, cb.Message.MessageID, reply); err != nil {
		// Сообщение могло устареть: Telegram не даёт править старше двух
		// суток. Человек не должен остаться без ответа из-за этого.
		h.log.Warn("edit failed, sending a new message", "error", err)
		return h.send(ctx, chatID, reply)
	}
	return nil
}

// dropStaleButton гасит крутящийся индикатор у человека и не трогает
// воронку: иначе интерфейс залипнет, а Telegram будет вечно повторять
// заведомо безнадёжный update.
func (h *Handler) dropStaleButton(ctx context.Context, cb CallbackQuery, updateID int64, cause error) error {
	h.log.Warn("callback ignored", "update_id", updateID, "error", cause)

	if err := h.sender.AnswerCallback(ctx, cb.ID); err != nil {
		return fmt.Errorf("answering callback: %w", err)
	}
	return nil
}

func (h *Handler) send(ctx context.Context, chatID int64, reply funnel.Reply) error {
	if reply.Skip {
		return nil
	}
	if err := h.sender.SendMessage(ctx, chatID, reply); err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	return nil
}

// callbackChatID: у старого сообщения Telegram может не прислать message.
// В личке chat.id совпадает с id пользователя, поэтому запасной вариант
// ведёт в тот же диалог.
func callbackChatID(cb CallbackQuery) int64 {
	if cb.Message != nil && cb.Message.Chat.ID != 0 {
		return cb.Message.Chat.ID
	}
	return cb.From.ID
}
