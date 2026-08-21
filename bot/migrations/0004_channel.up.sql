-- Канал как вторая половина воронки: бот знает, кто пришёл, канал знает,
-- кто остался. Пока эти две базы врозь, вопрос «сколько людей из бота
-- дошло до подписки» не имеет ответа.
--
-- Ссылок на users здесь нет намеренно, ни одной. Подписчик канала может
-- никогда не открывать бота, и внешний ключ на users отвергал бы ровно
-- тех людей, ради которых всё считается. Связь лид ↔ подписчик делается
-- при чтении, по telegram_id.

-- Текущее состояние подписки. Одна строка на человека: история живёт в
-- channel_events, здесь только «как обстоят дела сейчас».
create table if not exists channel_members (
    telegram_id bigint      primary key,
    username    text        not null default '',
    first_name  text        not null default '',
    -- Слово Telegram как есть: member, administrator, creator, left,
    -- kicked. Перевод в свои термины — это место, где однажды появится
    -- статус, которого мы не ждали.
    status      text        not null default '',
    -- Считается ли этот статус подпиской. Решает Go, при записи: правило
    -- «админ канала тоже подписан» должно жить в одном месте, а не
    -- переписываться в каждый запрос отчёта заново.
    subscribed  boolean     not null default false,
    -- NULL = подписался до того, как бот стал админом и начал считать.
    -- Даты подписки у этих людей нет и никогда не будет: Telegram её не
    -- отдаёт. Подставлять сюда день сверки нельзя — весь прирост за тот
    -- день окажется липовым.
    joined_at   timestamptz,
    left_at     timestamptz,
    -- Пригласительная ссылка, по которой человек вошёл, если Telegram её
    -- прислал. Ссылки заводятся руками в настройках канала; бот их не
    -- создаёт, но записывает, если они есть.
    invite_link text        not null default '',
    -- Метка источника этого человека в боте на момент подписки. Пустая —
    -- значит в боте его тогда не знали.
    source_id   text        not null default '',
    -- Когда мы последний раз подтверждали этот статус. По самой свежей
    -- строке видно, работает ли замер вообще.
    seen_at     timestamptz not null
);

create index if not exists channel_members_subscribed_idx on channel_members (subscribed);
create index if not exists channel_members_joined_idx on channel_members (joined_at);

-- Лента подписок и отписок, только добавление. Отток нельзя хранить
-- вычитанием из счётчика: «минус три» не отвечает на вопрос, кто именно
-- ушёл и через сколько дней после подписки.
create table if not exists channel_events (
    id          bigserial   primary key,
    telegram_id bigint      not null,
    name        text        not null,
    invite_link text        not null default '',
    source_id   text        not null default '',
    -- true = событие не пришло от Telegram, а замечено сверкой. Значит
    -- время приблизительное: человек ушёл когда-то до этой минуты.
    -- Смешивать такие строки с точными без пометки — врать в графике.
    noticed     boolean     not null default false,
    occurred_at timestamptz not null
);

create index if not exists channel_events_name_idx on channel_events (name, occurred_at);
create index if not exists channel_events_user_idx on channel_events (telegram_id, occurred_at);

-- Снимки размера канала. Нужны для двух разных вещей: показать прирост
-- вместе с базой, которую мы не знаем поимённо, и поймать расхождение —
-- если наши события не сходятся с реальным числом, замер дырявый, и это
-- должно быть видно раньше, чем по цифрам примут решение.
create table if not exists channel_size (
    taken_at timestamptz primary key,
    members  int         not null
);
