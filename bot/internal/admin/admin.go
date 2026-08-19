// Package admin — одна страница на чтение: кто пришёл, откуда и куда
// дошёл.
//
// Ничего не редактирует. Пока людей единицы, полезно видеть цифры, а не
// править маршруты: маршруты меняются раз в месяц, а смотреть на воронку
// хочется каждый день.
//
// Пароль спрашивает Caddy, не мы: складывать в бота свою авторизацию
// ради одной страницы незачем.
package admin

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

//go:embed page.html
var pageFS embed.FS

// Stage — шаг воронки: сколько было событий и сколько разных людей.
type Stage struct {
	Name   string
	Label  string
	Events int
	People int
}

// Source — метка источника и её результат.
type Source struct {
	ID       string
	Started  int
	Selected int
	Opened   int
}

// Lead — человек и что с ним произошло.
type Lead struct {
	TelegramID int64
	Username   string
	FirstName  string
	FirstSeen  time.Time
	Source     string
	Role       string
	Materials  string
	Opened     bool
}

// Reader — то, что странице нужно от базы. Только чтение.
type Reader interface {
	Stages(ctx context.Context) (map[string]Stage, error)
	Sources(ctx context.Context) ([]Source, error)
	Leads(ctx context.Context, limit int) ([]Lead, error)
}

// Порядок и человеческие названия шагов. Порядок задаётся здесь, а не
// сортировкой по количеству: воронка должна читаться сверху вниз даже
// когда на нижних шагах ноль.
var stageOrder = []struct{ name, label string }{
	{"bot_started", "Запустили бота"},
	{"role_answered", "Ответили на вопрос"},
	{"alternative_asked", "Попросили другое"},
	{"material_selected", "Выбрали материал"},
	{"material_opened", "Открыли статью"},
}

const recentLeads = 50

type Handler struct {
	reader Reader
	tmpl   *template.Template
	log    *slog.Logger
}

func NewHandler(reader Reader, log *slog.Logger) (*Handler, error) {
	if reader == nil {
		return nil, fmt.Errorf("reader is required")
	}
	if log == nil {
		log = slog.Default()
	}

	tmpl, err := template.New("page.html").Funcs(template.FuncMap{
		"moscow": func(t time.Time) string { return t.Format("02.01 15:04") },
		"role": func(r string) string {
			switch r {
			case "solo":
				return "сам"
			case "team":
				return "с командой"
			default:
				return ""
			}
		},
		"share": func(part, whole int) string {
			if whole == 0 {
				return ""
			}
			return fmt.Sprintf("%d%%", part*100/whole)
		},
	}).ParseFS(pageFS, "page.html")
	if err != nil {
		return nil, fmt.Errorf("parsing page template: %w", err)
	}

	return &Handler{reader: reader, tmpl: tmpl, log: log}, nil
}

type pageData struct {
	Stages  []Stage
	Sources []Source
	Leads   []Lead
	Now     time.Time
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stages, err := h.reader.Stages(ctx)
	if err != nil {
		h.fail(w, "stages", err)
		return
	}
	sources, err := h.reader.Sources(ctx)
	if err != nil {
		h.fail(w, "sources", err)
		return
	}
	leads, err := h.reader.Leads(ctx, recentLeads)
	if err != nil {
		h.fail(w, "leads", err)
		return
	}

	ordered := make([]Stage, 0, len(stageOrder))
	for _, s := range stageOrder {
		stage := stages[s.name]
		stage.Name, stage.Label = s.name, s.label
		ordered = append(ordered, stage)
	}

	data := pageData{Stages: ordered, Sources: sources, Leads: leads, Now: time.Now()}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Страница за паролем и с личными данными: её не должен закэшировать
	// ни браузер, ни что-либо по дороге.
	w.Header().Set("Cache-Control", "no-store")
	if err := h.tmpl.Execute(w, data); err != nil {
		h.log.Error("admin page render failed", "error", err)
	}
}

func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("admin query failed", "query", what, "error", err)
	http.Error(w, "не удалось прочитать базу", http.StatusInternalServerError)
}
