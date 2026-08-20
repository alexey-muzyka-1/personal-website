package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

// Чтение для страницы воронки. Запросы идут мимо транзакций записи: это
// отчёт, ему не нужна ни атомарность, ни блокировки.
//
// Строки собираются по именам колонок (RowToStructByName), а не по
// порядку. Раньше было по порядку, и `stage` с `source` в Leads стояли в
// select не в том порядке, что поля структуры: обе колонки текстовые,
// ошибки не возникало, значения просто менялись местами. По именам такое
// не собирается вовсе — запрос падает, а не врёт.

// cohort — общий пролог всех запросов страницы: люди, попавшие в срез.
//
// Источник человека это его первое непустое касание. Определять его один
// раз здесь, а не в каждом запросе, важно не для скорости: иначе «людей
// из site_home» в одной таблице и в другой считалось бы по разным
// правилам, и цифры перестали бы сходиться между собой.
//
// $1 скрытые id, $2 граница периода, $3 источник, $4 состояние.
const cohort = `
	with visible as (
		select
			u.telegram_id,
			u.username,
			u.first_name,
			u.first_seen_at,
			u.stage,
			coalesce((
				select a.source_id from attributions a
				where a.telegram_id = u.telegram_id and a.source_id <> ''
				order by a.occurred_at, a.id
				limit 1
			), '') as source
		from users u
		where not (u.telegram_id = any($1::bigint[]))
		  and ($2::timestamptz is null or u.first_seen_at >= $2)
	),
	people as (
		select * from visible
		where case
				when $3::text = ''  then true
				when $3::text = '-' then source = ''
				else source = $3
			  end
		  and case
				when $4::text = ''  then true
				when $4::text = '-' then stage = ''
				else stage = $4
			  end
	)`

// args раскладывает фильтр в параметры запроса в том порядке, в каком их
// ждёт cohort.
func args(f admin.Filter) []any {
	hidden := f.Hidden
	if hidden == nil {
		hidden = []int64{}
	}
	var since any
	if !f.Since.IsZero() {
		since = f.Since
	}
	return []any{hidden, since, f.Source, f.Stage}
}

func (p *Postgres) Stages(ctx context.Context, f admin.Filter) (map[string]admin.Stage, error) {
	const query = cohort + `
		select
			e.name                          as name,
			count(*)                        as events,
			count(distinct e.telegram_id)   as people
		from events e
		join people p on p.telegram_id = e.telegram_id
		group by e.name`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return nil, fmt.Errorf("querying stages: %w", err)
	}
	defer rows.Close()

	collected, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.Stage])
	if err != nil {
		return nil, fmt.Errorf("collecting stages: %w", err)
	}

	stages := make(map[string]admin.Stage, len(collected))
	for _, s := range collected {
		stages[s.Name] = s
	}
	return stages, nil
}

// Segments — кого куда привели. Отвечает на вопрос, кому сейчас есть что
// предложить, не открывая карточку каждого человека.
//
// Не ответившие остаются строкой, а не выпадают: сумма по таблице должна
// сходиться с числом пришедших, иначе непонятно, потерялись люди до
// вопроса или после.
func (p *Postgres) Segments(ctx context.Context, f admin.Filter) ([]admin.Segment, error) {
	const query = cohort + `
		select
			p.stage    as stage,
			count(*)   as people,
			count(*) filter (where exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'waitlist_joined'
			)) as waitlist
		from people p
		group by p.stage
		order by people desc, p.stage`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return nil, fmt.Errorf("querying segments: %w", err)
	}
	defer rows.Close()

	segments, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.Segment])
	if err != nil {
		return nil, fmt.Errorf("collecting segments: %w", err)
	}
	return segments, nil
}

// Sources — метка и её результат. Колонки идут по пути к деньгам, а не по
// порядку событий: «выбрал материал» равен «запустил бота» у всех и не
// различает источники.
func (p *Postgres) Sources(ctx context.Context, f admin.Filter) ([]admin.Source, error) {
	const query = cohort + `
		select
			p.source  as source,
			count(*)  as started,
			count(*) filter (where exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'material_opened'
			)) as opened,
			count(*) filter (where exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'offer_shown'
			)) as offered,
			count(*) filter (where exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'waitlist_joined'
			)) as waitlist
		from people p
		group by p.source
		order by started desc, p.source`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return nil, fmt.Errorf("querying sources: %w", err)
	}
	defer rows.Close()

	sources, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.Source])
	if err != nil {
		return nil, fmt.Errorf("collecting sources: %w", err)
	}
	return sources, nil
}

// leadColumns — карточка человека одинаково собирается и для списка, и
// для одной персоны. Держать её в одном месте дешевле, чем ловить потом
// расхождение между таблицей и карточкой.
const leadColumns = `
	p.telegram_id   as telegram_id,
	p.username      as username,
	p.first_name    as first_name,
	p.first_seen_at as first_seen_at,
	p.source        as source,
	p.stage         as stage,
	coalesce((
		select string_agg(distinct e.material_id, ', ')
		from events e
		where e.telegram_id = p.telegram_id and e.material_id <> ''
	), '') as materials,
	exists(
		select 1 from events e
		where e.telegram_id = p.telegram_id and e.name = 'material_opened'
	) as opened,
	exists(
		select 1 from events e
		where e.telegram_id = p.telegram_id and e.name = 'waitlist_joined'
	) as waitlist`

