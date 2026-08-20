package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// Интерфейс админки собран Astro и лежит здесь статикой. Вшит в бинарник,
// поэтому деплой остаётся одним артефактом: нет второго контейнера, нет
// Node в проде и нет способа выкатить страницы и данные по отдельности.
//
// Вёрстка правится в bot/admin-ui и пересобирается там же — Go про неё
// ничего не знает, кроме того, что это файлы.

//go:embed all:ui
var uiFS embed.FS

// NewUI отдаёт собранные страницы.
//
// Несобранный интерфейс не роняет процесс: бот — это в первую очередь
// webhook, и падать из-за отсутствующей админки он не должен. Вместо
// белого экрана показывается страница с командой сборки, а Built говорит
// вызывающему, о чём написать в лог на старте.
func NewUI() (h http.Handler, built bool) {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return notBuilt{}, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return notBuilt{}, false
	}
	return &uiHandler{files: http.FS(sub), fsys: sub}, true
}

type uiHandler struct {
	files http.FileSystem
	fsys  fs.FS
}

// ServeHTTP раздаёт статику Astro с учётом того, что страницы лежат
// каталогами: /admin/people/ это people/index.html.
func (u *uiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
	if name == "" {
		name = "index.html"
	}

	status := http.StatusOK
	if !exists(u.fsys, name) {
		switch {
		case exists(u.fsys, name+"/index.html"):
			name += "/index.html"
		case exists(u.fsys, name+".html"):
			name += ".html"
		default:
			// Неизвестный адрес внутри админки — опечатка в ссылке, а не
			// отсутствующий файл. Показываем главную, но кодом 404, чтобы
			// это не выглядело как рабочая страница.
			name, status = "index.html", http.StatusNotFound
		}
	}

	f, err := u.files.Open(name)
	if err != nil {
		http.Error(w, "интерфейс админки не собран", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "интерфейс админки не собран", http.StatusInternalServerError)
		return
	}

	// У ассетов Astro хэш в имени, у страниц его нет. Страницы за паролем
	// и с личными данными не кэшируются вовсе; ассеты можно держать долго,
	// их имя меняется вместе с содержимым.
	if strings.HasPrefix(name, "_astro/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}

	http.ServeContent(w, r, name, stat.ModTime(), f)
}

func exists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

// notBuilt — заглушка вместо белого экрана. Говорит ровно то, что
// случилось, и как это чинить.
type notBuilt struct{}

func (notBuilt) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><html lang="ru"><meta charset="utf-8">
<title>Админка не собрана</title>
<body style="font:16px/1.6 -apple-system,sans-serif;max-width:36rem;margin:15vh auto;padding:0 20px">
<h1 style="font-size:1.3rem;font-weight:500">Интерфейс админки не собран</h1>
<p>Бот работает, данные пишутся. Не хватает только статики.</p>
<pre style="background:#f2efe9;padding:12px;border-radius:6px;overflow:auto">cd bot/admin-ui &amp;&amp; npm ci &amp;&amp; npm run build</pre>
<p>В проде это делает Docker: сборка Astro — отдельный слой перед сборкой Go.</p>
</body></html>`))
}
