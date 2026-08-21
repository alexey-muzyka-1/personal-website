package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/channel"
)

var _ channel.Store = (*Postgres)(nil)

// Запись того, что происходит в канале.
//
// Здесь одна транзакционная операция вместо привычного Atomically: у
// хранилища уже есть Atomically для воронки, и второго с другим
// замыканием Go не даст. Смысл при этом тот же — отсечка повторной
// доставки, состояние подписки и событие перехода коммитятся вместе.

// Save записывает изменение подписки.
func (p *Postgres) Save(ctx context.Context, c channel.Change) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Отсекается только то, что пришло update-ом. У сверки update_id нет:
	// она не доставка, а наш собственный вопрос Telegram, и повторять её
	// не только можно, но и нужно.
	if c.UpdateID != 0 {
		const mark = `
			insert into processed_updates (update_id, processed_at)
			values ($1, now())
			on conflict (update_id) do nothing`

		tag, err := tx.Exec(ctx, mark, c.UpdateID)
		if err != nil {
			return false, fmt.Errorf("marking update: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return false, nil
		}
	}

	if err := saveMembership(ctx, tx, c); err != nil {
		return false, err
	}
	if c.Event != "" {
		if err := saveChannelEvent(ctx, tx, c); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing transaction: %w", err)
	}
	return true, nil
}

// saveMembership — текущее состояние подписки.
//
// Пустым именем не затираем известное: сверка про удалённый аккаунт
// возвращает пустого пользователя, и в таблице отписок вместо человека
// остался бы голый id. Дату подписки трогаем только на самой подписке —
// иначе первая же сверка проставила бы всей старой базе сегодняшнее
// число и нарисовала прирост, которого не было.
func saveMembership(ctx context.Context, tx pgx.Tx, c channel.Change) error {
	const query = `
		insert into channel_members (
			telegram_id, username, first_name, status, subscribed,
			joined_at, left_at, invite_link, source_id, seen_at
		)
		values ($1, $2, $3, $4, $5, $6, $7, $8, coalesce((
			select a.source_id from attributions a
			where a.telegram_id = $1 and a.source_id <> ''
			order by a.occurred_at, a.id
			limit 1
		), ''), $9)
		on conflict (telegram_id) do update set
			username    = case when excluded.username <> ''
			                   then excluded.username else channel_members.username end,
			first_name  = case when excluded.first_name <> ''
			                   then excluded.first_name else channel_members.first_name end,
			status      = excluded.status,
			subscribed  = excluded.subscribed,
			joined_at   = case when $10 = 'channel_joined'
			                   then excluded.joined_at else channel_members.joined_at end,
			left_at     = case when $10 = 'channel_joined' then null
			                   when $10 = 'channel_left'   then excluded.left_at
			                   else channel_members.left_at end,
			invite_link = case when excluded.invite_link <> ''
			                   then excluded.invite_link else channel_members.invite_link end,
			source_id   = case when channel_members.source_id <> ''
			                   then channel_members.source_id else excluded.source_id end,
			seen_at     = excluded.seen_at`

	var joinedAt, leftAt *time.Time
	switch c.Event {
	case channel.EventJoined:
		joinedAt = &c.At
	case channel.EventLeft:
		leftAt = &c.At
	}

	_, err := tx.Exec(ctx, query,
		c.Member.TelegramID, c.Member.Username, c.Member.FirstName,
		c.Status, channel.Subscribed(c.Status),
		joinedAt, leftAt, c.InviteLink, c.At, c.Event,
	)
	if err != nil {
		return fmt.Errorf("saving membership of %d: %w", c.Member.TelegramID, err)
	}
	return nil
}

func saveChannelEvent(ctx context.Context, tx pgx.Tx, c channel.Change) error {
	const query = `
		insert into channel_events (telegram_id, name, invite_link, source_id, noticed, occurred_at)
		values ($1, $2, $3, coalesce((
			select a.source_id from attributions a
			where a.telegram_id = $1 and a.source_id <> ''
			order by a.occurred_at, a.id
			limit 1
		), ''), $4, $5)`

	_, err := tx.Exec(ctx, query,
		c.Member.TelegramID, c.Event, c.InviteLink, c.Noticed, c.At)
	if err != nil {
		return fmt.Errorf("appending %s: %w", c.Event, err)
	}
	return nil
}

func (p *Postgres) SaveSize(ctx context.Context, at time.Time, members int) error {
	const query = `
		insert into channel_size (taken_at, members)
		values ($1, $2)
		on conflict (taken_at) do update set members = excluded.members`

	if _, err := p.pool.Exec(ctx, query, at, members); err != nil {
		return fmt.Errorf("saving channel size: %w", err)
	}
	return nil
}

func (p *Postgres) Known(ctx context.Context) (map[int64]string, error) {
	const query = `select telegram_id, status from channel_members`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying known members: %w", err)
	}
	defer rows.Close()

	known := map[int64]string{}
	for rows.Next() {
		var id int64
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("scanning known member: %w", err)
		}
		known[id] = status
	}
	return known, rows.Err()
}

// Watched — за кем следит сверка: лиды бота и все, кого мы видели в
// канале. Первых спрашиваем, чтобы узнать про подписку, вторых — чтобы не
// пропустить отписку, случившуюся, пока процесс лежал.
func (p *Postgres) Watched(ctx context.Context) ([]int64, error) {
	const query = `
		select telegram_id from users
		union
		select telegram_id from channel_members
		order by 1`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying watched people: %w", err)
	}
	defer rows.Close()

	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("collecting watched people: %w", err)
	}
	return ids, nil
}
