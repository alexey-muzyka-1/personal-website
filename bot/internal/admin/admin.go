// Package admin — одна страница на чтение: кто пришёл, откуда и куда
// дошёл.
//
// Ничего не редактирует. Клик меняет только срез: источник, состояние,
// период, конкретный человек. Срез целиком лежит в query-параметрах,
// поэтому его можно послать себе ссылкой и вернуться к нему завтра.
//
// Страница считает шаг к шагу, а не только от запуска. Доля от первого
// шага на длинной цепочке падает у всех строк сразу и перестаёт что-либо
// различать; потерю видно только в переходе между соседними шагами.
//
// Пароль спрашивает Caddy, не мы: складывать в бота свою авторизацию
// ради одной страницы незачем.
package admin

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

//go:embed page.html
var pageFS embed.FS

// ErrNoPerson — карточку просили, а такого человека нет (или он скрыт).
var ErrNoPerson = errors.New("no such person")

// NoValue — значение фильтра «поле пустое»: пришёл без метки, не ответил
// на вопрос. Пустая строка уже занята под «фильтра нет», поэтому пустоту
// приходится называть отдельным словом.
const NoValue = "-"

// Москва без перехода на летнее время с 2014 года. Фиксированный сдвиг
// честнее, чем tzdata, которой в контейнере может не оказаться.
var moscow = time.FixedZone("MSK", 3*60*60)

// Filter — срез данных, выбранный кликом.
type Filter struct {
	// Source — метка источника, NoValue = «без метки», пустая = все.
	Source string
	// Stage — состояние человека, NoValue = «не ответил», пустая = все.
	Stage string
	// Since — берём только пришедших после этого момента. Отбор идёт по
	// дате первого появления, а не по датам событий: иначе в воронке
	// смешаются люди, у которых часть шагов осталась за границей периода,
	// и проценты перестанут сходиться.
	Since time.Time
	// Hidden — telegram_id, которых не должно быть ни в одной цифре.
	// Свой тестовый аккаунт в отчёте о чужом поведении — это шум.
	Hidden []int64
}

// Stage — шаг воронки: сколько было событий и сколько разных людей.
type Stage struct {
	Name   string `db:"name"`
	Events int    `db:"events"`
	People int    `db:"people"`
}

// Source — метка источника и её результат вплоть до последнего
// измеримого шага.
type Source struct {
	ID       string `db:"source"`
	Started  int    `db:"started"`
	Opened   int    `db:"opened"`
	Offered  int    `db:"offered"`
	Waitlist int    `db:"waitlist"`
}

// Segment — сколько людей в каком состоянии. Это и есть ответ на вопрос
// «кого куда привели»: по нему видно, кому есть что предложить.
type Segment struct {
	Stage    string `db:"stage"`
	People   int    `db:"people"`
	Waitlist int    `db:"waitlist"`
}

// Lead — человек и что с ним произошло.
type Lead struct {
	TelegramID int64     `db:"telegram_id"`
	Username   string    `db:"username"`
	FirstName  string    `db:"first_name"`
	FirstSeen  time.Time `db:"first_seen_at"`
	Source     string    `db:"source"`
	Stage      string    `db:"stage"`
	Materials  string    `db:"materials"`
	Opened     bool      `db:"opened"`
	Waitlist   bool      `db:"waitlist"`
}

// Moment — одно событие в истории человека.
type Moment struct {
	Name       string            `db:"name"`
	SourceID   string            `db:"source_id"`
	MaterialID string            `db:"material_id"`
	Meta       map[string]string `db:"metadata"`
	OccurredAt time.Time         `db:"occurred_at"`
}

// TimelineRow — событие в выгрузке: то же, что Moment, но с именем
// человека, чтобы лист «Шаги» читался без сверки с листом «Люди».
type TimelineRow struct {
	TelegramID int64             `db:"telegram_id"`
	Username   string            `db:"username"`
	Name       string            `db:"name"`
	SourceID   string            `db:"source_id"`
	MaterialID string            `db:"material_id"`
	Meta       map[string]string `db:"metadata"`
	OccurredAt time.Time         `db:"occurred_at"`
}

// Person — карточка человека: кто он и что делал по порядку.
type Person struct {
	Lead
	Moments []Moment
}

