// Package xlsx пишет книгу Excel из готовых таблиц.
//
// Почему не CSV. CSV в русском Excel открывается через мастер импорта:
// кириллица без BOM превращается в кракозябры, а запятая как разделитель
// в русской локали складывает всю строку в одну ячейку. Файл, который
// нужно чинить руками перед тем, как посмотреть, — это не экспорт.
//
// Почему без библиотеки. xlsx это zip с несколькими файлами XML, и того,
// что нужно отчёту — листы, заголовок жирным, ширина колонок, — набирается
// на полторы сотни строк на archive/zip и encoding/xml. Зависимость ради
// этого пришлось бы обновлять и проверять, а формат не меняется.
//
// Числа пишутся числами, остальное — строками, поэтому в Excel сразу
// работают сортировка и сумма по колонке.
package xlsx

import (
	"archive/zip"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Sheet — один лист книги.
type Sheet struct {
	Name string
	Head []string
	Rows [][]string
}

// Write собирает книгу. Порядок листов — порядок аргументов.
func Write(w io.Writer, sheets ...Sheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("нужен хотя бы один лист")
	}

	zw := zip.NewWriter(w)
	files := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypes(len(sheets))},
		{"_rels/.rels", rootRels},
		{"xl/workbook.xml", workbook(sheets)},
		{"xl/_rels/workbook.xml.rels", workbookRels(len(sheets))},
		{"xl/styles.xml", styles},
	}
	for i, s := range sheets {
		files = append(files, struct {
			name string
			body string
		}{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), worksheet(s)})
	}

	for _, f := range files {
		part, err := zw.Create(f.name)
		if err != nil {
			return fmt.Errorf("создание %s: %w", f.name, err)
		}
		if _, err := io.WriteString(part, f.body); err != nil {
			return fmt.Errorf("запись %s: %w", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("закрытие книги: %w", err)
	}
	return nil
}

func worksheet(s Sheet) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	// Ширина колонки по самому длинному значению: иначе всё приезжает
	// восемью символами и первое, что делает человек, — тянет границы.
	widths := make([]int, len(s.Head))
	for i, h := range s.Head {
		widths[i] = len([]rune(h))
	}
	for _, row := range s.Rows {
		for i, cell := range row {
			if i < len(widths) && len([]rune(cell)) > widths[i] {
				widths[i] = len([]rune(cell))
			}
		}
	}
	b.WriteString(`<cols>`)
	for i, wdt := range widths {
		width := min(max(wdt+3, 9), 48)
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, width)
	}
	b.WriteString(`</cols>`)

	// Шапка закреплена: без этого таблица на пару сотен строк листается
	// вслепую.
	b.WriteString(`<sheetViews><sheetView workbookViewId="0">`)
	b.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	b.WriteString(`</sheetView></sheetViews>`)

	b.WriteString(`<sheetData>`)
	writeRow(&b, 1, s.Head, true)
	for i, row := range s.Rows {
		writeRow(&b, i+2, row, false)
	}
	b.WriteString(`</sheetData>`)

	if len(s.Rows) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, columnName(len(s.Head)), len(s.Rows)+1)
	}
	b.WriteString(`</worksheet>`)
	return b.String()
}

func writeRow(b *strings.Builder, n int, cells []string, head bool) {
	fmt.Fprintf(b, `<row r="%d">`, n)
	for i, value := range cells {
		ref := fmt.Sprintf("%s%d", columnName(i+1), n)
		style := ""
		if head {
			style = ` s="1"`
		}
		switch {
		case value == "":
			fmt.Fprintf(b, `<c r="%s"%s/>`, ref, style)
		case !head && isNumber(value):
			fmt.Fprintf(b, `<c r="%s"%s><v>%s</v></c>`, ref, style, value)
		default:
			fmt.Fprintf(b, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				ref, style, escape(value))
		}
	}
	b.WriteString(`</row>`)
}

// isNumber — то, что Excel должен считать числом. Пустая строка, ведущий
// ноль и знак процента остаются текстом: «007» не должно превратиться в 7.
func isNumber(v string) bool {
	if v == "" || (len(v) > 1 && v[0] == '0' && v[1] != '.') {
		return false
	}
	_, err := strconv.ParseFloat(v, 64)
	return err == nil
}

func columnName(n int) string {
	name := ""
	for n > 0 {
		n--
		name = string(rune('A'+n%26)) + name
		n /= 26
	}
	return name
}

// escape готовит значение для XML. Кроме обычной разметки убираются
// управляющие символы: XML 1.0 их не допускает, и Excel на таком файле
// молча показывает «повреждён».
func escape(v string) string {
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case r < 0x20:
			// выбрасываем
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

const rootRels = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

// Один стиль: жирная шапка. Больше отчёту не нужно, а лишние стили это
// лишний способ сделать файл невалидным.
const styles = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
	`<fills count="1"><fill><patternFill patternType="none"/></fill></fills>` +
	`<borders count="1"><border/></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
	// Стиль по умолчанию обязателен: без него строгие читалки ругаются
	// на книгу, а часть просто отказывается её открывать.
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

func contentTypes(sheets int) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i)
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func workbook(sheets []Sheet) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" `)
	b.WriteString(`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, s := range sheets {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, escape(sheetName(s.Name)), i+1, i+1)
	}
	b.WriteString(`</sheets></workbook>`)
	return b.String()
}

// sheetName приводит имя к тому, что принимает Excel: не длиннее 31
// символа и без служебных знаков. Иначе книга открывается с ошибкой.
func sheetName(name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) {
			return '-'
		}
		return r
	}, name)

	runes := []rune(name)
	if len(runes) > 31 {
		runes = runes[:31]
	}
	if len(runes) == 0 {
		return "Лист"
	}
	return string(runes)
}

func workbookRels(sheets int) string {
	var b strings.Builder
	b.WriteString(xmlHeader)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, sheets+1)
	b.WriteString(`</Relationships>`)
	return b.String()
}
