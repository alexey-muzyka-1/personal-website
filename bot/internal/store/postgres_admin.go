package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

var _ admin.Reader = (*Postgres)(nil)

// Чтение для страницы воронки. Запросы идут мимо транзакций записи: это
// отчёт, ему не нужна ни атомарность, ни блокировки.

func (p *Postgres) Stages(ctx context.Context) (map[string]admin.Stage, error) {
	const query = `
		select name, count(*) as events, count(distinct telegram_id) as people
		from events
		group by name`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying stages: %w", err)
	}
	defer rows.Close()

	stages := map[string]admin.Stage{}
	for rows.Next() {
		var s admin.Stage
		if err := rows.Scan(&s.Name, &s.Events, &s.People); err != nil {
			return nil, fmt.Errorf("scanning stage: %w", err)
		}
		stages[s.Name] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stages: %w", err)
	}
	return stages, nil
}

func (p *Postgres) Sources(ctx context.Context) ([]admin.Source, error) {
	// Пустая метка остаётся отдельной строкой, а не сваливается к
	// остальным: «пришли без источника» это тоже ответ.
	const query = `
		select
			case when source_id = '' then 'без метки' else source_id end as source,
			count(distinct telegram_id) filter (where name = 'bot_started')       as started,
			count(distinct telegram_id) filter (where name = 'material_selected') as selected,
			count(distinct telegram_id) filter (where name = 'material_opened')   as opened
		from events
		group by 1
		order by started desc, source`

	rows, err := p.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying sources: %w", err)
	}
	defer rows.Close()

	sources, err := pgx.CollectRows(rows, pgx.RowToStructByPos[admin.Source])
	if err != nil {
		return nil, fmt.Errorf("collecting sources: %w", err)
	}
	return sources, nil
}

func (p *Postgres) Leads(ctx context.Context, limit int) ([]admin.Lead, error) {
	// Источник берём первый непустой: интересно, что человека привело,
	// а не куда он потом возвращался.
	const query = `
		select
			u.telegram_id,
			u.username,
			u.first_name,
			u.first_seen_at,
			coalesce((
				select a.source_id from attributions a
				where a.telegram_id = u.telegram_id and a.source_id <> ''
				order by a.occurred_at, a.id
				limit 1
			), '') as source,
			coalesce((
				select string_agg(distinct e.material_id, ', ')
				from events e
				where e.telegram_id = u.telegram_id and e.material_id <> ''
			), '') as materials,
			exists(
				select 1 from events e
				where e.telegram_id = u.telegram_id and e.name = 'material_opened'
			) as opened
		from users u
		order by u.first_seen_at desc
		limit $1`

	rows, err := p.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying leads: %w", err)
	}
	defer rows.Close()

	leads, err := pgx.CollectRows(rows, pgx.RowToStructByPos[admin.Lead])
	if err != nil {
		return nil, fmt.Errorf("collecting leads: %w", err)
	}
	return leads, nil
}
