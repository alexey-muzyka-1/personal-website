package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

// Чтение канала для админки.
//
// Период здесь применяется не так, как в воронке, и это намеренно. В
// воронке срез — это когорта по дате прихода: иначе у человека часть шагов
// останется за границей периода и проценты перестанут сходиться. У канала
// период — это окно событий: вопрос «сколько подписалось за неделю» про
// подписки на этой неделе, а не про людей, которые на этой неделе впервые
// открыли бота.
//
// Исключение — конверсия и метки: там знаменатель это люди воронки,
// поэтому используется тот же cohort, что и на остальных страницах.

// ChannelSummary — размер канала, движение за период и здоровье замера.
func (p *Postgres) ChannelSummary(ctx context.Context, f admin.Filter) (admin.ChannelSummary, error) {
	const query = `
		select
			coalesce((select members from channel_size order by taken_at desc limit 1), 0) as members,
			(select taken_at from channel_size order by taken_at desc limit 1) as measured_at,
			(select max(seen_at) from channel_members) as synced_at,
			count(*) filter (where m.subscribed)                            as known,
			count(*) filter (where m.subscribed and m.joined_at is not null) as dated,
			count(*) filter (where m.subscribed and m.joined_at is null)     as undated,
			(select count(*) from channel_events e
			 where e.name = 'channel_joined'
			   and not (e.telegram_id = any($1::bigint[]))
			   and ($2::timestamptz is null or e.occurred_at >= $2)) as joined,
			(select count(*) from channel_events e
			 where e.name = 'channel_left'
			   and not (e.telegram_id = any($1::bigint[]))
			   and ($2::timestamptz is null or e.occurred_at >= $2)) as gone
		from channel_members m
		where not (m.telegram_id = any($1::bigint[]))`

	rows, err := p.pool.Query(ctx, query, hidden(f), since(f))
	if err != nil {
		return admin.ChannelSummary{}, fmt.Errorf("querying channel summary: %w", err)
	}
	defer rows.Close()

	summary, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[admin.ChannelSummary])
	if err != nil {
		return admin.ChannelSummary{}, fmt.Errorf("collecting channel summary: %w", err)
	}
	return summary, nil
}

// ChannelConversion — что воронка даёт каналу.
//
// Корзины взаимоисключающие по построению: один case на человека. Считать
// их отдельными exists — верный способ получить сумму больше числа людей
// и не заметить этого.
func (p *Postgres) ChannelConversion(ctx context.Context, f admin.Filter) (admin.ChannelConversion, error) {
	const query = cohort + `,
		buckets as (
			select case
				when cm.telegram_id is null                                then 'never'
				when cm.subscribed and cm.joined_at is null                then 'undated'
				when cm.subscribed and cm.joined_at >= p.first_seen_at     then 'after'
				when cm.subscribed                                         then 'before'
				when cm.left_at is not null                                then 'left'
				else 'never'
			end as bucket
			from people p
			left join channel_members cm on cm.telegram_id = p.telegram_id
		)
		select
			count(*)                                    as people,
			count(*) filter (where bucket = 'after')    as after_start,
			count(*) filter (where bucket = 'before')   as before_start,
			count(*) filter (where bucket = 'undated')  as undated,
			count(*) filter (where bucket = 'left')     as gone,
			count(*) filter (where bucket = 'never')    as never
		from buckets`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return admin.ChannelConversion{}, fmt.Errorf("querying channel conversion: %w", err)
	}
	defer rows.Close()

	conversion, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[admin.ChannelConversion])
	if err != nil {
		return admin.ChannelConversion{}, fmt.Errorf("collecting channel conversion: %w", err)
	}
	return conversion, nil
}

// ChannelDaily — движение канала по дням.
//
// Дни без событий остаются нулями, а не пропадают: разрыв в ряду читается
// как «данных нет», хотя на самом деле никто не приходил и не уходил.
// Размер канала берётся последним снимком суток — за день их несколько, и
// вечерний ближе к итогу дня, чем утренний.
func (p *Postgres) ChannelDaily(ctx context.Context, f admin.Filter) ([]admin.ChannelDay, error) {
	const query = `
		with bounds as (
			select
				coalesce($2::timestamptz, least(
					(select min(occurred_at) from channel_events),
					(select min(taken_at) from channel_size),
					now()
				)) as lo,
				now() as hi
		),
		days as (
			select generate_series(
				date_trunc('day', (select lo from bounds) at time zone 'Europe/Moscow'),
				date_trunc('day', (select hi from bounds) at time zone 'Europe/Moscow'),
				interval '1 day'
			)::date as day
		),
		moves as (
			select
				(occurred_at at time zone 'Europe/Moscow')::date as day,
				count(*) filter (where name = 'channel_joined')  as joined,
				count(*) filter (where name = 'channel_left')    as gone
			from channel_events
			where not (telegram_id = any($1::bigint[]))
			group by 1
		),
		sizes as (
			select distinct on ((taken_at at time zone 'Europe/Moscow')::date)
				(taken_at at time zone 'Europe/Moscow')::date as day,
				members
			from channel_size
			order by (taken_at at time zone 'Europe/Moscow')::date, taken_at desc
		)
		select
			to_char(d.day, 'YYYY-MM-DD')  as date,
			coalesce(m.joined, 0)         as joined,
			coalesce(m.gone, 0)           as gone,
			coalesce(s.members, 0)        as members
		from days d
		left join moves m on m.day = d.day
		left join sizes s on s.day = d.day
		order by d.day`

	rows, err := p.pool.Query(ctx, query, hidden(f), since(f))
	if err != nil {
		return nil, fmt.Errorf("querying channel daily: %w", err)
	}
	defer rows.Close()

	daily, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.ChannelDay])
	if err != nil {
		return nil, fmt.Errorf("collecting channel daily: %w", err)
	}
	return daily, nil
}

