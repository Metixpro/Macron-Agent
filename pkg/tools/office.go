package tools

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	maxSheetCells = 2_000_000
	maxSheetCols  = 1000
)

type sheet struct {
	Name string
	Rows [][]string
}

type ooxml map[string]*zip.File

func openOOXML(r io.ReaderAt, size int64) (ooxml, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	parts := ooxml{}
	for _, f := range zr.File {
		parts[strings.TrimPrefix(f.Name, "/")] = f
	}
	return parts, nil
}

func (o ooxml) decoder(name string) (*xml.Decoder, func(), error) {
	f, ok := o[name]
	if !ok {
		return nil, func() {}, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, func() {}, err
	}
	return xml.NewDecoder(io.LimitReader(rc, docTextMaxBytes)), func() { rc.Close() }, nil
}

func (o ooxml) rels(part string) map[string]string {
	dir, base := path.Split(part)
	dec, done, err := o.decoder(dir + "_rels/" + base + ".rels")
	defer done()
	out := map[string]string{}
	if dec == nil || err != nil {
		return out
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "Relationship" {
			id, target, external := attr(t, "Id"), attr(t, "Target"), attr(t, "TargetMode") == "External"
			if external || target == "" {
				continue
			}
			if strings.HasPrefix(target, "/") {
				out[id] = strings.TrimPrefix(target, "/")
			} else {
				out[id] = path.Clean(path.Join(dir, target))
			}
		}
	}
}

func relID(t xml.StartElement) string {
	for _, a := range t.Attr {
		if a.Name.Local == "id" && a.Name.Space != "" {
			return a.Value
		}
	}
	return ""
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func readXLSX(r io.ReaderAt, size int64) ([]sheet, error) {
	o, err := openOOXML(r, size)
	if err != nil {
		return nil, err
	}
	if _, ok := o["xl/workbook.xml"]; !ok {
		return nil, errors.New("not an Excel workbook")
	}
	shared, err := o.sharedStrings()
	if err != nil {
		return nil, err
	}
	dates := o.dateStyles()
	type entry struct{ name, part string }
	var list []entry
	date1904 := false
	rels := o.rels("xl/workbook.xml")
	dec, done, err := o.decoder("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	for dec != nil {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if t, ok := tok.(xml.StartElement); ok {
			switch t.Name.Local {
			case "workbookPr":
				date1904 = attr(t, "date1904") == "1" || attr(t, "date1904") == "true"
			case "sheet":
				if part := rels[relID(t)]; part != "" {
					list = append(list, entry{attr(t, "name"), part})
				}
			}
		}
	}
	done()
	cells := 0
	var out []sheet
	for _, e := range list {
		rows, err := o.sheetRows(e.part, shared, dates, date1904, &cells)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", e.name, err)
		}
		out = append(out, sheet{Name: e.name, Rows: rows})
	}
	return out, nil
}

func (o ooxml) sharedStrings() ([]string, error) {
	dec, done, err := o.decoder("xl/sharedStrings.xml")
	defer done()
	if dec == nil || err != nil {
		return nil, err
	}
	var (
		out      []string
		cur      strings.Builder
		inT, inP bool
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = true
			case "rPh":
				inP = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "t":
				inT = false
			case "rPh":
				inP = false
			}
		case xml.CharData:
			if inT && !inP {
				cur.Write(t)
			}
		}
	}
}

func (o ooxml) dateStyles() map[int]bool {
	dec, done, err := o.decoder("xl/styles.xml")
	defer done()
	out := map[int]bool{}
	if dec == nil || err != nil {
		return out
	}
	custom := map[int]bool{}
	inXfs, index := false, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "numFmt":
				id, _ := strconv.Atoi(attr(t, "numFmtId"))
				custom[id] = dateFormat(attr(t, "formatCode"))
			case "cellXfs":
				inXfs = true
			case "xf":
				if inXfs {
					id, _ := strconv.Atoi(attr(t, "numFmtId"))
					if (id >= 14 && id <= 22) || (id >= 45 && id <= 47) || custom[id] {
						out[index] = true
					}
					index++
				}
			}
		case xml.EndElement:
			if t.Name.Local == "cellXfs" {
				inXfs = false
			}
		}
	}
}

