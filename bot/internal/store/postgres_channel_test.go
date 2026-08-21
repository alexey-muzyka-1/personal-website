package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/store"
)

// Канал проверяется на настоящем Postgres по той же причине, что и
// воронка: ломается здесь не логика на Go, а SQL — upsert, который
// затирает дату подписки, exists, который цепляется не за ту колонку,
// и left join, съедающий дни без событий.

var _ channel.Store = (*store.Postgres)(nil)

func member(id int64, status string) channel.Change {
	return channel.Change{
		Member: channel.Member{TelegramID: id, Username: "u", FirstName: "Имя"},
		Status: status,
		At:     start,
	}
}

func join(t *testing.T, db *store.Postgres, id int64, at time.Time) {
	t.Helper()

	c := member(id, "member")
	c.Event = channel.EventJoined
	c.At = at
	if _, err := db.Save(context.Background(), c); err != nil {
		t.Fatalf("подписка %d: %v", id, err)
	}
}

func leave(t *testing.T, db *store.Postgres, id int64, at time.Time) {
	t.Helper()

	c := member(id, "left")
	c.Event = channel.EventLeft
	c.At = at
	if _, err := db.Save(context.Background(), c); err != nil {
		t.Fatalf("отписка %d: %v", id, err)
	}
}

func TestJoinAndLeaveAreRemembered(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 1, start)
	leave(t, db, 1, start.Add(time.Hour))

	got, found, err := db.ChannelMember(context.Background(), 1)
	if err != nil || !found {
		t.Fatalf("ChannelMember: %v, found=%v", err, found)
	}
	if got.Subscribed {
		t.Error("ушедший остался подписанным")
	}
	if got.JoinedAt == nil || !got.JoinedAt.Equal(start) {
		t.Errorf("дата подписки = %v, хочу %v", got.JoinedAt, start)
	}
	if got.LeftAt == nil {
		t.Error("дата ухода потеряна")
	}
}

// Повторная доставка одного update не должна ни удваивать событие, ни
// сдвигать дату подписки.
func TestRepeatedUpdateIsNotApplied(t *testing.T) {
	db, pool := openDB(t)
	ctx := context.Background()

	c := member(1, "member")
	c.Event, c.UpdateID = channel.EventJoined, 500

	applied, err := db.Save(ctx, c)
	if err != nil || !applied {
		t.Fatalf("первая доставка: %v, applied=%v", err, applied)
	}
	c.At = start.Add(24 * time.Hour)
	applied, err = db.Save(ctx, c)
	if err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if applied {
		t.Error("повторная доставка применилась второй раз")
	}

	var events int
	if err := pool.QueryRow(ctx, `select count(*) from channel_events`).Scan(&events); err != nil {
		t.Fatalf("подсчёт событий: %v", err)
	}
	if events != 1 {
		t.Errorf("событий = %d, хочу одно", events)
	}
}

// Главный случай первой выкатки: сверка узнаёт про старую базу и обязана
// оставить дату подписки пустой. Проставленное «сегодня» нарисовало бы
// прирост на шесть сотен человек за один день.
func TestSyncDoesNotInventAJoinDate(t *testing.T) {
	db, _ := openDB(t)

	if _, err := db.Save(context.Background(), member(1, "member")); err != nil {
		t.Fatalf("сверка: %v", err)
	}

	got, _, err := db.ChannelMember(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelMember: %v", err)
	}
	if !got.Subscribed {
		t.Error("сверка не записала подписку")
	}
	if got.JoinedAt != nil {
		t.Errorf("дата подписки = %v, хочу пустую", got.JoinedAt)
	}
}

