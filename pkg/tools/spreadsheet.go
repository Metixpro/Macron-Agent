package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ToolCreateSpreadsheet = "create_spreadsheet"

	maxXLSXSheets = 50
	maxXLSXCells  = 1_000_000
	maxTableBytes = 25 << 20
)

type xlsxSheet struct {
	Name   string
	Rows   [][]any
	Header bool
	RTL    *bool
}

func (r *Registry) registerSpreadsheetTools() {
	r.add(ToolCreateSpreadsheet,
		"Make a real Excel file: sheets of rows, the first the header (numbers stay numbers, '=SUM(B2:B9)' is a formula; "+
			"RTL for Persian), or convert a .csv/.tsv/.json file.",
		map[string]any{
			"filename": prop("string", "e.g. 'prices.xlsx'"),
			"sheets": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"name": prop("string", ""),
				"rows": map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{}}},
			}, "required": []string{"rows"}}},
			"from_file": prop("string", ""),
		}, []string{"filename"}, r.createSpreadsheet)
}

func (r *Registry) createSpreadsheet(_ context.Context, sessionID string, args map[string]any) (string, error) {
	name := strings.TrimPrefix(strings.TrimSpace(strArg(args, "filename")), "/workspace/")
	switch ext := strings.ToLower(path.Ext(name)); {
	case name == "":
		return "error: filename is required", nil
	case ext == "":
		name += ".xlsx"
	case ext != ".xlsx":
		return "error: filename must end in .xlsx", nil
	}
	header := true
	if v, ok := args["header"].(bool); ok {
		header = v
	}
	var sheets []xlsxSheet
	if from := strings.TrimPrefix(strings.TrimSpace(strArg(args, "from_file")), "/workspace/"); from != "" {
		rows, err := r.tableFile(sessionID, from)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		sheets = append(sheets, xlsxSheet{Name: strings.TrimSuffix(path.Base(from), path.Ext(from)), Rows: rows, Header: header})
	}
	list, _ := args["sheets"].([]any)
	for i, s := range list {
		m, ok := s.(map[string]any)
		if !ok {
			return fmt.Sprintf("error: sheet %d must be an object {name, rows}", i+1), nil
		}
		var rows [][]any
		raw, _ := m["rows"].([]any)
		for _, row := range raw {
			if cells, ok := row.([]any); ok {
				rows = append(rows, cells)
			} else {
				rows = append(rows, []any{row})
			}
		}
		sheets = append(sheets, xlsxSheet{Name: strArg(m, "name"), Rows: rows, Header: header})
	}
	if len(sheets) == 0 {
		return "error: give sheets [{name, rows}] or from_file", nil
	}
	var buf bytes.Buffer
	if err := writeXLSX(&buf, sheets); err != nil {
		return "error: " + err.Error(), nil
	}
	if err := r.Machine.WriteFile(sessionID, name, buf.Bytes()); err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	var parts []string
	for _, s := range sheets {
		cols := 0
		for _, row := range s.Rows {
			cols = max(cols, len(row))
		}
		parts = append(parts, fmt.Sprintf("%s: %d rows × %d columns", sheetName(s.Name, nil), len(s.Rows), cols))
	}
	return fmt.Sprintf("Created %s (%s): %s.", name, humanSize(int64(buf.Len())), strings.Join(parts, "; ")), nil
}

func (r *Registry) tableFile(sessionID, name string) ([][]any, error) {
	data, truncated, err := r.Machine.ReadFile(sessionID, name, maxTableBytes)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s: %s", name, friendlyFSError(err))
	case truncated:
		return nil, fmt.Errorf("%s is larger than %s", name, humanSize(maxTableBytes))
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return jsonTable(data)
	case ".csv", ".tsv", ".txt":
		cr := csv.NewReader(bytes.NewReader(data))
		cr.FieldsPerRecord, cr.LazyQuotes = -1, true
		if strings.EqualFold(path.Ext(name), ".tsv") || (!bytes.Contains(firstLineBytes(data), []byte(",")) && bytes.Contains(firstLineBytes(data), []byte("\t"))) {
			cr.Comma = '\t'
		}
		var rows [][]any
		cells := 0
		for {
			rec, err := cr.Read()
			if err == io.EOF {
				return rows, nil
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %v", name, err)
			}
			if cells += len(rec); cells > maxXLSXCells {
				return nil, fmt.Errorf("%s has more than %d cells, too many for one Excel file; keep it as CSV", name, maxXLSXCells)
			}
			row := make([]any, len(rec))
			for i, c := range rec {
				row[i] = numberOrText(c)
			}
			rows = append(rows, row)
		}
	}
	return nil, fmt.Errorf("%s: use a .csv, .tsv or .json file", name)
}

func firstLineBytes(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}

