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
	"github.com/alexey-muzyka-1/personal-website/bot/internal/memstore"
)

const (
	// Адреса для карты. Сайт настоящий, адрес бота — заглушка: реальный
	// появится вместе с хостингом.
	siteBase = "https://alexeymuzyka.com"
	linkBase = "https://bot.example.com"
	channel  = "https://t.me/alexeymuzykablog"
)

// entries — точки входа. Место ссылки берётся из каталога: сайт и бот
// это две половины одной системы, и держать «где что стоит» в двух
// местах значит однажды разойтись.
var entries = []struct {
	source string
	where  string
}{
	{funnel.SourceSiteHome, ""},
	{funnel.SourceSiteMethod6x5, ""},
	{funnel.SourceSiteBlueprint50, ""},
	{funnel.SourceSiteHealth, ""},
	{funnel.SourceSocialContent, ""},
	{funnel.SourceSocialPipeline, ""},
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
	b.WriteString("| Метка | Где ставится | Что отдаёт бот |\n|---|---|---|\n")

	for _, e := range entries {
		sess, err := newSession()
		if err != nil {
			return err
		}
		reply, _, err := sess.start(ctx, e.source)
		if err != nil {
			return fmt.Errorf("entry %q: %w", e.source, err)
		}
		m, err := sess.catalog.ByID(reply.Buttons[1].Action.MaterialID)
		if err != nil {
			return err
		}

		label := "`" + e.source + "`"
		if e.source == "" {
			label = "без метки"
		}
		where := e.where
		for _, rt := range sess.catalog.RouteTable() {
			if rt.Source == e.source && rt.Where != "" {
				where = rt.Where
			}
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", label, where, m.Title)
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

	opened, _, err := s.open(ctx, tokenFromButton(start))
	if err != nil {
		return err
	}
	b.WriteString("### 2. Нажал «" + start.Buttons[0].Label + "»\n\n")
	b.WriteString("Открывается статья на сайте:\n\n")
	b.WriteString("```\n" + opened.Target + "\n```\n\n")
	b.WriteString("Метка перехода нужна аналитике сайта: без неё человек из бота ")
	b.WriteString("попадает в direct и неотличим от прямого захода. ")
	b.WriteString("Telegram ID в адрес не попадает.\n\n")

	if opened.FollowUp == nil {
		return fmt.Errorf("после первого открытия ожидается вопрос о состоянии")
	}
	writeScreen(b, "3. Следом в чат прилетает вопрос", "переход засчитан", *opened.FollowUp)
	b.WriteString("Вопрос задаётся один раз и только после того, как человек ")
	b.WriteString("действительно открыл разбор. До этого спрашивать не за что.\n\n")

	for _, branch := range []struct {
		title string
		stage funnel.Stage
	}{
		{"4а. Ответил «Не получается выпускать стабильно»", funnel.StageNotShipping},
		{"4б. Ответил «Выпускаю, но не понимаю, что работает»", funnel.StageNoSignal},
		{"4в. Ответил «Другая ситуация»", funnel.StageOther},
	} {
		sess, err := newSession()
		if err != nil {
			return err
		}
		reply, _, err := sess.start(ctx, funnel.SourceSiteHome)
		if err != nil {
			return err
		}
		if _, _, err := sess.open(ctx, tokenFromButton(reply)); err != nil {
			return err
		}
		answer, _, err := sess.answerStage(ctx, branch.stage)
		if err != nil {
			return err
		}
		writeScreen(b, branch.title, "кнопка `stage:"+branch.stage.String()+"`", answer)

		if branch.stage == funnel.StageNotShipping {
			joined, _, err := sess.joinWaitlist(ctx)
			if err != nil {
				return err
			}
			writeScreen(b, "5. Нажал «"+answer.Buttons[0].Label+"»", "кнопка `waitlist:me`", joined)
		}
	}

	b.WriteString("Два оффера одновременно не показываются никогда: у каждого ")
	b.WriteString("состояния ровно один следующий шаг. «Другая ситуация» не тупик ")
	b.WriteString("и не меню — один уточняющий вопрос возвращает человека в одно ")
	b.WriteString("из двух состояний.\n\n")
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

	reply, events, err := s.start(ctx, funnel.SourceSiteMethod6x5)
	if err != nil {
		return err
	}
	steps = append(steps, step{"открыл бота и получил разбор", events})

	_, events, err = s.open(ctx, tokenFromButton(reply))
	if err != nil {
		return err
	}
	steps = append(steps, step{"открыл статью", events})

	_, events, err = s.answerStage(ctx, funnel.StageNotShipping)
	if err != nil {
		return err
	}
	steps = append(steps, step{"ответил про состояние", events})

	_, events, err = s.joinWaitlist(ctx)
	if err != nil {
		return err
	}
	steps = append(steps, step{"записался на эфир", events})

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
	broken, _, err := s.start(ctx, "разбор!")
	if err != nil {
		return err
	}
	offered, err := s.catalog.ByID(broken.Buttons[1].Action.MaterialID)
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

	fmt.Fprintf(b, "| Ссылка `/r/…` с чужим токеном | %s |\n", unknownTokenVerdict(s.openUnknown(ctx)))

	b.WriteString("| Нажата кнопка снятого материала | Гасит крутящийся индикатор и молчит: повторять такое бессмысленно |\n")
	b.WriteString("| В кнопке чужое состояние | То же самое: до воронки не доходит |\n")
	b.WriteString("| Статью открыли второй раз | Переход засчитывается снова, но вопрос повторно не задаётся |\n")
	b.WriteString("| Человек написал текст вместо кнопки | Пока молчит. Свободный текст станет входом в анализ Reel на тикете 09 |\n")
	b.WriteString("\n")
	return nil
}

// session — один человек и его бот на памяти.
type session struct {
	funnel  *funnel.Funnel
	store   *memstore.Memory
	catalog funnel.Catalog
	user    funnel.User
	update  int64
	tokens  int
	shown   int
}

func newSession() (*session, error) {
	mem := memstore.NewMemory()
	catalog := funnel.DefaultCatalog()
	at := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)

	s := &session{
		store:   mem,
		catalog: catalog,
		user:    funnel.User{TelegramID: 1, Username: "example", FirstName: "Человек"},
	}

	f, err := funnel.New(
		mem, catalog, siteBase, linkBase,
		funnel.WithChannel(channel),
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

func (s *session) answerStage(ctx context.Context, stage funnel.Stage) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.AnswerStage(ctx, funnel.StageCommand{
		UpdateID: s.update,
		User:     s.user,
		Stage:    stage,
	})
	return reply, s.fresh(), err
}

func (s *session) joinWaitlist(ctx context.Context) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.JoinWaitlist(ctx, funnel.JoinWaitlistCommand{
		UpdateID: s.update,
		User:     s.user,
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

func (s *session) alternative(ctx context.Context, materialID string) (funnel.Reply, []funnel.Event, error) {
	s.update++
	reply, err := s.funnel.Alternative(ctx, funnel.AlternativeCommand{
		UpdateID:          s.update,
		User:              s.user,
		CurrentMaterialID: materialID,
	})
	return reply, s.fresh(), err
}

func (s *session) open(ctx context.Context, token string) (funnel.Opened, []funnel.Event, error) {
	out, err := s.funnel.Open(ctx, token)
	return out, s.fresh(), err
}

func (s *session) openUnknown(ctx context.Context) error {
	_, err := s.funnel.Open(ctx, "TOKEN-CHUZHOJ")
	return err
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
	case funnel.ActionOther:
		return "other:" + a.MaterialID
	case funnel.ActionStage:
		return "stage:" + a.Stage.String()
	case funnel.ActionWaitlist:
		return "waitlist:me"
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