// Сверка проходит регулярно и не должна затирать то, что мы узнали из
// события. Дата подписки — единственное, чего у старой базы не будет
// никогда, и терять её у тех, у кого она есть, нельзя.
func TestSyncKeepsAKnownJoinDate(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 1, start)
	if _, err := db.Save(context.Background(), member(1, "member")); err != nil {
		t.Fatalf("сверка: %v", err)
	}

	got, _, err := db.ChannelMember(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelMember: %v", err)
	}
	if got.JoinedAt == nil || !got.JoinedAt.Equal(start) {
		t.Errorf("дата подписки = %v, хочу %v", got.JoinedAt, start)
	}
}

// Вернувшийся подписчик не должен остаться с датой ухода: строка описывает
// сейчас, а история живёт в событиях.
func TestReturningMemberLosesTheLeftDate(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 1, start)
	leave(t, db, 1, start.Add(time.Hour))
	join(t, db, 1, start.Add(2*time.Hour))

	got, _, err := db.ChannelMember(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelMember: %v", err)
	}
	if got.LeftAt != nil {
		t.Errorf("дата ухода = %v, хочу пустую", got.LeftAt)
	}
	if got.JoinedAt == nil || !got.JoinedAt.Equal(start.Add(2*time.Hour)) {
		t.Errorf("дата подписки не обновилась: %v", got.JoinedAt)
	}
}

// Пустое имя из сверки по удалённому аккаунту не должно затирать то, что
// мы уже знаем: иначе в таблице отписок вместо человека остаётся id.
func TestEmptyNameDoesNotOverwriteAKnownOne(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 1, start)
	blank := channel.Change{Member: channel.Member{TelegramID: 1}, Status: "left", At: start}
	if _, err := db.Save(context.Background(), blank); err != nil {
		t.Fatalf("сверка: %v", err)
	}

	got, _, err := db.ChannelMember(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelMember: %v", err)
	}
	if got.Username != "u" {
		t.Errorf("имя = %q, хочу сохранённое", got.Username)
	}
}

// Подписка человека, которого в базе бота нет, обязана записаться. Внешний
// ключ на users отверг бы ровно тех людей, ради которых всё считается.
func TestSubscriberOutsideTheBotIsSaved(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 999, start)
	got, found, err := db.ChannelMember(context.Background(), 999)
	if err != nil || !found {
		t.Fatalf("ChannelMember: %v, found=%v", err, found)
	}
	if got.Lead {
		t.Error("человек не из бота помечен лидом")
	}
}

// Метка источника проставляется по первому касанию в боте: событие должно
// оставаться разбираемым в одиночку.
func TestJoinCarriesTheSourceOfALead(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{id: 1, username: "one", source: "site_metod6x5", events: wholeFunnel})

	join(t, db, 1, start.Add(time.Hour))

	got, _, err := db.ChannelMember(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelMember: %v", err)
	}
	if got.SourceID != "site_metod6x5" {
		t.Errorf("метка = %q", got.SourceID)
	}
	if !got.Lead {
		t.Error("лид не опознан")
	}
}

func TestWatchedCoversBothSides(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool, person{id: 1, username: "one", source: "a", events: []string{"bot_started"}})
	join(t, db, 999, start)

	ids, err := db.Watched(context.Background())
	if err != nil {
		t.Fatalf("Watched: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 999 {
		t.Errorf("следим за %v, хочу и лида, и подписчика мимо бота", ids)
	}
}

func TestKnownReturnsCurrentStatuses(t *testing.T) {
	db, _ := openDB(t)

	join(t, db, 1, start)
	leave(t, db, 2, start)

	known, err := db.Known(context.Background())
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	if known[1] != "member" || known[2] != "left" {
		t.Errorf("статусы = %v", known)
	}
}

