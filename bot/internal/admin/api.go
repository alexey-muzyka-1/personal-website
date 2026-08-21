package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/botmap"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// JSON для интерфейса. Страницы собраны Astro и лежат статикой, данные
// приходят сюда — поэтому вёрстка правится без пересборки Go, а Go
// остаётся единственным, кто знает схему базы.
//
// Всё только на чтение. Ответы не кэшируются: это личные данные за
// паролем, им нечего делать ни в браузере, ни по дороге.

type apiFilter struct {
	Source string `json:"source"`
	Stage  string `json:"stage"`
	Days   int    `json:"days"`
}

type apiStep struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Note     string `json:"note"`
	People   int    `json:"people"`
	Events   int    `json:"events"`
	FromPrev int    `json:"fromPrev"`
	FromTop  int    `json:"fromTop"`
	// HasPrev отличает «ноль процентов» от «не с чем сравнивать».
	// Без этого пустая база показывала стопроцентную проходимость.
	HasPrev bool `json:"hasPrev"`
	HasTop  bool `json:"hasTop"`
}

type apiSource struct {
	ID       string `json:"id"`
	Started  int    `json:"started"`
	Opened   int    `json:"opened"`
	Offered  int    `json:"offered"`
	Waitlist int    `json:"waitlist"`
}

type apiSegment struct {
	Stage    string `json:"stage"`
	Label    string `json:"label"`
	People   int    `json:"people"`
	Waitlist int    `json:"waitlist"`
}

type apiLead struct {
	TelegramID int64  `json:"telegramId"`
	Handle     string `json:"handle"`
	FirstName  string `json:"firstName"`
	Chat       string `json:"chat"`
	FirstSeen  string `json:"firstSeen"`
	Source     string `json:"source"`
	Stage      string `json:"stage"`
	StageLabel string `json:"stageLabel"`
	Materials  string `json:"materials"`
	Opened     bool   `json:"opened"`
	Waitlist   bool   `json:"waitlist"`
}

type apiMoment struct {
	Name       string            `json:"name"`
	Label      string            `json:"label"`
	SourceID   string            `json:"sourceId"`
	MaterialID string            `json:"materialId"`
	Meta       map[string]string `json:"meta"`
	At         string            `json:"at"`
}

type apiDay struct {
	Date     string `json:"date"`
	People   int    `json:"people"`
	Opened   int    `json:"opened"`
	Waitlist int    `json:"waitlist"`
}

// ServeOverview — всё, что нужно главной странице: шаги, сегменты,
// источники и динамика по дням.
func (h *Handler) ServeOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, days := h.parseFilter(r, h.now())

	stages, err := h.reader.Stages(ctx, filter)
	if err != nil {
		h.failJSON(w, "stages", err)
		return
	}
	segments, err := h.reader.Segments(ctx, filter)
	if err != nil {
		h.failJSON(w, "segments", err)
		return
	}
	sources, err := h.reader.Sources(ctx, filter)
	if err != nil {
		h.failJSON(w, "sources", err)
		return
	}
	daily, err := h.reader.Daily(ctx, filter)
	if err != nil {
		h.failJSON(w, "daily", err)
		return
	}
	hidden, err := h.reader.HiddenPeople(ctx, filter)
	if err != nil {
		h.failJSON(w, "hidden", err)
		return
	}

	steps := make([]apiStep, 0, len(stageOrder))
	var top, prev int
	for i, s := range stageOrder {
		found := stages[s.name]
		step := apiStep{
			Name: s.name, Label: s.label, Note: s.note,
			People: found.People, Events: found.Events,
		}
		if i == 0 {
			top = found.People
			if top > 0 {
				step.FromTop, step.HasTop = 100, true
			}
		} else {
			if prev > 0 {
				step.FromPrev, step.HasPrev = percent(found.People, prev), true
			}
			if top > 0 {
				step.FromTop, step.HasTop = percent(found.People, top), true
			}
		}
		prev = found.People
		steps = append(steps, step)
	}

	outSegments := make([]apiSegment, 0, len(segments))
	for _, s := range segments {
		outSegments = append(outSegments, apiSegment{
			Stage: s.Stage, Label: stageLabel(s.Stage),
			People: s.People, Waitlist: s.Waitlist,
		})
	}

	outSources := make([]apiSource, 0, len(sources))
	for _, s := range sources {
		outSources = append(outSources, apiSource(s))
	}

	outDays := make([]apiDay, 0, len(daily))
	for _, d := range daily {
		outDays = append(outDays, apiDay(d))
	}

	writeJSON(w, map[string]any{
		"filter":   apiFilter{Source: filter.Source, Stage: filter.Stage, Days: days},
		"steps":    steps,
		"segments": outSegments,
		"sources":  outSources,
		"daily":    outDays,
		"hidden":   hidden,
		"cohort":   top,
		"now":      h.now().In(moscow).Format("02.01 15:04"),
	})
}

