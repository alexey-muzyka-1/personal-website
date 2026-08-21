# Развёртывание воронки на VPS

Та же схема, что в `viralmaxing/deploy/vps_setup`: `Makefile` с `ENV`,
инвентарь на окружение, роли. Отличий два, оба намеренные.

**Postgres не ставится на хост.** Он поднимается контейнером в том же
compose, что и бот: одна машина, один файл, наружу порт не смотрит.

**Секретов в репозитории нет.** Ansible не возит ни токен бота, ни пароль
базы: `deploy/.env` создаётся один раз руками на самой машине, и
`deploy.yml` откажется выкатываться, пока его там нет.

## Один раз

```bash
ansible-galaxy collection install -r requirements.yml
cp inventories/prod/hosts.yml.dist inventories/prod/hosts.yml
vim inventories/prod/hosts.yml            # ansible_host = адрес машины
vim inventories/prod/env.yml              # домен бота

make init-vps ENV=prod                    # apt, docker, swap, юзер, ufw
```

Дальше — секреты на машину, руками:

```bash
scp ../.env.example root@<адрес>:/opt/funnel/deploy/.env
ssh root@<адрес> vim /opt/funnel/deploy/.env
```

Первая выкатка:

```bash
make deploy ENV=prod      # синхронизация, сборка, запуск, проверка /healthz
make migrate ENV=prod     # схема базы
```

Webhook регистрируется сам на каждом старте: набор типов update меняется
вместе с кодом, и ручной флаг был способом остаться без половины событий.
Убедиться по логам — `webhook registered`.

## Каждый раз

```bash
make deploy ENV=prod                      # код
make migrate ENV=prod                     # только если появились миграции
```

`make deploy` заканчивается запросом `https://<домен>/healthz` с самой
машины разработчика. Не ответил — выкатка считается неудачной.

## Что делает `make init-vps`

| Роль | Зачем |
|---|---|
| `docker` | docker + compose, ежедневная чистка образов |
| `swap` | на машине за шесть долларов сборка Go иначе падает по памяти |
| `user` | пользователь для выкатки в группе docker |
| `firewall` | ufw: наружу только 22, 80 и 443 |

## Куда что кладётся

```text
/opt/funnel/              ← синхронизируется с bot/
  deploy/.env             ← только на машине, rsync его не трогает
  deploy/docker-compose.yml
  migrations/
```

Сборка идёт на самой машине. Реестр образов и CI не подключены
намеренно: ради одного маленького бинарника это лишний слой, который
нужно отдельно поднимать и чинить. Если сборки станут долгими — тогда и
переедем на реестр.
