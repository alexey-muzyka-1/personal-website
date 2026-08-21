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
	EventMaterialSelected = "material_selected"
	EventMaterialOpened   = "material_opened"
	EventAlternativeAsked = "alternative_asked"
	EventStageAnswered    = "stage_answered"
	EventOfferShown       = "offer_shown"
	// EventChannelOffered — человеку показали канал. Не переход и не
	// подписка: Telegram о нажатии URL-кнопки не сообщает. Само по себе
	// это половина пары «показали — подписался», а вторую половину
	// приносит уже канал, событием о вступлении.
	EventChannelOffered = "channel_offered"
	// Блокировка бота. Приходит из my_chat_member в личном чате и
	// значит ровно то, что человеку больше нельзя написать: ни про эфир,
	// ни про что ещё. Без этого рассылка «всем записавшимся» молча
	// уходит в половину адресов.
	EventBotBlocked   = "bot_blocked"
	EventBotUnblocked = "bot_unblocked"
)

// Stage — состояние контент-системы человека, а не то, кто он вообще.
//
// Вопрос задаётся после того, как человек получил разбор: до этого
// спрашивать не за что. У каждого состояния ровно один следующий шаг,
// и офферы не показываются одновременно.
//
// Ноль — «не спрашивали». Забытый ответ не должен сойти за состояние.
type Stage int

const (
	StageUnknown Stage = iota
	// StageNotShipping — не получается выпускать стабильно.
	StageNotShipping
	// StageNoSignal — контент выходит, но непонятно, что работает.
	StageNoSignal
	// StageOther — «другая ситуация». Не тупик: дальше один уточняющий
	// вопрос, который возвращает человека в одно из двух состояний.
	StageOther
)

// String — то, что уезжает в базу и в отчёты.
func (s Stage) String() string {
	switch s {
	case StageNotShipping:
		return "not_shipping"
	case StageNoSignal:
		return "no_signal"
	case StageOther:
		return "other"
	default:
		return ""
	}
}

func ParseStage(s string) (Stage, bool) {
	switch s {
	case "not_shipping":
		return StageNotShipping, true
	case "no_signal":
		return StageNoSignal, true
	case "other":
		return StageOther, true
	default:
		return StageUnknown, false
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
	ActionNone     ActionKind = iota // нулевое значение = кнопка-ссылка
	ActionOther                      // «мне это не подходит»
	ActionStage                      // ответ про состояние контент-системы
	ActionWaitlist                   // «записать меня» на будущий эфир
)

type Action struct {
	Kind       ActionKind
	MaterialID string
	Stage      Stage
}