// Reader — то, что странице нужно от базы. Только чтение.
type Reader interface {
	Stages(ctx context.Context, f Filter) (map[string]Stage, error)
	Segments(ctx context.Context, f Filter) ([]Segment, error)
	Sources(ctx context.Context, f Filter) ([]Source, error)
	Leads(ctx context.Context, f Filter, limit int) ([]Lead, error)
	Person(ctx context.Context, telegramID int64, f Filter) (Person, error)
	HiddenPeople(ctx context.Context, f Filter) (int, error)
	Timeline(ctx context.Context, f Filter, limit int) ([]TimelineRow, error)
}

// Порядок и человеческие названия шагов. Порядок задаётся здесь, а не
// сортировкой по количеству: воронка должна читаться сверху вниз даже
// когда на нижних шагах ноль.
//
// note — оговорка, без которой цифра врёт. Показ оффера это не переход,
// а запись на эфир это не оплата; в отчёте они стоят последними и их
// слишком легко прочитать как результат.
var stageOrder = []struct{ name, label, note string }{
	{"bot_started", "Запустили бота", ""},
	{"material_selected", "Получили разбор", ""},
	{"material_opened", "Открыли статью", ""},
	{"stage_answered", "Ответили про состояние", ""},
	{"offer_shown", "Увидели предложение", "показ, не переход"},
	{"waitlist_joined", "Записались на эфир", "интерес, не деньги"},
}

// Step — шаг воронки с двумя долями. FromPrev показывает, где именно
// теряются люди, FromTop — сколько осталось от всех пришедших.
type Step struct {
	Label    string
	Note     string
	People   int
	Events   int
	FromPrev string
	FromTop  string
}

// Названия событий для карточки человека. Шаги воронки берутся из
// stageOrder, здесь только то, что в неё не входит.
var momentLabels = map[string]string{
	"alternative_asked": "Попросил другой материал",
}

const recentLeads = 50

// Периоды, между которыми можно переключаться. Ноль — за всё время.
var periods = []struct {
	days  int
	label string
}{
	{7, "7 дней"},
	{30, "30 дней"},
	{0, "всё время"},
}

type Handler struct {
	reader Reader
	hidden []int64
	tmpl   *template.Template
	log    *slog.Logger
	now    func() time.Time
}

// Option — подменяемая деталь.
type Option func(*Handler)

// WithHidden прячет со страницы перечисленные telegram_id. Данные при
// этом остаются в базе: это фильтр отчёта, а не удаление.
func WithHidden(ids []int64) Option {
	return func(h *Handler) { h.hidden = ids }
}

// WithClock подменяет часы. Нужен тестам: страница показывает время
// съёмки и считает период от «сейчас».
func WithClock(now func() time.Time) Option {
	return func(h *Handler) { h.now = now }
}

func NewHandler(reader Reader, log *slog.Logger, opts ...Option) (*Handler, error) {
	if reader == nil {
		return nil, fmt.Errorf("reader is required")
	}
	if log == nil {
		log = slog.Default()
	}

	tmpl, err := template.New("page.html").Funcs(template.FuncMap{
		"moscow": func(t time.Time) string { return t.In(moscow).Format("02.01 15:04") },
		"stage":  stageLabel,
		"chat":   ChatLink,
		"moment": momentLabel,
		"share":  share,
	}).ParseFS(pageFS, "page.html")
	if err != nil {
		return nil, fmt.Errorf("parsing page template: %w", err)
	}

	h := &Handler{reader: reader, tmpl: tmpl, log: log, now: time.Now}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

// stageLabel — состояние человека по-русски. Пустое значение это не
// состояние, а «не спрашивали»: отдельная формулировка, иначе человек,
// который до вопроса не дошёл, смешается с теми, кто ответил.
func stageLabel(v string) string {
	switch v {
	case "not_shipping":
		return "не выпускает стабильно"
	case "no_signal":
		return "выпускает, не видит сигнала"
	case "other":
		return "другая ситуация"
	case "":
		return "не ответил"
	default:
		return v
	}
}

func momentLabel(name string) string {
	for _, s := range stageOrder {
		if s.name == name {
			return s.label
		}
	}
	if label, ok := momentLabels[name]; ok {
		return label
	}
	return name
}

func share(part, whole int) string {
	if whole == 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", part*100/whole)
}

type pageData struct {
	Filter  Filter
	Days    int
	Periods []period

	Steps    []Step
	Segments []segmentRow
	Sources  []sourceRow
	Leads    []leadRow
	// Cohort — сколько всего людей в выбранном срезе. Нужен, чтобы
	// сказать вслух, что список людей обрезан, а не полон.
	Cohort int
	Person *Person
	// Error — сообщение вместо данных. Ошибка остаётся внутри страницы,
	// а не выпадает в голый текст браузера: с голого текста некуда
	// вернуться, и человек упирается в тупик там, где нужен один клик.
	Error string
	// HiddenNote — вслух сказанное «здесь не все». Страница, которая
	// молча выкидывает строки, выглядит точно так же, как страница, где
	// этих строк не было.
	HiddenNote string
	Now        time.Time
}

// hiddenNote — сколько аккаунтов не попало в цифры.
func hiddenNote(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "скрыт 1 тестовый аккаунт"
	case n < 5:
		return fmt.Sprintf("скрыты %d тестовых аккаунта", n)
	default:
		return fmt.Sprintf("скрыто %d тестовых аккаунтов", n)
	}
}

