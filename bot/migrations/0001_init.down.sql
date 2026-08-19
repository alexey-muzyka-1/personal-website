-- Откат сносит базу лидов целиком. Запускать только на пустом стенде.
drop table if exists processed_updates;
drop table if exists links;
drop table if exists events;
drop table if exists attributions;
drop table if exists users;