// Конверсия обязана раскладывать людей по непересекающимся корзинам, и
// сумма обязана сходиться с числом пришедших. Иначе страница покажет пять
// правдоподобных цифр, которые вместе врут.
func TestConversionBucketsCoverEveryone(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, username: "after", source: "a", events: []string{"bot_started"}},
		person{id: 2, username: "before", source: "a", events: []string{"bot_started"}},
		person{id: 3, username: "undated", source: "a", events: []string{"bot_started"}},
		person{id: 4, username: "gone", source: "a", events: []string{"bot_started"}},
		person{id: 5, username: "never", source: "a", events: []string{"bot_started"}},
	)

	join(t, db, 1, start.Add(time.Hour))     // подписался после бота
	join(t, db, 2, start.Add(-24*time.Hour)) // был подписан раньше
	if _, err := db.Save(context.Background(), member(3, "member")); err != nil {
		t.Fatalf("сверка: %v", err) // подписан, дата неизвестна
	}
	join(t, db, 4, start.Add(time.Hour))
	leave(t, db, 4, start.Add(2*time.Hour))

	got, err := db.ChannelConversion(context.Background(), admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelConversion: %v", err)
	}
	want := admin.ChannelConversion{
		People: 5, AfterStart: 1, BeforeStart: 1, Undated: 1, Gone: 1, Never: 1,
	}
	if got != want {
		t.Errorf("конверсия = %+v, хочу %+v", got, want)
	}
}

func TestChannelSourcesSeparateMeritFromCoincidence(t *testing.T) {
	db, pool := openDB(t)
	seed(t, pool,
		person{id: 1, username: "one", source: "reel_a", events: []string{"bot_started"}},
		person{id: 2, username: "two", source: "reel_a", events: []string{"bot_started"}},
	)

	join(t, db, 1, start.Add(time.Hour))     // заслуга метки
	join(t, db, 2, start.Add(-48*time.Hour)) // был в канале до бота

	sources, err := db.ChannelSources(context.Background(), admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("меток = %d", len(sources))
	}
	got := sources[0]
	if got.Started != 2 || got.Subscribed != 2 || got.AfterStart != 1 {
		t.Errorf("метка = %+v, хочу 2/2/1", got)
	}
}

func TestChannelSummaryCountsMovementAndBase(t *testing.T) {
	db, ctxPool := openDB(t)
	ctx := context.Background()
	_ = ctxPool

	join(t, db, 1, start)
	leave(t, db, 2, start)
	if _, err := db.Save(ctx, member(3, "member")); err != nil {
		t.Fatalf("сверка: %v", err)
	}
	if err := db.SaveSize(ctx, start, 658); err != nil {
		t.Fatalf("SaveSize: %v", err)
	}

	got, err := db.ChannelSummary(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelSummary: %v", err)
	}
	if got.Members != 658 {
		t.Errorf("размер = %d", got.Members)
	}
	if got.Known != 2 || got.Dated != 1 || got.Undated != 1 {
		t.Errorf("известно = %d (%d с датой, %d без)", got.Known, got.Dated, got.Undated)
	}
	if got.Joined != 1 || got.Gone != 1 {
		t.Errorf("движение = +%d/−%d", got.Joined, got.Gone)
	}
	if got.MeasuredAt == nil || !got.MeasuredAt.Equal(start) {
		t.Errorf("время снимка = %v", got.MeasuredAt)
	}
}

// Пустая база — это ноль, а не ошибка: страницу открывают и до первого
// подписчика.
func TestEmptyChannelIsNotAnError(t *testing.T) {
	db, _ := openDB(t)
	ctx := context.Background()

	summary, err := db.ChannelSummary(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelSummary: %v", err)
	}
	if summary.Members != 0 || summary.MeasuredAt != nil {
		t.Errorf("пустой канал = %+v", summary)
	}
	if _, err := db.ChannelConversion(ctx, admin.Filter{}); err != nil {
		t.Fatalf("ChannelConversion: %v", err)
	}
	if _, err := db.ChannelDaily(ctx, admin.Filter{}); err != nil {
		t.Fatalf("ChannelDaily: %v", err)
	}
	if _, err := db.ChannelPeople(ctx, admin.Filter{}, admin.CohortGone, 10); err != nil {
		t.Fatalf("ChannelPeople: %v", err)
	}
	if _, found, err := db.ChannelMember(ctx, 42); err != nil || found {
		t.Errorf("незнакомый человек: found=%v, err=%v", found, err)
	}
}

