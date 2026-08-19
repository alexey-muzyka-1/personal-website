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

		// Один вопрос вместо сразу материала. Он окупается дважды: разборы
		// действительно разные для одиночки и для команды, и ответ нужен
		// потом, когда появится что продавать.
		return Reply{
			Text: lines(
				"Привет, это Лёша.",
				"",
				"Выкладываю разборы того, как мы каждый день выпускаем короткие видео. Статьи целиком, без подписки и почты.",
				"",
				"Один вопрос, чтобы дать нужное: контент ты тянешь сам или с командой?",
			),
			Buttons: []Button{
				{Label: "Сам", Action: Action{Kind: ActionRole, Role: RoleSolo}},
				{Label: "С командой", Action: Action{Kind: ActionRole, Role: RoleTeam}},
			},
		}, nil
	})
}

// QualifyCommand — ответ на вопрос про команду.
type QualifyCommand struct {
	UpdateID int64
	User     User
	Role     Role
}

// Qualify — ответ получен: запоминаем его и отдаём тот разбор, который
// человеку подходит.
func (f *Funnel) Qualify(ctx context.Context, cmd QualifyCommand) (Reply, error) {
	if cmd.Role == RoleUnknown {
		return Reply{}, errors.New("qualify without a role")
	}

	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return Reply{}, fmt.Errorf("reading last source: %w", err)
		}

		if err := s.SetUserRole(ctx, cmd.User.TelegramID, cmd.Role); err != nil {
			return Reply{}, fmt.Errorf("saving role: %w", err)
		}

		m := f.catalog.ForRoleAndSource(cmd.Role, sourceID)

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventRoleAnswered,
			SourceID:   sourceID,
			MaterialID: m.ID,
			Metadata:   map[string]string{"role": cmd.Role.String()},
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventRoleAnswered, err)
		}

		return Reply{
			Text: lines(
				roleLead(cmd.Role),
				"",
				quote(m.Title),
				m.Pitch,
			),
			Buttons: []Button{
				{Label: m.Button, Action: Action{Kind: ActionTake, MaterialID: m.ID}},
				{Label: "Мне это не подходит", Action: Action{Kind: ActionOther, MaterialID: m.ID}},
			},
		}, nil
	})
}

// roleLead — одна строка, которая показывает, что ответ услышали.
// Без неё вопрос выглядит формальностью.
func roleLead(role Role) string {
	if role == RoleTeam {
		return "Тогда с того, что держится на объёме и на нескольких руках."
	}
	return "Тогда с того, что собирается в одиночку за один вечер."
}

// Choose — человек выбрал материал. Ссылку отдаём не прямую, а через свой
// redirect: иначе факт перехода не существует в данных.
func (f *Funnel) Choose(ctx context.Context, cmd ChooseCommand) (Reply, error) {
	m, err := f.catalog.ByID(cmd.MaterialID)
	if err != nil {
		return Reply{}, err
	}

	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return Reply{}, fmt.Errorf("reading last source: %w", err)
		}

		token, err := f.newToken()
		if err != nil {
			return Reply{}, fmt.Errorf("creating link token: %w", err)
		}
		link := Link{
			Token:      token,
			TelegramID: cmd.User.TelegramID,
			MaterialID: m.ID,
			SourceID:   sourceID,
			CreatedAt:  at,
		}
		if err := s.SaveLink(ctx, link); err != nil {
			return Reply{}, fmt.Errorf("saving link: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
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
				quote(m.Title),
				"",
				m.Inside,
			),
			Buttons: []Button{
				{Label: "Открыть", URL: f.linkBase + "/r/" + token},
			},
		}, nil
	})
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

		return Reply{
			Text: lines(
				"Тогда вот второе.",
				"",
				quote(alt.Title),
				alt.Pitch,
			),
			Buttons: []Button{
				{Label: alt.Button, Action: Action{Kind: ActionTake, MaterialID: alt.ID}},
			},
		}, nil
	})
}

// Open — переход по tracked-ссылке: пишем факт клика и говорим, куда вести
// человека дальше.
//
// Здесь нет ни отсечки повторов, ни отметки активности: это не шаг
// диалога. Повторное открытие статьи — реальный повторный интерес.
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
