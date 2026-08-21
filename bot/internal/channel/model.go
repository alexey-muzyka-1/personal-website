// Package channel — вторая половина воронки: кто подписан на канал, когда
// подписался и как это связано с ботом.
//
// Здесь нет ни Telegram, ни SQL: транспорт и хранилище подключаются
// снаружи, как и у funnel. Пакет отвечает на один вопрос — что считать
// изменением подписки и что из этого записать.
//
// Чего здесь нет и быть не может: списка подписчиков. Bot API не отдаёт
// участников канала ни целиком, ни постранично. Всё, что доступно, —
// общее число, статус конкретного известного нам человека и события о
// входах и выходах с того момента, как бот стал админом. Отсюда деление
// на «датированных» и «базу до замера», которое тянется дальше в отчёты.
package channel

import (
	"strconv"
	"strings"
	"time"
)

// Имена событий. Живут в той же плоскости, что события бота: отчёту
// придётся показывать их в одном ряду с bot_started и material_opened.
const (
	EventJoined = "channel_joined"
	EventLeft   = "channel_left"
)

// Chat — канал, про который пришло событие. Telegram присылает и число, и
// имя; мы сверяем по тому, что задано в конфигурации.
type Chat struct {
	ID       int64
	Username string
}

// Member — человек в терминах канала. Имя и username нужны отчёту: без
// них в таблице отписок останется голый числовой id.
type Member struct {
	TelegramID int64
	Username   string
	FirstName  string
}

// MemberUpdate — изменение подписки, как его прислал Telegram.
//
// Прежний статус приходит в самом update, поэтому решение «это вход,
// выход или смена роли» принимается без единого обращения к базе.
type MemberUpdate struct {
	UpdateID   int64
	Chat       Chat
	Member     Member
	OldStatus  string
	NewStatus  string
	InviteLink string
	At         time.Time
}

// Change — то, что нужно записать. Одна плоская команда на оба способа
// узнать о подписке: событие от Telegram и сверка.
type Change struct {
	// UpdateID больше нуля только у событий от Telegram: по нему
	// отсекается повторная доставка. У сверки его нет — она не update.
	UpdateID int64
	Member   Member
	Status   string
	// Event пустой, когда состояние изменилось, но человек как был
	// подписан, так и остался: например, стал администратором.
	Event      string
	InviteLink string
	At         time.Time
	// Noticed — время приблизительное: изменение замечено сверкой, а не
	// получено событием. Человек ушёл когда-то до этой минуты.
	Noticed bool
}

// Membership — что мы знаем про подписку одного человека.
type Membership struct {
	TelegramID int64      `db:"telegram_id"`
	Username   string     `db:"username"`
	FirstName  string     `db:"first_name"`
	Status     string     `db:"status"`
	JoinedAt   *time.Time `db:"joined_at"`
	LeftAt     *time.Time `db:"left_at"`
	InviteLink string     `db:"invite_link"`
	SourceID   string     `db:"source_id"`
	SeenAt     time.Time  `db:"seen_at"`
}

// Subscribed — считается ли такой статус подпиской.
//
// Администратор и владелец подписаны по определению, и терять их — значит
// каждый раз объяснять, почему в отчёте на одного человека меньше, чем в
// самом Telegram. restricted в каналах не встречается, но в группах
// означает «ограничен, но внутри», и правило должно быть одно.
func Subscribed(status string) bool {
	switch status {
	case "member", "administrator", "creator", "restricted":
		return true
	default:
		return false
	}
}

// transition — какое событие описывает переход между статусами. Пусто,
// когда человек по обе стороны перехода одинаково подписан или одинаково
// нет: смена роли внутри канала это не подписка.
func transition(from, to string) string {
	was, is := Subscribed(from), Subscribed(to)
	switch {
	case !was && is:
		return EventJoined
	case was && !is:
		return EventLeft
	default:
		return ""
	}
}

// ParseChat разбирает канал из конфигурации. Принимает то, что реально
// оказывается под рукой: @имя, голое имя, ссылку t.me и числовой id.
//
// Разбор здесь, а не в cmd, потому что от него зависит сверка входящих
// событий: канал, заданный именем, приходит в update числом, и сравнивать
// их надо одинаково в обе стороны.
func ParseChat(raw string) (Chat, bool) {
	v := strings.TrimSpace(raw)
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "t.me/")
	v = strings.TrimPrefix(v, "telegram.me/")
	v = strings.Trim(v, "/")
	v = strings.TrimPrefix(v, "@")
	if v == "" {
		return Chat{}, false
	}

	// Числовой id канала: -100 и дальше цифры. Ссылка-приглашение в
	// приватный канал (t.me/+abc) каналом не является — по ней нельзя ни
	// спросить размер, ни проверить человека.
	if id, ok := numericID(v); ok {
		return Chat{ID: id}, true
	}
	if !publicName(v) {
		return Chat{}, false
	}
	return Chat{Username: v}, true
}

// numericID — только отрицательные числа: id канала в Telegram всегда со
// знаком минус, а положительное число — это чей-то личный id, и молча
// принять его за канал значит потом долго искать, почему замер пуст.
func numericID(v string) (int64, bool) {
	if !strings.HasPrefix(v, "-") {
		return 0, false
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// publicName — публичное имя канала: буквы, цифры и подчёркивание, и
// обязательно с буквы. Плюс в начале означает приватную
// ссылку-приглашение, а строка из одних цифр — чей-то личный id: ни то,
// ни другое именем канала не является.
func publicName(v string) bool {
	if first := v[0]; !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z') {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// same — тот ли это канал. Сравниваем по тому полю, которым канал задан:
// событие всегда несёт число, а конфигурация обычно имя.
func (c Chat) same(other Chat) bool {
	if c.ID != 0 && other.ID != 0 {
		return c.ID == other.ID
	}
	if c.Username != "" && other.Username != "" {
		return strings.EqualFold(c.Username, other.Username)
	}
	return false
}

// ref — как канал называть в вызовах Bot API. Имя с собакой или число.
func (c Chat) ref() string {
	if c.Username != "" {
		return "@" + c.Username
	}
	return strconv.FormatInt(c.ID, 10)
}