// Дни без движения обязаны остаться нулями: разрыв в ряду читается как
// «данных нет», хотя на самом деле никто не приходил и не уходил.
func TestChannelDailyKeepsQuietDays(t *testing.T) {
	db, _ := openDB(t)
	ctx := context.Background()

	join(t, db, 1, start)
	join(t, db, 2, start.Add(72*time.Hour))
	if err := db.SaveSize(ctx, start, 100); err != nil {
		t.Fatalf("SaveSize: %v", err)
	}

	days, err := db.ChannelDaily(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelDaily: %v", err)
	}
	if len(days) < 4 {
		t.Fatalf("дней = %d, хочу непрерывный ряд", len(days))
	}
	if days[0].Joined != 1 || days[0].Members != 100 {
		t.Errorf("первый день = %+v", days[0])
	}
	if days[1].Joined != 0 || days[1].Gone != 0 {
		t.Errorf("тихий день = %+v, хочу нули", days[1])
	}
	// Размер в день без снимка не достраивается нулём: канал не
	// схлопывался, мы просто не смотрели.
	if days[1].Members != 0 {
		t.Errorf("размер в день без снимка = %d", days[1].Members)
	}
}

// Последний снимок суток ближе к итогу дня, чем первый.
func TestChannelDailyTakesTheLastSnapshotOfADay(t *testing.T) {
	db, _ := openDB(t)
	ctx := context.Background()

	join(t, db, 1, start)
	for _, s := range []struct {
		at time.Time
		n  int
	}{{start, 100}, {start.Add(3 * time.Hour), 105}} {
		if err := db.SaveSize(ctx, s.at, s.n); err != nil {
			t.Fatalf("SaveSize: %v", err)
		}
	}

	days, err := db.ChannelDaily(ctx, admin.Filter{})
	if err != nil {
		t.Fatalf("ChannelDaily: %v", err)
	}
	if days[0].Members != 105 {
		t.Errorf("размер за день = %d, хочу последний снимок", days[0].Members)
	}
}

func TestChannelCohortsAnswerDifferentQuestions(t *testing.T) {
	db, pool := openDB(t)
	ctx := context.Background()
	seed(t, pool, person{id: 1, username: "lead", source: "a", events: []string{"bot_started"}})

	join(t, db, 1, start)
	leave(t, db, 1, start.Add(time.Hour))
	join(t, db, 999, start)

	gone, err := db.ChannelPeople(ctx, admin.Filter{}, admin.CohortGone, 10)
	if err != nil {
		t.Fatalf("CohortGone: %v", err)
	}
	if len(gone) != 1 || gone[0].TelegramID != 1 || !gone[0].Lead {
		t.Errorf("ушедшие = %+v", gone)
	}

	outside, err := db.ChannelPeople(ctx, admin.Filter{}, admin.CohortOutside, 10)
	if err != nil {
		t.Fatalf("CohortOutside: %v", err)
	}
	if len(outside) != 1 || outside[0].TelegramID != 999 {
		t.Errorf("мимо бота = %+v", outside)
	}

	everyone, err := db.ChannelPeople(ctx, admin.Filter{}, admin.CohortEveryone, 10)
	if err != nil {
		t.Fatalf("CohortEveryone: %v", err)
	}
	if len(everyone) != 2 {
		t.Errorf("всего известно = %d, хочу двоих", len(everyone))
	}
}