func jsonTable(data []byte) ([][]any, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	if m, ok := v.(map[string]any); ok {
		for _, k := range jsonRecordKeys(data, 1) {
			if list, ok := m[k].([]any); ok {
				v = list
				break
			}
		}
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errors.New("the JSON must be a list of objects or of lists")
	}
	depth := 2
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '{' {
		depth = 3
	}
	keys := jsonRecordKeys(data, depth)
	var rows [][]any
	if len(keys) > 0 {
		head := make([]any, len(keys))
		for i, k := range keys {
			head[i] = k
		}
		rows = append(rows, head)
	}
	for _, item := range list {
		switch x := item.(type) {
		case map[string]any:
			row := make([]any, len(keys))
			for i, k := range keys {
				row[i] = x[k]
			}
			rows = append(rows, row)
		case []any:
			rows = append(rows, x)
		default:
			rows = append(rows, []any{x})
		}
	}
	return rows, nil
}

func jsonRecordKeys(data []byte, depth int) []string {
	type frame struct{ obj, wantKey bool }
	var (
		stack []frame
		keys  []string
		seen  = map[string]bool{}
	)
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				stack = append(stack, frame{obj: d == '{', wantKey: d == '{'})
			default:
				stack = stack[:len(stack)-1]
				if n := len(stack); n > 0 && stack[n-1].obj {
					stack[n-1].wantKey = true
				}
			}
			continue
		}
		n := len(stack)
		if n == 0 || !stack[n-1].obj {
			continue
		}
		if !stack[n-1].wantKey {
			stack[n-1].wantKey = true
			continue
		}
		stack[n-1].wantKey = false
		if k, ok := tok.(string); ok && n == depth && !seen[k] && len(keys) < 1000 {
			seen[k] = true
			keys = append(keys, k)
		}
	}
}

var plainNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]+)?$`)

func numberOrText(s string) any {
	t := strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		case r == '٫':
			return '.'
		}
		return r
	}, s))
	if plainNumber.MatchString(t) {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			return f
		}
	}
	return s
}

func writeXLSX(w io.Writer, sheets []xlsxSheet) error {
	if len(sheets) > maxXLSXSheets {
		return fmt.Errorf("at most %d sheets", maxXLSXSheets)
	}
	cells := 0
	for _, s := range sheets {
		if len(s.Rows) > 1<<20 {
			return errors.New("a sheet holds at most 1,048,576 rows")
		}
		for _, row := range s.Rows {
			if len(row) > 16384 {
				return errors.New("a sheet holds at most 16,384 columns")
			}
			cells += len(row)
		}
	}
	if cells > maxXLSXCells {
		return fmt.Errorf("at most %d cells", maxXLSXCells)
	}
	zw := zip.NewWriter(w)
	add := func(name, body string) error {
		f, err := zw.Create(name)
		if err == nil {
			_, err = io.WriteString(f, body)
		}
		return err
	}
	const head = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
	var types, wb, rels strings.Builder
	types.WriteString(head + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	wb.WriteString(head + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView/></bookViews><sheets>`)
	rels.WriteString(head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	used := map[string]bool{}
	var defined strings.Builder
	for i, s := range sheets {
		n := i + 1
		name := sheetName(s.Name, used)
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscape(name), n, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		body, filter := sheetXML(s)
		if filter != "" {
			fmt.Fprintf(&defined, `<definedName name="_xlnm._FilterDatabase" localSheetId="%d" hidden="1">'%s'!%s</definedName>`,
				i, xmlEscape(strings.ReplaceAll(name, "'", "''")), absRef(filter))
		}
		if err := add(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), head+body); err != nil {
			return err
		}
	}
	wb.WriteString(`</sheets>`)
	if defined.Len() > 0 {
		wb.WriteString(`<definedNames>` + defined.String() + `</definedNames>`)
	}
	wb.WriteString(`<calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`)
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, len(sheets)+1)
	types.WriteString(`</Types>`)
	for _, part := range []struct{ name, body string }{
		{"[Content_Types].xml", types.String()},
		{"_rels/.rels", head + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", wb.String()},
		{"xl/_rels/workbook.xml.rels", rels.String()},
		{"xl/styles.xml", head + xlsxStyles},
	} {
		if err := add(part.name, part.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

const xlsxStyles = `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2"><font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font></fonts>` +
	`<fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill>` +
	`<fill><patternFill patternType="solid"><fgColor rgb="FFE8EEF7"/><bgColor indexed="64"/></patternFill></fill></fills>` +
	`<borders count="2"><border><left/><right/><top/><bottom/><diagonal/></border>` +
	`<border><left/><right/><top/><bottom style="thin"><color rgb="FF8EA9DB"/></bottom><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1"/></cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`

func sheetXML(s xlsxSheet) (string, string) {
	cols := 0
	for _, row := range s.Rows {
		cols = max(cols, len(row))
	}
	widths := make([]int, cols)
	arabic, latin := 0, 0
	calc := newSheetCalc(s.Rows)
	var data strings.Builder
	for i, row := range s.Rows {
		fmt.Fprintf(&data, `<row r="%d">`, i+1)
		for j, v := range row {
			ref := colName(j) + strconv.Itoa(i+1)
			style := ""
			if s.Header && i == 0 {
				style = ` s="1"`
			}
			text := ""
			switch x := v.(type) {
			case nil:
				if style == "" {
					continue
				}
				fmt.Fprintf(&data, `<c r="%s"%s/>`, ref, style)
			case bool:
				b := 0
				if x {
					b = 1
				}
				fmt.Fprintf(&data, `<c r="%s"%s t="b"><v>%d</v></c>`, ref, style, b)
				text = strconv.FormatBool(x)
			case float64, int, int64:
				f := toFloat(x)
				if math.IsNaN(f) || math.IsInf(f, 0) {
					text = fmt.Sprint(f)
					fmt.Fprintf(&data, `<c r="%s"%s t="inlineStr"><is><t>%s</t></is></c>`, ref, style, text)
					break
				}
				text = excelNumber(f)
				fmt.Fprintf(&data, `<c r="%s"%s><v>%s</v></c>`, ref, style, strconv.FormatFloat(f, 'g', -1, 64))
			default:
				str, ok := v.(string)
				if !ok {
					b, _ := json.Marshal(v)
					str = string(b)
				}
				if len(str) > 1 && str[0] == '=' && !(s.Header && i == 0) {
					if v, ok := calc.formulaValue(i, j); ok {
						fmt.Fprintf(&data, `<c r="%s"%s><f>%s</f><v>%s</v></c>`, ref, style, xmlEscape(str[1:]), strconv.FormatFloat(v, 'g', -1, 64))
						text = excelNumber(v)
						break
					}
					fmt.Fprintf(&data, `<c r="%s"%s><f>%s</f></c>`, ref, style, xmlEscape(str[1:]))
					text = "0000000000"
					break
				}
				if len(str) > 32767 {
					str = str[:32767]
				}
				fmt.Fprintf(&data, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlEscape(str))
				text = str
				for _, r := range str {
					switch {
					case unicode.Is(unicode.Arabic, r) && unicode.IsLetter(r):
						arabic++
					case unicode.IsLetter(r) && r < 0x250:
						latin++
					}
				}
			}
			widths[j] = max(widths[j], displayWidth(text))
		}
		data.WriteString(`</row>`)
	}
	rtl := arabic > latin
	if s.RTL != nil {
		rtl = *s.RTL
	}
	last := colName(max(cols, 1)-1) + strconv.Itoa(max(len(s.Rows), 1))
	var b strings.Builder
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	fmt.Fprintf(&b, `<dimension ref="A1:%s"/>`, last)
	b.WriteString(`<sheetViews><sheetView workbookViewId="0"`)
	if rtl {
		b.WriteString(` rightToLeft="1"`)
	}
	frozen := s.Header && len(s.Rows) > 1
	if frozen {
		b.WriteString(`><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
			`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/></sheetView></sheetViews>`)
	} else {
		b.WriteString(`/></sheetViews>`)
	}
	b.WriteString(`<sheetFormatPr defaultRowHeight="15"/>`)
	if cols > 0 {
		b.WriteString(`<cols>`)
		for j, w := range widths {
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%.1f" customWidth="1"/>`, j+1, j+1, math.Min(60, math.Max(8, float64(w)*1.15+2)))
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>` + data.String() + `</sheetData>`)
	filter := ""
	if frozen && cols > 0 {
		filter = "A1:" + last
		fmt.Fprintf(&b, `<autoFilter ref="%s"/>`, filter)
	}
	b.WriteString(`<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/></worksheet>`)
	return b.String(), filter
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func colName(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

func absRef(ref string) string {
	parts := strings.Split(ref, ":")
	for i, p := range parts {
		j := strings.IndexAny(p, "0123456789")
		if j > 0 {
			parts[i] = "$" + p[:j] + "$" + p[j:]
		}
	}
	return strings.Join(parts, ":")
}

func sheetName(name string, used map[string]bool) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.Trim(name, "'")
	if utf8.RuneCountInString(name) > 31 {
		name = string([]rune(name)[:31])
	}
	if name == "" || strings.EqualFold(name, "history") {
		name = "Sheet"
	}
	if used == nil {
		return name
	}
	base := name
	for n := 2; used[strings.ToLower(name)]; n++ {
		suffix := fmt.Sprintf(" (%d)", n)
		name = string([]rune(base)[:min(utf8.RuneCountInString(base), 31-len(suffix))]) + suffix
	}
	used[strings.ToLower(name)] = true
	return name
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r == '\n' {
			break
		}
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func xmlEscape(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000 {
			return r
		}
		return -1
	}, s)
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
