// Package telegram — транспорт: превращает update от Telegram в команду
// воронки и отправляет ответ обратно.
//
// Здесь нет решений о сценарии. Если появляется «а вот в этом случае
// покажем другое» — это ошибка слоя, такое живёт в funnel.
package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// Update и вложенные типы — только те поля, которые бот действительно
// читает. Telegram присылает намного больше; расширять по мере надобности.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
	// ChatMember — кто-то вошёл в канал или вышел из него. Приходит
	// только пока бот админ канала и только если тип update запрошен явно
	// в setWebhook: по умолчанию Telegram его не шлёт.
	ChatMember *ChatMemberUpdated `json:"chat_member"`
	// MyChatMember — сменился статус самого бота. Так видно, что бота
	// сняли с админов и замер канала кончился.
	MyChatMember *ChatMemberUpdated `json:"my_chat_member"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	// Username есть у публичного канала и у человека с ником.
	Username string `json:"username"`
}

// private — личная переписка. Один и тот же my_chat_member означает в ней
// совсем не то, что в канале: там это доступ бота к замеру, здесь —
// блокировка, после которой человеку нельзя написать ничего.
func (c Chat) private() bool { return c.Type == "private" }

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// ChatMemberUpdated — изменение статуса участника. Прежний статус
// Telegram присылает вместе с новым, поэтому «вошёл» и «вышел»
// различаются без обращения к базе.
type ChatMemberUpdated struct {
	Chat Chat `json:"chat"`
	// From — кто вызвал изменение. В личке это сам человек: он и есть
	// тот, кто заблокировал бота.
	From *User `json:"from"`
	// Date — когда это случилось, в секундах эпохи. Берём его, а не
	// «сейчас»: между событием и обработкой может лежать простой, и тогда
	// весь прирост уедет на день, когда бот поднялся.
	Date          int64           `json:"date"`
	OldChatMember ChatMember      `json:"old_chat_member"`
	NewChatMember ChatMember      `json:"new_chat_member"`
	InviteLink    *ChatInviteLink `json:"invite_link"`
}

type ChatMember struct {
	Status string `json:"status"`
	User   *User  `json:"user"`
}

// ChatInviteLink — по какой ссылке человек вошёл. Бот таких ссылок не
// создаёт, но именные ссылки можно завести руками в настройках канала, и
// тогда источник подписки становится известен без всякого бота.
type ChatInviteLink struct {
	InviteLink string `json:"invite_link"`
	Name       string `json:"name"`
}

func (u User) toFunnel() funnel.User {
	return funnel.User{
		TelegramID: u.ID,
		Username:   u.Username,
		FirstName:  u.FirstName,
	}
}

func (c Chat) toChannel() channel.Chat {
	return channel.Chat{ID: c.ID, Username: c.Username}
}

// toChannel собирает изменение подписки. Человек берётся из
// new_chat_member, а не из from: подписку может снять и админ канала, и
// тогда from — это он, а не тот, кого это касается.
func (m ChatMemberUpdated) toChannel(updateID int64) channel.MemberUpdate {
	out := channel.MemberUpdate{
		UpdateID:  updateID,
		Chat:      m.Chat.toChannel(),
		OldStatus: m.OldChatMember.Status,
		NewStatus: m.NewChatMember.Status,
	}
	if m.NewChatMember.User != nil {
		out.Member = channel.Member{
			TelegramID: m.NewChatMember.User.ID,
			Username:   m.NewChatMember.User.Username,
			FirstName:  m.NewChatMember.User.FirstName,
		}
	}
	if m.InviteLink != nil {
		// Имя ссылки полезнее самого адреса: в отчёте нужна метка, а не
		// строка t.me/+…, по которой ничего не понять.
		out.InviteLink = m.InviteLink.Name
		if out.InviteLink == "" {
			out.InviteLink = m.InviteLink.InviteLink
		}
	}
	if m.Date > 0 {
		out.At = time.Unix(m.Date, 0).UTC()
	}
	return out
}

// startPayload разбирает команду /start. Второй результат false означает,
// что это не /start.
//
// Telegram присылает команду как «/start», «/start abc» или
// «/start@my_bot abc» — последнее в группах.
func startPayload(text string) (payload string, ok bool) {
	command, rest, _ := strings.Cut(strings.TrimSpace(text), " ")
	name, _, _ := strings.Cut(command, "@")
	if name != "/start" {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Действия кодируются в callback_data как «take:metod-6x5». Лимит
// Telegram — 64 байта, идентификаторы материалов короткие.
const (
	actionOther    = "other"
	actionStage    = "stage"
	actionWaitlist = "waitlist"
)

func encodeAction(a funnel.Action) (string, error) {
	switch a.Kind {
	case funnel.ActionOther:
		return actionOther + ":" + a.MaterialID, nil
	case funnel.ActionStage:
		return actionStage + ":" + a.Stage.String(), nil
	case funnel.ActionWaitlist:
		return actionWaitlist + ":me", nil
	case funnel.ActionNone:
		return "", fmt.Errorf("button without action and without url")
	default:
		return "", fmt.Errorf("unsupported action kind %d", a.Kind)
	}
}

func decodeAction(data string) (funnel.Action, error) {
	kind, value, found := strings.Cut(data, ":")
	if !found || value == "" {
		return funnel.Action{}, fmt.Errorf("malformed callback data %q", data)
	}

	switch kind {
	case actionOther:
		return funnel.Action{Kind: funnel.ActionOther, MaterialID: value}, nil
	case actionStage:
		stage, ok := funnel.ParseStage(value)
		if !ok {
			return funnel.Action{}, fmt.Errorf("unknown stage %q", value)
		}
		return funnel.Action{Kind: funnel.ActionStage, Stage: stage}, nil
	case actionWaitlist:
		return funnel.Action{Kind: funnel.ActionWaitlist}, nil
	default:
		return funnel.Action{}, fmt.Errorf("unknown callback action %q", kind)
	}
}
