drop index if exists users_stage_idx;
alter table users drop column if exists stage;
alter table users add column if not exists role text not null default '';
