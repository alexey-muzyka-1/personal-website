package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

var testNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

// stub — база, которую можно предсказать. Запоминает фильтр: половина
// смысла страницы в том, что клик доезжает до запроса неискажённым.
type stub struct {
	got admin.Filter

	stages   map[string]admin.Stage
	segments []admin.Segment
	sources  []admin.Source
	leads    []admin.Lead
	person   admin.Person
	personOK bool
	timeline []admin.TimelineRow
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

func (s *stub) HiddenPeople(_ context.Context, f admin.Filter) (int, error) {
	s.got = f
	return s.hidden, nil
}

func get(t *testing.T, r *stub, target string, opts ...admin.Option) (*httptest.ResponseRecorder, string) {
	t.Helper()

	opts = append(opts, admin.WithClock(func() time.Time { return testNow }))
	h, err := admin.NewHandler(r, nil, opts...)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return rec, string(body)
}

func fullFunnel() *stub {
	return &stub{
		stages: map[string]admin.Stage{
			"bot_started":       {Name: "bot_started", People: 10, Events: 12},
			"material_selected": {Name: "material_selected", People: 10, Events: 12},
			"material_opened":   {Name: "material_opened", People: 5, Events: 6},
			"stage_answered":    {Name: "stage_answered", People: 4, Events: 4},
			"offer_shown":       {Name: "offer_shown", People: 4, Events: 4},
			"waitlist_joined":   {Name: "waitlist_joined", People: 1, Events: 1},
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
	}
}

// Доля от предыдущего шага — единственная цифра, по которой видно, где
// именно теряются люди. Доля от запуска на нижних шагах мала у всех сразу.
func TestStepsCountBothShares(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	// Открыли статью: 5 из 10 пришедших, они же 50% от предыдущего шага.
	if !strings.Contains(body, "50%") {
		t.Error("нет доли открывших статью")
	}
	// Записались: 1 из 4 увидевших предложение — 25% с прошлого шага,
	// и 10% от всех пришедших.
	for _, want := range []string{"25%", "10%"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет доли %s", want)
		}
	}
}

// Первый шаг не с чем сравнивать: доля «с прошлого» у него отсутствует, а
// не равна ста процентам.
func TestFirstStepHasNoPreviousShare(t *testing.T) {
	r := &stub{stages: map[string]admin.Stage{"bot_started": {Name: "bot_started", People: 7}}}
	_, body := get(t, r, "/admin")

	if !strings.Contains(body, "—") {
		t.Error("у первого шага должен быть прочерк вместо доли с прошлого шага")
	}
}

// Цифры, которые слишком легко прочитать как результат, подписаны.
func TestOfferAndWaitlistAreQualified(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	for _, want := range []string{"показ, не переход", "интерес, не деньги", "Платного шага в воронке пока нет"} {
		if !strings.Contains(body, want) {
			t.Errorf("страница не оговаривает %q", want)
		}
	}
}

func TestFilterReachesTheReader(t *testing.T) {
	cases := map[string]struct {
		target string
		want   admin.Filter
	}{
		"источник": {"/admin?source=site_home", admin.Filter{Source: "site_home"}},
		"состояние": {
			"/admin?stage=no_signal",
			admin.Filter{Stage: "no_signal"},
		},
		// Пустое поле — тоже срез: «пришли без метки» и «не ответил» это
		// осмысленные группы, и кликнуть по ним нужно уметь.
		"без метки":  {"/admin?source=-", admin.Filter{Source: "-"}},
		"не ответил": {"/admin?stage=-", admin.Filter{Stage: "-"}},
		"вместе": {
			"/admin?source=site_home&stage=no_signal",
			admin.Filter{Source: "site_home", Stage: "no_signal"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := fullFunnel()
			get(t, r, tc.target)

			if r.got.Source != tc.want.Source || r.got.Stage != tc.want.Stage {
				t.Errorf("фильтр = %+v, ожидали source=%q stage=%q", r.got, tc.want.Source, tc.want.Stage)
			}
			if !r.got.Since.IsZero() {
				t.Errorf("период не задавали, а Since = %v", r.got.Since)
			}
		})
	}
}

func TestPeriodIsACohortByArrival(t *testing.T) {
	r := fullFunnel()
	get(t, r, "/admin?days=7")

	want := testNow.AddDate(0, 0, -7)
	if !r.got.Since.Equal(want) {
		t.Errorf("Since = %v, ожидали %v", r.got.Since, want)
	}
}

// Период берётся только из известного списка: иначе адрес с мусором
// молча покажет срез, которого никто не выбирал.
func TestUnknownPeriodMeansAllTime(t *testing.T) {
	for _, raw := range []string{"1", "365", "abc", "-7", ""} {
		r := fullFunnel()
		get(t, r, "/admin?days="+raw)

		if !r.got.Since.IsZero() {
			t.Errorf("days=%q дал период %v, ожидали всё время", raw, r.got.Since)
		}
	}
}

// Клик по второму фильтру не должен сбрасывать первый: иначе до среза
// «этот источник за эту неделю» невозможно добраться кликами.
func TestFiltersCombineInLinks(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin?days=7&source=site_metod6x5")

	want := "/admin?days=7&amp;source=site_metod6x5&amp;stage=not_shipping"
	if !strings.Contains(body, want) {
		t.Errorf("клик по состоянию не сохраняет период и источник, ждали %s", want)
	}
}

// По выбранной строке можно кликнуть второй раз и выйти из среза.
func TestSelectedRowUnsetsItsFilter(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin?source=site_metod6x5")

	// aria-current, а не aria-selected: выбранность строки вне грида —
	// невалидная ARIA, а «текущий фильтр» это ровно aria-current.
	// Ссылка ведёт на /admin без параметров: это и есть снятие фильтра.
	if !strings.Contains(body, `<a class="row-link" href="/admin" aria-current="true">`) {
		t.Error("повторный клик по выбранной строке не снимает фильтр и не помечен для скринридера")
	}
	if !strings.Contains(body, `class="picked"`) {
		t.Error("выбранная строка не отмечена визуально")
	}
}

func TestHiddenAccountsAreDeclared(t *testing.T) {
	r := fullFunnel()
	r.hidden = 1

	_, body := get(t, r, "/admin", admin.WithHidden([]int64{577134700}))

	if !strings.Contains(body, "скрыт 1 тестовый аккаунт") {
		t.Error("страница молча выкидывает строки, не сказав об этом")
	}
	if len(r.got.Hidden) != 1 || r.got.Hidden[0] != 577134700 {
		t.Errorf("список скрытых не доехал до базы: %v", r.got.Hidden)
	}
}

// Ничего не скрыто — и подписи нет: постоянная строчка «скрыто 0» это шум.
func TestNothingHiddenSaysNothing(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	if strings.Contains(body, "скрыт") {
		t.Error("подпись про скрытых появилась там, где никого не скрывали")
	}
}

// Карточка человека тоже уважает список скрытых: прямая ссылка не должна
// быть способом обойти фильтр страницы.
func TestPersonCardCarriesHiddenList(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{Lead: r.leads[0]}

	get(t, r, "/admin?id=763464443", admin.WithHidden([]int64{577134700}))

	if len(r.got.Hidden) != 1 {
		t.Errorf("карточка запрошена без списка скрытых: %v", r.got.Hidden)
	}
}

func TestUnknownPersonIsNotFound(t *testing.T) {
	rec, _ := get(t, fullFunnel(), "/admin?id=1")

	if rec.Code != http.StatusNotFound {
		t.Errorf("код = %d, ожидали 404", rec.Code)
	}
}

func TestPersonCardShowsTheirPath(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{
		Lead: r.leads[0],
		Moments: []admin.Moment{
			{Name: "bot_started", SourceID: "site_metod6x5", OccurredAt: testNow},
			{Name: "stage_answered", Meta: map[string]string{"stage": "not_shipping"}, OccurredAt: testNow},
		},
	}

	_, body := get(t, r, "/admin?id=763464443")

	for _, want := range []string{"Запустили бота", "Ответили про состояние", "stage=not_shipping", "ко всем"} {
		if !strings.Contains(body, want) {
			t.Errorf("в карточке нет %q", want)
		}
	}
}

// Состояние и источник — разные вещи и живут в разных колонках. Раньше
// они собирались из базы по порядку полей и молча менялись местами.
func TestStageAndSourceDoNotSwap(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	stage := strings.Index(body, "не выпускает стабильно")
	if stage < 0 {
		t.Fatal("состояние не показано человеческим текстом")
	}
	if !strings.Contains(body, "site_metod6x5") {
		t.Error("метка источника не показана")
	}
	// «not_shipping» голым кодом на странице означает, что в колонку
	// состояния попало что-то нераспознанное.
	if strings.Contains(body, "<code>not_shipping</code>") {
		t.Error("состояние отрисовано как метка источника")
	}
}

// Не ответившие остаются в таблице: иначе непонятно, потерялись люди до
// вопроса или после него.
func TestUnansweredStageIsASegment(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	if !strings.Contains(body, "не ответил") {
		t.Error("сегмент «не ответил» пропал из таблицы")
	}
}

func TestListSaysWhenItIsTruncated(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin")

	// Пришло 10, в списке одна строка — это должно быть сказано вслух.
	if !strings.Contains(body, "Показаны последние 1 из 10") {
		t.Error("обрезанный список не подписан")
	}
}
