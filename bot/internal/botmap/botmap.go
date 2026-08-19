// Package botmap собирает карту бота: все входы, все реплики, все кнопки
// и все события, которые бот пишет в базу.
//
// Карта не пишется руками. Она прогоняет настоящий сценарий на
// хранилище в памяти и записывает то, что бот реально ответит и реально
// сохранит. Поэтому она не может разойтись с кодом: разойдётся — упадёт
// TestBotMapIsUpToDate.
package botmap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/store"
)

const (
	// Адреса для карты. Сайт настоящий, адрес бота — заглушка: реальный
	// появится вместе с хостингом.
	siteBase = "https://alexeymuzyka.com"
	linkBase = "https://bot.example.com"
)

// entries — точки входа. Метки живут в коде воронки, а описание места
// на сайте — здесь: сайт и бот это две половины одной системы, и
// человеку нужна строчка «откуда это вообще берётся».
var entries = []struct {
	source string
	where  string
}{
	{funnel.SourceSiteHome, "главная сайта, блок Telegram"},
	{funnel.SourceSiteMethod6x5, "конец статьи «Метод 6 × 5»"},
	{funnel.SourceSiteBlueprint50, "конец статьи про 50 млн просмотров"},
	{funnel.SourceSiteHealth, "конец статьи про здоровье"},
	{"reel_razbor", "пример метки Reel — маршрута нет, значит материал по умолчанию"},
	{"", "человек открыл бота без метки: из профиля, из поиска, из старого поста"},
}

