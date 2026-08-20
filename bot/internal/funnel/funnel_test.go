package funnel_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/store"
)

const (
	siteBase = "https://site.test"
	linkBase = "https://bot.test"
)

var testUser = funnel.User{TelegramID: 777, Username: "founder", FirstName: "Аня"}

func newFunnel(t *testing.T, db funnel.DB) *funnel.Funnel {
	t.Helper()

	at := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	issued := 0

	f, err := funnel.New(
		db,
		funnel.DefaultCatalog(),
		siteBase,
		linkBase,
		funnel.WithClock(func() time.Time { return at }),
		funnel.WithTokenSource(func() (string, error) {
			issued++
			return fmt.Sprintf("token-%d", issued), nil
		}),
	)
	if err != nil {
		t.Fatalf("funnel.New: %v", err)
	}
	return f
}

func newMemoryFunnel(t *testing.T) (*funnel.Funnel, *store.Memory) {
	t.Helper()

	mem := store.NewMemory()
	return newFunnel(t, mem), mem
}

func eventNames(events []funnel.Event) []string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.Name)
	}
	return names
}

func findEvent(t *testing.T, mem *store.Memory, name string) funnel.Event {
	t.Helper()

	for _, e := range mem.Events() {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("event %q not found, got %v", name, eventNames(mem.Events()))
	return funnel.Event{}
}

func TestStartRecordsUserSourceAndAsksTheQuestion(t *testing.T) {
	f, mem := newMemoryFunnel(t)

	reply, err := f.Start(context.Background(), funnel.StartCommand{
		UpdateID: 1,
		User:     testUser,
		Payload:  "reel_razbor_01",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if reply.Skip {
		t.Fatal("first start must produce a reply")
	}
	// Материал выдаётся сразу: вопрос про состояние приходит потом, когда
	// человек уже что-то получил.
	if !strings.Contains(reply.Text, "Метод 6 × 5") {
		t.Errorf("reply must offer the material, got:\n%s", reply.Text)
	}
	if len(reply.Buttons) != 2 {
		t.Fatalf("want link + escape, got %d", len(reply.Buttons))
	}
	if reply.Buttons[0].URL == "" {
		t.Error("first button must be the tracked link")
	}

	started := findEvent(t, mem, funnel.EventBotStarted)
	if started.SourceID != "reel_razbor_01" {
		t.Errorf("bot_started source = %q, want reel_razbor_01", started.SourceID)
	}
	if started.TelegramID != testUser.TelegramID {
		t.Errorf("bot_started telegram id = %d, want %d", started.TelegramID, testUser.TelegramID)
	}

	history := mem.Attributions(testUser.TelegramID)
	if len(history) != 1 || history[0].SourceID != "reel_razbor_01" {
		t.Fatalf("attribution not saved: %+v", history)
	}

	if _, _, _, ok := mem.User(testUser.TelegramID); !ok {
		t.Error("user must be saved on start")
	}
}

func TestStartKeepsFirstTouchAndFollowsLastTouch(t *testing.T) {
	f, mem := newMemoryFunnel(t)
	ctx := context.Background()

	first := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: "reel_first"}
	if _, err := f.Start(ctx, first); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	second := funnel.StartCommand{UpdateID: 2, User: testUser, Payload: "reel_second"}
	if _, err := f.Start(ctx, second); err != nil {
		t.Fatalf("second Start: %v", err)
	}

	history := mem.Attributions(testUser.TelegramID)
	if len(history) != 2 {
		t.Fatalf("want both touches saved, got %d", len(history))
	}
	if history[0].SourceID != "reel_first" {
		t.Errorf("first touch overwritten: %q", history[0].SourceID)
	}
	if history[1].SourceID != "reel_second" {
		t.Errorf("last touch = %q, want reel_second", history[1].SourceID)
	}

	// Выдача относится к свежему источнику, а не к первому.
	// Второй /start выдаёт разбор уже под свежий источник.
	var last funnel.Event
	for _, e := range mem.Events() {
		if e.Name == funnel.EventMaterialSelected {
			last = e
		}
	}
	if last.SourceID != "reel_second" {
		t.Errorf("material_selected source = %q, want reel_second", last.SourceID)
	}
}

func TestStartWithoutOrWithBrokenPayload(t *testing.T) {
	tests := map[string]struct {
		payload     string
		wantSource  string
		wantInvalid string
	}{
		"без источника":    {payload: "", wantSource: "", wantInvalid: ""},
		"пробелы":          {payload: "   ", wantSource: "", wantInvalid: ""},
		"нормальный":       {payload: "reel-42_A", wantSource: "reel-42_A", wantInvalid: ""},
		"чужие символы":    {payload: "reel 42!", wantSource: "", wantInvalid: "reel 42!"},
		"кириллица":        {payload: "разбор", wantSource: "", wantInvalid: "разбор"},
		"попытка инъекции": {payload: "'; drop table users; --", wantSource: "", wantInvalid: "'; drop table users; --"},
		"слишком длинный":  {payload: strings.Repeat("a", 65), wantSource: "", wantInvalid: strings.Repeat("a", 65)},
		"ровно 64 символа": {payload: strings.Repeat("b", 64), wantSource: strings.Repeat("b", 64), wantInvalid: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f, mem := newMemoryFunnel(t)

			cmd := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: tc.payload}
			if _, err := f.Start(context.Background(), cmd); err != nil {
				t.Fatalf("Start: %v", err)
			}

			started := findEvent(t, mem, funnel.EventBotStarted)
			if started.SourceID != tc.wantSource {
				t.Errorf("source = %q, want %q", started.SourceID, tc.wantSource)
			}
			if got := started.Metadata["invalid_payload"]; got != tc.wantInvalid {
				t.Errorf("invalid_payload = %q, want %q", got, tc.wantInvalid)
			}
		})
	}
}