// Скрытые аккаунты не должны попадать ни в одну цифру канала — ровно так
// же, как они не попадают в цифры воронки.
func TestHiddenPeopleDisappearFromChannelNumbers(t *testing.T) {
	db, pool := openDB(t)
	ctx := context.Background()
	seed(t, pool, person{id: 7, username: "test", source: "a", events: []string{"bot_started"}})

	join(t, db, 7, start)
	filter := admin.Filter{Hidden: []int64{7}}

	summary, err := db.ChannelSummary(ctx, filter)
	if err != nil {
		t.Fatalf("ChannelSummary: %v", err)
	}
	if summary.Known != 0 || summary.Joined != 0 {
		t.Errorf("скрытый попал в сводку: %+v", summary)
	}

	days, err := db.ChannelDaily(ctx, filter)
	if err != nil {
		t.Fatalf("ChannelDaily: %v", err)
	}
	for _, d := range days {
		if d.Joined != 0 {
			t.Errorf("скрытый попал в день %s", d.Date)
		}
	}
}

// Блокировка снимается и ставится сколько угодно раз, поэтому важен не
// факт события, а последнее из пары. «Заблокировал год назад и вернулся»
// и «заблокировал вчера» — разные люди.
func TestBlockedFlagFollowsTheLastEvent(t *testing.T) {
	db, pool := openDB(t)
	ctx := context.Background()
	seed(t, pool,
		person{id: 1, username: "blocked", source: "a", events: []string{"bot_started"}},
		person{id: 2, username: "returned", source: "a", events: []string{"bot_started"}},
		person{id: 3, username: "quiet", source: "a", events: []string{"bot_started"}},
	)

	blocks := []struct {
		id   int64
		name string
		at   time.Time
	}{
		{1, "bot_blocked", start.Add(time.Hour)},
		{2, "bot_blocked", start.Add(time.Hour)},
		{2, "bot_unblocked", start.Add(2 * time.Hour)},
	}
	for _, b := range blocks {
		_, err := pool.Exec(ctx, `
			insert into events (telegram_id, name, source_id, material_id, metadata, occurred_at)
			values ($1, $2, '', '', '{}'::jsonb, $3)`, b.id, b.name, b.at)
		if err != nil {
			t.Fatalf("вставка %s: %v", b.name, err)
		}
	}

	leads, err := db.Leads(ctx, admin.Filter{}, 10)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	got := map[int64]bool{}
	for _, l := range leads {
		got[l.TelegramID] = l.Blocked
	}
	if !got[1] {
		t.Error("заблокировавший не помечен")
	}
	if got[2] {
		t.Error("вернувшийся остался помеченным")
	}
	if got[3] {
		t.Error("человек без событий блокировки помечен")
	}
}

// Фильтр по каналу должен сужать список людей, а не молча пропускать всех.
// Раньше здесь был бы разрешимый в подзапросе telegram_id, и условие
// всегда оказывалось истинным.
func TestChannelFilterNarrowsThePeopleList(t *testing.T) {
	db, pool := openDB(t)
	ctx := context.Background()
	seed(t, pool,
		person{id: 1, username: "in", source: "a", events: []string{"bot_started"}},
		person{id: 2, username: "out", source: "a", events: []string{"bot_started"}},
	)
	join(t, db, 1, start)

	subscribed, err := db.Leads(ctx, admin.Filter{Channel: "member"}, 10)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(subscribed) != 1 || subscribed[0].TelegramID != 1 {
		t.Errorf("подписанные = %+v", subscribed)
	}
	if !subscribed[0].Subscribed {
		t.Error("подписка не доехала до строки человека")
	}

	rest, err := db.Leads(ctx, admin.Filter{Channel: "-"}, 10)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(rest) != 1 || rest[0].TelegramID != 2 {
		t.Errorf("неподписанные = %+v", rest)
	}

	leave(t, db, 1, start.Add(time.Hour))
	churned, err := db.Leads(ctx, admin.Filter{Channel: "left"}, 10)
	if err != nil {
		t.Fatalf("Leads: %v", err)
	}
	if len(churned) != 1 || !churned[0].Churned {
		t.Errorf("ушедшие = %+v", churned)
	}
}