// Render собирает карту целиком.
func Render(ctx context.Context) (string, error) {
	var b strings.Builder

	b.WriteString("# Карта бота\n\n")
	b.WriteString("Собрана из работающего кода: сценарий прогоняется на памяти, ")
	b.WriteString("в карту попадает то, что бот реально отвечает и реально пишет в базу.\n\n")
	b.WriteString("Править здесь бесполезно — правится `bot/internal/funnel`, ")
	b.WriteString("потом `go run ./cmd/botmap > BOTMAP.md`.\n\n")
	b.WriteString("Адрес бота в примерах — заглушка `" + linkBase + "`, реальный появится с хостингом.\n\n")

	for _, section := range []func(context.Context, *strings.Builder) error{
		renderEntries,
		renderDialog,
		renderEvents,
		renderEdgeCases,
	} {
		if err := section(ctx, &b); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// renderEntries — таблица «метка → что человек получит».
func renderEntries(ctx context.Context, b *strings.Builder) error {
	b.WriteString("## Точки входа\n\n")
	b.WriteString("Одна метка — одно место. По ней видно, какое место приводит людей.\n\n")
	b.WriteString("| Метка | Где ставится | Ответил «сам» | Ответил «с командой» |\n|---|---|---|---|\n")

	for _, e := range entries {
		offers := map[funnel.Role]string{}
		for _, role := range []funnel.Role{funnel.RoleSolo, funnel.RoleTeam} {
			sess, err := newSession()
			if err != nil {
				return err
			}
			if _, _, err := sess.start(ctx, e.source); err != nil {
				return fmt.Errorf("entry %q: %w", e.source, err)
			}
			reply, _, err := sess.qualify(ctx, role)
			if err != nil {
				return fmt.Errorf("entry %q, role %v: %w", e.source, role, err)
			}
			m, err := sess.catalog.ByID(reply.Buttons[0].Action.MaterialID)
			if err != nil {
				return err
			}
			offers[role] = m.Title
		}

		label := "`" + e.source + "`"
		if e.source == "" {
			label = "без метки"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", label, e.where, offers[funnel.RoleSolo], offers[funnel.RoleTeam])
	}

	b.WriteString("\nМетка попадает в каждое событие человека, поэтому путь ")
	b.WriteString("«Reel → бот → статья» виден целиком. Если под ответ подходит ")
	b.WriteString("ровно тот разбор, который человек только что читал на сайте, ")
	b.WriteString("бот отдаёт второй.\n\n")
	return nil
}

// renderDialog — весь разговор дословно.
func renderDialog(ctx context.Context, b *strings.Builder) error {
	b.WriteString("## Диалог\n\n")

	s, err := newSession()
	if err != nil {
		return err
	}

	start, _, err := s.start(ctx, funnel.SourceSiteHome)
	if err != nil {
		return err
	}
	writeScreen(b, "1. Человек открыл бота", "`/start site_home`", start)

	qualified, _, err := s.qualify(ctx, funnel.RoleSolo)
	if err != nil {
		return err
	}
	writeScreen(b, "2. Ответил «"+start.Buttons[0].Label+"»", "кнопка `role:solo`", qualified)

	offered := qualified.Buttons[0].Action.MaterialID
	choose, _, err := s.choose(ctx, offered)
	if err != nil {
		return err
	}
	writeScreen(b, "3. Нажал «"+qualified.Buttons[0].Label+"»", "кнопка `take:"+offered+"`", choose)

	target, _, err := s.open(ctx, tokenFromButton(choose))
	if err != nil {
		return err
	}
	b.WriteString("### 4. Нажал «" + choose.Buttons[0].Label + "»\n\n")
	b.WriteString("Открывается статья на сайте:\n\n")
	b.WriteString("```\n" + target + "\n```\n\n")
	b.WriteString("Метка перехода нужна аналитике сайта: без неё человек из бота ")
	b.WriteString("попадает в direct и неотличим от прямого захода. ")
	b.WriteString("Telegram ID в адрес не попадает.\n\n")

	alt, _, err := s.alternative(ctx, offered)
	if err != nil {
		return err
	}
	writeScreen(b, "5. Вместо этого нажал «"+qualified.Buttons[1].Label+"»", "кнопка `other:"+offered+"`", alt)

	b.WriteString("Дальше всё повторяется: кнопка ведёт на тот же экран 3, ")
	b.WriteString("только со вторым материалом.\n\n")
	return nil
}

// renderEvents — что и когда попадает в базу.
func renderEvents(ctx context.Context, b *strings.Builder) error {
	b.WriteString("## Что записывается\n\n")

	s, err := newSession()
	if err != nil {
		return err
	}

	type step struct {
		name   string
		events []funnel.Event
	}
	var steps []step

	_, events, err := s.start(ctx, funnel.SourceSiteMethod6x5)
	if err != nil {
		return err
	}
	steps = append(steps, step{"открыл бота", events})

	qualified, events, err := s.qualify(ctx, funnel.RoleTeam)
	if err != nil {
		return err
	}
	steps = append(steps, step{"ответил про команду", events})

	offered := qualified.Buttons[0].Action.MaterialID
	choose, events, err := s.choose(ctx, offered)
	if err != nil {
		return err
	}
	steps = append(steps, step{"выбрал материал", events})

	_, events, err = s.open(ctx, tokenFromButton(choose))
	if err != nil {
		return err
	}
	steps = append(steps, step{"открыл статью", events})

	b.WriteString("| Шаг | Событие | Что в нём |\n|---|---|---|\n")
	for _, st := range steps {
		for _, e := range st.events {
			fmt.Fprintf(b, "| %s | `%s` | %s |\n", st.name, e.Name, describe(e))
		}
	}

	b.WriteString("\nПлюс на каждый `/start` пишется касание в `attributions`. ")
	b.WriteString("Таблица только пополняется: первое касание остаётся первым навсегда, ")
	b.WriteString("а приход из другого Reel не затирает историю.\n\n")
	return nil
}

// renderEdgeCases — поведение, которое иначе видно только в тестах.
func renderEdgeCases(ctx context.Context, b *strings.Builder) error {
	b.WriteString("## Крайние случаи\n\n")
	b.WriteString("| Что случилось | Что делает бот |\n|---|---|\n")

	s, err := newSession()
	if err != nil {
		return err
	}
	if _, _, err := s.start(ctx, "разбор!"); err != nil {
		return err
	}
	broken, _, err := s.qualify(ctx, funnel.RoleSolo)
	if err != nil {
		return err
	}
	offered, err := s.catalog.ByID(broken.Buttons[0].Action.MaterialID)
	if err != nil {
		return err
	}
	fmt.Fprintf(b,
		"| Метка битая (`разбор!`) | Спрашивает как обычно и отдаёт «%s». Метку не сохраняет, но кладёт сырое значение в событие — видно, что ссылка сломана |\n",
		offered.Title)

	// Повтор доставки: Telegram присылает тот же update второй раз.
	repeat, err := s.repeatLastStart(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "| Telegram прислал тот же update дважды | %s |\n", repeatVerdict(repeat))

	_, err = s.openUnknown(ctx)
	fmt.Fprintf(b, "| Ссылка `/r/…` с чужим токеном | %s |\n", unknownTokenVerdict(err))

	b.WriteString("| Нажата кнопка снятого материала | Гасит крутящийся индикатор и молчит: повторять такое бессмысленно |\n")
	b.WriteString("| В кнопке чужая роль | То же самое: до воронки не доходит |\n")
	b.WriteString("| Человек написал текст вместо кнопки | Пока молчит. Свободный текст станет входом в анализ Reel на тикете 09 |\n")
	b.WriteString("\n")
	return nil
}

// session — один человек и его бот на памяти.
type session struct {
	funnel  *funnel.Funnel
	store   *store.Memory
	catalog funnel.Catalog
	user    funnel.User
	update  int64
	tokens  int
	shown   int
}

func newSession() (*session, error) {
	mem := store.NewMemory()
	catalog := funnel.DefaultCatalog()
	at := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)

	s := &session{
		store:   mem,
		catalog: catalog,
		user:    funnel.User{TelegramID: 1, Username: "example", FirstName: "Человек"},
	}

	f, err := funnel.New(
		mem, catalog, siteBase, linkBase,
		funnel.WithClock(func() time.Time { return at }),
		funnel.WithTokenSource(func() (string, error) {
			s.tokens++
			return fmt.Sprintf("TOKEN%d", s.tokens), nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("building funnel: %w", err)
	}
	s.funnel = f
	return s, nil
}

func (s *session) qualify(ctx context.Context, role funnel.Role) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.Qualify(ctx, funnel.QualifyCommand{
		UpdateID: s.update,
		User:     s.user,
		Role:     role,
	})
	return reply, s.fresh(), err
}

func (s *session) start(ctx context.Context, payload string) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.Start(ctx, funnel.StartCommand{
		UpdateID: s.update,
		User:     s.user,
		Payload:  payload,
	})
	return reply, s.fresh(), err
}

// repeatLastStart повторяет предыдущий update — так выглядит повторная
// доставка от Telegram.
func (s *session) repeatLastStart(ctx context.Context) (funnel.Reply, error) {
	return s.funnel.Start(ctx, funnel.StartCommand{
		UpdateID: s.update,
		User:     s.user,
		Payload:  "разбор!",
	})
}

func (s *session) choose(ctx context.Context, materialID string) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.Choose(ctx, funnel.ChooseCommand{
		UpdateID:   s.update,
		User:       s.user,
		MaterialID: materialID,
	})
	return reply, s.fresh(), err
}

func (s *session) alternative(ctx context.Context, materialID string) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.Alternative(ctx, funnel.AlternativeCommand{
		UpdateID:          s.update,
		User:              s.user,
		CurrentMaterialID: materialID,
	})
	return reply, s.fresh(), err
}

