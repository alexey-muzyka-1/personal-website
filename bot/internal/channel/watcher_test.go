package channel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
)

var testNow = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// fakeStore запоминает всё, что записали: половина смысла замера в том,
// что событие доезжает до базы неискажённым.
type fakeStore struct {
	saved []channel.Change
	sizes []int
	known map[int64]string
	watch []int64
	// seen — какие update_id считать уже обработанными.
	seen map[int64]bool
	err  error
}

func (f *fakeStore) Save(_ context.Context, c channel.Change) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if c.UpdateID != 0 && f.seen[c.UpdateID] {
		return false, nil
	}
	f.saved = append(f.saved, c)
	return true, nil
}

func (f *fakeStore) SaveSize(_ context.Context, _ time.Time, members int) error {
	f.sizes = append(f.sizes, members)
	return nil
}

func (f *fakeStore) Known(_ context.Context) (map[int64]string, error) {
	if f.known == nil {
		return map[int64]string{}, nil
	}
	return f.known, nil
}

func (f *fakeStore) Watched(_ context.Context) ([]int64, error) { return f.watch, nil }

// fakeTelegram отвечает заранее заданными статусами.
type fakeTelegram struct {
	count    int
	statuses map[int64]string
	asked    []int64
	fail     map[int64]bool
	countErr error
}

func (f *fakeTelegram) ChatMemberCount(_ context.Context, _ string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.count, nil
}

func (f *fakeTelegram) ChatMember(_ context.Context, _ string, userID int64) (channel.Member, string, error) {
	f.asked = append(f.asked, userID)
	if f.fail[userID] {
		return channel.Member{}, "", errors.New("user not found")
	}
	status, ok := f.statuses[userID]
	if !ok {
		status = "left"
	}
	return channel.Member{TelegramID: userID, Username: "u" + status}, status, nil
}

func watcher(t *testing.T, store channel.Store, tg channel.Telegram) *channel.Watcher {
	t.Helper()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := channel.New(store, tg, "https://t.me/alexeymuzykablog", quiet,
		channel.WithClock(func() time.Time { return testNow }),
		// Пауза между запросами в тесте не нужна: проверяется порядок
		// вопросов, а не вежливость к чужому серверу.
		channel.WithIntervals(time.Hour, time.Hour, 0),
	)
	if err != nil {
		t.Fatalf("channel.New: %v", err)
	}
	return w
}

func joined(id int64, from, to string) channel.MemberUpdate {
	return channel.MemberUpdate{
		UpdateID:  100,
		Chat:      channel.Chat{ID: -1001234567890, Username: "alexeymuzykablog"},
		Member:    channel.Member{TelegramID: id, Username: "akhmadullintf"},
		OldStatus: from,
		NewStatus: to,
		At:        testNow,
	}
}

func TestJoinIsRecordedAsAnEvent(t *testing.T) {
	store, tg := &fakeStore{}, &fakeTelegram{}

	if err := watcher(t, store, tg).Apply(context.Background(), joined(763464443, "left", "member")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("записей = %d, хочу одну", len(store.saved))
	}
	got := store.saved[0]
	if got.Event != channel.EventJoined {
		t.Errorf("событие = %q, хочу %q", got.Event, channel.EventJoined)
	}
	if !got.At.Equal(testNow) {
		t.Errorf("время = %v, хочу время события", got.At)
	}
	if got.Noticed {
		t.Error("событие от Telegram помечено приблизительным")
	}
}

func TestLeaveIsRecorded(t *testing.T) {
	store := &fakeStore{}

	if err := watcher(t, store, &fakeTelegram{}).Apply(context.Background(), joined(1, "member", "left")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if store.saved[0].Event != channel.EventLeft {
		t.Errorf("событие = %q", store.saved[0].Event)
	}
}

// Повышение подписчика до админа это не новая подписка. Иначе каждый
// такой случай прибавлял бы каналу человека, которого там уже считали.
func TestPromotionIsNotAJoin(t *testing.T) {
	store := &fakeStore{}

	if err := watcher(t, store, &fakeTelegram{}).Apply(context.Background(), joined(1, "member", "administrator")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("записей = %d", len(store.saved))
	}
	if store.saved[0].Event != "" {
		t.Errorf("событие = %q, хочу пустое", store.saved[0].Event)
	}
	if store.saved[0].Status != "administrator" {
		t.Errorf("статус не обновился: %q", store.saved[0].Status)
	}
}

// Бота могут добавить в любой чат. События оттуда не должны попадать в
// цифры личного канала.
func TestForeignChatIsIgnored(t *testing.T) {
	store := &fakeStore{}

	update := joined(1, "left", "member")
	update.Chat = channel.Chat{ID: -100999, Username: "someone_else"}
	if err := watcher(t, store, &fakeTelegram{}).Apply(context.Background(), update); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("чужой чат записан: %+v", store.saved)
	}
}

// Канал задан именем, а событие приходит с числовым id и именем. Сверка
// должна ловить его по имени, иначе замер молчит.
func TestChannelMatchesByUsernameWhenConfiguredByName(t *testing.T) {
	store := &fakeStore{}

	update := joined(1, "left", "member")
	update.Chat = channel.Chat{ID: -100777, Username: "AlexeyMuzykaBlog"}
	if err := watcher(t, store, &fakeTelegram{}).Apply(context.Background(), update); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("событие своего канала потеряно: %+v", store.saved)
	}
}

