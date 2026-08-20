package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/store"
)

// Запросы страницы воронки проверяются на настоящем Postgres. На моках
// это бессмысленно: ломается тут не логика на Go, а SQL — порядок колонок,
// пустая строка вместо NULL, group by, съедающий строки.
//
// Без TEST_DATABASE_URL тесты пропускаются, чтобы обычный go test ./...
// не требовал базы:
//
//	docker run -d --name pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=funnel \
//	  -p 55432:5432 postgres:17-alpine
//	TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/funnel' go test ./internal/store/

var start = time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

func openDB(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	t.Cleanup(pool.Close)

	// Чистая схема на каждый тест: отчёт считает всех, кто есть в базе,
	// поэтому чужие строки ломают ожидаемые цифры.
	if _, err := pool.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatalf("сброс схемы: %v", err)
	}
	applyMigrations(t, pool)

	db, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("store.NewPostgres: %v", err)
	}
	t.Cleanup(db.Close)

	return db, pool
}

// applyMigrations прогоняет те же файлы, что уезжают на сервер. Схема из
// теста, собранная руками, проверяла бы не то, что работает в проде.
func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("миграции не найдены: %v", err)
	}
	sort.Strings(paths)

	for _, path := range paths {
		sql, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("чтение %s: %v", path, err)
		}
		if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
			t.Fatalf("миграция %s: %v", filepath.Base(path), err)
		}
	}
}

// person — один человек с готовой историей.
type person struct {
	id       int64
	username string
	source   string
	stage    string
	seenAt   time.Time
	events   []string
}

func seed(t *testing.T, pool *pgxpool.Pool, people ...person) {
	t.Helper()
	ctx := context.Background()

	for _, p := range people {
		seenAt := p.seenAt
		if seenAt.IsZero() {
			seenAt = start
		}

		_, err := pool.Exec(ctx, `
			insert into users (telegram_id, username, first_name, first_seen_at, last_seen_at, stage)
			values ($1, $2, $3, $4, $4, $5)`,
			p.id, p.username, "Имя", seenAt, p.stage)
		if err != nil {
			t.Fatalf("вставка пользователя %d: %v", p.id, err)
		}

		_, err = pool.Exec(ctx, `
			insert into attributions (telegram_id, source_id, raw_payload, occurred_at)
			values ($1, $2, $2, $3)`, p.id, p.source, seenAt)
		if err != nil {
			t.Fatalf("вставка атрибуции %d: %v", p.id, err)
		}

		for i, name := range p.events {
			_, err := pool.Exec(ctx, `
				insert into events (telegram_id, name, source_id, material_id, metadata, occurred_at)
				values ($1, $2, $3, $4, $5, $6)`,
				p.id, name, p.source, "metod-6x5",
				map[string]string{"stage": p.stage},
				seenAt.Add(time.Duration(i)*time.Minute))
			if err != nil {
				t.Fatalf("вставка события %s: %v", name, err)
			}
		}
	}
}

var wholeFunnel = []string{
	"bot_started", "material_selected", "material_opened",
	"stage_answered", "offer_shown", "waitlist_joined",
}

// Главная проверка: состояние и источник не должны меняться местами.
// Раньше строки собирались по порядку полей, обе колонки текстовые, и
// подмена проходила молча — на странице «Состояние» была пустой.
func TestLeadKeepsStageAndSourceApart(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{
		id: 1, username: "gость", source: "site_metod6x5",
		stage: "not_shipping", events: wholeFunnel,
	})

	leads, err := db.Leads(context.Background(), admin.Filter{}, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 {
		t.Fatalf("получили %d строк, ожидали одну", len(leads))
	}

	got := leads[0]
	if got.Source != "site_metod6x5" {
		t.Errorf("Source = %q, ожидали метку источника", got.Source)
	}
	if got.Stage != "not_shipping" {
		t.Errorf("Stage = %q, ожидали состояние", got.Stage)
	}
	if !got.Opened || !got.Waitlist {
		t.Errorf("флаги пути потерялись: opened=%v waitlist=%v", got.Opened, got.Waitlist)
	}
	if got.Materials != "metod-6x5" {
		t.Errorf("Materials = %q", got.Materials)
	}
}

func TestHiddenPeopleDisappearFromEveryNumber(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "site_home", stage: "not_shipping", events: wholeFunnel},
		person{id: 577134700, username: "Lesha_Muzyka", source: "site_home",
			stage: "no_signal", events: wholeFunnel},
	)

	ctx := context.Background()
	filter := admin.Filter{Hidden: []int64{577134700}}

	stages, err := db.Stages(ctx, filter)
	if err != nil {
		t.Fatalf("Stages: %v", err)
	}
	if got := stages["bot_started"].People; got != 1 {
		t.Errorf("на первом шаге %d человек, ожидали одного", got)
	}
	if got := stages["bot_started"].Events; got != 1 {
		t.Errorf("событий на первом шаге %d, ожидали одно", got)
	}

	sources, err := db.Sources(ctx, filter)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(sources) != 1 || sources[0].Started != 1 {
		t.Errorf("источники = %+v, ожидали одну метку с одним человеком", sources)
	}

	segments, err := db.Segments(ctx, filter)
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	for _, s := range segments {
		if s.Stage == "no_signal" {
			t.Error("состояние скрытого аккаунта попало в сегменты")
		}
	}

	leads, err := db.Leads(ctx, filter, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 || leads[0].TelegramID != 1 {
		t.Errorf("в списке людей = %+v, ожидали одного не скрытого", leads)
	}

	count, err := db.HiddenPeople(ctx, filter)
	if err != nil {
		t.Fatalf("HiddenPeople: %v", err)
	}
	if count != 1 {
		t.Errorf("скрытых = %d, ожидали 1", count)
	}
}

