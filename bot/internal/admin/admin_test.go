package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

var testNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// stub — база, которую можно предсказать. Запоминает фильтр: половина
// смысла админки в том, что выбранный срез доезжает до запроса неискажённым.
type stub struct {
	got admin.Filter

	stages   map[string]admin.Stage
	segments []admin.Segment
	sources  []admin.Source
	leads    []admin.Lead
	person   admin.Person
	personOK bool
	timeline []admin.TimelineRow
	daily    []admin.Day
	hidden   int
}

func (s *stub) Stages(_ context.Context, f admin.Filter) (map[string]admin.Stage, error) {
	s.got = f
	return s.stages, nil
}

func (s *stub) Segments(_ context.Context, f admin.Filter) ([]admin.Segment, error) {
	s.got = f
	return s.segments, nil
}

func (s *stub) Sources(_ context.Context, f admin.Filter) ([]admin.Source, error) {
	s.got = f
	return s.sources, nil
}

func (s *stub) Leads(_ context.Context, f admin.Filter, _ int) ([]admin.Lead, error) {
	s.got = f
	return s.leads, nil
}

func (s *stub) Person(_ context.Context, _ int64, f admin.Filter) (admin.Person, error) {
	s.got = f
	if !s.personOK {
		return admin.Person{}, admin.ErrNoPerson
	}
	return s.person, nil
}

func (s *stub) Timeline(_ context.Context, f admin.Filter, _ int) ([]admin.TimelineRow, error) {
	s.got = f
	return s.timeline, nil
}

func (s *stub) Daily(_ context.Context, f admin.Filter) ([]admin.Day, error) {
	s.got = f
	return s.daily, nil
}

func (s *stub) HiddenPeople(_ context.Context, f admin.Filter) (int, error) {
	s.got = f
	return s.hidden, nil
}

func handler(t *testing.T, r *stub, opts ...admin.Option) *admin.Handler {
	t.Helper()

	opts = append(opts,
		admin.WithClock(func() time.Time { return testNow }),
		admin.WithCatalog(funnel.DefaultCatalog(), "https://t.me/testbot", "https://example.com"),
	)
	h, err := admin.NewHandler(r, nil, opts...)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

// call дёргает эндпоинт и разбирает ответ.
func call(t *testing.T, r *stub, serve func(*admin.Handler) http.HandlerFunc, target string, opts ...admin.Option) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	rec := httptest.NewRecorder()
	serve(handler(t, r, opts...))(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("ответ не JSON: %v\n%s", err, rec.Body.String())
	}
	return rec, body
}

func overview(h *admin.Handler) http.HandlerFunc { return h.ServeOverview }
func people(h *admin.Handler) http.HandlerFunc   { return h.ServePeople }
func person(h *admin.Handler) http.HandlerFunc   { return h.ServePerson }
func sources(h *admin.Handler) http.HandlerFunc  { return h.ServeSources }
func scenario(h *admin.Handler) http.HandlerFunc { return h.ServeScenario }

func fullFunnel() *stub {
	return &stub{
		stages: map[string]admin.Stage{
			"bot_started":       {Name: "bot_started", People: 10, Events: 12},
			"material_selected": {Name: "material_selected", People: 10, Events: 12},
			"material_opened":   {Name: "material_opened", People: 5, Events: 6},
			"stage_answered":    {Name: "stage_answered", People: 4, Events: 4},
			"offer_shown":       {Name: "offer_shown", People: 3, Events: 3},
			"waitlist_joined":   {Name: "waitlist_joined", People: 2, Events: 2},
		},
		segments: []admin.Segment{
			{Stage: "not_shipping", People: 3, Waitlist: 1},
			{Stage: "", People: 6},
		},
		sources: []admin.Source{
			{ID: "site_metod6x5", Started: 6, Opened: 4, Offered: 4, Waitlist: 1},
			{ID: "", Started: 4},
		},
		leads: []admin.Lead{
			{TelegramID: 763464443, Username: "akhmadullintf", FirstSeen: testNow,
				Source: "site_metod6x5", Stage: "not_shipping", Materials: "blueprint-50m",
				Opened: true, Waitlist: true},
		},
		daily: []admin.Day{{Date: "2026-08-20", People: 10, Opened: 5, Waitlist: 2}},
	}
}

