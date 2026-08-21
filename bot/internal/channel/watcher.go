package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Store — всё, что наблюдателю нужно от базы.
//
// Единица работы здесь одна — Save, и транзакцию держит она сама. У бота
// это устроено иначе: там funnel сам собирает шаг из нескольких операций
// внутри Atomically. Повторить приём не вышло — Go не даёт одному типу
// два метода Atomically с разными замыканиями, а хранилище воронки уже
// занято. Смысл при этом сохранён: отсечка повторной доставки, состояние
// подписки и событие перехода коммитятся вместе или не коммитятся вовсе.
type Store interface {
	// Save записывает изменение и сообщает, применилось ли оно. false без
	// ошибки означает «такой update уже обработан».
	Save(ctx context.Context, c Change) (applied bool, err error)
	// SaveSize запоминает размер канала на момент времени.
	SaveSize(ctx context.Context, at time.Time, members int) error
	// Known — статусы, которые мы уже знаем. Сверка сравнивает с ними:
	// без этого каждый проход выглядел бы как массовая подписка.
	Known(ctx context.Context) (map[int64]string, error)
	// Watched — за кем следим: лиды бота и все, кого мы когда-либо видели
	// в канале. Первых проверяем, чтобы узнать про подписку, вторых —
	// чтобы не пропустить отписку.
	Watched(ctx context.Context) ([]int64, error)
}

// Telegram — исходящая часть Bot API, которая нужна каналу. Списка
// участников среди этих методов нет, потому что его нет в Bot API.
type Telegram interface {
	ChatMemberCount(ctx context.Context, chat string) (int, error)
	ChatMember(ctx context.Context, chat string, userID int64) (Member, string, error)
}

const (
	defaultSnapshotEvery = 30 * time.Minute
	// Сверка — дорогой проход: один запрос на человека. Раз в шесть часов
	// достаточно, потому что её работа — латать дыры за событиями, а не
	// быть основным источником данных.
	defaultSyncEvery = 6 * time.Hour
	// Пауза между запросами сверки. Bot API держит порядка тридцати
	// запросов в секунду на всё, включая ответы людям; десять в секунду
	// оставляют запас, а проход по нынешней базе занимает около минуты.
	defaultPause = 100 * time.Millisecond
)

// Watcher — единственное место, где живёт замер канала.
type Watcher struct {
	store Store
	tg    Telegram
	chat  Chat
	log   *slog.Logger
	now   func() time.Time

	snapshotEvery time.Duration
	syncEvery     time.Duration
	pause         time.Duration

	// refresh — просьба посмотреть на канал прямо сейчас. Буфер на одну
	// позицию: десять просьб подряд означают ровно один проход.
	refresh chan struct{}
}

type Option func(*Watcher)

func WithClock(now func() time.Time) Option {
	return func(w *Watcher) { w.now = now }
}

// WithIntervals задаёт периодичность снимков и сверки. Нужен тестам:
// ждать полчаса ради проверки цикла никто не будет.
func WithIntervals(snapshot, sync, pause time.Duration) Option {
	return func(w *Watcher) {
		w.snapshotEvery, w.syncEvery, w.pause = snapshot, sync, pause
	}
}

// New собирает наблюдателя. raw — канал в любом виде, в каком он записан
// в конфигурации: @имя, ссылка t.me или числовой id.
func New(store Store, tg Telegram, raw string, log *slog.Logger, opts ...Option) (*Watcher, error) {
	if store == nil || tg == nil {
		return nil, errors.New("store and telegram are required")
	}
	chat, ok := ParseChat(raw)
	if !ok {
		return nil, fmt.Errorf("%q is not a channel: нужно @имя, ссылка t.me/имя или числовой id", raw)
	}
	if log == nil {
		log = slog.Default()
	}

	w := &Watcher{
		store: store, tg: tg, chat: chat, log: log, now: time.Now,
		snapshotEvery: defaultSnapshotEvery,
		syncEvery:     defaultSyncEvery,
		pause:         defaultPause,
		refresh:       make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w, nil
}

// Apply записывает изменение подписки, пришедшее событием.
//
// Чужой канал молча пропускается: бота могут добавить куда угодно, и
// считать это подпиской на личный канал нельзя.
func (w *Watcher) Apply(ctx context.Context, u MemberUpdate) error {
	if !w.chat.same(u.Chat) || u.Member.TelegramID == 0 {
		return nil
	}

	change := Change{
		UpdateID:   u.UpdateID,
		Member:     u.Member,
		Status:     u.NewStatus,
		Event:      transition(u.OldStatus, u.NewStatus),
		InviteLink: u.InviteLink,
		At:         u.At,
	}
	if change.At.IsZero() {
		change.At = w.now()
	}

	applied, err := w.store.Save(ctx, change)
	if err != nil {
		return fmt.Errorf("saving channel change: %w", err)
	}
	if applied && change.Event != "" {
		w.log.Info("channel membership changed",
			"event", change.Event, "telegram_id", change.Member.TelegramID)
	}
	return nil
}

// BotAccessChanged — сменился статус самого бота в канале.
//
// Это не подписка и в замер не идёт. Но снятый с админов бот перестаёт
// получать события молча, и в отчёте это выглядит как «никого не
// прибавилось» — самый дорогой из возможных тихих сбоев, поэтому здесь
// он громкий.
func (w *Watcher) BotAccessChanged(chat Chat, status string) {
	if !w.chat.same(chat) {
		return
	}
	if status == "administrator" || status == "creator" {
		w.log.Info("bot is an admin of the channel", "status", status)
		w.Refresh()
		return
	}
	w.log.Error("bot lost admin rights in the channel: подписки и отписки больше не считаются",
		"status", status)
}

// Refresh просит посмотреть на канал вне расписания.
func (w *Watcher) Refresh() {
	select {
	case w.refresh <- struct{}{}:
	default:
	}
}

// Run ведёт замер до отмены контекста.
//
// Ошибки не возвращаются наружу: канал — это отчёт, а процесс — это
// webhook. Недоступный канал не повод ронять бота, но повод написать в
// лог и оставить в отчёте видимую дыру.
func (w *Watcher) Run(ctx context.Context) {
	w.look(ctx, true)

	snapshots := time.NewTicker(w.snapshotEvery)
	defer snapshots.Stop()
	syncs := time.NewTicker(w.syncEvery)
	defer syncs.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-snapshots.C:
			w.look(ctx, false)
		case <-syncs.C:
			w.look(ctx, true)
		case <-w.refresh:
			w.look(ctx, true)
		}
	}
}

