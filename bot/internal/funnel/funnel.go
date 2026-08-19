package funnel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrUnknownToken = errors.New("unknown link token")

// Funnel — единственное место, где живёт сценарий тикета 01.
type Funnel struct {
	db         DB
	catalog    Catalog
	siteBase   string
	linkBase   string
	channelURL string
	now        func() time.Time
	newToken   func() (string, error)
}

// Option — подменяемая деталь. Часы и генератор токенов вынесены, чтобы
// тесты не зависели от времени и случайности.
type Option func(*Funnel)

func WithClock(now func() time.Time) Option {
	return func(f *Funnel) { f.now = now }
}

func WithTokenSource(newToken func() (string, error)) Option {
	return func(f *Funnel) { f.newToken = newToken }
}

// WithChannel задаёт канал, который бот предлагает после полученной
// ценности. Не задан — предложения канала просто нет.
func WithChannel(url string) Option {
	return func(f *Funnel) { f.channelURL = url }
}

// New собирает воронку.
//
// siteBase — публичный адрес сайта, где лежат статьи.
// linkBase — адрес самого бота, на котором живёт tracked redirect.
// Оба проверяются здесь: неверный адрес должен ломать старт процесса,
// а не превращаться в битую ссылку в чате у человека.
func New(db DB, catalog Catalog, siteBase, linkBase string, opts ...Option) (*Funnel, error) {
	if db == nil {
		return nil, errors.New("db is required")
	}
	if len(catalog.order) == 0 {
		return nil, errors.New("catalog is required")
	}
	site, err := absoluteBase(siteBase)
	if err != nil {
		return nil, fmt.Errorf("site base url: %w", err)
	}
	link, err := absoluteBase(linkBase)
	if err != nil {
		return nil, fmt.Errorf("link base url: %w", err)
	}

	f := &Funnel{
		db:       db,
		catalog:  catalog,
		siteBase: site,
		linkBase: link,
		now:      time.Now,
		newToken: randomToken,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

// StartCommand — /start, с payload из deep link или без него.
type StartCommand struct {
	UpdateID int64
	User     User
	Payload  string
}

// AlternativeCommand — «мне это не подходит» для показанного материала.
type AlternativeCommand struct {
	UpdateID          int64
	User              User
	CurrentMaterialID string
}

// Telegram разрешает в start payload только эти символы, не длиннее 64.
// Всё остальное — чужой мусор или попытка подсунуть лишнее.
var sourceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// step — каркас любого шага: одна транзакция, отсечка повторной доставки,
// отметка активности человека.
//
// Сам сценарий живёт в fn и не знает ни про транзакции, ни про повторы.
// Ответ отдаётся только при успешном коммите: сообщение о шаге, который
// не записан, — худшее, что можно послать человеку.
func (f *Funnel) step(
	ctx context.Context,
	updateID int64,
	user User,
	fn func(s Store, at time.Time) (Reply, error),
) (Reply, error) {
	var reply Reply

	err := f.db.Atomically(ctx, func(s Store) error {
		seen, err := s.MarkUpdate(ctx, updateID)
		if err != nil {
			return fmt.Errorf("marking update %d: %w", updateID, err)
		}
		if seen {
			reply = Reply{Skip: true}
			return nil
		}

		at := f.now()
		if err := s.SaveUser(ctx, user, at); err != nil {
			return fmt.Errorf("saving user %d: %w", user.TelegramID, err)
		}

		reply, err = fn(s, at)
		return err
	})
	if err != nil {
		return Reply{}, err
	}
	return reply, nil
}

// Start — вход в воронку: запоминаем человека и источник, показываем один
// рекомендованный материал.
func (f *Funnel) Start(ctx context.Context, cmd StartCommand) (Reply, error) {
	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, raw := normalizeSource(cmd.Payload)

		// Атрибуция пишется на каждый /start, даже повторный и даже
		// пустой: первое касание останется первым, а новый источник не
		// затрёт историю.
		attribution := Attribution{
			TelegramID: cmd.User.TelegramID,
			SourceID:   sourceID,
			RawPayload: raw,
			OccurredAt: at,
		}
		if err := s.AppendAttribution(ctx, attribution); err != nil {
			return Reply{}, fmt.Errorf("appending attribution: %w", err)
		}

		meta := map[string]string{}
		if raw != "" && sourceID == "" {
			// Источник был, но нечитаемый. Молча терять такое нельзя:
			// это сломанный deep link, а не человек без источника.
			meta["invalid_payload"] = raw
		}
		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventBotStarted,
			SourceID:   sourceID,
			Metadata:   meta,
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventBotStarted, err)
		}

		m := f.catalog.ForSource(sourceID)
		return f.offer(ctx, s, cmd.User, m, sourceID, lines(
			"Привет, это Лёша.",
			"",
			"Выкладываю разборы того, как мы каждый день выпускаем короткие видео. Статьи целиком, без подписки и почты.",
			"",
			"Начал бы с этого:",
		), at)
	})
}