func steps(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()

	out := map[string]map[string]any{}
	list, ok := body["steps"].([]any)
	if !ok {
		t.Fatalf("в ответе нет шагов: %v", body)
	}
	for _, raw := range list {
		s := raw.(map[string]any)
		out[s["name"].(string)] = s
	}
	return out
}

// Доля от предыдущего шага — единственная цифра, по которой видно, где
// именно теряются люди.
func TestStepsCountBothShares(t *testing.T) {
	_, body := call(t, fullFunnel(), overview, "/admin/api/overview")
	s := steps(t, body)

	// Открыли статью: 5 из 10 пришедших.
	if got := s["material_opened"]["fromPrev"]; got != float64(50) {
		t.Errorf("доля с прошлого шага = %v, ожидали 50", got)
	}
	// Записались: 2 из 3 увидевших предложение и 2 из 10 пришедших.
	if got := s["waitlist_joined"]["fromPrev"]; got != float64(67) {
		t.Errorf("доля с прошлого шага = %v, ожидали 67", got)
	}
	if got := s["waitlist_joined"]["fromTop"]; got != float64(20) {
		t.Errorf("доля от запуска = %v, ожидали 20", got)
	}
}

// Проценты округляются, а не отбрасываются: целочисленное деление давало
// 66% там, где интерфейс на той же цифре показывал 67%.
func TestSharesAreRounded(t *testing.T) {
	r := fullFunnel()
	_, body := call(t, r, overview, "/admin/api/overview")

	if got := steps(t, body)["waitlist_joined"]["fromPrev"]; got == float64(66) {
		t.Error("процент отброшен вместо округления: 2 из 3 это 67%, а не 66%")
	}
}

// Первый шаг не с чем сравнивать, и пустая база не должна выглядеть как
// стопроцентная проходимость.
func TestNoShareWithoutABase(t *testing.T) {
	_, body := call(t, fullFunnel(), overview, "/admin/api/overview")
	if got := steps(t, body)["bot_started"]["hasPrev"]; got != false {
		t.Error("у первого шага появилась доля с предыдущего")
	}

	_, empty := call(t, &stub{}, overview, "/admin/api/overview")
	for name, s := range steps(t, empty) {
		if s["hasTop"] != false || s["hasPrev"] != false {
			t.Errorf("на пустой базе у шага %s есть доля: %v", name, s)
		}
	}
}

// Цифры, которые слишком легко прочитать как результат, подписаны.
func TestOfferAndWaitlistAreQualified(t *testing.T) {
	_, body := call(t, fullFunnel(), overview, "/admin/api/overview")
	s := steps(t, body)

	if s["offer_shown"]["note"] != "показ, не переход" {
		t.Error("показ оффера не оговорён")
	}
	if s["waitlist_joined"]["note"] != "интерес, не деньги" {
		t.Error("запись на эфир не оговорена")
	}
}

func TestFilterReachesTheReader(t *testing.T) {
	cases := map[string]struct{ target, source, stage string }{
		"источник":  {"/admin/api/overview?source=site_home", "site_home", ""},
		"состояние": {"/admin/api/overview?stage=no_signal", "", "no_signal"},
		// Пустое поле — тоже срез: «пришли без метки» и «не ответил» это
		// осмысленные группы, и выбрать их нужно уметь.
		"без метки":  {"/admin/api/overview?source=-", "-", ""},
		"не ответил": {"/admin/api/overview?stage=-", "", "-"},
		"вместе":     {"/admin/api/overview?source=site_home&stage=no_signal", "site_home", "no_signal"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := fullFunnel()
			call(t, r, overview, tc.target)

			if r.got.Source != tc.source || r.got.Stage != tc.stage {
				t.Errorf("фильтр = %+v, ожидали source=%q stage=%q", r.got, tc.source, tc.stage)
			}
			if !r.got.Since.IsZero() {
				t.Errorf("период не задавали, а Since = %v", r.got.Since)
			}
		})
	}
}