type period struct {
	Days   int
	Label  string
	Href   string
	Active bool
}

// Строки таблиц — это данные из базы плюс то, куда ведёт клик. Адрес
// считается здесь, а не в шаблоне: он зависит от текущего среза, и
// собирать его из кусков в HTML значит развести две правды о фильтрах.
//
// Active — строка, по которой уже отфильтровано. Клик по ней снимает
// фильтр, поэтому она подсвечена и подписана иначе.
type sourceRow struct {
	Source
	Href   string
	Active bool
}

type segmentRow struct {
	Segment
	Href   string
	Active bool
}

type leadRow struct {
	Lead
	Href string
}

// filterValue — как записать значение в query. Пустое поле само по себе
// значит «фильтра нет», поэтому пустоту приходится называть словом.
func filterValue(v string) string {
	if v == "" {
		return NoValue
	}
	return v
}

// toggle — повторный клик по уже выбранной строке снимает фильтр. Иначе
// из среза некуда выйти, кроме как чистить адрес руками.
func toggle(active bool, value string) string {
	if active {
		return ""
	}
	return value
}

// query — текущий срез в параметрах адреса.
func (d pageData) query() url.Values {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("source", d.Filter.Source)
	set("stage", d.Filter.Stage)
	if d.Days != 0 {
		set("days", strconv.Itoa(d.Days))
	}
	return q
}

// Link — адрес этой же страницы с одним изменённым параметром. Остальной
// срез сохраняется: клик по источнику не должен сбрасывать период.
// Пустое значение убирает параметр — так же работает крестик на фильтре.
func (d pageData) Link(key, value string) string {
	q := d.query()

	// Карточка человека — не фильтр, а другой экран: открывая её, срез
	// сбрасывать не надо, а возвращаясь, надо убрать только её.
	if value == "" {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/admin"
	}
	return "/admin?" + q.Encode()
}

