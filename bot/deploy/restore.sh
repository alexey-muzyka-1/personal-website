#!/bin/sh
# Проверка бэкапа восстановлением.
#
# Разворачивает дамп в отдельную базу рядом с боевой и печатает, что в
# нём оказалось. Боевую базу не трогает.
#
#   ./restore.sh                       # последний дамп
#   ./restore.sh funnel-2026….sql.gz   # конкретный
#
# Запускать раз в месяц. Бэкап, который ни разу не разворачивали, — не
# бэкап, а надежда.

set -eu

COMPOSE="docker compose"
CHECK_DB="funnel_restore_check"

# psql внутри контейнера должен ходить под пользователем базы, а не под
# root: роли root в Postgres нет.
psql_run() {
	# shellcheck disable=SC2016
	$COMPOSE exec -T postgres sh -c 'psql -U "$POSTGRES_USER" '"$1"
}

dump="${1:-}"
if [ -z "$dump" ]; then
	dump="$($COMPOSE exec -T backup sh -c 'ls -1t /backups/funnel-*.sql.gz 2>/dev/null | head -1')"
	if [ -z "$dump" ]; then
		echo "нет ни одного дампа в /backups" >&2
		exit 1
	fi
else
	dump="/backups/$dump"
fi

echo "проверяю $dump"

psql_run "-v ON_ERROR_STOP=1 -d postgres -c 'drop database if exists $CHECK_DB;'"
psql_run "-v ON_ERROR_STOP=1 -d postgres -c 'create database $CHECK_DB;'"

# shellcheck disable=SC2016
$COMPOSE exec -T backup sh -c "gunzip -c '$dump'" \
	| $COMPOSE exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -v ON_ERROR_STOP=1 -d '"$CHECK_DB" >/dev/null

echo
echo "что развернулось:"
psql_run "-d $CHECK_DB -c \"
	select 'users' as tbl, count(*) from users
	union all select 'attributions', count(*) from attributions
	union all select 'events', count(*) from events
	union all select 'links', count(*) from links
	order by 1;\""

echo "самое свежее событие в копии:"
psql_run "-d $CHECK_DB -t -c 'select max(occurred_at) from events;'"

psql_run "-v ON_ERROR_STOP=1 -d postgres -c 'drop database $CHECK_DB;'"
echo "проверочная база удалена, боевая не тронута"
