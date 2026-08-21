package funnel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Вопрос после ценности и три взаимоисключающих ответа. Меню продуктов
// здесь нет и не будет: человек выбирает своё состояние, а не оффер.
const (
	stageQuestion  = "Что у тебя сейчас с короткими видео?"
	labelNotShip   = "Не получается выпускать стабильно"
	labelNoSignal  = "Выпускаю, но не понимаю, что работает"
	labelOther     = "Другая ситуация"
	labelWaitlist  = "Записать меня"
	labelNotNeeded = "Пока не надо"
)

// Отказ от показанного оффера. Слово в слово как отказ от материала в
// offer(): человеку это одна и та же мысль «не моё», и звучать она должна
// одинаково. Константы всё же разные — ведут кнопки в разные места, и
// связывать их одним именем значит однажды поменять обе разом по ошибке.
const labelNotForMe = "Мне это не подходит"

// AskStage — вопрос про состояние. Задаётся один раз, после того как
// человек открыл разбор: до этого спрашивать не за что.
func (f *Funnel) AskStage() Reply {
	return Reply{
		Text: lines(
			bold(stageQuestion),
			"Один вопрос, дальше подскажу шаг.",
		),
		Buttons: []Button{
			{Label: labelNotShip, Action: Action{Kind: ActionStage, Stage: StageNotShipping}},
			{Label: labelNoSignal, Action: Action{Kind: ActionStage, Stage: StageNoSignal}},
			{Label: labelOther, Action: Action{Kind: ActionStage, Stage: StageOther}},
		},
	}
}

// StageCommand — ответ на вопрос о состоянии.
type StageCommand struct {
	UpdateID int64
	User     User
	Stage    Stage
}

// AnswerStage запоминает состояние и показывает ровно один следующий шаг.
func (f *Funnel) AnswerStage(ctx context.Context, cmd StageCommand) (Reply, error) {
	if cmd.Stage == StageUnknown {
		return Reply{}, errors.New("stage answer without a stage")
	}

	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return Reply{}, fmt.Errorf("reading last source: %w", err)
		}
		if err := s.SetUserStage(ctx, cmd.User.TelegramID, cmd.Stage); err != nil {
			return Reply{}, fmt.Errorf("saving stage: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventStageAnswered,
			SourceID:   sourceID,
			Metadata:   map[string]string{"stage": cmd.Stage.String()},
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventStageAnswered, err)
		}

		// Отказ от уже показанного оффера — не повод спрашивать заново.
		// Уточняющий вопрос хорош ровно один раз, на входе; после «мне
		// это не подходит» он возвращает человека к тому же самому
		// предложению, и получается не escape, а круг.
		if cmd.Stage == StageOther {
			shown, err := s.HasEvent(ctx, cmd.User.TelegramID, EventOfferShown)
			if err != nil {
				return Reply{}, fmt.Errorf("checking offers: %w", err)
			}
			if shown {
				return f.offerChannel(ctx, s, cmd.User, sourceID, at)
			}
		}

		reply, offer := f.stepFor(cmd.Stage)
		if offer == "" {
			return reply, nil
		}

		shown := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventOfferShown,
			SourceID:   sourceID,
			Metadata:   map[string]string{"offer": offer, "stage": cmd.Stage.String()},
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, shown); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventOfferShown, err)
		}
		return reply, nil
	})
}

// offerChannel — единственный бесплатный следующий шаг, и он показывается
// вместо отклонённого оффера, а не рядом с ним.
//
// Правило «один оффер за раз» этим не нарушается: канал не продаётся и не
// конкурирует за то же решение. Нарушалось бы обратное — человек, который
// только что сказал «не надо», не должен уходить в пустоту, когда есть
// что дать бесплатно.
func (f *Funnel) offerChannel(ctx context.Context, s Store, user User, sourceID string, at time.Time) (Reply, error) {
	if f.channelURL == "" {
		// Канал не задан — уточняющий вопрос всё же лучше тупика.
		reply, _ := f.stepFor(StageOther)
		return reply, nil
	}

	event := Event{
		TelegramID: user.TelegramID,
		Name:       EventChannelOffered,
		SourceID:   sourceID,
		Metadata:   map[string]string{"place": "after_decline"},
		OccurredAt: at,
	}
	if err := s.AppendEvent(ctx, event); err != nil {
		return Reply{}, fmt.Errorf("appending %s: %w", EventChannelOffered, err)
	}

	return Reply{
		Text: lines(
			"Понял, не буду навязывать.",
			"",
			"Тогда просто оставлю канал: там разборы и находки появляются раньше, чем доезжают до сайта.",
		),
		Buttons: []Button{{Label: labelReadChannel, URL: f.channelURL}},
	}, nil
}

// labelReadChannel — одна и та же кнопка в обоих местах, где предлагается
// канал. Разные слова про одно действие читались бы как разные действия.
const labelReadChannel = "Читать канал"

// Имена офферов. Их видит только аналитика, человеку они не показываются.
const (
	OfferWaitlist    = "waitlist"
	OfferViralmaxing = "viralmaxing"
)

