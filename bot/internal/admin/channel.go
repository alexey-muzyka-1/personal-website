package admin

import (
	"net/http"
	"time"
)

// Канал в админке.
//
// Здесь два разных вопроса, и путать их нельзя. Первый — что происходит с
// каналом: сколько людей, кто пришёл, кто ушёл. Второй — что воронка даёт
// каналу: сколько из пришедших в бота дошло до подписки.
//
// Всё упирается в то, чего Bot API не отдаёт: списка подписчиков нет.
// Поимённо мы знаем только тех, кто подписался после того, как бот стал
// админом, и тех, кто когда-либо запускал бота. Остальные видны одним
// числом — размером канала. Поэтому у людей из старой базы нет даты
// подписки, и в конверсию воронки они не идут ни в какую сторону: это не
// ноль и не сто процентов, это «неизвестно», и так и написано.

// ChannelSummary — состояние канала и здоровье самого замера.
type ChannelSummary struct {
	// Members — размер канала по последнему снимку. Единственная цифра,
	// которая включает людей, известных нам только числом.
	Members int `db:"members"`
	// MeasuredAt пустое, если снимков ещё не было. Старый снимок значит,
	// что бота сняли с админов или он не может достучаться до Telegram.
	MeasuredAt *time.Time `db:"measured_at"`
	SyncedAt   *time.Time `db:"synced_at"`
	// Known — сколько подписчиков мы знаем поимённо. Меньше Members и
	// будет меньше всегда: остальных Telegram не называет.
	Known int `db:"known"`
	// Dated — из них с известной датой подписки, Undated — база до замера.
	Dated   int `db:"dated"`
	Undated int `db:"undated"`
	// Joined и Gone — подписки и отписки за выбранный период.
	Joined int `db:"joined"`
	Gone   int `db:"gone"`
}

// ChannelConversion — что воронка даёт каналу. Разбиение исчерпывающее:
// сумма пяти корзин равна числу людей в срезе, иначе цифру нельзя
// проверить глазами.
type ChannelConversion struct {
	People int `db:"people"`
	// AfterStart — подписались после того, как запустили бота. Только это
	// и есть заслуга воронки.
	AfterStart int `db:"after_start"`
	// BeforeStart — были подписаны раньше, чем пришли в бота.
	BeforeStart int `db:"before_start"`
	// Undated — подписаны, но когда — неизвестно: база до замера.
	Undated int `db:"undated"`
	// Gone — подписывались и ушли.
	Gone int `db:"gone"`
	// Never — не подписаны и не отписывались.
	Never int `db:"never"`
}

// ChannelDay — сутки канала. Gone, а не Left: left в SQL зарезервирован,
// и колонку пришлось бы всюду брать в кавычки.
type ChannelDay struct {
	Date   string `db:"date"`
	Joined int    `db:"joined"`
	Gone   int    `db:"gone"`
	// Members — размер по последнему снимку этих суток, 0 если снимков
	// в этот день не было.
	Members int `db:"members"`
}

// ChannelSource — метка источника и её путь до подписки.
type ChannelSource struct {
	ID         string `db:"source"`
	Started    int    `db:"started"`
	Subscribed int    `db:"subscribed"`
	AfterStart int    `db:"after_start"`
}

// ChannelPerson — подписчик. Lead отличает того, кто пришёл через бота,
// от того, кто подписался сам: второму бот написать не может, и это не
// деталь интерфейса, а разница в том, что с человеком можно сделать.
type ChannelPerson struct {
	TelegramID int64  `db:"telegram_id"`
	Username   string `db:"username"`
	FirstName  string `db:"first_name"`
	Status     string `db:"status"`
	// Subscribed берётся колонкой, а не выводится из статуса: правило
	// «кто считается подписанным» уже применено при записи, и повторять
	// его здесь значит однажды разойтись с ним.
	Subscribed bool       `db:"subscribed"`
	JoinedAt   *time.Time `db:"joined_at"`
	LeftAt     *time.Time `db:"left_at"`
	SourceID   string     `db:"source_id"`
	Lead       bool       `db:"lead"`
}

// ChannelCohort — какой список подписчиков спрашивают.
type ChannelCohort string

const (
	// CohortGone — отписавшиеся за период.
	CohortGone ChannelCohort = "left"
	// CohortOutside — подписаны, но бота не запускали. Про этих людей мы
	// знаем только то, что они есть.
	CohortOutside ChannelCohort = "outside"
	// CohortEveryone — всё, что известно про канал. Для выгрузки.
	CohortEveryone ChannelCohort = "all"
)

// channelFilter — значения фильтра по каналу. Список закрытый: адрес с
// произвольным словом не должен молча показать срез, которого никто не
// выбирал.
func channelFilter(v string) string {
	switch v {
	case "member", "left", NoValue:
		return v
	default:
		return ""
	}
}

type apiChannelPerson struct {
	TelegramID int64  `json:"telegramId"`
	Handle     string `json:"handle"`
	Chat       string `json:"chat"`
	Joined     string `json:"joined"`
	Left       string `json:"left"`
	Source     string `json:"source"`
	Lead       bool   `json:"lead"`
	Days       int    `json:"days"`
}