// offer — единственное место, где человеку выдаётся материал.
//
// Раньше между рекомендацией и ссылкой стояла лишняя кнопка «Забрать».
// Она ничего не решала: человек уже сказал, что ему нужно. Ссылка идёт
// сразу, а событие выбора пишется здесь же.
func (f *Funnel) offer(
	ctx context.Context,
	s Store,
	user User,
	m Material,
	sourceID string,
	lead string,
	at time.Time,
) (Reply, error) {
	token, err := f.newToken()
	if err != nil {
		return Reply{}, fmt.Errorf("creating link token: %w", err)
	}
	link := Link{
		Token:      token,
		TelegramID: user.TelegramID,
		MaterialID: m.ID,
		SourceID:   sourceID,
		CreatedAt:  at,
	}
	if err := s.SaveLink(ctx, link); err != nil {
		return Reply{}, fmt.Errorf("saving link: %w", err)
	}

	event := Event{
		TelegramID: user.TelegramID,
		Name:       EventMaterialSelected,
		SourceID:   sourceID,
		MaterialID: m.ID,
		OccurredAt: at,
	}
	if err := s.AppendEvent(ctx, event); err != nil {
		return Reply{}, fmt.Errorf("appending %s: %w", EventMaterialSelected, err)
	}

	return Reply{
		Text: lines(
			lead,
			"",
			bold(m.Title),
			blockquote(m.Pitch),
			"",
			m.Inside,
		),
		Buttons: []Button{
			{Label: "Открыть разбор", URL: f.linkBase + "/r/" + token},
			{Label: "Мне это не подходит", Action: Action{Kind: ActionOther, MaterialID: m.ID}},
		},
	}, nil
}

// Alternative — escape-ветка. В тикете 01 материала всего два, поэтому
// уточняющий вопрос ничего не решает: честнее сразу отдать второй.
// Когда появится анализ Reel (тикет 09), здесь встанет один вопрос.
func (f *Funnel) Alternative(ctx context.Context, cmd AlternativeCommand) (Reply, error) {
	alt, ok := f.catalog.Alternative(cmd.CurrentMaterialID)
	if !ok {
		return Reply{}, fmt.Errorf("%w: no alternative for %q", ErrUnknownMaterial, cmd.CurrentMaterialID)
	}

	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return Reply{}, fmt.Errorf("reading last source: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventAlternativeAsked,
			SourceID:   sourceID,
			MaterialID: cmd.CurrentMaterialID,
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventAlternativeAsked, err)
		}

		return f.offer(ctx, s, cmd.User, alt, sourceID, "Тогда вот второе.", at)
	})
}

// Opened — результат перехода по tracked-ссылке.
type Opened struct {
	// Target — куда вести браузер.
	Target string
	// TelegramID и FollowUp заполнены, когда вслед за открытием человеку
	// надо написать в чат. Так задаётся вопрос про состояние: ровно
	// после того, как ценность получена, и ровно один раз.
	TelegramID int64
	FollowUp   *Reply
}