func TestPeriodIsACohortByArrival(t *testing.T) {
	r := fullFunnel()
	call(t, r, overview, "/admin/api/overview?days=7")

	want := testNow.AddDate(0, 0, -7)
	if !r.got.Since.Equal(want) {
		t.Errorf("Since = %v, ожидали %v", r.got.Since, want)
	}
}

// Период берётся только из известного списка: адрес с мусором не должен
// молча показать срез, которого никто не выбирал.
func TestUnknownPeriodMeansAllTime(t *testing.T) {
	for _, raw := range []string{"1", "365", "abc", "-7", ""} {
		r := fullFunnel()
		call(t, r, overview, "/admin/api/overview?days="+raw)

		if !r.got.Since.IsZero() {
			t.Errorf("days=%q дал период %v, ожидали всё время", raw, r.got.Since)
		}
	}
}

func TestHiddenListReachesEveryEndpoint(t *testing.T) {
	endpoints := map[string]struct {
		serve  func(*admin.Handler) http.HandlerFunc
		target string
	}{
		"обзор":   {overview, "/admin/api/overview"},
		"люди":    {people, "/admin/api/people"},
		"человек": {person, "/admin/api/person?id=1"},
	}

	for name, e := range endpoints {
		t.Run(name, func(t *testing.T) {
			r := fullFunnel()
			r.personOK = true
			r.person = admin.Person{Lead: r.leads[0]}

			call(t, r, e.serve, e.target, admin.WithHidden([]int64{577134700}))

			if len(r.got.Hidden) != 1 || r.got.Hidden[0] != 577134700 {
				t.Errorf("список скрытых не доехал до базы: %v", r.got.Hidden)
			}
		})
	}
}

func TestHiddenCountIsReported(t *testing.T) {
	r := fullFunnel()
	r.hidden = 1

	_, body := call(t, r, overview, "/admin/api/overview", admin.WithHidden([]int64{577134700}))
	if body["hidden"] != float64(1) {
		t.Errorf("скрытые не посчитаны: %v", body["hidden"])
	}
}

func TestUnknownPersonIsNotFound(t *testing.T) {
	rec, body := call(t, fullFunnel(), person, "/admin/api/person?id=1")

	if rec.Code != http.StatusNotFound {
		t.Errorf("код = %d, ожидали 404", rec.Code)
	}
	if body["error"] == nil {
		t.Error("404 без объяснения")
	}
}

func TestPersonWithoutIDIsABadRequest(t *testing.T) {
	rec, _ := call(t, fullFunnel(), person, "/admin/api/person")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("код = %d, ожидали 400", rec.Code)
	}
}

func TestPersonCarriesTheirPath(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{
		Lead: r.leads[0],
		Moments: []admin.Moment{
			{Name: "bot_started", SourceID: "site_metod6x5", OccurredAt: testNow},
			{Name: "stage_answered", Meta: map[string]string{"stage": "not_shipping"}, OccurredAt: testNow},
		},
	}

	_, body := call(t, r, person, "/admin/api/person?id=763464443")

	moments := body["moments"].([]any)
	if len(moments) != 2 {
		t.Fatalf("шагов %d, ожидали 2", len(moments))
	}
	if got := moments[1].(map[string]any)["label"]; got != "Ответили про состояние" {
		t.Errorf("шаг не подписан по-человечески: %v", got)
	}

	// Ссылка на переписку нужна всем, включая тех, у кого нет @имени.
	if got := body["person"].(map[string]any)["chat"]; got != "https://t.me/akhmadullintf" {
		t.Errorf("ссылка на переписку = %v", got)
	}
}