func TestDuplicateUpdateIsIgnored(t *testing.T) {
	f, mem := newMemoryFunnel(t)
	ctx := context.Background()
	cmd := funnel.StartCommand{UpdateID: 42, User: testUser, Payload: "reel_dup"}

	if _, err := f.Start(ctx, cmd); err != nil {
		t.Fatalf("first delivery: %v", err)
	}

	reply, err := f.Start(ctx, cmd)
	if err != nil {
		t.Fatalf("repeated delivery: %v", err)
	}
	if !reply.Skip {
		t.Error("repeated update must not produce a second message")
	}
	// Один /start пишет два события: запуск и выданный разбор.
	if got := len(mem.Events()); got != 2 {
		t.Errorf("events = %d, want 2: %v", got, eventNames(mem.Events()))
	}
	if got := len(mem.Attributions(testUser.TelegramID)); got != 1 {
		t.Errorf("attributions = %d, want 1", got)
	}
}

func TestStartGivesTrackedLinkAndOpenRecordsClick(t *testing.T) {
	f, mem := newMemoryFunnel(t)
	ctx := context.Background()

	start := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: "reel_x"}
	reply, err := f.Start(ctx, start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Ссылка выдаётся сразу вместе с рекомендацией.
	if len(reply.Buttons) != 2 {
		t.Fatalf("want link + escape, got %d", len(reply.Buttons))
	}
	wantURL := linkBase + "/r/token-1"
	if reply.Buttons[0].URL != wantURL {
		t.Fatalf("button url = %q, want %q", reply.Buttons[0].URL, wantURL)
	}
	// Прямой ссылки на статью в сообщении быть не должно: иначе переход
	// пройдёт мимо счётчика.
	if strings.Contains(reply.Text, siteBase) {
		t.Errorf("reply must not leak a direct article link:\n%s", reply.Text)
	}

	opened, err := f.Open(ctx, "token-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Переход должен приходить на сайт с меткой: иначе в аналитике он
	// попадёт в direct и станет неотличим от прямого захода.
	want := siteBase + "/articles/metod-6x5?utm_campaign=reel_x&utm_medium=bot&utm_source=telegram"
	if opened.Target != want {
		t.Errorf("target = %q, want %q", opened.Target, want)
	}
	// Ровно после первого открытия человеку прилетает вопрос о состоянии.
	if opened.FollowUp == nil {
		t.Fatal("want the stage question after the first open")
	}
	if len(opened.FollowUp.Buttons) != 3 {
		t.Errorf("want three answers, got %d", len(opened.FollowUp.Buttons))
	}

	event := findEvent(t, mem, funnel.EventMaterialOpened)
	if event.SourceID != "reel_x" || event.MaterialID != funnel.MaterialMethod6x5 {
		t.Errorf("material_opened lost context: %+v", event)
	}

	// Повторное открытие — реальный повторный интерес, а не дубль update.
	// Но вопрос второй раз не задаётся.
	again, err := f.Open(ctx, "token-1")
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if again.FollowUp != nil {
		t.Error("the question must be asked once")
	}
	var opens int
	for _, name := range eventNames(mem.Events()) {
		if name == funnel.EventMaterialOpened {
			opens++
		}
	}
	if opens != 2 {
		t.Errorf("material_opened count = %d, want 2", opens)
	}
}

func TestOpenUnknownToken(t *testing.T) {
	f, mem := newMemoryFunnel(t)

	_, err := f.Open(context.Background(), "no-such-token")
	if !errors.Is(err, funnel.ErrUnknownToken) {
		t.Fatalf("err = %v, want ErrUnknownToken", err)
	}
	if got := len(mem.Events()); got != 0 {
		t.Errorf("unknown token must not write events, got %v", eventNames(mem.Events()))
	}
}

func TestAlternativeToUnknownMaterialStillAnswers(t *testing.T) {
	f, _ := newMemoryFunnel(t)
	ctx := context.Background()

	if _, err := f.Start(ctx, funnel.StartCommand{UpdateID: 1, User: testUser}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Каталог из двух материалов: даже на незнакомый идентификатор есть
	// что предложить, человек не остаётся ни с чем.
	cmd := funnel.AlternativeCommand{UpdateID: 2, User: testUser, CurrentMaterialID: "lesson-that-does-not-exist"}
	reply, err := f.Alternative(ctx, cmd)
	if err != nil {
		t.Fatalf("Alternative: %v", err)
	}
	if len(reply.Buttons) == 0 {
		t.Error("want a reply with buttons")
	}
}

func TestAlternativeOffersTheOtherMaterial(t *testing.T) {
	f, mem := newMemoryFunnel(t)
	ctx := context.Background()

	start := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: "reel_y"}
	if _, err := f.Start(ctx, start); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cmd := funnel.AlternativeCommand{UpdateID: 2, User: testUser, CurrentMaterialID: funnel.MaterialMethod6x5}
	reply, err := f.Alternative(ctx, cmd)
	if err != nil {
		t.Fatalf("Alternative: %v", err)
	}

	if len(reply.Buttons) != 2 {
		t.Fatalf("want link + escape, got %d", len(reply.Buttons))
	}
	if got := reply.Buttons[1].Action.MaterialID; got != funnel.MaterialBlueprint50 {
		t.Errorf("alternative material = %q, want %q", got, funnel.MaterialBlueprint50)
	}
	if got := findEvent(t, mem, funnel.EventAlternativeAsked).MaterialID; got != funnel.MaterialMethod6x5 {
		t.Errorf("alternative_asked must record the rejected material, got %q", got)
	}
}

// failingDB имитирует падение базы на конкретной операции.
type failingDB struct {
	inner  funnel.DB
	failOn string
	failed bool
}

func (d *failingDB) Atomically(ctx context.Context, fn func(funnel.Store) error) error {
	return d.inner.Atomically(ctx, func(s funnel.Store) error {
		return fn(&failingStore{Store: s, db: d})
	})
}

type failingStore struct {
	funnel.Store
	db *failingDB
}

func (s *failingStore) AppendEvent(ctx context.Context, e funnel.Event) error {
	if s.db.failOn == e.Name && !s.db.failed {
		s.db.failed = true
		return errors.New("database is down")
	}
	return s.Store.AppendEvent(ctx, e)
}

// Главное свойство: сбой посреди шага не должен съесть лида. Update
// остаётся необработанным, и повтор от Telegram проходит целиком.
func TestFailedStepLeavesUpdateRetryable(t *testing.T) {
	mem := store.NewMemory()
	db := &failingDB{inner: mem, failOn: funnel.EventBotStarted}
	f := newFunnel(t, db)
	ctx := context.Background()
	cmd := funnel.StartCommand{UpdateID: 7, User: testUser, Payload: "reel_z"}

	if _, err := f.Start(ctx, cmd); err == nil {
		t.Fatal("Start must fail while the database is down")
	}
	if got := len(mem.Events()); got != 0 {
		t.Fatalf("failed step must not leave events, got %v", eventNames(mem.Events()))
	}
	if got := len(mem.Attributions(testUser.TelegramID)); got != 0 {
		t.Fatalf("failed step must not leave attributions, got %d", got)
	}

	reply, err := f.Start(ctx, cmd)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if reply.Skip {
		t.Fatal("retry after failure must be processed, not skipped")
	}
	if got := findEvent(t, mem, funnel.EventBotStarted).SourceID; got != "reel_z" {
		t.Errorf("source after retry = %q, want reel_z", got)
	}
}

func TestNewRejectsBrokenConfig(t *testing.T) {
	tests := map[string]struct{ site, link string }{
		"сайт без схемы": {site: "alexeymuzyka.com", link: linkBase},
		"сайт пустой":    {site: "", link: linkBase},
		"бот без хоста":  {site: siteBase, link: "https://"},
		"бот не http":    {site: siteBase, link: "tg://bot"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := funnel.New(store.NewMemory(), funnel.DefaultCatalog(), tc.site, tc.link); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestOpenWithoutSourceStillCarriesCampaign(t *testing.T) {
	f, _ := newMemoryFunnel(t)
	ctx := context.Background()

	if _, err := f.Start(ctx, funnel.StartCommand{UpdateID: 1, User: testUser}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	opened, err := f.Open(ctx, "token-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !strings.Contains(opened.Target, "utm_campaign=direct") {
		t.Errorf("target = %q, want utm_campaign=direct", opened.Target)
	}
	if !strings.Contains(opened.Target, "utm_medium=bot") {
		t.Errorf("target = %q, want utm_medium=bot", opened.Target)
	}
}

// Ни один оффер не должен быть тупиком: если предложение не подошло, у
// человека обязана оставаться кнопка. Предыдущее сообщение к этому
// моменту уже заменено, вернуться иначе некуда.
//
// Выход у оффера ровно один и тот же — уточняющий вопрос. Проверять «есть
// хоть какая-то кнопка с действием» мало: этому условию удовлетворяет и
// «Записать меня», после которого человек снова стоит перед стеной.
func TestNoOfferIsADeadEnd(t *testing.T) {
	cases := map[string]struct {
		stage  funnel.Stage
		escape bool
	}{
		"не выпускает": {funnel.StageNotShipping, true},
		"нет сигнала":  {funnel.StageNoSignal, true},
		// «Другая ситуация» сама и есть тот уточняющий вопрос. Выводить ей
		// некуда, кроме двух состояний; достаточно, чтобы не молчала.
		"другая": {funnel.StageOther, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, _ := newMemoryFunnel(t)
			ctx := context.Background()

			reply, err := f.Start(ctx, funnel.StartCommand{UpdateID: 1, User: testUser})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if _, err := f.Open(ctx, tokenOf(t, reply)); err != nil {
				t.Fatalf("Open: %v", err)
			}

			answer, err := f.AnswerStage(ctx, funnel.StageCommand{UpdateID: 2, User: testUser, Stage: tc.stage})
			if err != nil {
				t.Fatalf("AnswerStage: %v", err)
			}

			var hasAction, hasEscape bool
			for _, b := range answer.Buttons {
				if b.Action.Kind != funnel.ActionNone {
					hasAction = true
				}
				if b.Action.Kind == funnel.ActionStage && b.Action.Stage == funnel.StageOther {
					hasEscape = true
				}
			}

			if tc.escape {
				if !hasEscape {
					t.Errorf("из оффера нет выхода в уточняющий вопрос: %+v", answer.Buttons)
				}
				return
			}
			if !hasAction {
				t.Errorf("у ответа нет ни одной кнопки, ведущей дальше: %+v", answer.Buttons)
			}
		})
	}
}

// tokenOf достаёт токен из кнопки-ссылки.
func tokenOf(t *testing.T, reply funnel.Reply) string {
	t.Helper()
	for _, b := range reply.Buttons {
		if b.URL != "" {
			return b.URL[strings.LastIndex(b.URL, "/")+1:]
		}
	}
	t.Fatal("в ответе нет кнопки-ссылки")
	return ""
}