// ChannelSources — метка источника и её путь до подписки.
//
// Колонки разные не для полноты: «подписан» включает тех, кто был в
// канале раньше, чем узнал про бота, и приписывать их метке нельзя.
// Заслуга метки — только последняя колонка.
func (p *Postgres) ChannelSources(ctx context.Context, f admin.Filter) ([]admin.ChannelSource, error) {
	const query = cohort + `
		select
			p.source as source,
			count(*) as started,
			count(*) filter (where exists(
				select 1 from channel_members cm
				where cm.telegram_id = p.telegram_id and cm.subscribed
			)) as subscribed,
			count(*) filter (where exists(
				select 1 from channel_members cm
				where cm.telegram_id = p.telegram_id
				  and cm.subscribed
				  and cm.joined_at >= p.first_seen_at
			)) as after_start
		from people p
		group by p.source
		order by started desc, p.source`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return nil, fmt.Errorf("querying channel sources: %w", err)
	}
	defer rows.Close()

	sources, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.ChannelSource])
	if err != nil {
		return nil, fmt.Errorf("collecting channel sources: %w", err)
	}
	return sources, nil
}

// channelPersonColumns — подписчик одинаково собирается для всех списков
// и для карточки человека.
const channelPersonColumns = `
	m.telegram_id as telegram_id,
	m.username    as username,
	m.first_name  as first_name,
	m.status      as status,
	m.subscribed  as subscribed,
	m.joined_at   as joined_at,
	m.left_at     as left_at,
	m.source_id   as source_id,
	exists(select 1 from users u where u.telegram_id = m.telegram_id) as lead`

// ChannelPeople — список подписчиков под конкретный вопрос.
func (p *Postgres) ChannelPeople(
	ctx context.Context, f admin.Filter, c admin.ChannelCohort, limit int,
) ([]admin.ChannelPerson, error) {
	// Параметры собираются вместе с условием: у выгрузки периода нет
	// вовсе, и лишний $2 в списке аргументов был бы не «не влияет», а
	// «запрос не выполнится».
	params := []any{hidden(f), limit}
	var where, order string
	switch c {
	case admin.CohortGone:
		// Период по дате ухода: вопрос «кто отписался за неделю» про эту
		// неделю, а не про то, когда эти люди подписались.
		where = `not m.subscribed and m.left_at is not null
			and ($3::timestamptz is null or m.left_at >= $3)`
		order = `m.left_at desc`
		params = append(params, since(f))
	case admin.CohortOutside:
		where = `m.subscribed and not exists(
			select 1 from users u where u.telegram_id = m.telegram_id)
			and ($3::timestamptz is null or m.joined_at >= $3)`
		order = `m.joined_at desc nulls last`
		params = append(params, since(f))
	case admin.CohortEveryone:
		// Выгрузка отдаёт всё, что известно, без периода: у половины
		// подписчиков даты нет вовсе, и период вычеркнул бы именно их.
		where = `true`
		order = `m.joined_at desc nulls last`
	default:
		return nil, fmt.Errorf("unknown channel cohort %q", c)
	}

	query := `
		select` + channelPersonColumns + `
		from channel_members m
		where not (m.telegram_id = any($1::bigint[]))
		  and (` + where + `)
		order by ` + order + `
		limit $2`

	rows, err := p.pool.Query(ctx, query, params...)
	if err != nil {
		return nil, fmt.Errorf("querying channel people: %w", err)
	}
	defer rows.Close()

	people, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.ChannelPerson])
	if err != nil {
		return nil, fmt.Errorf("collecting channel people: %w", err)
	}
	return people, nil
}

// ChannelMember — подписка одного человека для его карточки. Скрытые
// аккаунты сюда не подмешиваются: карточка сама решает, кого показывать.
func (p *Postgres) ChannelMember(ctx context.Context, telegramID int64) (admin.ChannelPerson, bool, error) {
	query := `
		select` + channelPersonColumns + `
		from channel_members m
		where m.telegram_id = $1`

	rows, err := p.pool.Query(ctx, query, telegramID)
	if err != nil {
		return admin.ChannelPerson{}, false, fmt.Errorf("querying channel member: %w", err)
	}
	defer rows.Close()

	member, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[admin.ChannelPerson])
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Про человека просто ничего не известно. Это не сбой: в канале
		// его могло не быть, а могло и быть — Telegram списка не отдаёт.
		return admin.ChannelPerson{}, false, nil
	case err != nil:
		return admin.ChannelPerson{}, false, fmt.Errorf("collecting channel member: %w", err)
	}
	return member, true, nil
}

// hidden — скрытые id как параметр запроса. Пустой срез вместо nil:
// в SQL это пустой массив, а не NULL, с которым any() ничего не найдёт.
func hidden(f admin.Filter) []int64 {
	if f.Hidden == nil {
		return []int64{}
	}
	return f.Hidden
}