// Прямая ссылка на карточку не должна показывать того, кто скрыт.
func TestHiddenPersonIsNotReachableByLink(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{id: 577134700, source: "site_home", events: wholeFunnel})

	_, err := db.Person(context.Background(), 577134700, admin.Filter{Hidden: []int64{577134700}})
	if !errors.Is(err, admin.ErrNoPerson) {
		t.Errorf("ошибка = %v, ожидали ErrNoPerson", err)
	}
}

func TestPersonReturnsPathInOrder(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{
		id: 1, username: "kto", source: "site_metod6x5",
		stage: "not_shipping", events: wholeFunnel,
	})

	got, err := db.Person(context.Background(), 1, admin.Filter{})
	if err != nil {
		t.Fatalf("Person: %v", err)
	}
	if got.Username != "kto" || got.Source != "site_metod6x5" || got.Stage != "not_shipping" {
		t.Errorf("карточка собрана неверно: %+v", got.Lead)
	}
	if len(got.Moments) != len(wholeFunnel) {
		t.Fatalf("шагов %d, ожидали %d", len(got.Moments), len(wholeFunnel))
	}
	for i, want := range wholeFunnel {
		if got.Moments[i].Name != want {
			t.Errorf("шаг %d = %q, ожидали %q", i, got.Moments[i].Name, want)
		}
	}
	if got.Moments[0].Meta["stage"] != "not_shipping" {
		t.Errorf("metadata не доехала: %v", got.Moments[0].Meta)
	}
}

func TestUnknownPersonIsAnError(t *testing.T) {
	db, _ := openDB(t)

	_, err := db.Person(context.Background(), 42, admin.Filter{})
	if !errors.Is(err, admin.ErrNoPerson) {
		t.Errorf("ошибка = %v, ожидали ErrNoPerson", err)
	}
}

// Пришедшие без метки — это отдельная группа, а не отсутствие строки.
func TestPeopleWithoutSourceAreTheirOwnRow(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "site_home", events: []string{"bot_started"}},
		person{id: 2, source: "", events: []string{"bot_started"}},
	)
	ctx := context.Background()

	sources, err := db.Sources(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	var empty bool
	for _, s := range sources {
		if s.ID == "" {
			empty = true
		}
	}
	if !empty {
		t.Errorf("нет строки для пришедших без метки: %+v", sources)
	}

	// И по ней можно отфильтроваться.
	leads, err := db.Leads(ctx, admin.Filter{Source: admin.NoValue}, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 || leads[0].TelegramID != 2 {
		t.Errorf("фильтр «без метки» вернул %+v", leads)
	}
}

func TestNotAnsweredIsAFilterableSegment(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "site_home", stage: "not_shipping", events: wholeFunnel},
		person{id: 2, source: "site_home", stage: "", events: []string{"bot_started"}},
	)
	ctx := context.Background()

	segments, err := db.Segments(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	total := 0
	for _, s := range segments {
		total += s.People
	}
	if total != 2 {
		t.Errorf("в сегментах %d человек из 2: %+v", total, segments)
	}

	leads, err := db.Leads(ctx, admin.Filter{Stage: admin.NoValue}, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 || leads[0].TelegramID != 2 {
		t.Errorf("фильтр «не ответил» вернул %+v", leads)
	}
}

func TestSourceFilterNarrowsEveryTable(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "site_home", stage: "not_shipping", events: wholeFunnel},
		person{id: 2, source: "site_metod6x5", stage: "no_signal", events: []string{"bot_started"}},
	)
	ctx := context.Background()
	filter := admin.Filter{Source: "site_home"}

	stages, err := db.Stages(ctx, filter)
	if err != nil {
		t.Fatalf("Stages: %v", err)
	}
	if got := stages["bot_started"].People; got != 1 {
		t.Errorf("шаги не отфильтровались: %d человек", got)
	}
	if got := stages["waitlist_joined"].People; got != 1 {
		t.Errorf("нижний шаг потерялся: %d", got)
	}

	segments, err := db.Segments(ctx, filter)
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if len(segments) != 1 || segments[0].Stage != "not_shipping" {
		t.Errorf("сегменты = %+v", segments)
	}
	if segments[0].Waitlist != 1 {
		t.Errorf("запись на эфир не сосчиталась: %+v", segments[0])
	}
}