// ServeChannel — всё, что нужно странице канала.
func (h *Handler) ServeChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, days := h.parseFilter(r, h.now())

	summary, err := h.reader.ChannelSummary(ctx, filter)
	if err != nil {
		h.failJSON(w, "channel summary", err)
		return
	}
	conversion, err := h.reader.ChannelConversion(ctx, filter)
	if err != nil {
		h.failJSON(w, "channel conversion", err)
		return
	}
	daily, err := h.reader.ChannelDaily(ctx, filter)
	if err != nil {
		h.failJSON(w, "channel daily", err)
		return
	}
	sources, err := h.reader.ChannelSources(ctx, filter)
	if err != nil {
		h.failJSON(w, "channel sources", err)
		return
	}

	// Списки короткие: это не «люди», а два ответа на конкретные вопросы —
	// кто ушёл и кто пришёл мимо бота. Весь список целиком живёт в
	// выгрузке.
	const listLimit = 200
	gone, err := h.reader.ChannelPeople(ctx, filter, CohortGone, listLimit)
	if err != nil {
		h.failJSON(w, "channel leavers", err)
		return
	}
	outside, err := h.reader.ChannelPeople(ctx, filter, CohortOutside, listLimit)
	if err != nil {
		h.failJSON(w, "channel outsiders", err)
		return
	}

	writeJSON(w, map[string]any{
		"filter":     filterBody(filter, days),
		"members":    summary.Members,
		"known":      summary.Known,
		"dated":      summary.Dated,
		"undated":    summary.Undated,
		"joined":     summary.Joined,
		"left":       summary.Gone,
		"measuredAt": moment(summary.MeasuredAt),
		"syncedAt":   moment(summary.SyncedAt),
		// stale отвечает на вопрос «замер вообще идёт?». Считает Go, а не
		// страница: срок годности снимка — свойство замера, а не вёрстки.
		"stale":      h.staleSnapshot(summary.MeasuredAt),
		"conversion": conversionBody(conversion),
		"daily":      dailyBody(daily),
		"sources":    sourcesBody(sources),
		"gone":       peopleBody(gone, h.now()),
		"outside":    peopleBody(outside, h.now()),
		"now":        h.now().In(moscow).Format("02.01 15:04"),
	})
}

// staleSnapshot — снимку пора быть свежее. Порог вдвое больше шага
// снимков: один пропущенный тик это сеть, два подряд — это уже перестало
// работать.
func (h *Handler) staleSnapshot(at *time.Time) bool {
	if at == nil {
		return true
	}
	return h.now().Sub(*at) > 2*snapshotEvery
}

// snapshotEvery повторяет шаг снимков в channel.Watcher. Связывать пакеты
// ради одной константы дороже, чем держать её здесь с этой строкой.
const snapshotEvery = 30 * time.Minute

func conversionBody(c ChannelConversion) map[string]any {
	return map[string]any{
		"people":      c.People,
		"afterStart":  c.AfterStart,
		"beforeStart": c.BeforeStart,
		"undated":     c.Undated,
		"left":        c.Gone,
		"never":       c.Never,
	}
}

func dailyBody(days []ChannelDay) []map[string]any {
	out := make([]map[string]any, 0, len(days))
	for _, d := range days {
		out = append(out, map[string]any{
			"date": d.Date, "joined": d.Joined, "left": d.Gone, "members": d.Members,
		})
	}
	return out
}

func sourcesBody(sources []ChannelSource) []map[string]any {
	out := make([]map[string]any, 0, len(sources))
	for _, s := range sources {
		out = append(out, map[string]any{
			"source": s.ID, "started": s.Started,
			"subscribed": s.Subscribed, "afterStart": s.AfterStart,
		})
	}
	return out
}

func peopleBody(people []ChannelPerson, now time.Time) []apiChannelPerson {
	out := make([]apiChannelPerson, 0, len(people))
	for _, p := range people {
		out = append(out, apiChannelPerson{
			TelegramID: p.TelegramID,
			Handle:     channelHandle(p),
			Chat:       ChatLink(p.TelegramID, p.Username),
			Joined:     moment(p.JoinedAt),
			Left:       moment(p.LeftAt),
			Source:     p.SourceID,
			Lead:       p.Lead,
			Days:       lived(p, now),
		})
	}
	return out
}

// lived — сколько дней человек пробыл в канале. Ноль там, где считать не
// от чего: без даты подписки срок жизни неизвестен, а не равен нулю.
func lived(p ChannelPerson, now time.Time) int {
	if p.JoinedAt == nil {
		return 0
	}
	end := now
	if p.LeftAt != nil {
		end = *p.LeftAt
	}
	days := int(end.Sub(*p.JoinedAt).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

func channelHandle(p ChannelPerson) string {
	return handle(Lead{
		TelegramID: p.TelegramID,
		Username:   p.Username,
		FirstName:  p.FirstName,
	})
}

// moment — время для интерфейса. Пустая строка означает «даты нет», и это
// не то же самое, что «дата нулевая».
func moment(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.In(moscow).Format("02.01 15:04")
}
