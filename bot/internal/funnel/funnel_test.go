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
	// Первым делом бот задаёт один вопрос, а не суёт материал: ответ
	// решает, какой разбор подойдёт, и нужен потом для продукта.
	if !strings.Contains(reply.Text, "сам или с командой") {
		t.Errorf("reply must ask the question, got:\n%s", reply.Text)
	}
	if len(reply.Buttons) != 2 {
		t.Fatalf("want two answers, got %d", len(reply.Buttons))
	}
	if got := reply.Buttons[0].Action; got.Kind != funnel.ActionRole || got.Role != funnel.RoleSolo {
		t.Errorf("first button must answer «сам», got %+v", got)
	}
	if got := reply.Buttons[1].Action; got.Kind != funnel.ActionRole || got.Role != funnel.RoleTeam {
		t.Errorf("second button must answer «с командой», got %+v", got)
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
	qualify := funnel.QualifyCommand{UpdateID: 4, User: testUser, Role: funnel.RoleSolo}
	if _, err := f.Qualify(ctx, qualify); err != nil {
		t.Fatalf("Qualify: %v", err)
	}
	if got := findEvent(t, mem, funnel.EventMaterialSelected).SourceID; got != "reel_second" {
		t.Errorf("material_selected source = %q, want reel_second", got)
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
	if got := len(mem.Events()); got != 1 {
		t.Errorf("events = %d, want 1: %v", got, eventNames(mem.Events()))
	}
	if got := len(mem.Attributions(testUser.TelegramID)); got != 1 {
		t.Errorf("attributions = %d, want 1", got)
	}
}

func TestChooseGivesTrackedLinkAndOpenRecordsClick(t *testing.T) {
	f, mem := newMemoryFunnel(t)
	ctx := context.Background()

	start := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: "reel_x"}
	if _, err := f.Start(ctx, start); err != nil {
		t.Fatalf("Start: %v", err)
	}

	reply, err := f.Qualify(ctx, funnel.QualifyCommand{UpdateID: 2, User: testUser, Role: funnel.RoleTeam})
	if err != nil {
		t.Fatalf("Qualify: %v", err)
	}

	// Ссылка выдаётся сразу вместе с рекомендацией: лишней кнопки
	// «Забрать» между ними больше нет.
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

	target, err := f.Open(ctx, "token-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Переход должен приходить на сайт с меткой: иначе в аналитике он
	// попадёт в direct и станет неотличим от прямого захода.
	want := siteBase + "/articles/blueprint-50m?utm_campaign=reel_x&utm_medium=bot&utm_source=telegram"
	if target != want {
		t.Errorf("target = %q, want %q", target, want)
	}

	opened := findEvent(t, mem, funnel.EventMaterialOpened)
	if opened.SourceID != "reel_x" || opened.MaterialID != funnel.MaterialBlueprint50 {
		t.Errorf("material_opened lost context: %+v", opened)
	}

	// Повторное открытие — реальный повторный интерес, а не дубль update.
	if _, err := f.Open(ctx, "token-1"); err != nil {
		t.Fatalf("second Open: %v", err)
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

// Ответ на вопрос решает, какой разбор человек получит. Отдельно
// проверяем поправку: если под его роль подходит ровно то, что он только
// что прочитал на сайте, бот отдаёт второй разбор.
func TestRoleAndSourceDecideTheMaterial(t *testing.T) {
	tests := map[string]struct {
		source    string
		role      funnel.Role
		wantOffer string
	}{
		"сам, без источника":        {source: "", role: funnel.RoleSolo, wantOffer: funnel.MaterialMethod6x5},
		"с командой, без источника": {source: "", role: funnel.RoleTeam, wantOffer: funnel.MaterialBlueprint50},
		"сам, но метод уже прочитан": {
			source: funnel.SourceSiteMethod6x5, role: funnel.RoleSolo, wantOffer: funnel.MaterialBlueprint50,
		},
		"с командой, но блупринт уже прочитан": {
			source: funnel.SourceSiteBlueprint50, role: funnel.RoleTeam, wantOffer: funnel.MaterialMethod6x5,
		},
		"с командой, пришёл из метода": {
			source: funnel.SourceSiteMethod6x5, role: funnel.RoleTeam, wantOffer: funnel.MaterialBlueprint50,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f, mem := newMemoryFunnel(t)
			ctx := context.Background()

			start := funnel.StartCommand{UpdateID: 1, User: testUser, Payload: tc.source}
			if _, err := f.Start(ctx, start); err != nil {
				t.Fatalf("Start: %v", err)
			}

			reply, err := f.Qualify(ctx, funnel.QualifyCommand{UpdateID: 2, User: testUser, Role: tc.role})
			if err != nil {
				t.Fatalf("Qualify: %v", err)
			}
			// Первая кнопка — ссылка, материал несёт вторая.
			if got := reply.Buttons[1].Action.MaterialID; got != tc.wantOffer {
				t.Errorf("offer = %q, want %q", got, tc.wantOffer)
			}
			if got := mem.Role(testUser.TelegramID); got != tc.role {
				t.Errorf("role saved = %v, want %v", got, tc.role)
			}
			answered := findEvent(t, mem, funnel.EventRoleAnswered)
			if got := answered.Metadata["role"]; got != tc.role.String() {
				t.Errorf("event role = %q, want %q", got, tc.role.String())
			}
		})
	}
}

// Роль без ответа — программная ошибка, а не состояние человека.
func TestQualifyWithoutRoleFails(t *testing.T) {
	f, mem := newMemoryFunnel(t)

	cmd := funnel.QualifyCommand{UpdateID: 1, User: testUser, Role: funnel.RoleUnknown}
	if _, err := f.Qualify(context.Background(), cmd); err == nil {
		t.Fatal("want error, got nil")
	}
	if got := len(mem.Events()); got != 0 {
		t.Errorf("failed qualify must not write events, got %v", eventNames(mem.Events()))
	}
}

// Без источника метка перехода всё равно должна быть: иначе такие люди
// молча сливаются в direct и исчезают из отчёта.
func TestOpenWithoutSourceStillCarriesCampaign(t *testing.T) {
	f, _ := newMemoryFunnel(t)
	ctx := context.Background()

	if _, err := f.Start(ctx, funnel.StartCommand{UpdateID: 1, User: testUser}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := f.Qualify(ctx, funnel.QualifyCommand{UpdateID: 2, User: testUser, Role: funnel.RoleSolo}); err != nil {
		t.Fatalf("Qualify: %v", err)
	}

	target, err := f.Open(ctx, "token-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !strings.Contains(target, "utm_campaign=direct") {
		t.Errorf("target = %q, want utm_campaign=direct", target)
	}
	if !strings.Contains(target, "utm_medium=bot") {
		t.Errorf("target = %q, want utm_medium=bot", target)
	}
}