func TestPersonWithoutUsernameStillHasAChatLink(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{Lead: admin.Lead{TelegramID: 811200011, FirstName: "Марина"}}

	_, body := call(t, r, person, "/admin/api/person?id=811200011")
	p := body["person"].(map[string]any)

	if p["chat"] != "tg://user?id=811200011" {
		t.Errorf("ссылка на переписку = %v", p["chat"])
	}
	if p["handle"] != "Марина" {
		t.Errorf("человек без @имени остался без подписи: %v", p["handle"])
	}
}

// Состояние приходит и кодом, и человеческой подписью: код нужен фильтру,
// подпись — глазам, и вычислять её в браузере значит завести вторую
// таблицу переводов.
func TestStagesComeWithLabels(t *testing.T) {
	_, body := call(t, fullFunnel(), overview, "/admin/api/overview")

	labels := map[string]string{}
	for _, raw := range body["segments"].([]any) {
		s := raw.(map[string]any)
		labels[s["stage"].(string)] = s["label"].(string)
	}
	if labels["not_shipping"] != "не выпускает стабильно" {
		t.Errorf("состояние без подписи: %v", labels)
	}
	// Не ответившие остаются сегментом: иначе непонятно, потерялись люди
	// до вопроса или после.
	if labels[""] != "не ответил" {
		t.Errorf("сегмент «не ответил» пропал: %v", labels)
	}
}

// Страница маршрутов собирается из того же каталога, по которому отвечает
// бот, поэтому разойтись с ним она не может.
func TestRoutesMatchTheCatalog(t *testing.T) {
	_, body := call(t, fullFunnel(), sources, "/admin/api/sources")

	bySource := map[string]map[string]any{}
	for _, raw := range body["channels"].([]any) {
		r := raw.(map[string]any)
		bySource[r["source"].(string)] = r
	}

	// Пришедшему со статьи предлагается не она же, а вторая.
	if got := bySource["site_metod6x5"]["material"]; got != "blueprint-50m" {
		t.Errorf("site_metod6x5 отдаёт %v", got)
	}
	if got := bySource["site_metod6x5"]["alreadyRead"]; got != "metod-6x5" {
		t.Errorf("не сказано, что человек уже прочитал: %v", got)
	}
	// Метка без своего правила получает материал по умолчанию — и это
	// должно быть видно, а не выглядеть как настроенный маршрут.
	if got := bySource["site_home"]["fallback"]; got != true {
		t.Errorf("site_home помечен как своё правило: %v", bySource["site_home"])
	}
	// Пустая метка тоже строка: по ней приходят из профиля.
	if _, ok := bySource[""]; !ok {
		t.Error("в таблице нет строки для пришедших без метки")
	}
	if body["fallback"] != "metod-6x5" {
		t.Errorf("материал по умолчанию = %v", body["fallback"])
	}
}

// Цепочка сообщений собирается прогоном настоящего сценария: страница не
// может рассказать про бота то, чего он не делает.
func TestScenarioComesFromTheRealBot(t *testing.T) {
	_, body := call(t, fullFunnel(), scenario, "/admin/api/scenario")

	screens, ok := body["screens"].([]any)
	if !ok || len(screens) < 5 {
		t.Fatalf("цепочка слишком короткая: %v", body["screens"])
	}

	first := screens[0].(map[string]any)
	if first["text"] == "" {
		t.Error("первая реплика пустая")
	}
	if len(first["buttons"].([]any)) == 0 {
		t.Error("у первой реплики нет кнопок")
	}

	// Все три ветки вопроса должны быть показаны: иначе страница врёт о
	// том, что бот отвечает всем одинаково.
	branches := map[string]bool{}
	for _, raw := range screens {
		s := raw.(map[string]any)
		if b, _ := s["branch"].(string); b != "" {
			branches[b] = true
		}
	}
	if len(branches) != 3 {
		t.Errorf("веток показано %d, ожидали три: %v", len(branches), branches)
	}
}

