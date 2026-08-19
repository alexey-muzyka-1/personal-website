// Package funnel — сценарий личной воронки: кто пришёл, из какого Reel,
// какой материал ему подходит и что из этого записано.
//
// Здесь нет ничего про Telegram и про базу. Транспорт и хранилище —
// адаптеры снаружи: сценарий не должен переписываться, когда меняется
// способ доставки сообщения или тип базы.
//
// Тексты для человека живут здесь же. Это часть сценария, а не транспорта:
// если разнести реплики по обработчикам, воронка перестанет читаться
// как один документ.
package funnel

import "time"

// Имена событий совпадают с канонической цепочкой из хаба «Личная воронка»
// (reel_reached → keyword_lead → bot_started → ... → paid). Аналитика не
// должна переводить названия из кода в стратегию и обратно.
const (
	EventBotStarted       = "bot_started"
	EventRoleAnswered     = "role_answered"
	EventMaterialSelected = "material_selected"
	EventMaterialOpened   = "material_opened"
	EventAlternativeAsked = "alternative_asked"
)

// Role — ответ на единственный вопрос, который бот задаёт: человек ведёт
// контент сам или с командой.
//
// Это не анкета. Ответ решает, какой разбор отдать первым, и он же
// понадобится позже: одиночке и команде нужны разные продукты. Ноль —
// «не спрашивали», чтобы забытый ответ нельзя было принять за «сам».
type Role int

const (
	RoleUnknown Role = iota
	RoleSolo
	RoleTeam
)

// String — то, что уезжает в базу и в отчёты.
func (r Role) String() string {
	switch r {
	case RoleSolo:
		return "solo"
	case RoleTeam:
		return "team"
	default:
		return ""
	}
}

func ParseRole(s string) (Role, bool) {
	switch s {
	case "solo":
		return RoleSolo, true
	case "team":
		return RoleTeam, true
	default:
		return RoleUnknown, false
	}
}

// User — человек в Telegram. Отдельная авторизация не нужна: update уже
// содержит Telegram user ID.
type User struct {
	TelegramID int64
	Username   string
	FirstName  string
}

// Attribution — один факт «пришёл из источника». Таблица append-only:
// первое касание неизменяемо, последнее — просто самая свежая строка.
// Перезаписывать источник у пользователя нельзя, иначе повторный /start
// из другого Reel стирает историю.
type Attribution struct {
	TelegramID int64
	SourceID   string
	// RawPayload — то, что реально пришло в deep link, включая мусор.
	// Отличает «источника не было» от «источник был битый».
	RawPayload string
	OccurredAt time.Time
}

// Event — шаг воронки. SourceID и MaterialID денормализованы специально:
// событие должно оставаться разбираемым в одиночку, без джойнов по истории.
type Event struct {
	TelegramID int64
	Name       string
	SourceID   string
	MaterialID string
	Metadata   map[string]string
	OccurredAt time.Time
}

// Link — непубличный токен для tracked redirect.
//
// Telegram не сообщает о клике по URL-кнопке, поэтому единственный честный
// способ записать переход — вести человека через свой redirect. Токен
// случайный: Telegram ID не должен утекать в ссылку, которая попадёт в
// браузер, историю и в GA.
type Link struct {
	Token      string
	TelegramID int64
	MaterialID string
	SourceID   string
	CreatedAt  time.Time
}

// Reply — что бот говорит человеку.
//
// Text размечен минимальным HTML: <b>, <i> и <blockquote>. Разметка это
// часть реплики, а не транспорта: решение «название жирным, обещание
// цитатой» принимается там же, где написан текст.
type Reply struct {
	Text    string
	Buttons []Button
	// Skip = отправлять нечего. Так выглядит update, который Telegram
	// прислал повторно.
	Skip bool
}

// Button — либо внутреннее действие, либо внешняя ссылка, но не оба сразу.
type Button struct {
	Label  string
	Action Action
	URL    string
}

// ActionKind — что человек хочет сделать, в терминах воронки. Как это
// закодировано в callback_data, знает только Telegram-адаптер.
type ActionKind int

const (
	ActionNone  ActionKind = iota // нулевое значение = кнопка-ссылка
	ActionOther                   // «мне это не подходит»
	ActionRole                    // ответ на вопрос про команду
)

type Action struct {
	Kind       ActionKind
	MaterialID string
	Role       Role
}
