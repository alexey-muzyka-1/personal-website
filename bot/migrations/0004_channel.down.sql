-- Откат сносит всё, что известно про канал. Подписки и отписки
-- восстановлению не подлежат: Telegram отдаёт их один раз, событием.
drop table if exists channel_size;
drop table if exists channel_events;
drop table if exists channel_members;
