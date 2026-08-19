# Деплой: одна машина, четыре контейнера

Всё живёт на одном VPS: прокси с сертификатами, бот, Postgres и снятие
дампов. Managed-базы нет намеренно — см. «Что мы взяли на себя».

```text
интернет ──► caddy (443) ──► bot ──► postgres
                                      ▲
                                   backup ──► rclone наружу
```

## Что нужно

- VPS, 2 ядра и 2 ГБ памяти с запасом. Docker и docker compose.
- Домен с A-записью на этот VPS. Сертификат Caddy выпустит сам.
- Токен из BotFather.

## Первый запуск

```bash
git clone <репозиторий> && cd bot/deploy
cp .env.example .env && vim .env          # токен, секрет, пароль базы

openssl rand -hex 32                      # TELEGRAM_WEBHOOK_SECRET
openssl rand -hex 24                      # POSTGRES_PASSWORD

docker compose up -d postgres             # сначала база
docker compose --profile tools run --rm migrate up   # потом схема
docker compose up -d                      # потом всё остальное
```

Когда контейнеры поднялись — один раз зарегистрировать webhook:

```bash
# в .env: TELEGRAM_SET_WEBHOOK=true
docker compose up -d bot
docker compose logs bot | grep "webhook registered"
# вернуть TELEGRAM_SET_WEBHOOK=false, чтобы не дёргать Telegram
# при каждом рестарте
```

Проверка:

```bash
curl https://<домен>/healthz            # 200
curl https://<домен>/telegram/webhook   # 404, потому что не POST и без секрета
```

## Обновление

```bash
git pull
docker compose --profile tools run --rm migrate up   # если появились миграции
docker compose up -d --build bot
```

Миграции всегда отдельным шагом и до выката кода. Автоматически при
старте они не применяются специально: схема базы лидов не должна
меняться как побочный эффект деплоя.

## Бэкапы

Дамп снимается раз в сутки, хранится 14 дней. Если задан `BACKUP_REMOTE`,
каждая копия сразу уезжает наружу через rclone.

```bash
docker compose logs backup | tail        # что снялось и уехало
docker compose exec backup ls -lh /backups
./restore.sh                             # развернуть последний и посмотреть
```

**Раз в месяц запускать `./restore.sh`.** Он разворачивает дамп в
отдельную базу рядом с боевой и печатает, сколько там людей, касаний и
событий. Бэкап, который ни разу не разворачивали, — не бэкап.

Пока `BACKUP_REMOTE` пустой, копии лежат на том же диске. От «удалил не
ту строку» это спасает, от смерти машины — нет.

## Что мы взяли на себя, отказавшись от managed-базы

| Забота | Кто делает | Как не забыть |
|---|---|---|
| Бэкапы | контейнер `backup` | логи раз в неделю |
| Проверка восстановления | человек | `./restore.sh` раз в месяц |
| Обновления Postgres | человек | версия зафиксирована, мажор — отдельная операция с дампом |
| Место на диске | человек | `df -h`, дампы жмутся gzip |
| Смерть машины | человек | только `BACKUP_REMOTE` |

Взамен: одна машина вместо двух, никаких внешних лимитов и засыпающих
бесплатных тиров, база рядом с ботом и полный контроль над данными
лидов и будущих платежей.

Обратная дорога открыта: код ходит в базу по `DATABASE_URL` и не знает,
где она стоит. Переезд на managed — это смена одной строки и `pg_restore`.

## Если сломалось

```bash
docker compose ps                     # кто упал
docker compose logs -f bot            # ошибки шагов воронки
docker compose logs caddy             # сертификаты и маршруты
docker compose exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "select name, count(*) from events group by 1 order by 2 desc;"'
```

Последняя команда отвечает на главный вопрос: доходят ли вообще люди и
на каком шаге теряются.