func (w *Watcher) look(ctx context.Context, deep bool) {
	if err := w.Snapshot(ctx); err != nil && ctx.Err() == nil {
		w.log.Error("channel snapshot failed", "error", err)
	}
	if !deep {
		return
	}
	if err := w.Sync(ctx); err != nil && ctx.Err() == nil {
		w.log.Error("channel sync failed", "error", err)
	}
}

// Snapshot запоминает размер канала.
//
// Один запрос, который заодно отвечает на вопрос «а бот вообще ещё
// админ»: если ответа нет, то и снимка нет, и в отчёте видно, что
// последний снимок старый.
func (w *Watcher) Snapshot(ctx context.Context) error {
	count, err := w.tg.ChatMemberCount(ctx, w.chat.ref())
	if err != nil {
		return fmt.Errorf("counting channel members: %w", err)
	}
	if err := w.store.SaveSize(ctx, w.now(), count); err != nil {
		return fmt.Errorf("saving channel size: %w", err)
	}
	return nil
}

// Sync — сверка: проходим по всем, кого знаем, и спрашиваем Telegram, как
// у них дела.
//
// Это единственный способ узнать про людей, которые подписались до того,
// как бот стал админом: даты подписки у них не появится, но сам факт
// появится. Дальше сверка нужна на случай простоя — событие, которое
// Telegram доставлял, пока процесс лежал, иначе потеряется навсегда.
func (w *Watcher) Sync(ctx context.Context) error {
	watched, err := w.store.Watched(ctx)
	if err != nil {
		return fmt.Errorf("reading watched people: %w", err)
	}
	if len(watched) == 0 {
		return nil
	}
	known, err := w.store.Known(ctx)
	if err != nil {
		return fmt.Errorf("reading known statuses: %w", err)
	}

	ref := w.chat.ref()
	var checked, changed, failed int
	for _, id := range watched {
		if err := w.wait(ctx); err != nil {
			return err
		}

		member, status, err := w.tg.ChatMember(ctx, ref, id)
		if err != nil {
			// Один недоступный человек не должен ронять весь проход:
			// удалённый аккаунт или чужой id это не сбой замера.
			failed++
			w.log.Warn("channel member check failed", "telegram_id", id, "error", err)
			continue
		}
		checked++

		change := Change{
			Member:  member,
			Status:  status,
			At:      w.now(),
			Noticed: true,
		}
		if change.Member.TelegramID == 0 {
			change.Member.TelegramID = id
		}
		// Событие пишем только когда сверка увидела то, чего мы не знали.
		// Первый проход по существующей базе не пишет ни одного: у этих
		// людей нет даты подписки, и выдумывать её нельзя.
		if before, seen := known[id]; seen {
			change.Event = transition(before, status)
		}
		if _, err := w.store.Save(ctx, change); err != nil {
			return fmt.Errorf("saving checked member %d: %w", id, err)
		}
		if change.Event != "" {
			changed++
		}
	}

	w.log.Info("channel sync finished",
		"checked", checked, "changed", changed, "failed", failed)
	return nil
}

// wait выдерживает паузу между запросами и умеет прерваться.
func (w *Watcher) wait(ctx context.Context) error {
	if w.pause <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(w.pause)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
