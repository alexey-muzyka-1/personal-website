-- Тикет 01. Четыре сущности и таблица идемпотентности — всё, что нужно,
-- чтобы не потерять ни одного пришедшего человека.
--
-- Пустая строка вместо NULL там, где «значения не было»: source_id и
-- material_id участвуют в группировках, а NULL в group by молча съедает
-- строки и врёт в отчётах.

create table if not exists users (
    telegram_id   bigint      primary key,
    username      text        not null default '',
    first_name    text        not null default '',
    first_seen_at timestamptz not null,
    last_seen_at  timestamptz not null
);

-- Касания источника, только добавление. Первая строка = first touch и
-- никогда не меняется, последняя = last touch.
create table if not exists attributions (
    id          bigserial   primary key,
    telegram_id bigint      not null references users (telegram_id) on delete cascade,
    source_id   text        not null default '',
    raw_payload text        not null default '',
    occurred_at timestamptz not null
);

-- Выборка last touch: ищем последнее непустое касание конкретного человека.
create index if not exists attributions_last_source_idx
    on attributions (telegram_id, occurred_at desc, id desc)
    where source_id <> '';

-- Лента событий воронки: bot_started → material_selected → material_opened.
create table if not exists events (
    id          bigserial   primary key,
    telegram_id bigint      not null references users (telegram_id) on delete cascade,
    name        text        not null,
    source_id   text        not null default '',
    material_id text        not null default '',
    metadata    jsonb       not null default '{}'::jsonb,
    occurred_at timestamptz not null
);

-- Путь одного человека по времени.
create index if not exists events_user_idx on events (telegram_id, occurred_at);
-- Воронка целиком: сколько дошло до шага за период.
create index if not exists events_name_idx on events (name, occurred_at);
-- Выручка и переходы в разрезе конкретного Reel.
create index if not exists events_source_idx on events (source_id, name, occurred_at)
    where source_id <> '';

-- Токены tracked redirect. Токен случайный и не связан с telegram_id
-- ничем, кроме этой строки.
create table if not exists links (
    token       text        primary key,
    telegram_id bigint      not null references users (telegram_id) on delete cascade,
    material_id text        not null,
    source_id   text        not null default '',
    created_at  timestamptz not null
);

create index if not exists links_user_idx on links (telegram_id, created_at);

-- Идемпотентность: Telegram штатно повторяет доставку update.
-- Строка пишется в той же транзакции, что и события, поэтому повтор
-- после сбоя обработается заново, а повтор после успеха — нет.
create table if not exists processed_updates (
    update_id    bigint      primary key,
    processed_at timestamptz not null
);
