// Package telegram — транспорт: превращает update от Telegram в команду
// воронки и отправляет ответ обратно.
//
// Здесь нет решений о сценарии. Если появляется «а вот в этом случае
// покажем другое» — это ошибка слоя, такое живёт в funnel.
package telegram

import (
	"fmt"
	"strings"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// Update и вложенные типы — только те поля, которые бот действительно
// читает. Telegram присылает намного больше; расширять по мере надобности.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
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
	ID int64 `json:"id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

func (u User) toFunnel() funnel.User {
	return funnel.User{
		TelegramID: u.ID,
		Username:   u.Username,
		FirstName:  u.FirstName,
	}
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
	actionTake  = "take"
	actionOther = "other"
	actionRole  = "role"
)

func encodeAction(a funnel.Action) (string, error) {
	switch a.Kind {
	case funnel.ActionTake:
		return actionTake + ":" + a.MaterialID, nil
	case funnel.ActionOther:
		return actionOther + ":" + a.MaterialID, nil
	case funnel.ActionRole:
		return actionRole + ":" + a.Role.String(), nil
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
	case actionTake:
		return funnel.Action{Kind: funnel.ActionTake, MaterialID: value}, nil
	case actionOther:
		return funnel.Action{Kind: funnel.ActionOther, MaterialID: value}, nil
	case actionRole:
		role, ok := funnel.ParseRole(value)
		if !ok {
			return funnel.Action{}, fmt.Errorf("unknown role %q", value)
		}
		return funnel.Action{Kind: funnel.ActionRole, Role: role}, nil
	default:
		return funnel.Action{}, fmt.Errorf("unknown callback action %q", kind)
	}
}