// ServePeople — список людей среза.
func (h *Handler) ServePeople(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, _ := h.parseFilter(r, h.now())

	// Список читают глазами и ищут по нему конкретного человека, поэтому
	// потолок здесь щедрее, чем на старой странице: обрезанный список
	// выглядит так же, как полный.
	const limit = 1000
	leads, err := h.reader.Leads(ctx, filter, limit)
	if err != nil {
		h.failJSON(w, "leads", err)
		return
	}

	out := make([]apiLead, 0, len(leads))
	for _, l := range leads {
		out = append(out, toAPILead(l))
	}
	writeJSON(w, map[string]any{
		"people":    out,
		"truncated": len(leads) == limit,
	})
}

// ServePerson — карточка человека и его путь по шагам.
func (h *Handler) ServePerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, _ := h.parseFilter(r, h.now())

	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "нужен числовой id"})
		return
	}

	person, err := h.reader.Person(ctx, id, filter)
	if err != nil {
		// Скрытый аккаунт неотличим от несуществующего намеренно: прямая
		// ссылка не должна быть способом обойти фильтр страницы.
		writeJSONStatus(w, http.StatusNotFound, map[string]any{
			"error": "Такого человека нет. Либо он ещё не запускал бота, либо это скрытый тестовый аккаунт.",
		})
		return
	}

	moments := make([]apiMoment, 0, len(person.Moments))
	for _, m := range person.Moments {
		moments = append(moments, apiMoment{
			Name: m.Name, Label: momentLabel(m.Name),
			SourceID: m.SourceID, MaterialID: m.MaterialID,
			Meta: m.Meta, At: m.OccurredAt.In(moscow).Format("02.01 15:04:05"),
		})
	}

	writeJSON(w, map[string]any{
		"person":  toAPILead(person.Lead),
		"moments": moments,
	})
}

