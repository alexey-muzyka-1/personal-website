# Образ для снятия дампов: pg_dump нужной версии плюс rclone, чтобы
# копия могла уехать с этой машины. Без rclone настройка BACKUP_REMOTE
# была бы ручкой, которая ничего не делает.
FROM postgres:17-alpine

RUN apk add --no-cache rclone

ENTRYPOINT ["/bin/sh", "/usr/local/bin/backup.sh"]
