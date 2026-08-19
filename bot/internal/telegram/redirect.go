package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// Opener — переход по tracked-ссылке.
// followUpTimeout — сообщение вслед за переходом не должно висеть
// дольше, чем человек читает первый абзац.
const followUpTimeout = 10 * time.Second

type Opener interface {
	Open(ctx context.Context, token string) (funnel.Opened, error)
}

// Redirect — единственное место, где считается факт клика: Telegram о
// нажатии URL-кнопки не сообщает.
//
// Здесь же бот догоняет человека вопросом о состоянии. Момент выбран не
// случайно: ценность уже получена, значит спрашивать есть о чём.
type Redirect struct {
	opener   Opener
	sender   Sender
	fallback string
	log      *slog.Logger
}

func NewRedirect(opener Opener, sender Sender, fallbackURL string, log *slog.Logger) (*Redirect, error) {
	if opener == nil || sender == nil {
		return nil, errors.New("opener and sender are required")
	}
	if fallbackURL == "" {
		return nil, errors.New("fallback url is required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Redirect{opener: opener, sender: sender, fallback: fallbackURL, log: log}, nil
}

func (h *Redirect) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	opened, err := h.opener.Open(r.Context(), token)
	if err != nil {
		// Человек не должен упереться в ошибку из-за нашей проблемы или
		// чужой ссылки: ведём его в список статей.
		level := slog.LevelError
		if errors.Is(err, funnel.ErrUnknownToken) {
			level = slog.LevelWarn
		}
		h.log.Log(r.Context(), level, "redirect failed", "error", err)
		h.redirect(w, r, h.fallback)
		return
	}

	// Сначала уводим браузер, потом пишем в чат: человек ждёт статью, а
	// не наш вызов к Telegram.
	h.redirect(w, r, opened.Target)

	if opened.FollowUp == nil {
		return
	}
	// Контекст запроса уже закрыт вместе с ответом, поэтому берём свой.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), followUpTimeout)
	defer cancel()

	if err := h.sender.SendMessage(ctx, opened.TelegramID, *opened.FollowUp); err != nil {
		h.log.Error("follow-up failed", "telegram_id", opened.TelegramID, "error", err)
	}
}

func (h *Redirect) redirect(w http.ResponseWriter, r *http.Request, target string) {
	// Ссылка одноразовая по смыслу: браузер не должен подменять переход
	// закэшированным ответом, иначе клик не досчитается.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}