func dateFormat(code string) bool {
	var plain strings.Builder
	quoted, bracket := false, false
	for _, r := range code {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '[' && !quoted:
			bracket = true
		case r == ']' && !quoted:
			bracket = false
		case !quoted && !bracket:
			plain.WriteRune(r)
		}
	}
	p := strings.ToLower(plain.String())
	return strings.ContainsAny(p, "ydhs") || (strings.Contains(p, "m") && !strings.ContainsAny(p, "0#?"))
}

func (o ooxml) sheetRows(part string, shared []string, dates map[int]bool, date1904 bool, cells *int) ([][]string, error) {
	dec, done, err := o.decoder(part)
	defer done()
	if dec == nil || err != nil {
		return nil, err
	}
	var (
		rows           [][]string
		row            []string
		col, style     int
		kind, value    string
		inV, inT, inIs bool
		nextRow        int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return trimRows(rows), nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				nextRow++
				if n, err := strconv.Atoi(attr(t, "r")); err == nil && n > 0 {
					nextRow = n
				}
				if gap := nextRow - 1 - len(rows); gap > 0 && gap <= 10000 {
					rows = append(rows, make([][]string, gap)...)
				}
				row = nil
			case "c":
				kind, value = attr(t, "t"), ""
				style, _ = strconv.Atoi(attr(t, "s"))
				if c := cellColumn(attr(t, "r")); c >= 0 {
					col = c
				} else {
					col = len(row)
				}
			case "v":
				inV = true
			case "is":
				inIs = true
			case "t":
				inT = inIs
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "is":
				inIs = false
			case "c":
				if col >= maxSheetCols {
					continue
				}
				text := cellText(kind, value, style, shared, dates, date1904)
				if text == "" {
					continue
				}
				if *cells++; *cells > maxSheetCells {
					return nil, fmt.Errorf("more than %d cells", maxSheetCells)
				}
				for len(row) <= col {
					row = append(row, "")
				}
				row[col] = text
			case "row":
				rows = append(rows, row)
			}
		case xml.CharData:
			if inV || inT {
				value += string(t)
			}
		}
	}
}

func cellColumn(ref string) int {
	n := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A'+1)
	}
	if i == 0 {
		return -1
	}
	return n - 1
}

func cellText(kind, value string, style int, shared []string, dates map[int]bool, date1904 bool) string {
	switch kind {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || i < 0 || i >= len(shared) {
			return ""
		}
		return shared[i]
	case "b":
		if strings.TrimSpace(value) == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "str", "inlineStr", "e":
		return value
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return strings.TrimSpace(value)
	}
	if dates[style] {
		return excelDate(f, date1904)
	}
	return excelNumber(f)
}

func excelNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	g, _ := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 15, 64), 64)
	return strconv.FormatFloat(g, 'f', -1, 64)
}