// Open — переход по tracked-ссылке: пишем факт клика и говорим, куда вести
// человека дальше.
//
// Здесь нет ни отсечки повторов, ни отметки активности: это не шаг
// диалога. Повторное открытие статьи — реальный повторный интерес.
func (f *Funnel) Open(ctx context.Context, token string) (Opened, error) {
	var out Opened

	err := f.db.Atomically(ctx, func(s Store) error {
		link, ok, err := s.LinkByToken(ctx, token)
		if err != nil {
			return fmt.Errorf("reading link: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownToken, token)
		}
		m, err := f.catalog.ByID(link.MaterialID)
		if err != nil {
			return err
		}

		// Считаем до записи собственного события, иначе оно же и ответит
		// «уже открывал».
		openedBefore, err := s.HasEvent(ctx, link.TelegramID, EventMaterialOpened)
		if err != nil {
			return fmt.Errorf("checking opens: %w", err)
		}
		stage, err := s.UserStage(ctx, link.TelegramID)
		if err != nil {
			return fmt.Errorf("reading stage: %w", err)
		}

		event := Event{
			TelegramID: link.TelegramID,
			Name:       EventMaterialOpened,
			SourceID:   link.SourceID,
			MaterialID: m.ID,
			OccurredAt: f.now(),
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("appending %s: %w", EventMaterialOpened, err)
		}

		out.Target = f.articleURL(m, link.SourceID)
		out.TelegramID = link.TelegramID

		// Вопрос задаётся один раз: человек уже ответил или уже открывал —
		// значит спрашивать не о чем.
		if stage == StageUnknown && !openedBefore {
			reply := f.AskStage()
			out.FollowUp = &reply
		}
		return nil
	})
	if err != nil {
		return Opened{}, err
	}
	return out, nil
}

// articleURL — адрес статьи с меткой перехода.
//
// Метка нужна аналитике сайта: без неё переход из бота попадает в direct
// и неотличим от человека, который просто открыл сайт. utm_campaign несёт
// исходный Reel, поэтому путь «Reel → бот → статья» виден целиком.
// Telegram ID в адрес не попадает — он не должен оказаться в GA.
func (f *Funnel) articleURL(m Material, sourceID string) string {
	campaign := sourceID
	if campaign == "" {
		campaign = "direct"
	}

	query := url.Values{}
	query.Set("utm_source", "telegram")
	query.Set("utm_medium", "bot")
	query.Set("utm_campaign", campaign)

	return f.siteBase + m.Path + "?" + query.Encode()
}

// normalizeSource возвращает валидный source_id и сырой payload.
// Битый payload не становится источником, но и не исчезает.
func normalizeSource(payload string) (sourceID, raw string) {
	raw = strings.TrimSpace(payload)
	if raw == "" {
		return "", ""
	}
	// Обрезаем до длины, которую вообще может прислать Telegram, чтобы в
	// метаданные события не уехала чужая простыня.
	if len(raw) > 128 {
		raw = raw[:128]
	}
	if !sourceRe.MatchString(raw) {
		return "", raw
	}
	return raw, raw
}

// absoluteBase проверяет, что база — абсолютный http(s)-адрес, и снимает
// хвостовой слэш, чтобы склейка путей не давала двойной.
func absoluteBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("want http(s) url, got %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url without host: %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// randomToken — 128 бит из crypto/rand. В ссылке не должно быть ничего,
// что связывает её с Telegram ID.
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func lines(parts ...string) string {
	return strings.Join(parts, "\n")
}

// Разметка реплик. Telegram понимает узкий набор тегов, и мы держимся
// внутри него: жирный для названия, цитата для обещания.
func bold(s string) string       { return "<b>" + escape(s) + "</b>" }
func blockquote(s string) string { return "<blockquote>" + escape(s) + "</blockquote>" }

// escape — тексты наши, но угловые скобки в них однажды появятся, и
// тогда сообщение молча не отправится.
func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