// stepFor — один следующий шаг на состояние. Два оффера одновременно не
// показываются никогда: это и есть разница между воронкой и витриной.
func (f *Funnel) stepFor(stage Stage) (Reply, string) {
	switch stage {
	case StageNotShipping:
		return Reply{
			Text: lines(
				"Тогда дело не в идеях, а в том, что между идеей и публикацией слишком много шагов.",
				"",
				"Готовлю разбор в прямом эфире: как за один вечер собрать план на месяц и выпускать, не решая каждое утро, что снимать.",
				blockquote("Даты и цены пока нет. Могу позвать первым, когда назначу."),
			),
			Buttons: []Button{
				{Label: labelWaitlist, Action: Action{Kind: ActionWaitlist}},
				{Label: labelNotNeeded, Action: Action{Kind: ActionStage, Stage: StageOther}},
			},
		}, OfferWaitlist

	case StageNoSignal:
		return Reply{
			Text: lines(
				"Значит не хватает не идей, а цифр: какой ролик выстрелил и почему.",
				"",
				"Мы для этого собрали Viralmaxing и сами сидим в нём каждый день. Свои аккаунты и конкуренты в одной таблице, видно, что дожимать.",
			),
			Buttons: []Button{
				{Label: "Открыть Viralmaxing", URL: f.productURL()},
				// Без второй кнопки ветка была тупиком: не нажал — и
				// вернуться некуда, предыдущее сообщение уже заменено.
				// Ведёт туда же, куда «Пока не надо» в соседней ветке.
				{Label: labelNotForMe, Action: Action{Kind: ActionStage, Stage: StageOther}},
			},
		}, OfferViralmaxing

	default:
		// Escape не запирает и не открывает меню: один уточняющий вопрос,
		// который возвращает человека в одно из двух состояний.
		return Reply{
			Text: lines(
				bold("Тогда уточню одно."),
				"Ролики выходят регулярно или пока рывками?",
			),
			Buttons: []Button{
				{Label: "Скорее рывками", Action: Action{Kind: ActionStage, Stage: StageNotShipping}},
				{Label: "Регулярно", Action: Action{Kind: ActionStage, Stage: StageNoSignal}},
			},
		}, ""
	}
}

// JoinWaitlistCommand — «записать меня» на будущий эфир.
type JoinWaitlistCommand struct {
	UpdateID int64
	User     User
}

// EventWaitlistJoined — запись на будущий эфир. Это интерес, а не деньги:
// в денежную метрику он не идёт.
const EventWaitlistJoined = "waitlist_joined"

// JoinWaitlist записывает человека в предзапись. Ничего не обещает сверх
// того, что уже сказано: ни даты, ни цены не существует.
func (f *Funnel) JoinWaitlist(ctx context.Context, cmd JoinWaitlistCommand) (Reply, error) {
	return f.step(ctx, cmd.UpdateID, cmd.User, func(s Store, at time.Time) (Reply, error) {
		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return Reply{}, fmt.Errorf("reading last source: %w", err)
		}

		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       EventWaitlistJoined,
			SourceID:   sourceID,
			Metadata:   map[string]string{"offer": OfferWaitlist},
			OccurredAt: at,
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return Reply{}, fmt.Errorf("appending %s: %w", EventWaitlistJoined, err)
		}

		// Канал предлагается только если он задан: пустая кнопка-ссылка
		// ломает клавиатуру целиком.
		reply := Reply{Text: "Записал. Напишу сюда же, когда назначу дату."}
		if f.channelURL != "" {
			reply.Text = lines(
				reply.Text,
				"",
				"Пока можно читать канал: там разборы и находки появляются раньше, чем доезжают до сайта.",
			)
			reply.Buttons = []Button{{Label: labelReadChannel, URL: f.channelURL}}

			shown := Event{
				TelegramID: cmd.User.TelegramID,
				Name:       EventChannelOffered,
				SourceID:   sourceID,
				Metadata:   map[string]string{"place": "after_waitlist"},
				OccurredAt: at,
			}
			if err := s.AppendEvent(ctx, shown); err != nil {
				return Reply{}, fmt.Errorf("appending %s: %w", EventChannelOffered, err)
			}
		}
		return reply, nil
	})
}

// BlockCommand — человек заблокировал бота или вернулся.
type BlockCommand struct {
	UpdateID int64
	User     User
	Blocked  bool
}

// SetBlocked записывает блокировку.
//
// Это не шаг воронки, а её потолок: заблокировавшему нельзя написать
// вообще ничего. Без этой отметки предзапись на десять человек тихо
// превращается в рассылку на семь, и узнаётся это в день эфира.
//
// Отдельно от step() именно потому, что здесь нельзя заводить человека:
// заблокировать бота можно и не запуская его, из профиля. Такой человек
// не лид, и строка в users испортила бы знаменатель во всех отчётах.
func (f *Funnel) SetBlocked(ctx context.Context, cmd BlockCommand) error {
	return f.db.Atomically(ctx, func(s Store) error {
		seen, err := s.MarkUpdate(ctx, cmd.UpdateID)
		if err != nil {
			return fmt.Errorf("marking update %d: %w", cmd.UpdateID, err)
		}
		if seen {
			return nil
		}

		known, err := s.HasUser(ctx, cmd.User.TelegramID)
		if err != nil {
			return fmt.Errorf("checking user %d: %w", cmd.User.TelegramID, err)
		}
		if !known {
			return nil
		}

		sourceID, err := s.LastSource(ctx, cmd.User.TelegramID)
		if err != nil {
			return fmt.Errorf("reading last source: %w", err)
		}

		name := EventBotUnblocked
		if cmd.Blocked {
			name = EventBotBlocked
		}
		event := Event{
			TelegramID: cmd.User.TelegramID,
			Name:       name,
			SourceID:   sourceID,
			OccurredAt: f.now(),
		}
		if err := s.AppendEvent(ctx, event); err != nil {
			return fmt.Errorf("appending %s: %w", name, err)
		}
		return nil
	})
}

// productURL — ссылка в Viralmaxing с меткой, чтобы переход из бота был
// отличим от перехода с сайта.
func (f *Funnel) productURL() string {
	return "https://viralmaxing.com/?utm_source=lesha-bot&utm_medium=referral&utm_campaign=funnel"
}