func excelDate(serial float64, date1904 bool) string {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if serial < 0 || serial > 2958465 {
		return excelNumber(serial)
	}
	t := base.Add(time.Duration(math.Round(serial*86400)) * time.Second)
	switch {
	case serial < 1:
		return t.Format("15:04:05")
	case serial == math.Trunc(serial):
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func trimRows(rows [][]string) [][]string {
	for i, r := range rows {
		for len(r) > 0 && r[len(r)-1] == "" {
			r = r[:len(r)-1]
		}
		rows[i] = r
	}
	for len(rows) > 0 && len(rows[len(rows)-1]) == 0 {
		rows = rows[:len(rows)-1]
	}
	return rows
}

func xlsxPages(sheets []sheet) ([]string, []docOutlineItem) {
	var pages []string
	var outline []docOutlineItem
	for _, s := range sheets {
		cols := 0
		for _, r := range s.Rows {
			cols = max(cols, len(r))
		}
		title := fmt.Sprintf("Sheet %q — %d rows × %d columns", s.Name, len(s.Rows), cols)
		outline = append(outline, docOutlineItem{Title: s.Name, Page: len(pages) + 1, Level: 1})
		if len(s.Rows) == 0 {
			pages = append(pages, title+"\n(empty)")
			continue
		}
		line := func(i int, r []string) string {
			cells := make([]string, len(r))
			for j, c := range r {
				cells[j] = strings.ReplaceAll(textutil.TruncateRunes(textutil.OneLine(c), 200, "…"), "|", "¦")
			}
			return fmt.Sprintf("%d | %s", i+1, strings.Join(cells, " | "))
		}
		header := line(0, s.Rows[0])
		var cur strings.Builder
		start := func() {
			cur.Reset()
			cur.WriteString(title + "\n" + header + "\n")
		}
		start()
		for i := 1; i < len(s.Rows); i++ {
			if len(s.Rows[i]) == 0 {
				continue
			}
			l := line(i, s.Rows[i])
			if cur.Len()+len(l) > 2*docPartChars && cur.Len() > len(title)+len(header)+2 {
				pages = append(pages, strings.TrimSpace(cur.String()))
				start()
			}
			cur.WriteString(l + "\n")
		}
		pages = append(pages, strings.TrimSpace(cur.String()))
	}
	return pages, outline
}

func pptxPages(r io.ReaderAt, size int64) ([]string, []docOutlineItem, error) {
	o, err := openOOXML(r, size)
	if err != nil {
		return nil, nil, err
	}
	if _, ok := o["ppt/presentation.xml"]; !ok {
		return nil, nil, errors.New("not a PowerPoint presentation")
	}
	rels := o.rels("ppt/presentation.xml")
	var slides []string
	dec, done, err := o.decoder("ppt/presentation.xml")
	if err != nil {
		return nil, nil, err
	}
	for dec != nil {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "sldId" {
			if part := rels[relID(t)]; part != "" {
				slides = append(slides, part)
			}
		}
	}
	done()
	if len(slides) == 0 {
		for name := range o {
			if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
				slides = append(slides, name)
			}
		}
		sort.Slice(slides, func(i, j int) bool { return slideNumber(slides[i]) < slideNumber(slides[j]) })
	}
	var pages []string
	var outline []docOutlineItem
	for i, part := range slides {
		if i >= docMaxPages {
			break
		}
		title, body, err := o.slideText(part, false)
		if err != nil {
			return nil, nil, fmt.Errorf("slide %d: %w", i+1, err)
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Slide %d", i+1)
		if title != "" {
			sb.WriteString(": " + title)
		}
		sb.WriteString("\n")
		if body != "" {
			sb.WriteString(body + "\n")
		}
		for _, target := range o.rels(part) {
			if strings.Contains(target, "notesSlides/") {
				if _, notes, err := o.slideText(target, true); err == nil && notes != "" {
					sb.WriteString("\nNotes: " + notes + "\n")
				}
			}
		}
		pages = append(pages, strings.TrimSpace(sb.String()))
		label := title
		if label == "" {
			label = textutil.TruncateRunes(firstLine(body), 80, "…")
		}
		if label != "" {
			outline = append(outline, docOutlineItem{Title: label, Page: i + 1, Level: 1})
		}
	}
	return pages, outline, nil
}

func slideNumber(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path.Base(name), "slide"), ".xml"))
	return n
}

func (o ooxml) slideText(part string, notes bool) (title, body string, err error) {
	dec, done, err := o.decoder(part)
	defer done()
	if dec == nil || err != nil {
		return "", "", err
	}
	var (
		lines           []string
		para, cell      strings.Builder
		row             []string
		phType          string
		inT, inCell     bool
		titleLines      []string
		skip            bool
		inShape, inPara bool
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sp":
				inShape, phType, skip = true, "", false
			case "ph":
				phType = attr(t, "type")
				skip = notes && phType != "body" && phType != ""
				skip = skip || phType == "sldNum" || phType == "dt" || phType == "ftr" || phType == "hdr"
			case "p":
				if t.Name.Space != "" && !strings.Contains(t.Name.Space, "drawingml") {
					continue
				}
				para.Reset()
				inPara = true
			case "t":
				inT = true
			case "br":
				para.WriteString(" ")
			case "tc":
				inCell = true
				cell.Reset()
			case "tr":
				row = nil
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if !inPara {
					continue
				}
				inPara = false
				text := strings.TrimSpace(para.String())
				switch {
				case text == "" || skip:
				case inCell:
					if cell.Len() > 0 {
						cell.WriteString(" ")
					}
					cell.WriteString(text)
				case inShape && (phType == "title" || phType == "ctrTitle"):
					titleLines = append(titleLines, text)
				default:
					lines = append(lines, text)
				}
			case "tc":
				inCell = false
				row = append(row, strings.ReplaceAll(cell.String(), "|", "¦"))
			case "tr":
				if strings.TrimSpace(strings.Join(row, "")) != "" {
					lines = append(lines, "| "+strings.Join(row, " | ")+" |")
				}
			case "sp":
				inShape, phType, skip = false, "", false
			}
		case xml.CharData:
			if inT {
				para.Write(t)
			}
		}
	}
	return strings.Join(titleLines, " "), strings.Join(lines, "\n"), nil
}
