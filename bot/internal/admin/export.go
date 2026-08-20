package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/xlsx"
)

// Выгрузка того же среза, что на экране. Экспорт, который всегда отдаёт
// всю базу, заставляет фильтровать второй раз уже в Excel — тогда
// фильтры на странице не значат ничего.
//
// Листа два. «Люди» отвечает на вопрос «кому написать»: строка на
// человека, с меткой, состоянием и тем, дошёл ли он до записи. «Шаги»
// отвечает на вопрос «что он делал»: строка на событие. Складывать это
// в один лист нельзя — человек размножится по числу своих событий, и
// любой подсчёт по колонке начнёт врать.

// exportLimit — потолок выгрузки. Не «сколько влезет»: молча обрезанная
// выгрузка выглядит точно так же, как полная, поэтому обрезание видно в
// отдельной строке отчёта.
const exportLimit = 100000

func (h *Handler) exportName(f Filter, days int) string {
	parts := []string{"воронка"}
	if f.Source != "" {
		parts = append(parts, "источник-"+filterFileLabel(f.Source))
	}
	if f.Stage != "" {
		parts = append(parts, "состояние-"+filterFileLabel(f.Stage))
	}
	if days != 0 {
		parts = append(parts, strconv.Itoa(days)+"дней")
	}
	parts = append(parts, h.now().In(moscow).Format("2006-01-02"))
	return strings.Join(parts, "_") + ".xlsx"
}

func filterFileLabel(v string) string {
	if v == NoValue {
		return "без-метки"
	}
	return v
}

// ServeExport отдаёт срез книгой Excel.
func (h *Handler) ServeExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter, days := h.parseFilter(r, h.now())

	leads, err := h.reader.Leads(ctx, filter, exportLimit)
	if err != nil {
		h.fail(w, "export leads", err)
		return
	}
	timeline, err := h.reader.Timeline(ctx, filter, exportLimit)
	if err != nil {
		h.fail(w, "export timeline", err)
		return
	}

	// Имя из Telegram у половины людей пустое, а в выгрузке колонка
	// «кто» не должна быть пустой ни у кого: иначе строку не с чем
	// сопоставить глазами.
	people := xlsx.Sheet{
		Name: "Люди",
		Head: []string{
			"Telegram ID", "Кто", "Имя", "Ссылка",
			"Пришёл", "Откуда", "Состояние", "Забрал", "Открыл", "На эфире",
		},
	}
	for _, l := range leads {
		people.Rows = append(people.Rows, []string{
			strconv.FormatInt(l.TelegramID, 10),
			handle(l),
			l.FirstName,
			ChatLink(l.TelegramID, l.Username),
			stamp(l.FirstSeen),
			sourceLabel(l.Source),
			stageLabel(l.Stage),
			l.Materials,
			yesNo(l.Opened),
			yesNo(l.Waitlist),
		})
	}

	steps := xlsx.Sheet{
		Name: "Шаги",
		Head: []string{"Telegram ID", "Кто", "Когда", "Шаг", "Метка", "Материал", "Подробности"},
	}
	for _, m := range timeline {
		steps.Rows = append(steps.Rows, []string{
			strconv.FormatInt(m.TelegramID, 10),
			m.Username,
			stamp(m.OccurredAt),
			momentLabel(m.Name),
			sourceLabel(m.SourceID),
			m.MaterialID,
			meta(m.Meta),
		})
	}

	if len(leads) == exportLimit || len(timeline) == exportLimit {
		h.log.Warn("admin export truncated", "leads", len(leads), "timeline", len(timeline))
		people.Rows = append(people.Rows, []string{"", "ВЫГРУЗКА ОБРЕЗАНА: строк больше, чем " + strconv.Itoa(exportLimit)})
	}

	// Заголовки до первого байта тела: после начала записи статус уже не
	// поменять, а книга собирается потоком в ответ.
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", contentDisposition(h.exportName(filter, days)))

	if err := xlsx.Write(w, people, steps); err != nil {
		// Тело уже частично ушло, менять статус поздно: единственное
		// честное действие — записать в лог, а не дорисовывать книгу.
		h.log.Error("admin export failed", "error", err)
	}
}

// ChatLink — как открыть переписку с человеком. По @имени, если оно есть,
// иначе по числовому id: без username ссылка t.me не существует, а
// tg://user открывает чат в приложении.
func ChatLink(telegramID int64, username string) string {
	if username != "" {
		return "https://t.me/" + username
	}
	return "tg://user?id=" + strconv.FormatInt(telegramID, 10)
}

func handle(l Lead) string {
	if l.Username != "" {
		return "@" + l.Username
	}
	if l.FirstName != "" {
		return l.FirstName
	}
	return strconv.FormatInt(l.TelegramID, 10)
}

func sourceLabel(v string) string {
	if v == "" {
		return "без метки"
	}
	return v
}

func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}

// stamp — время в московской зоне и в порядке, который правильно
// сортируется как текст. Формат «20.08 10:39» в таблице бесполезен:
// он путает дни разных месяцев и не сортируется вовсе.
func stamp(t time.Time) string {
	return t.In(moscow).Format("2006-01-02 15:04:05")
}

// meta схлопывает метаданные события в одну ячейку с устойчивым
// порядком: в Go обход карты случайный, и без сортировки одна и та же
// выгрузка дважды подряд давала бы разные строки.
func meta(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

// contentDisposition отдаёт имя файла дважды: ASCII-запасное для старых
// клиентов и UTF-8 по RFC 5987 для настоящих. Кириллица в обычном
// filename приезжает мусором.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '-'
		}
		return r
	}, name)

	var encoded strings.Builder
	for _, b := range []byte(name) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.IndexByte("-._~", b) >= 0 {
			encoded.WriteByte(b)
			continue
		}
		fmt.Fprintf(&encoded, "%%%02X", b)
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, encoded.String())
}
