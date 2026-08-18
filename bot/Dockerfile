# Бот — один статический бинарник без зависимостей рантайма.
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

# distroless: нет шелла и пакетного менеджера, поэтому взломанный процесс
# почти нечем продолжить.
FROM gcr.io/distroless/static-debian12

COPY --from=build /out/bot /bot
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/bot"]