// ExportHref — выгрузка ровно того среза, который сейчас на экране.
// Кнопка, отдающая всю базу независимо от фильтров, обесценивает сами
// фильтры: отбирать пришлось бы второй раз, уже в Excel.
func (d pageData) ExportHref() string {
	q := d.query()
	if len(q) == 0 {
		return "/admin/export.xlsx"
	}
	return "/admin/export.xlsx?" + q.Encode()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now()
	filter, days := h.parseFilter(r, now)

	data := pageData{Filter: filter, Days: days, Now: now}
	for _, p := range periods {
		data.Periods = append(data.Periods, period{
			Days:   p.days,
			Label:  p.label,
			Href:   data.Link("days", periodParam(p.days)),
			Active: p.days == days,
		})
	}

	hidden, err := h.reader.HiddenPeople(ctx, filter)
	if err != nil {
		h.fail(w, "hidden", err)
		return
	}
	data.HiddenNote = hiddenNote(hidden)

	// Карточка человека — отдельный экран: сводные таблицы на нём не
	// нужны, и лишние четыре запроса тоже.
	if id, ok := personID(r); ok {
		person, err := h.reader.Person(ctx, id, filter)
		switch {
		case errors.Is(err, ErrNoPerson):
			h.renderError(w, http.StatusNotFound,
				"Такого человека нет. Либо он ещё не запускал бота, либо это скрытый тестовый аккаунт.")
			return
		case err != nil:
			h.fail(w, "person", err)
			return
		}
		data.Person = &person
		h.render(w, data)
		return
	}

	stages, err := h.reader.Stages(ctx, filter)
	if err != nil {
		h.fail(w, "stages", err)
		return
	}
	segments, err := h.reader.Segments(ctx, filter)
	if err != nil {
		h.fail(w, "segments", err)
		return
	}
	sources, err := h.reader.Sources(ctx, filter)
	if err != nil {
		h.fail(w, "sources", err)
		return
	}
	leads, err := h.reader.Leads(ctx, filter, recentLeads)
	if err != nil {
		h.fail(w, "leads", err)
		return
	}

	data.Steps = steps(stages)
	for _, s := range segments {
		active := filter.Stage == filterValue(s.Stage)
		data.Segments = append(data.Segments, segmentRow{
			Segment: s,
			Href:    data.Link("stage", toggle(active, filterValue(s.Stage))),
			Active:  active,
		})
	}
	for _, s := range sources {
		active := filter.Source == filterValue(s.ID)
		data.Sources = append(data.Sources, sourceRow{
			Source: s,
			Href:   data.Link("source", toggle(active, filterValue(s.ID))),
			Active: active,
		})
	}
	for _, l := range leads {
		data.Leads = append(data.Leads, leadRow{
			Lead: l,
			Href: data.Link("id", strconv.FormatInt(l.TelegramID, 10)),
		})
	}
	if len(data.Steps) > 0 {
		data.Cohort = data.Steps[0].People
	}

	h.render(w, data)
}

// steps раскладывает события по порядку воронки и считает обе доли.
func steps(stages map[string]Stage) []Step {
	out := make([]Step, 0, len(stageOrder))
	var top, prev int
	for i, s := range stageOrder {
		found := stages[s.name]
		step := Step{
			Label:  s.label,
			Note:   s.note,
			People: found.People,
			Events: found.Events,
		}
		if i == 0 {
			top = found.People
			// «100%» пишется только когда есть от чего считать. Пустая
			// база иначе показывает стопроцентную проходимость на всех
			// шагах при нуле людей — самая дорогая ложь на странице.
			if top > 0 {
				step.FromTop = "100%"
			}
		} else {
			step.FromPrev = share(found.People, prev)
			step.FromTop = share(found.People, top)
		}
		prev = found.People
		out = append(out, step)
	}
	return out
}

func (h *Handler) parseFilter(r *http.Request, now time.Time) (Filter, int) {
	q := r.URL.Query()
	filter := Filter{
		Source: q.Get("source"),
		Stage:  q.Get("stage"),
		Hidden: h.hidden,
	}

	days := 0
	for _, p := range periods {
		if p.days != 0 && q.Get("days") == strconv.Itoa(p.days) {
			days = p.days
		}
	}
	if days != 0 {
		filter.Since = now.AddDate(0, 0, -days)
	}
	return filter, days
}

func personID(r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("id")
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

func periodParam(days int) string {
	if days == 0 {
		return ""
	}
	return strconv.Itoa(days)
}

func (h *Handler) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Страница за паролем и с личными данными: её не должен закэшировать
	// ни браузер, ни что-либо по дороге.
	w.Header().Set("Cache-Control", "no-store")
	if err := h.tmpl.Execute(w, data); err != nil {
		h.log.Error("admin page render failed", "error", err)
	}
}

func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("admin query failed", "query", what, "error", err)
	h.renderError(w, http.StatusInternalServerError,
		"Не удалось прочитать базу. Что именно сломалось — в логах бота.")
}

// renderError отдаёт ошибку той же страницей: с заголовком, оформлением и
// ссылкой назад.
func (h *Handler) renderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.tmpl.Execute(w, pageData{Error: message, Now: h.now()}); err != nil {
		h.log.Error("admin error page render failed", "error", err)
	}
}