func (s *session) open(ctx context.Context, token string) (string, []funnel.Event, error) {
	target, err := s.funnel.Open(ctx, token)
	return target, s.fresh(), err
}

func (s *session) openUnknown(ctx context.Context) (string, error) {
	return s.funnel.Open(ctx, "TOKEN-CHUZHOJ")
}

// fresh — события, появившиеся с прошлого шага.
func (s *session) fresh() []funnel.Event {
	all := s.store.Events()
	out := all[s.shown:]
	s.shown = len(all)
	return out
}

func writeScreen(b *strings.Builder, title, trigger string, reply funnel.Reply) {
	b.WriteString("### " + title + "\n\n")
	b.WriteString("Вход: " + trigger + "\n\n")
	b.WriteString("```\n" + reply.Text + "\n```\n\n")

	if len(reply.Buttons) == 0 {
		return
	}
	b.WriteString("Кнопки:\n\n")
	for _, btn := range reply.Buttons {
		if btn.URL != "" {
			fmt.Fprintf(b, "- **%s** → %s\n", btn.Label, btn.URL)
			continue
		}
		fmt.Fprintf(b, "- **%s** → `%s`\n", btn.Label, actionCode(btn.Action))
	}
	b.WriteString("\n")
}

func actionCode(a funnel.Action) string {
	switch a.Kind {
	case funnel.ActionTake:
		return "take:" + a.MaterialID
	case funnel.ActionOther:
		return "other:" + a.MaterialID
	default:
		return "?"
	}
}

// tokenFromButton достаёт токен из ссылки, чтобы карта ходила по тем же
// адресам, что и живой человек.
func tokenFromButton(reply funnel.Reply) string {
	url := reply.Buttons[0].URL
	if i := strings.LastIndex(url, "/"); i >= 0 {
		return url[i+1:]
	}
	return url
}

func describe(e funnel.Event) string {
	parts := []string{}
	if e.SourceID != "" {
		parts = append(parts, "метка `"+e.SourceID+"`")
	}
	if e.MaterialID != "" {
		parts = append(parts, "материал `"+e.MaterialID+"`")
	}
	keys := make([]string, 0, len(e.Metadata))
	for k := range e.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, "`"+k+"` = "+e.Metadata[k])
	}
	if len(parts) == 0 {
		return "только человек и время"
	}
	return strings.Join(parts, ", ")
}

func repeatVerdict(reply funnel.Reply) string {
	if reply.Skip {
		return "Молчит и ничего не пишет второй раз: шаг уже засчитан"
	}
	return "Обрабатывает заново"
}

func unknownTokenVerdict(err error) string {
	if err != nil {
		return "Ведёт на список статей сайта, событие не пишет"
	}
	return "Открывает статью"
}
