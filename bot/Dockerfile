# Интерфейс админки собирается первым и уезжает внутрь бинарника.
#
# Node остаётся здесь, в сборке: в проде его нет и не будет. Так админка
# получает несколько страниц и графики, а деплой — по-прежнему один
# артефакт, который нечем выкатить наполовину.
FROM node:22-alpine AS ui

WORKDIR /ui
# Зависимости отдельным слоем: правка вёрстки не должна тянуть npm ci.
COPY admin-ui/package.json admin-ui/package-lock.json* ./
RUN npm ci --no-audit --no-fund

COPY admin-ui/ ./
# Сборка кладёт файлы в ../internal/admin/ui, поэтому каталог должен
# существовать: скрипт пишет в него, а не создаёт дерево репозитория.
RUN mkdir -p /internal/admin/ui && npm run build

# Бот — один статический бинарник без зависимостей рантайма.
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Собранный интерфейс кладётся туда, откуда его вшивает go:embed.
COPY --from=ui /internal/admin/ui/ ./internal/admin/ui/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# distroless: нет шелла и пакетного менеджера, поэтому взломанный процесс
# почти нечем продолжить.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/bot /bot
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/bot"]
