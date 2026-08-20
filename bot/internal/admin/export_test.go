package admin_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

func export(t *testing.T, r *stub, target string, opts ...admin.Option) *httptest.ResponseRecorder {
	t.Helper()

	opts = append(opts, admin.WithClock(func() time.Time { return testNow }))
	h, err := admin.NewHandler(r, nil, opts...)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeExport(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// Книга должна открываться. Excel на битом zip или невалидном XML
// показывает «файл повреждён» и не говорит, где именно, поэтому
// проверяем структуру сами.
func TestExportIsAValidWorkbook(t *testing.T) {
	r := fullFunnel()
	r.timeline = []admin.TimelineRow{
		{TelegramID: 763464443, Username: "akhmadullintf", Name: "bot_started",
			SourceID: "site_metod6x5", OccurredAt: testNow},
	}

	rec := export(t, r, "/admin/export.xlsx")
	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d", rec.Code)
	}

	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("это не zip: %v", err)
	}

	required := []string{
		"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml",
		"xl/_rels/workbook.xml.rels", "xl/styles.xml",
		"xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml",
	}
	have := map[string]bool{}
	for _, f := range zr.File {
		have[f.Name] = true

		// Каждая часть должна быть разбираемым XML: Excel не прощает.
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("открытие %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("чтение %s: %v", f.Name, err)
		}
		dec := xml.NewDecoder(bytes.NewReader(data))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s не разбирается как XML: %v", f.Name, err)
			}
		}
	}
	for _, name := range required {
		if !have[name] {
			t.Errorf("в книге нет %s", name)
		}
	}
}

func sheet(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()

	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("это не zip: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("открытие %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("чтение %s: %v", name, err)
		}
		return string(data)
	}
	t.Fatalf("в книге нет %s", name)
	return ""
}

func TestExportCarriesPeopleAndSteps(t *testing.T) {
	r := fullFunnel()
	r.timeline = []admin.TimelineRow{
		{TelegramID: 763464443, Username: "akhmadullintf", Name: "stage_answered",
			SourceID: "site_metod6x5", Meta: map[string]string{"stage": "not_shipping"},
			OccurredAt: testNow},
	}
	rec := export(t, r, "/admin/export.xlsx")

	people := sheet(t, rec, "xl/worksheets/sheet1.xml")
	for _, want := range []string{"@akhmadullintf", "site_metod6x5", "не выпускает стабильно", "blueprint-50m"} {
		if !strings.Contains(people, want) {
			t.Errorf("на листе людей нет %q", want)
		}
	}
	// Ссылка на переписку — то, ради чего выгрузку открывают.
	if !strings.Contains(people, "https://t.me/akhmadullintf") {
		t.Error("на листе людей нет ссылки на переписку")
	}

	steps := sheet(t, rec, "xl/worksheets/sheet2.xml")
	if !strings.Contains(steps, "Ответили про состояние") {
		t.Error("шаг не подписан по-человечески")
	}
	if !strings.Contains(steps, "stage=not_shipping") {
		t.Error("подробности события не доехали")
	}
}

// Без @имени ссылки t.me не существует. Строка без способа связаться
// бесполезна ровно тем, ради чего выгрузку и делают.
func TestExportLinksPeopleWithoutUsername(t *testing.T) {
	r := fullFunnel()
	r.leads = []admin.Lead{{TelegramID: 811200011, FirstName: "Марина", FirstSeen: testNow}}

	rec := export(t, r, "/admin/export.xlsx")
	people := sheet(t, rec, "xl/worksheets/sheet1.xml")

	if !strings.Contains(people, "tg://user?id=811200011") {
		t.Error("нет ссылки на переписку для человека без @имени")
	}
	if !strings.Contains(people, "Марина") {
		t.Error("человек без @имени остался без подписи")
	}
}

// Выгружается тот же срез, что на экране, включая список скрытых.
func TestExportRespectsTheFilter(t *testing.T) {
	r := fullFunnel()
	export(t, r, "/admin/export.xlsx?source=site_home&stage=no_signal&days=7",
		admin.WithHidden([]int64{577134700}))

	if r.got.Source != "site_home" || r.got.Stage != "no_signal" {
		t.Errorf("фильтр не доехал до выгрузки: %+v", r.got)
	}
	if r.got.Since.IsZero() {
		t.Error("период не доехал до выгрузки")
	}
	if len(r.got.Hidden) != 1 {
		t.Errorf("скрытые аккаунты попали в выгрузку: %v", r.got.Hidden)
	}
}

// Имя файла с кириллицей ломается в заголовке, если отдать его одной
// строкой: нужен и ASCII-запасной вариант, и UTF-8 по RFC 5987.
func TestExportFileNameSurvivesCyrillic(t *testing.T) {
	rec := export(t, fullFunnel(), "/admin/export.xlsx?source=site_home")

	got := rec.Header().Get("Content-Disposition")
	if !strings.Contains(got, "filename*=UTF-8''") {
		t.Errorf("нет UTF-8 имени файла: %s", got)
	}
	if !strings.Contains(got, "source") && !strings.Contains(got, "site_home") {
		t.Errorf("срез не назван в имени файла: %s", got)
	}
	for _, r := range got {
		if r > 127 {
			t.Errorf("в заголовке остались не-ASCII байты: %s", got)
			break
		}
	}
}

func TestExportSetsSpreadsheetType(t *testing.T) {
	rec := export(t, fullFunnel(), "/admin/export.xlsx")

	want := "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	if got := rec.Header().Get("Content-Type"); got != want {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("выгрузка с личными данными кэшируется: %q", got)
	}
}

// Пустой срез — это книга с одними заголовками, а не ошибка и не пустой
// файл: иначе непонятно, сломалось что-то или выгружать нечего.
func TestExportOfNothingIsStillAWorkbook(t *testing.T) {
	rec := export(t, &stub{}, "/admin/export.xlsx")

	if rec.Code != http.StatusOK {
		t.Fatalf("код = %d", rec.Code)
	}
	people := sheet(t, rec, "xl/worksheets/sheet1.xml")
	if !strings.Contains(people, "Telegram ID") {
		t.Error("в пустой книге нет даже заголовков")
	}
}

// Кнопка выгрузки на странице ведёт в тот же срез, что открыт.
func TestExportButtonKeepsTheSlice(t *testing.T) {
	_, body := get(t, fullFunnel(), "/admin?days=7&source=site_metod6x5")

	want := "/admin/export.xlsx?days=7&amp;source=site_metod6x5"
	if !strings.Contains(body, want) {
		t.Errorf("кнопка выгрузки теряет срез, ждали %s", want)
	}
}
