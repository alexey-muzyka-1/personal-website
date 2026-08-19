#!/bin/sh
# Единственное определение того, как на машине запускается стек.
#
# Зовут отсюда все: руки, Ansible и GitHub Actions. Пока команда была
# записана в каждом из них отдельно, они гарантированно разъехались бы —
# и разъехались бы молча.
#
#   deploy/compose.sh ps
#   deploy/compose.sh logs -f bot
#   deploy/compose.sh up -d --build
#   deploy/compose.sh --profile tools run --rm migrate up

set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"

exec docker compose --env-file "$DIR/.env" -f "$DIR/docker-compose.yml" "$@"
