-- Состояние контент-системы человека вместо «кто он вообще».
--
-- Роль (сам или с командой) не показывала, что человеку нужно дальше:
-- одиночка и команда одинаково могут не выпускать стабильно. Тикет 02A
-- заменил её состоянием, у которого есть следующий шаг.
alter table users drop column if exists role;
alter table users add column if not exists stage text not null default '';

create index if not exists users_stage_idx on users (stage) where stage <> '';