// Постоянные метки видны в админке до первого человека. Иначе проверить
// ссылку можно только после того, как по ней кто-то придёт, а до этого
// непонятно, заведена она вообще или нет.
func TestPermanentSourcesAreVisibleWithoutTraffic(t *testing.T) {
	// Пустая база: ни одного события, ни одной атрибуции.
	_, body := call(t, &stub{}, sources, "/admin/api/sources")

	rows := map[string]map[string]any{}
	for _, raw := range body["channels"].([]any) {
		c := raw.(map[string]any)
		rows[c["source"].(string)] = c
	}

	want := map[string]string{
		"content":  "metod-6x5",
		"pipeline": "blueprint-50m",
	}
	for source, material := range want {
		c, ok := rows[source]
		if !ok {
			t.Errorf("метки %q нет в таблице при нулевом трафике", source)
			continue
		}
		if c["material"] != material {
			t.Errorf("метка %q отдаёт %v, ожидали %q", source, c["material"], material)
		}
		if c["started"] != float64(0) {
			t.Errorf("у метки %q не ноль пришедших: %v", source, c["started"])
		}
		if c["where"] == "" || c["why"] == "" {
			t.Errorf("метка %q без места и причины: %v", source, c)
		}
		if c["deepLink"] == "" {
			t.Errorf("у метки %q нет ссылки, которую можно скопировать", source)
		}
	}

	// Перекрёстные маршруты сайта на месте.
	if got := rows["site_metod6x5"]["material"]; got != "blueprint-50m" {
		t.Errorf("site_metod6x5 отдаёт %v", got)
	}
	if got := rows["site_blueprint50"]["material"]; got != "metod-6x5" {
		t.Errorf("site_blueprint50 отдаёт %v", got)
	}
}

// Живая метка из базы попадает в таблицу, даже если правила для неё нет:
// Reel запускают раньше, чем заводят маршрут.
func TestLiveSourceAppearsInRoutes(t *testing.T) {
	r := fullFunnel()
	r.sources = append(r.sources, admin.Source{ID: "reel_20260820_razbor_01", Started: 2})

	_, body := call(t, r, sources, "/admin/api/sources")

	found := false
	for _, raw := range body["channels"].([]any) {
		if raw.(map[string]any)["source"] == "reel_20260820_razbor_01" {
			found = true
			if raw.(map[string]any)["fallback"] != true {
				t.Error("метка без правила помечена как настроенная")
			}
		}
	}
	if !found {
		t.Error("живая метка Reel не попала в таблицу маршрутов")
	}
}

func TestAnswersAreNotCached(t *testing.T) {
	rec, _ := call(t, fullFunnel(), overview, "/admin/api/overview")

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("личные данные кэшируются: %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
}

// Интерфейс вшит в бинарник. Если он не собрался — это должно быть видно
// сразу, а не белым экраном.
func TestUIIsBuiltIn(t *testing.T) {
	ui, built := admin.NewUI()
	if !built {
		t.Skip("интерфейс не собран: cd bot/admin-ui && npm ci && npm run build")
	}

	for _, path := range []string{"/admin/", "/admin/sources/", "/admin/scenario/", "/admin/people/", "/admin/person/"} {
		rec := httptest.NewRecorder()
		ui.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s → %d", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s отдал пустую страницу", path)
		}
	}

	// Неизвестный адрес внутри админки — опечатка в ссылке. Показываем
	// главную, но кодом 404, чтобы это не выглядело рабочей страницей.
	rec := httptest.NewRecorder()
	ui.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/nonsense/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("неизвестный адрес → %d, ожидали 404", rec.Code)
	}
}
