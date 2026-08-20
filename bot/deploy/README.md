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

deploy/compose.sh up -d postgres          # сначала база
deploy/compose.sh --profile tools run --rm migrate up   # схема
deploy/compose.sh up -d                   # потом всё остальное
```

Когда контейнеры поднялись — один раз зарегистрировать webhook:

```bash
# в .env: TELEGRAM_SET_WEBHOOK=true
deploy/compose.sh up -d bot
deploy/compose.sh logs bot | grep "webhook registered"
# вернуть TELEGRAM_SET_WEBHOOK=false, чтобы не дёргать Telegram
# при каждом рестарте
```

Проверка:

```bash
curl https://<домен>/healthz            # 200
curl https://<домен>/telegram/webhook   # 404, потому что не POST и без секрета
```

## Обновление

Обычно ничего делать не надо: пуш в `main` с изменениями в `bot/` гоняет
тесты и выкатывает сам — `.github/workflows/bot-deploy.yml`. Миграции в
пайплайн не входят.

Руками, если CI недоступен:

```bash
git pull
deploy/compose.sh --profile tools run --rm migrate up   # если появились миграции
deploy/compose.sh up -d --build bot
```

Миграции всегда отдельным шагом и до выката кода. Автоматически при
старте они не применяются специально: схема базы лидов не должна
меняться как побочный эффект деплоя.

## Страница воронки

`https://<домен>/admin` — шаги воронки, метки источников и последние
пятьдесят человек. Только чтение. Логин и пароль спрашивает Caddy,
хэш лежит в `.env` (доллары в нём должны быть удвоены, см. `.env.example`).

## Бэкапы

Дамп снимается раз в сутки, хранится 14 дней. Если задан `BACKUP_REMOTE`,
каждая копия сразу уезжает наружу через rclone.

```bash
deploy/compose.sh logs backup | tail      # что снялось и уехало
deploy/compose.sh exec backup ls -lh /backups
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
deploy/compose.sh ps                  # кто упал
deploy/compose.sh logs -f bot         # ошибки шагов воронки
deploy/compose.sh logs caddy          # сертификаты и маршруты

# правка маршрутов: caddy/Caddyfile, затем
deploy/compose.sh exec caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
deploy/compose.sh exec postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  -c "select name, count(*) from events group by 1 order by 2 desc;"'
```

Последняя команда отвечает на главный вопрос: доходят ли вообще люди и
на каком шаге теряются.
