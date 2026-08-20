// Package admin — внутренний сайт по личной воронке: кто пришёл, откуда,
// куда дошёл и что бот ему показывал.
//
// Ничего не редактирует. Страницы собраны Astro и лежат статикой рядом
// (см. ui.go), данные к ним приходят JSON-ом отсюда (api.go). Разделение
// не косметическое: вёрстка правится и пересобирается без участия Go, а
// схему базы по-прежнему знает только Go — двое знающих схему базы лидов
// это худшее, что можно с ней сделать.
//
// Срез — источник, состояние, период — живёт в query-параметрах, поэтому
// любой вид можно послать себе ссылкой и вернуться к нему завтра.
//
// Пароль спрашивает Caddy, не мы: складывать в бота свою авторизацию ради
// набора страниц на чтение незачем.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

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

// Day — сутки в динамике: сколько пришло, сколько открыло разбор,
// сколько записалось. Нужен графику: таблица показывает итог, а график —
// был ли он набран равномерно или одним днём.
type Day struct {
	Date     string `db:"date"`
	People   int    `db:"people"`
	Opened   int    `db:"opened"`
	Waitlist int    `db:"waitlist"`
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

// Reader — то, что админке нужно от базы. Только чтение.
type Reader interface {
	Stages(ctx context.Context, f Filter) (map[string]Stage, error)
	Segments(ctx context.Context, f Filter) ([]Segment, error)
	Sources(ctx context.Context, f Filter) ([]Source, error)
	Leads(ctx context.Context, f Filter, limit int) ([]Lead, error)
	Person(ctx context.Context, telegramID int64, f Filter) (Person, error)
	HiddenPeople(ctx context.Context, f Filter) (int, error)
	Timeline(ctx context.Context, f Filter, limit int) ([]TimelineRow, error)
	Daily(ctx context.Context, f Filter) ([]Day, error)
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

// Названия событий для карточки человека. Шаги воронки берутся из
// stageOrder, здесь только то, что в неё не входит.
var momentLabels = map[string]string{
	"alternative_asked": "Попросил другой материал",
}

// Периоды, между которыми можно переключаться. Ноль — за всё время.
// Список закрытый: адрес с произвольным days не должен молча показать
// срез, которого никто не выбирал.
var periodDays = []int{7, 30}

type Handler struct {
	reader Reader
	hidden []int64
	log    *slog.Logger
	now    func() time.Time
	// catalog нужен странице маршрутов: что бот отдаёт по какой метке.
	// Берётся из того же каталога, по которому бот реально отвечает,
	// поэтому разойтись с ним страница не может.
	catalog  funnel.Catalog
	botLink  string
	siteBase string
}

// Option — подменяемая деталь.
type Option func(*Handler)

// WithHidden прячет из отчётов перечисленные telegram_id. Данные при этом
// остаются в базе: это фильтр отчёта, а не удаление.
func WithHidden(ids []int64) Option {
	return func(h *Handler) { h.hidden = ids }
}

// WithCatalog отдаёт странице маршрутов тот же каталог, по которому
// отвечает бот. botLink — адрес самого бота, чтобы deep link можно было
// скопировать прямо со страницы.
func WithCatalog(c funnel.Catalog, botLink, siteBase string) Option {
	return func(h *Handler) {
		h.catalog = c
		h.botLink = botLink
		h.siteBase = siteBase
	}
}

// WithClock подменяет часы. Нужен тестам: страницы показывают время
// съёмки и считают период от «сейчас».
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

	h := &Handler{reader: reader, log: log, now: time.Now}
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

// parseFilter читает срез из адреса. Возвращает ещё и выбранный период
// отдельным числом: интерфейсу нужно подсветить активную кнопку.
func (h *Handler) parseFilter(r *http.Request, now time.Time) (Filter, int) {
	q := r.URL.Query()
	filter := Filter{
		Source: q.Get("source"),
		Stage:  q.Get("stage"),
		Hidden: h.hidden,
	}

	days := 0
	for _, d := range periodDays {
		if q.Get("days") == strconv.Itoa(d) {
			days = d
		}
	}
	if days != 0 {
		filter.Since = now.AddDate(0, 0, -days)
	}
	return filter, days
}

// fail — ошибка для запросов, которые ждут файл, а не JSON.
func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("admin query failed", "query", what, "error", err)
	http.Error(w, "не удалось прочитать базу", http.StatusInternalServerError)
}