func TestRepeatedDeliveryIsNotCountedTwice(t *testing.T) {
	store := &fakeStore{seen: map[int64]bool{100: true}}

	if err := watcher(t, store, &fakeTelegram{}).Apply(context.Background(), joined(1, "left", "member")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(store.saved) != 0 {
		t.Errorf("повтор записан: %+v", store.saved)
	}
}

func TestSnapshotSavesTheSize(t *testing.T) {
	store, tg := &fakeStore{}, &fakeTelegram{count: 658}

	if err := watcher(t, store, tg).Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(store.sizes) != 1 || store.sizes[0] != 658 {
		t.Errorf("снимки = %v, хочу [658]", store.sizes)
	}
}

// Главный случай первой выкатки: в канале уже есть люди, а событий про
// них не было и не будет. Сверка обязана узнать про них факт подписки и
// не выдумать дату — иначе весь прирост дня окажется липовым.
func TestFirstSyncLearnsMembersWithoutInventingDates(t *testing.T) {
	store := &fakeStore{watch: []int64{1, 2}}
	tg := &fakeTelegram{statuses: map[int64]string{1: "member", 2: "left"}}

	if err := watcher(t, store, tg).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.saved) != 2 {
		t.Fatalf("записей = %d, хочу две", len(store.saved))
	}
	for _, c := range store.saved {
		if c.Event != "" {
			t.Errorf("первая сверка выдумала событие %q для %d", c.Event, c.Member.TelegramID)
		}
		if c.UpdateID != 0 {
			t.Errorf("у сверки не должно быть update_id, есть %d", c.UpdateID)
		}
	}
	if store.saved[0].Status != "member" || store.saved[1].Status != "left" {
		t.Errorf("статусы разъехались: %+v", store.saved)
	}
}

// Отписка, случившаяся при лежащем процессе, приходит только сверкой.
// Она обязана дойти до события, но с пометкой «время приблизительное».
func TestSyncNoticesWhatHappenedWhileWeWereDown(t *testing.T) {
	store := &fakeStore{watch: []int64{1}, known: map[int64]string{1: "member"}}
	tg := &fakeTelegram{statuses: map[int64]string{1: "left"}}

	if err := watcher(t, store, tg).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("записей = %d", len(store.saved))
	}
	got := store.saved[0]
	if got.Event != channel.EventLeft {
		t.Errorf("событие = %q, хочу %q", got.Event, channel.EventLeft)
	}
	if !got.Noticed {
		t.Error("замеченное сверкой не помечено приблизительным")
	}
}

// Повторная сверка по тем же людям не должна плодить события: иначе
// график рисовал бы подписку каждые шесть часов.
func TestRepeatedSyncIsQuiet(t *testing.T) {
	store := &fakeStore{watch: []int64{1}, known: map[int64]string{1: "member"}}
	tg := &fakeTelegram{statuses: map[int64]string{1: "member"}}

	if err := watcher(t, store, tg).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if store.saved[0].Event != "" {
		t.Errorf("сверка выдумала событие %q", store.saved[0].Event)
	}
}

// Один недоступный человек не должен ронять весь проход: удалённый
// аккаунт это обычное дело, а не сбой замера.
func TestSyncSurvivesOneBadUser(t *testing.T) {
	store := &fakeStore{watch: []int64{1, 2, 3}}
	tg := &fakeTelegram{
		statuses: map[int64]string{1: "member", 3: "member"},
		fail:     map[int64]bool{2: true},
	}

	if err := watcher(t, store, tg).Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(tg.asked) != 3 {
		t.Errorf("спросили про %v, хочу про всех троих", tg.asked)
	}
	if len(store.saved) != 2 {
		t.Errorf("записей = %d, хочу две", len(store.saved))
	}
}

func TestParseChat(t *testing.T) {
	cases := []struct {
		raw      string
		ok       bool
		id       int64
		username string
	}{
		{raw: "https://t.me/alexeymuzykablog", ok: true, username: "alexeymuzykablog"},
		{raw: "t.me/alexeymuzykablog/", ok: true, username: "alexeymuzykablog"},
		{raw: "@alexeymuzykablog", ok: true, username: "alexeymuzykablog"},
		{raw: "alexeymuzykablog", ok: true, username: "alexeymuzykablog"},
		{raw: "-1001234567890", ok: true, id: -1001234567890},
		// Приглашение в приватный канал каналом не является: по нему
		// нельзя ни спросить размер, ни проверить человека.
		{raw: "https://t.me/+AbCdEf", ok: false},
		{raw: "", ok: false},
		{raw: "https://t.me/", ok: false},
		// Положительное число — чей-то личный id, а не канал.
		{raw: "1234567890", ok: false},
	}

	for _, c := range cases {
		chat, ok := channel.ParseChat(c.raw)
		if ok != c.ok {
			t.Errorf("ParseChat(%q) ok = %v, хочу %v", c.raw, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if chat.ID != c.id || chat.Username != c.username {
			t.Errorf("ParseChat(%q) = %+v", c.raw, chat)
		}
	}
}

func TestWatcherNeedsARealChannel(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := channel.New(&fakeStore{}, &fakeTelegram{}, "https://t.me/+AbCdEf", quiet); err == nil {
		t.Error("want error for an invite link")
	}
}
