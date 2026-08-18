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
	"syscall"
	"time"

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
	botToken      string
	webhookSecret string
	databaseURL   string
	setWebhook    bool
}

func loadConfig() (config, error) {
	cfg := config{
		addr:       envOr("ADDR", ":8080"),
		siteBase:   envOr("SITE_BASE_URL", "https://alexeymuzyka.com"),
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
	return cfg, nil
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

	scenario, err := funnel.New(db, funnel.DefaultCatalog(), cfg.siteBase, cfg.publicBase)
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

	mux := http.NewServeMux()
	mux.Handle("POST "+webhookPath, webhook)
	mux.HandleFunc("GET /r/{token}", redirect(scenario, cfg.siteBase, log))
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

// redirect — tracked-переход на статью. Единственное место, где считается
// факт клика: Telegram о нажатии URL-кнопки не сообщает.
func redirect(scenario *funnel.Funnel, siteBase string, log *slog.Logger) http.HandlerFunc {
	fallback := siteBase + "/articles"

	return func(w http.ResponseWriter, r *http.Request) {
		token := r.PathValue("token")

		target, err := scenario.Open(r.Context(), token)
		if err != nil {
			// Человек не должен упереться в ошибку из-за нашей проблемы
			// или чужой ссылки: ведём его в список статей.
			level := slog.LevelError
			if errors.Is(err, funnel.ErrUnknownToken) {
				level = slog.LevelWarn
			}
			log.Log(r.Context(), level, "redirect failed", "error", err)
			target = fallback
		}

		// Ссылка одноразовая по смыслу: браузер не должен подменять
		// переход закэшированным ответом, иначе клик не досчитается.
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	}
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
