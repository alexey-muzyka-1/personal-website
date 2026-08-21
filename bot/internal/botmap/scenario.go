package botmap

import (
	"context"
	"fmt"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// Цепочка сообщений в структурированном виде — для админки.
//
// Как и BOTMAP.md, собирается прогоном настоящего сценария на памяти:
// в неё попадает то, что бот действительно отвечает. Написать здесь
// «бот говорит вот так» и разойтись с кодом невозможно — текст берётся
// из ответа воронки, а не из строки рядом.

// Screen — одно сообщение бота.
type Screen struct {
	// Step — номер в цепочке, одинаковый у веток одного шага.
	Step int `json:"step"`
	// Branch — пометка ветки: пусто у общего шага.
	Branch string `json:"branch"`
	// Title — что произошло, человеческим языком.
	Title string `json:"title"`
	// Trigger — что человек сделал, чтобы это увидеть.
	Trigger string `json:"trigger"`
	// Text — реплика бота дословно, с разметкой как в Telegram.
	Text string `json:"text"`
	// Buttons — кнопки под сообщением.
	Buttons []ScreenButton `json:"buttons"`
	// Events — что этот шаг записал в базу.
	Events []string `json:"events"`
	// Note — почему шаг устроен именно так.
	Note string `json:"note"`
}

// ScreenButton — кнопка: либо ссылка наружу, либо шаг внутри бота.
type ScreenButton struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	// Action — код действия, тот же, что уезжает в callback_data.
	Action string `json:"action"`
	// Leads — на какой шаг цепочки ведёт кнопка. Ноль — никуда внутри
	// бота: это ссылка наружу или конец ветки.
	Leads int `json:"leads"`
}

// Scenario — вся цепочка сообщений от входа до последнего шага.
//
// Ветки не разворачиваются в дерево: у воронки их три и они сходятся
// обратно, а дерево на три листа читается хуже, чем список с пометками.
func Scenario(ctx context.Context) ([]Screen, error) {
	var screens []Screen

	s, err := newSession()
	if err != nil {
		return nil, err
	}

	start, events, err := s.start(ctx, funnel.SourceSiteHome)
	if err != nil {
		return nil, fmt.Errorf("вход: %w", err)
	}
	screens = append(screens, Screen{
		Step:    1,
		Title:   "Человек открыл бота",
		Trigger: "/start site_home",
		Text:    start.Text,
		Buttons: buttons(start, 0),
		Events:  names(events),
		Note:    "Материал зависит от метки. Пришедшему со статьи бот отдаёт не её же, а вторую.",
	})

	opened, events, err := s.open(ctx, tokenFromButton(start))
	if err != nil {
		return nil, fmt.Errorf("переход: %w", err)
	}
	screens = append(screens, Screen{
		Step:    2,
		Title:   "Нажал «" + start.Buttons[0].Label + "»",
		Trigger: "переход по ссылке со счётчиком",
		Text:    "Открывается статья:\n" + opened.Target,
		Events:  names(events),
		Note:    "Метка перехода нужна аналитике сайта: без неё человек из бота попадает в direct и неотличим от прямого захода. Telegram ID в адрес не попадает.",
	})

	if opened.FollowUp == nil {
		return nil, fmt.Errorf("после первого открытия ожидается вопрос о состоянии")
	}
	screens = append(screens, Screen{
		Step:    3,
		Title:   "Следом в чат прилетает вопрос",
		Trigger: "переход засчитан",
		Text:    opened.FollowUp.Text,
		Buttons: buttons(*opened.FollowUp, 4),
		Note:    "Вопрос задаётся один раз и только после того, как человек действительно открыл разбор. До этого спрашивать не за что.",
	})

	branches := []struct {
		label string
		stage funnel.Stage
		note  string
	}{
		{"не выпускает стабильно", funnel.StageNotShipping,
			"Единственная ветка, где есть предзапись. Даты и цены не существует, и бот их не выдумывает."},
		{"выпускает, не видит сигнала", funnel.StageNoSignal,
			"Показывается только продукт. Переход в него не считается — считается лишь показ."},
		{"другая ситуация", funnel.StageOther,
			"Не тупик и не меню: один уточняющий вопрос возвращает человека в одно из двух состояний."},
	}

	var afterBranches []Screen
	for _, br := range branches {
		sess, err := newSession()
		if err != nil {
			return nil, err
		}
		reply, _, err := sess.start(ctx, funnel.SourceSiteHome)
		if err != nil {
			return nil, err
		}
		if _, _, err := sess.open(ctx, tokenFromButton(reply)); err != nil {
			return nil, err
		}

		answer, events, err := sess.answerStage(ctx, br.stage)
		if err != nil {
			return nil, fmt.Errorf("ветка %q: %w", br.label, err)
		}
		screens = append(screens, Screen{
			Step:    4,
			Branch:  br.label,
			Title:   "Ответил «" + br.label + "»",
			Trigger: "stage:" + br.stage.String(),
			Text:    answer.Text,
			Buttons: buttons(answer, 0),
			Events:  names(events),
			Note:    br.note,
		})

		// Отказ от оффера. Ветка одна на оба предложения и показывается на
		// карте один раз: человеку в обоих случаях говорится одно и то же.
		if br.stage == funnel.StageNoSignal {
			declined, events, err := sess.answerStage(ctx, funnel.StageOther)
			if err != nil {
				return nil, fmt.Errorf("отказ от оффера: %w", err)
			}
			afterBranches = append(afterBranches, Screen{
				Step: 5,
				// Ветки нет: обоим отказавшимся бот говорит одно и то же,
				// и показывать это дважды значит соврать про размер бота.
				Title:   "Нажал «" + answer.Buttons[1].Label + "»",
				Trigger: "stage:other после показанного оффера",
				Text:    declined.Text,
				Buttons: buttons(declined, 0),
				Events:  names(events),
				Note:    "Уточняющий вопрос здесь был бы кругом: он ведёт обратно к тому же офферу. Вместо него — единственное, что можно дать бесплатно.",
			})
		}

		if br.stage != funnel.StageNotShipping {
			continue
		}
		joined, events, err := sess.joinWaitlist(ctx)
		if err != nil {
			return nil, fmt.Errorf("предзапись: %w", err)
		}
		afterBranches = append(afterBranches, Screen{
			Step:    5,
			Branch:  br.label,
			Title:   "Нажал «" + answer.Buttons[0].Label + "»",
			Trigger: "waitlist:me",
			Text:    joined.Text,
			Buttons: buttons(joined, 0),
			Events:  names(events),
			Note:    "Последний измеримый шаг. Это интерес, а не деньги: платного шага в воронке пока нет.",
		})
	}

	return append(screens, afterBranches...), nil
}

// buttons переводит кнопки ответа в вид для страницы. leads — на какой
// шаг ведут кнопки-действия, чтобы цепочку можно было пройти глазами.
func buttons(reply funnel.Reply, leads int) []ScreenButton {
	out := make([]ScreenButton, 0, len(reply.Buttons))
	for _, b := range reply.Buttons {
		btn := ScreenButton{Label: b.Label, URL: b.URL}
		if b.URL == "" {
			btn.Action = actionCode(b.Action)
			btn.Leads = leads
		}
		out = append(out, btn)
	}
	return out
}

func names(events []funnel.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Name)
	}
	return out
}