// ServeSources — откуда приходят люди и что каждая метка им отдаёт.
//
// Раньше это были две страницы: «источники» с цифрами и «маршруты» с
// материалами. Разделение выглядело логичным в коде и бессмысленным в
// работе: вопрос «этот Reel окупается?» требует смотреть обе сразу —
// сколько привёл и что именно обещал. Здесь они в одной строке.
func (h *Handler) ServeSources(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, days := h.parseFilter(r, h.now())

	// Цифры считаются по выбранному срезу, а таблица маршрутов — нет:
	// метка существует независимо от того, приходил ли по ней кто-то за
	// последнюю неделю. Иначе метка без трафика исчезала бы ровно тогда,
	// когда важнее всего заметить, что она молчит.
	stats, err := h.reader.Sources(ctx, filter)
	if err != nil {
		h.failJSON(w, "sources", err)
		return
	}
	all, err := h.reader.Sources(ctx, Filter{Hidden: h.hidden})
	if err != nil {
		h.failJSON(w, "all sources", err)
		return
	}

	byID := make(map[string]Source, len(stats))
	for _, s := range stats {
		byID[s.ID] = s
	}
	live := make([]string, 0, len(all))
	for _, s := range all {
		live = append(live, s.ID)
	}

	type apiChannel struct {
		Source      string `json:"source"`
		Started     int    `json:"started"`
		Opened      int    `json:"opened"`
		Offered     int    `json:"offered"`
		Waitlist    int    `json:"waitlist"`
		Material    string `json:"material"`
		Title       string `json:"title"`
		Path        string `json:"path"`
		Fallback    bool   `json:"fallback"`
		AlreadyRead string `json:"alreadyRead"`
		Where       string `json:"where"`
		Why         string `json:"why"`
		DeepLink    string `json:"deepLink"`
	}

	routes := h.catalog.RouteTable(live...)
	out := make([]apiChannel, 0, len(routes))
	for _, rt := range routes {
		link := h.botLink
		if rt.Source != "" && link != "" {
			link += "?start=" + rt.Source
		}
		s := byID[rt.Source]
		out = append(out, apiChannel{
			Source: rt.Source, Started: s.Started, Opened: s.Opened,
			Offered: s.Offered, Waitlist: s.Waitlist,
			Material: rt.Material.ID, Title: rt.Material.Title, Path: rt.Material.Path,
			Fallback: rt.Fallback, AlreadyRead: rt.AlreadyRead,
			Where: rt.Where, Why: rt.Why, DeepLink: link,
		})
	}

	// Сверху те, кто реально приводит людей: молчащие метки нужны, но
	// разбираться каждый день приходится с работающими.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started > out[j].Started })

	type apiMaterial struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Path   string `json:"path"`
		Pitch  string `json:"pitch"`
		Inside string `json:"inside"`
		Button string `json:"button"`
	}
	materials := make([]apiMaterial, 0)
	for _, m := range h.catalog.Materials() {
		materials = append(materials, apiMaterial{
			ID: m.ID, Title: m.Title, Path: m.Path,
			Pitch: m.Pitch, Inside: m.Inside, Button: m.Button,
		})
	}

	writeJSON(w, map[string]any{
		"filter":    apiFilter{Source: filter.Source, Stage: filter.Stage, Days: days},
		"channels":  out,
		"materials": materials,
		"fallback":  h.catalog.Fallback().ID,
		"botLink":   h.botLink,
		"siteBase":  h.siteBase,
	})
}

// ServeScenario — цепочка сообщений: что бот говорит на каждом шаге.
//
// Собирается прогоном настоящего сценария, а не описанием рядом с ним:
// текст берётся из ответа воронки, поэтому страница не может рассказать
// про бота то, чего он не делает.
func (h *Handler) ServeScenario(w http.ResponseWriter, r *http.Request) {
	screens, err := botmap.Scenario(r.Context())
	if err != nil {
		h.failJSON(w, "scenario", err)
		return
	}

	writeJSON(w, map[string]any{
		"screens": screens,
		"steps":   stepNames(),
	})
}

// percent округляет, а не отбрасывает дробь. Целочисленное деление в Go
// давало 66% там, где интерфейс на той же цифре показывал 67%: два разных
// числа про одно и то же на одном экране.
func percent(part, whole int) int {
	if whole == 0 {
		return 0
	}
	return (part*200 + whole) / (whole * 2)
}

func stepNames() []map[string]string {
	out := make([]map[string]string, 0, len(stageOrder))
	for _, s := range stageOrder {
		out = append(out, map[string]string{"name": s.name, "label": s.label, "note": s.note})
	}
	return out
}

func toAPILead(l Lead) apiLead {
	return apiLead{
		TelegramID: l.TelegramID,
		Handle:     handle(l),
		FirstName:  l.FirstName,
		Chat:       ChatLink(l.TelegramID, l.Username),
		FirstSeen:  l.FirstSeen.In(moscow).Format("02.01 15:04"),
		Source:     l.Source,
		Stage:      l.Stage,
		StageLabel: stageLabel(l.Stage),
		Materials:  l.Materials,
		Opened:     l.Opened,
		Waitlist:   l.Waitlist,
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	writeJSONStatus(w, http.StatusOK, body)
}

func writeJSONStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// Ошибку записи чинить нечем: заголовки ушли, соединение оборвано на
	// той стороне. Молча выходим, разбор остаётся в логах вызывающего.
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) failJSON(w http.ResponseWriter, what string, err error) {
	h.log.Error("admin api query failed", "query", what, "error", err)
	writeJSONStatus(w, http.StatusInternalServerError, map[string]any{
		"error": "не удалось прочитать базу",
	})
}

// Проверка на этапе компиляции: каталог обязан уметь отдавать маршруты.
var _ = funnel.Catalog.RouteTable
