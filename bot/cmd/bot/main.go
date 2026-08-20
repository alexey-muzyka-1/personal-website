// Команда bot — процесс личной воронки: webhook Telegram и tracked
// redirect на статьи сайта.
//
// Конфигурация только из окружения. Токен, секрет webhook и DSN базы
// никогда не попадают в репозиторий.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/store"
	"github.com/alexey-muzyka-1/personal-website/bot/internal/telegram"
)

const (
	webhookPath     = "/telegram/webhook"
	shutdownTimeout = 15 * time.Second
	startupTimeout  = 15 * time.Second
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log); err != nil {
		log.Error("bot stopped", "error", err)
		os.Exit(1)
	}
}

type config struct {
	addr string
	// siteBase — куда ведём людей: публичный адрес сайта со статьями.
	siteBase string
	// publicBase — где живёт сам бот: на него Telegram шлёт webhook, с
	// него же уходит tracked redirect.
	publicBase    string
	channelURL    string
	botToken      string
	webhookSecret string
	databaseURL   string
	setWebhook    bool
	// hiddenIDs — кого не показывать на странице воронки. Свой тестовый
	// аккаунт в отчёте о чужом поведении даёт ложную картину: на выборке
	// из нескольких человек он один сдвигает все проценты.
	hiddenIDs []int64
}

func loadConfig() (config, error) {
	cfg := config{
		addr:       envOr("ADDR", ":8080"),
		siteBase:   envOr("SITE_BASE_URL", "https://alexeymuzyka.com"),
		channelURL: envOr("TELEGRAM_CHANNEL_URL", "https://t.me/alexeymuzykablog"),
		setWebhook: envOr("TELEGRAM_SET_WEBHOOK", "") == "true",
	}

	var missing []string
	for _, required := range []struct {
		name string
		dst  *string
	}{
		{"PUBLIC_BASE_URL", &cfg.publicBase},
		{"TELEGRAM_BOT_TOKEN", &cfg.botToken},
		{"TELEGRAM_WEBHOOK_SECRET", &cfg.webhookSecret},
		{"DATABASE_URL", &cfg.databaseURL},
	} {
		value := os.Getenv(required.name)
		if value == "" {
			missing = append(missing, required.name)
			continue
		}
		*required.dst = value
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing env: %v", missing)
	}

	hidden, err := parseIDs(os.Getenv("ADMIN_HIDDEN_TELEGRAM_IDS"))
	if err != nil {
		return config{}, fmt.Errorf("ADMIN_HIDDEN_TELEGRAM_IDS: %w", err)
	}
	cfg.hiddenIDs = hidden

	return cfg, nil
}

// parseIDs читает список telegram_id через запятую. Опечатка роняет
// старт, а не молча показывает отчёт вместе с тестовым аккаунтом: тихо
// проигнорированный фильтр здесь хуже упавшего процесса.
func parseIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a telegram id", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	// Хранилище только Postgres: база лидов не должна зависеть от того,
	// переживёт ли процесс перезапуск.
	db, err := store.NewPostgres(startupCtx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer db.Close()

	// Адрес бота для deep link на странице маршрутов. Берём из имени
	// пользователя бота, а не из домена: /admin/routes показывает ссылку,
	// которую человек отправит в Instagram, а не адрес сервера.
	botLink := "https://t.me/" + envOr("TELEGRAM_BOT_USERNAME", "alexeymuzyka_bot")

	catalog := funnel.DefaultCatalog()
	scenario, err := funnel.New(
		db, catalog, cfg.siteBase, cfg.publicBase,
		funnel.WithChannel(cfg.channelURL),
	)
	if err != nil {
		return fmt.Errorf("building funnel: %w", err)
	}

	client, err := telegram.NewClient(cfg.botToken)
	if err != nil {
		return fmt.Errorf("building telegram client: %w", err)
	}

	webhook, err := telegram.NewHandler(scenario, client, cfg.webhookSecret, log)
	if err != nil {
		return fmt.Errorf("building webhook handler: %w", err)
	}

	if cfg.setWebhook {
		url := cfg.publicBase + webhookPath
		if err := client.SetWebhook(startupCtx, url, cfg.webhookSecret); err != nil {
			return fmt.Errorf("registering webhook: %w", err)
		}
		log.Info("webhook registered", "url", url)
	}

	// Админка на чтение. Пароль спрашивает Caddy перед тем, как пустить
	// сюда запрос: своей авторизации в боте нет.
	//
	// Каталог тот же, по которому отвечает бот, поэтому страница маршрутов
	// не может разойтись с тем, что человек реально получает.
	adminPage, err := admin.NewHandler(db, log,
		admin.WithHidden(cfg.hiddenIDs),
		admin.WithCatalog(catalog, botLink, cfg.siteBase),
	)
	if err != nil {
		return fmt.Errorf("building admin page: %w", err)
	}

	adminUI, built := admin.NewUI()
	if !built {
		// Не падаем: бот это в первую очередь webhook, и отсутствующая
		// статика не повод ронять воронку. Но молчать об этом нельзя.
		log.Warn("admin ui is not built", "fix", "cd bot/admin-ui && npm ci && npm run build")
	}

	mux := http.NewServeMux()
	mux.Handle("POST "+webhookPath, webhook)

	// Данные и страницы разведены: JSON отдаёт Go, страницы собраны Astro
	// и вшиты в бинарник. Поэтому вёрстка правится без пересборки Go, а
	// схему базы по-прежнему знает только Go.
	mux.HandleFunc("GET /admin/api/overview", adminPage.ServeOverview)
	mux.HandleFunc("GET /admin/api/people", adminPage.ServePeople)
	mux.HandleFunc("GET /admin/api/person", adminPage.ServePerson)
	mux.HandleFunc("GET /admin/api/routes", adminPage.ServeRoutes)
	mux.HandleFunc("GET /admin/export.xlsx", adminPage.ServeExport)
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))
	mux.Handle("GET /admin/", adminUI)
	redirect, err := telegram.NewRedirect(scenario, client, cfg.siteBase+"/articles", log)
	if err != nil {
		return fmt.Errorf("building redirect handler: %w", err)
	}
	mux.Handle("GET /r/{token}", redirect)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.addr, "site", cfg.siteBase, "public", cfg.publicBase)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Даём доработать запросам, которые уже пишут события в базу.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	log.Info("stopped")
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