// Период — когорта по приходу, а не срез по датам событий: иначе у части
// людей шаги останутся за границей и проценты перестанут сходиться.
func TestPeriodTakesWholePathOfNewcomers(t *testing.T) {
	db, pool := openDB(t)
	old := start.AddDate(0, 0, -30)
	seed(t, pool,
		person{id: 1, source: "site_home", seenAt: old, stage: "no_signal", events: wholeFunnel},
		person{id: 2, source: "site_home", seenAt: start, stage: "not_shipping", events: wholeFunnel},
	)

	filter := admin.Filter{Since: start.AddDate(0, 0, -7)}
	stages, err := db.Stages(context.Background(), filter)
	if err != nil {
		t.Fatalf("Stages: %v", err)
	}
	if got := stages["bot_started"].People; got != 1 {
		t.Errorf("в когорту попало %d человек, ожидали одного", got)
	}
	for _, name := range wholeFunnel {
		if got := stages[name].People; got != 1 {
			t.Errorf("шаг %s = %d, ожидали 1: путь новичка обрезан периодом", name, got)
		}
	}
}

// Пустая база не должна быть ошибкой: страница открывается в первый день,
// когда ещё никто не приходил.
func TestEmptyDatabaseIsNotAnError(t *testing.T) {
	db, _ := openDB(t)
	ctx := context.Background()

	stages, err := db.Stages(ctx, admin.Filter{})
	if err != nil || len(stages) != 0 {
		t.Errorf("Stages: %v, %v", stages, err)
	}
	segments, err := db.Segments(ctx, admin.Filter{})
	if err != nil || len(segments) != 0 {
		t.Errorf("Segments: %v, %v", segments, err)
	}
	sources, err := db.Sources(ctx, admin.Filter{})
	if err != nil || len(sources) != 0 {
		t.Errorf("Sources: %v, %v", sources, err)
	}
	leads, err := db.Leads(ctx, admin.Filter{}, 50)
	if err != nil || len(leads) != 0 {
		t.Errorf("Leads: %v, %v", leads, err)
	}
	if n, err := db.HiddenPeople(ctx, admin.Filter{}); err != nil || n != 0 {
		t.Errorf("HiddenPeople: %d, %v", n, err)
	}
}

// Событие без metadata приходит из базы как {} и не должно ронять сборку.
func TestMomentsSurviveEmptyMetadata(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{id: 1, source: "site_home"})

	_, err := pool.Exec(context.Background(), `
		insert into events (telegram_id, name, occurred_at) values (1, 'bot_started', $1)`, start)
	if err != nil {
		t.Fatalf("вставка события: %v", err)
	}

	got, err := db.Person(context.Background(), 1, admin.Filter{})
	if err != nil {
		t.Fatalf("Person: %v", err)
	}
	if len(got.Moments) != 1 {
		t.Fatalf("шагов %d, ожидали один", len(got.Moments))
	}
	if len(got.Moments[0].Meta) != 0 {
		t.Errorf("пустая metadata приехала как %v", got.Moments[0].Meta)
	}
}

// Ограничение списка людей должно резать самых старых, а не случайных.
func TestLeadsReturnNewestFirst(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "site_home", seenAt: start.AddDate(0, 0, -2)},
		person{id: 2, source: "site_home", seenAt: start},
	)

	leads, err := db.Leads(context.Background(), admin.Filter{}, 1)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 || leads[0].TelegramID != 2 {
		t.Errorf("вернулось %+v, ожидали самого свежего", leads)
	}
}

// Источник человека — первое непустое касание. Повторный /start из другого
// Reel не должен переписывать, откуда он пришёл изначально.
func TestSourceIsTheFirstTouch(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{id: 1, source: "site_metod6x5", events: []string{"bot_started"}})

	_, err := pool.Exec(context.Background(), `
		insert into attributions (telegram_id, source_id, raw_payload, occurred_at)
		values (1, 'reel_42', 'reel_42', $1)`, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("вставка второго касания: %v", err)
	}

	leads, err := db.Leads(context.Background(), admin.Filter{}, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if leads[0].Source != "site_metod6x5" {
		t.Errorf("Source = %q, ожидали первое касание", leads[0].Source)
	}
}

// Метка с кавычкой или процентом не должна ни ломать запрос, ни ловить
// лишние строки: значения уходят параметрами, а не склейкой.
func TestOddSourceLabelIsMatchedLiterally(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, source: "a'b%c", events: []string{"bot_started"}},
		person{id: 2, source: "axbyc", events: []string{"bot_started"}},
	)

	leads, err := db.Leads(context.Background(), admin.Filter{Source: "a'b%c"}, 50)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(leads) != 1 || leads[0].TelegramID != 1 {
		t.Errorf("вернулось %+v, ожидали одну строку", leads)
	}
	if strings.Contains(leads[0].Source, "%") != true {
		t.Errorf("метка исказилась: %q", leads[0].Source)
	}
}
