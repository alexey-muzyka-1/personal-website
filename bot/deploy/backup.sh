#!/bin/sh
# Дамп базы по расписанию.
#
# Бэкап, который лежит на том же диске, спасает от «удалил не ту строку»
# и не спасает от смерти машины. Поэтому если задан BACKUP_REMOTE —
# каждая копия сразу уезжает наружу через rclone.
#
# Восстановление проверяется руками: ./restore.sh. Непроверенный бэкап
# считается отсутствующим.

set -eu

KEEP_DAYS="${BACKUP_KEEP_DAYS:-14}"
EVERY="${BACKUP_EVERY_SECONDS:-86400}"
REMOTE="${BACKUP_REMOTE:-}"
DIR=/backups

log() {
	echo "$(date -u '+%Y-%m-%dT%H:%M:%SZ') backup: $*"
}

dump_once() {
	stamp="$(date -u '+%Y%m%dT%H%M%SZ')"
	file="$DIR/funnel-$stamp.sql.gz"
	tmp="$file.partial"

	# Сначала во временный файл: оборванный дамп не должен выглядеть
	# как готовый бэкап.
	if ! pg_dump --no-owner --no-privileges | gzip -9 > "$tmp"; then
		log "ОШИБКА: дамп не снялся"
		rm -f "$tmp"
		return 1
	fi
	mv "$tmp" "$file"
	log "снят $file ($(du -h "$file" | cut -f1))"

	if [ -n "$REMOTE" ]; then
		if command -v rclone >/dev/null 2>&1; then
			if rclone copy "$file" "$REMOTE"; then
				log "выложен в $REMOTE"
			else
				log "ОШИБКА: не выложился в $REMOTE"
			fi
		else
			log "ОШИБКА: BACKUP_REMOTE задан, а rclone в образе нет"
		fi
	else
		log "ВНИМАНИЕ: копия только на этом диске, BACKUP_REMOTE не задан"
	fi

	find "$DIR" -name 'funnel-*.sql.gz' -mtime "+$KEEP_DAYS" -delete
}

mkdir -p "$DIR"
log "старт, интервал ${EVERY}с, храним ${KEEP_DAYS} дней"

while true; do
	dump_once || log "продолжаю, следующая попытка через ${EVERY}с"
	sleep "$EVERY"
done