func (p *Postgres) Leads(ctx context.Context, f admin.Filter, limit int) ([]admin.Lead, error) {
	const query = cohort + `
		select` + leadColumns + `
		from people p
		order by p.first_seen_at desc
		limit $5`

	rows, err := p.pool.Query(ctx, query, append(args(f), limit)...)
	if err != nil {
		return nil, fmt.Errorf("querying leads: %w", err)
	}
	defer rows.Close()

	leads, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.Lead])
	if err != nil {
		return nil, fmt.Errorf("collecting leads: %w", err)
	}
	return leads, nil
}

// Person — карточка одного человека и вся его история по порядку.
//
// Фильтр передаётся сюда не ради среза, а ради списка скрытых: открыть
// карточку по прямой ссылке не должно быть способом обойти то, что
// страница прячет.
func (p *Postgres) Person(ctx context.Context, telegramID int64, f admin.Filter) (admin.Person, error) {
	const leadQuery = cohort + `
		select` + leadColumns + `
		from people p
		where p.telegram_id = $5`

	rows, err := p.pool.Query(ctx, leadQuery, append(args(f), telegramID)...)
	if err != nil {
		return admin.Person{}, fmt.Errorf("querying person: %w", err)
	}
	defer rows.Close()

	lead, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[admin.Lead])
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return admin.Person{}, admin.ErrNoPerson
	case err != nil:
		return admin.Person{}, fmt.Errorf("collecting person: %w", err)
	}

	const momentsQuery = `
		select
			name        as name,
			source_id   as source_id,
			material_id as material_id,
			metadata    as metadata,
			occurred_at as occurred_at
		from events
		where telegram_id = $1
		order by occurred_at, id`

	momentRows, err := p.pool.Query(ctx, momentsQuery, telegramID)
	if err != nil {
		return admin.Person{}, fmt.Errorf("querying moments: %w", err)
	}
	defer momentRows.Close()

	moments, err := pgx.CollectRows(momentRows, pgx.RowToStructByName[admin.Moment])
	if err != nil {
		return admin.Person{}, fmt.Errorf("collecting moments: %w", err)
	}

	return admin.Person{Lead: lead, Moments: moments}, nil
}

// HiddenPeople — сколько скрытых аккаунтов реально есть в базе. Считаем,
// а не берём длину списка: id в конфиге может не соответствовать никому,
// и тогда подпись «скрыт 1 аккаунт» была бы неправдой.
func (p *Postgres) HiddenPeople(ctx context.Context, f admin.Filter) (int, error) {
	hidden := f.Hidden
	if len(hidden) == 0 {
		return 0, nil
	}

	const query = `select count(*) from users where telegram_id = any($1::bigint[])`

	var count int
	if err := p.pool.QueryRow(ctx, query, hidden).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting hidden people: %w", err)
	}
	return count, nil
}

// Timeline — все шаги всех людей среза. Нужен выгрузке: на экране путь
// смотрят по одному человеку, в таблице — списком.
func (p *Postgres) Timeline(ctx context.Context, f admin.Filter, limit int) ([]admin.TimelineRow, error) {
	const query = cohort + `
		select
			e.telegram_id as telegram_id,
			p.username    as username,
			e.name        as name,
			e.source_id   as source_id,
			e.material_id as material_id,
			e.metadata    as metadata,
			e.occurred_at as occurred_at
		from events e
		join people p on p.telegram_id = e.telegram_id
		order by e.occurred_at, e.id
		limit $5`

	rows, err := p.pool.Query(ctx, query, append(args(f), limit)...)
	if err != nil {
		return nil, fmt.Errorf("querying timeline: %w", err)
	}
	defer rows.Close()

	timeline, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.TimelineRow])
	if err != nil {
		return nil, fmt.Errorf("collecting timeline: %w", err)
	}
	return timeline, nil
}

// Daily — динамика по дням. Таблица показывает итог, график — набран он
// равномерно или одним днём; на выборке в десяток человек это разные
// выводы о том, работает ли источник.
//
// Дни без людей остаются нулями, а не пропадают: разрыв в ряду читается
// как «данных нет», хотя на самом деле никто не приходил.
func (p *Postgres) Daily(ctx context.Context, f admin.Filter) ([]admin.Day, error) {
	const query = cohort + `,
		bounds as (
			select
				coalesce(min(first_seen_at), now()) as lo,
				coalesce(max(first_seen_at), now()) as hi
			from people
		),
		days as (
			select generate_series(
				date_trunc('day', (select lo from bounds) at time zone 'Europe/Moscow'),
				date_trunc('day', (select hi from bounds) at time zone 'Europe/Moscow'),
				interval '1 day'
			)::date as day
		)
		select
			to_char(d.day, 'YYYY-MM-DD') as date,
			count(p.telegram_id)         as people,
			count(*) filter (where p.telegram_id is not null and exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'material_opened'
			)) as opened,
			count(*) filter (where p.telegram_id is not null and exists(
				select 1 from events e
				where e.telegram_id = p.telegram_id and e.name = 'waitlist_joined'
			)) as waitlist
		from days d
		left join people p
			on (p.first_seen_at at time zone 'Europe/Moscow')::date = d.day
		group by d.day
		order by d.day`

	rows, err := p.pool.Query(ctx, query, args(f)...)
	if err != nil {
		return nil, fmt.Errorf("querying daily: %w", err)
	}
	defer rows.Close()

	daily, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin.Day])
	if err != nil {
		return nil, fmt.Errorf("collecting daily: %w", err)
	}
	return daily, nil
}
