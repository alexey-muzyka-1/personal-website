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
	db       DB
	catalog  Catalog
	siteBase string
	linkBase string
	now      func() time.Time
	newToken func() (string, error)
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

// ChooseCommand — человек нажал кнопку материала.
type ChooseCommand struct {
	UpdateID   int64
	User       User
	MaterialID string
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

// Start — вход в воронку: запоминаем человека и источник, показываем один
// рекомендованный материал.
func (f *Funnel) Start(ctx context.Context, cmd StartCommand) (Reply, error) {
	var reply Reply

	err := f.db.Atomically(ctx, func(s Store) error {
		at, skip, err := begin(ctx, s, cmd.UpdateID, cmd.User, f.now)
		if err != nil || skip {
			reply = Reply{Skip: skip}
			return err
		}

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
			return fmt.Errorf("appending attribution: %w", err)
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
			return fmt.Errorf("appending %s: %w", EventBotStarted, err)
		}

		m := f.catalog.ForSource(sourceID)
		reply = Reply{
			Text: lines(
				"Привет, это Лёша.",
				"",
				"Здесь лежат разборы систем, по которым мы каждый день выпускаем короткие видео. Целиком и бесплатно, без обмена на подписку.",
				"",
				"Начал бы с этого:",
				quote(m.Title),
				m.Pitch,
			),
			Buttons: []Button{
				{Label: m.Button, Action: Action{Kind: ActionTake, MaterialID: m.ID}},
				{Label: "Мне это не подходит", Action: Action{Kind: ActionOther, MaterialID: m.ID}},
			},
		}
		return nil
	})

	return replyOrError(reply, err)
}

// Choose — человек выбрал материал. Ссылку отдаём не прямую, а через свой
// redirect: иначе факт перехода не существует в данных.
func (f *Funnel) Choose(ctx context.Context, cmd ChooseCommand) (Reply, error) {
	m, err := f.catalog.ByID(cmd.MaterialID)
	if err != nil {
		return Reply{}, err
	}

	var reply Reply

	err = f.db.Atomically(ctx, func(s Store) error {
		at, skip, err := begin(ctx, s, cmd.UpdateID, cmd.User, f.now)
		if err != nil || skip {
			reply = Reply{Skip: skip}
			return err
		}

		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return fmt.Errorf("reading last source: %w", err)
		}

		token, err := f.newToken()
		if err != nil {
			return fmt.Errorf("creating link token: %w", err)
		}
		link := Link{
			Token:      token,
			TelegramID: cmd.User.TelegramID,
			MaterialID: m.ID,
			SourceID:   sourceID,
			CreatedAt:  at,
		}
		if err := s.SaveLink(ctx, link); err != nil {
			return fmt.Errorf("saving link: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventMaterialSelected,
			SourceID:   sourceID,
			MaterialID: m.ID,
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("appending %s: %w", EventMaterialSelected, err)
		}

		reply = Reply{
			Text: lines(
				quote(m.Title),
				"",
				"Читается за один присест. Всё внутри статьи, взамен ничего оставлять не нужно.",
			),
			Buttons: []Button{
				{Label: "Открыть статью", URL: f.linkBase + "/r/" + token},
			},
		}
		return nil
	})

	return replyOrError(reply, err)
}

// Alternative — escape-ветка. В тикете 01 материала всего два, поэтому
// уточняющий вопрос ничего не решает: честнее сразу отдать второй.
// Когда появится анализ Reel (тикет 09), здесь встанет один вопрос.
func (f *Funnel) Alternative(ctx context.Context, cmd AlternativeCommand) (Reply, error) {
	alt, ok := f.catalog.Alternative(cmd.CurrentMaterialID)
	if !ok {
		return Reply{}, fmt.Errorf("%w: no alternative for %q", ErrUnknownMaterial, cmd.CurrentMaterialID)
	}

	var reply Reply

	err := f.db.Atomically(ctx, func(s Store) error {
		at, skip, err := begin(ctx, s, cmd.UpdateID, cmd.User, f.now)
		if err != nil || skip {
			reply = Reply{Skip: skip}
			return err
		}

		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return fmt.Errorf("reading last source: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventAlternativeAsked,
			SourceID:   sourceID,
			MaterialID: cmd.CurrentMaterialID,
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("appending %s: %w", EventAlternativeAsked, err)
		}

		reply = Reply{
			Text: lines(
				"Понял. Тогда второе:",
				quote(alt.Title),
				alt.Pitch,
			),
			Buttons: []Button{
				{Label: alt.Button, Action: Action{Kind: ActionTake, MaterialID: alt.ID}},
			},
		}
		return nil
	})

	return replyOrError(reply, err)
}

// Open — переход по tracked-ссылке: пишем факт клика и говорим, куда вести
// человека дальше.
//
// Дедупликации здесь нет специально: повторное открытие статьи — реальный
// повторный интерес, а не дубль update.
func (f *Funnel) Open(ctx context.Context, token string) (string, error) {
	var target string

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

		target = f.articleURL(m, link.SourceID)
		return nil
	})
	if err != nil {
		return "", err
	}
	return target, nil
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

// begin — общий пролог шага: отсечь повторный update и отметить, что
// человек снова активен.
func begin(
	ctx context.Context,
	s Store,
	updateID int64,
	u User,
	now func() time.Time,
) (at time.Time, skip bool, err error) {
	seen, err := s.MarkUpdate(ctx, updateID)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("marking update %d: %w", updateID, err)
	}
	if seen {
		return time.Time{}, true, nil
	}

	at = now()
	if err := s.SaveUser(ctx, u, at); err != nil {
		return time.Time{}, false, fmt.Errorf("saving user %d: %w", u.TelegramID, err)
	}
	return at, false, nil
}

// replyOrError: при ошибке ответ не отдаём вообще. Отправить человеку
// сообщение о шаге, который не записан, — худший из вариантов.
func replyOrError(reply Reply, err error) (Reply, error) {
	if err != nil {
		return Reply{}, err
	}
	return reply, nil
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

// quote — типографские кавычки вокруг названия статьи.
func quote(s string) string {
	return "«" + s + "»"
}
