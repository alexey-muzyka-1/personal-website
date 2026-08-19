drop index if exists users_role_idx;
alter table users drop column if exists role;
