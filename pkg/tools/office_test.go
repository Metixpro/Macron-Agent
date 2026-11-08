package tools

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readBack(t *testing.T, data []byte) []sheet {
	t.Helper()
	sheets, err := readXLSX(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return sheets
}

// A workbook made here opens as a real one: every part is well-formed XML,
// values come back, Persian sheets are right-to-left, the header is frozen
// with filters.
func TestCreateSpreadsheet(t *testing.T) {
	r := newRegistry(t, true, nil)
	out := call(t, r, "x", ToolCreateSpreadsheet, map[string]any{"filename": "prices", "sheets": []any{
		map[string]any{"name": "قیمت‌ها", "rows": []any{
			[]any{"کالا", "قیمت", "موجود"},
			[]any{"نان", 12000, true},
			[]any{"شیر", 45000.5, false},
			[]any{"جمع", "=SUM(B2:B3)"},
		}},
		map[string]any{"name": "Notes/2024", "rows": []any{[]any{"a <b> & c"}, []any{nil, "x"}}},
	}})
	if !strings.Contains(out, "Created prices.xlsx") || !strings.Contains(out, "قیمت‌ها: 4 rows × 3 columns") {
		t.Fatalf("%s", out)
	}
	data, _, err := r.Machine.ReadFile("x", "prices.xlsx", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		dec := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed: %v", f.Name, err)
			}
		}
	}
	for _, want := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml",
		"xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if _, ok := parts[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	s1 := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`rightToLeft="1"`, `state="frozen"`, `<autoFilter ref="A1:C4"/>`, `<f>SUM(B2:B3)</f>`, `t="b"><v>1</v>`, `<v>45000.5</v>`} {
		if !strings.Contains(s1, want) {
			t.Errorf("sheet 1 lacks %s", want)
		}
	}
	if strings.Contains(parts["xl/worksheets/sheet2.xml"], "rightToLeft") || !strings.Contains(parts["xl/workbook.xml"], `name="Notes_2024"`) {
		t.Error("an English sheet stays left-to-right; a name loses the characters Excel refuses")
	}
	sheets := readBack(t, data)
	if len(sheets) != 2 || sheets[0].Name != "قیمت‌ها" || strings.Join(sheets[0].Rows[1], ",") != "نان,12000,TRUE" ||
		sheets[0].Rows[2][1] != "45000.5" || sheets[1].Rows[0][0] != "a <b> & c" {
		t.Fatalf("read back: %+v", sheets)
	}

	if err := exec.Command("python3", "-c", "import openpyxl").Run(); err == nil {
		ws, _ := r.Machine.Workspace("x")
		check := `import openpyxl,sys; wb=openpyxl.load_workbook(sys.argv[1]); s=wb.worksheets[0]; ` +
			`assert s.sheet_view.rightToLeft and s.freeze_panes=="A2" and s["B4"].value=="=SUM(B2:B3)" and s["B2"].value==12000 and s["A1"].font.b, s; print("ok")`
		if out, err := exec.Command("python3", "-c", check, filepath.Join(ws, "prices.xlsx")).CombinedOutput(); err != nil || !strings.Contains(string(out), "ok") {
			t.Errorf("openpyxl: %s %v", out, err)
		}
	}

	_ = r.Machine.WriteFile("x", "list.csv", []byte("\xef\xbb\xbfname,phone,amount\nAli,09121234567,۱۲۵۰\nSara,0912,3.5\n"))
	if out := call(t, r, "x", ToolCreateSpreadsheet, map[string]any{"filename": "list.xlsx", "from_file": "list.csv"}); !strings.Contains(out, "list: 3 rows × 3 columns") {
		t.Fatalf("from csv: %s", out)
	}
	data, _, _ = r.Machine.ReadFile("x", "list.xlsx", 1<<20)
	if rows := readBack(t, data)[0].Rows; strings.Join(rows[1], ",") != "Ali,09121234567,1250" {
		t.Errorf("a phone number stays text, a Persian number becomes one: %v", rows)
	}
	_ = r.Machine.WriteFile("x", "items.json", []byte(`{"items":[{"title":"B","year":2001,"tags":["x"]},{"title":"A","author":"Z"}]}`))
	call(t, r, "x", ToolCreateSpreadsheet, map[string]any{"filename": "items.xlsx", "from_file": "items.json"})
	data, _, _ = r.Machine.ReadFile("x", "items.xlsx", 1<<20)
	if rows := readBack(t, data)[0].Rows; strings.Join(rows[0], ",") != "title,year,tags,author" || rows[1][2] != `["x"]` {
		t.Errorf("JSON keys in their order: %v", rows)
	}
	if out := call(t, r, "x", ToolCreateSpreadsheet, map[string]any{"filename": "x.docx", "sheets": []any{}}); !strings.HasPrefix(out, "error:") {
		t.Error("only .xlsx")
	}
}

func officeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// A workbook as Excel writes it: shared strings, a date style, a skipped row.
func TestDocumentReaderReadsExcel(t *testing.T) {
	r := newRegistry(t, true, nil)
	ns := `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	_ = r.Machine.WriteFile("x", "sales.xlsx", officeZip(t, map[string]string{
		"xl/workbook.xml": `<workbook ` + ns + `><sheets><sheet name="Sales" sheetId="1" r:id="rId1"/><sheet name="خالی" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="x/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="x/worksheet" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst ` + ns + `><si><t>Date</t></si><si><t>City</t></si><si><t>Amount</t></si><si><r><t>Teh</t></r><r><t>ran</t></r><rPh><t>x</t></rPh></si></sst>`,
		"xl/styles.xml":        `<styleSheet ` + ns + `><numFmts><numFmt numFmtId="164" formatCode="0.0%"/></numFmts><cellXfs><xf numFmtId="0"/><xf numFmtId="14"/><xf numFmtId="164"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml": `<worksheet ` + ns + `><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row>` +
			`<row r="3"><c r="A3" s="1"><v>45366</v></c><c r="B3" t="s"><v>3</v></c><c r="C3" s="2"><v>0.1250</v></c><c r="E3" t="inlineStr"><is><t>note | x</t></is></c></row>` +
			`</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet ` + ns + `><sheetData/></worksheet>`,
	}))
	out := call(t, r, "x", ToolDocumentReader, map[string]any{"action": "outline", "filename": "sales.xlsx"})
	if !strings.Contains(out, "Sales") || !strings.Contains(out, "خالی") {
		t.Fatalf("outline: %s", out)
	}
	out = call(t, r, "x", ToolDocumentReader, map[string]any{"action": "read", "filename": "sales.xlsx", "from_page": 1})
	for _, want := range []string{`Sheet "Sales" — 3 rows × 5 columns`, "1 | Date | City | Amount", "3 | 2024-03-15 | Tehran | 0.125 |  | note ¦ x"} {
		if !strings.Contains(out, want) {
			t.Errorf("read misses %q:\n%s", want, out)
		}
	}
}

// Slides come in the presentation's order, with their titles, tables and notes.
func TestDocumentReaderReadsSlides(t *testing.T) {
	r := newRegistry(t, true, nil)
	p := `xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	slide := func(title, body string) string {
		return `<p:sld ` + p + `><p:cSld><p:spTree>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="1" name="T"/><p:cNvSpPr/><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>` + title + `</a:t></a:r></a:p></p:txBody></p:sp>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="2" name="B"/><p:cNvSpPr/><p:nvPr><p:ph idx="1"/></p:nvPr></p:nvSpPr><p:txBody>` + body + `</p:txBody></p:sp>` +
			`</p:spTree></p:cSld></p:sld>`
	}
	_ = r.Machine.WriteFile("x", "deck.pptx", officeZip(t, map[string]string{
		"ppt/presentation.xml": `<p:presentation ` + p + `><p:sldIdLst><p:sldId id="256" r:id="rId3"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst></p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId2" Type="x/slide" Target="slides/slide1.xml"/><Relationship Id="rId3" Type="x/slide" Target="slides/slide2.xml"/></Relationships>`,
		"ppt/slides/slide2.xml": slide("Intro", `<a:p><a:r><a:t>Hello </a:t></a:r><a:r><a:t>world</a:t></a:r></a:p><a:p><a:r><a:t>Second point</a:t></a:r></a:p>`),
		"ppt/slides/slide1.xml": strings.Replace(slide("Budget", `<a:p><a:r><a:t>See the table</a:t></a:r></a:p>`), `</p:spTree>`,
			`<p:graphicFrame><a:graphic><a:graphicData><a:tbl><a:tr><a:tc><a:txBody><a:p><a:r><a:t>Q1</a:t></a:r></a:p></a:txBody></a:tc><a:tc><a:txBody><a:p><a:r><a:t>12</a:t></a:r></a:p></a:txBody></a:tc></a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame></p:spTree>`, 1),
		"ppt/slides/_rels/slide1.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x/notesSlide" Target="../notesSlides/notesSlide1.xml"/></Relationships>`,
		"ppt/notesSlides/notesSlide1.xml": `<p:notes ` + p + `><p:cSld><p:spTree>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="1" name="I"/><p:cNvSpPr/><p:nvPr><p:ph type="sldImg"/></p:nvPr></p:nvSpPr></p:sp>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="2" name="N"/><p:cNvSpPr/><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>Say the numbers slowly</a:t></a:r></a:p></p:txBody></p:sp>` +
			`<p:sp><p:nvSpPr><p:cNvPr id="3" name="S"/><p:cNvSpPr/><p:nvPr><p:ph type="sldNum"/></p:nvPr></p:nvSpPr><p:txBody><a:p><a:r><a:t>2</a:t></a:r></a:p></p:txBody></p:sp>` +
			`</p:spTree></p:cSld></p:notes>`,
	}))
	out := call(t, r, "x", ToolDocumentReader, map[string]any{"action": "read", "filename": "deck.pptx", "from_page": 1, "to_page": 2})
	for _, want := range []string{"Slide 1: Intro\nHello world\nSecond point", "Slide 2: Budget\nSee the table\n| Q1 | 12 |", "Notes: Say the numbers slowly"} {
		if !strings.Contains(out, want) {
			t.Errorf("read misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Notes: Say the numbers slowly 2") || strings.Contains(out, "\n2\n") {
		t.Errorf("the slide number is not a note:\n%s", out)
	}
	if out := call(t, r, "x", ToolDocumentReader, map[string]any{"action": "outline", "filename": "deck.pptx"}); !strings.Contains(out, "Intro") || !strings.Contains(out, "Budget") {
		t.Errorf("outline: %s", out)
	}
}

func makeSQLite(t *testing.T, r *Registry, session, name string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	ws, _ := r.Machine.Workspace(session)
	script := `import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.executescript("""
CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT);
CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id), total REAL);
INSERT INTO customers VALUES (1,'Ali','Tehran'),(2,'Sara','Shiraz'),(3,'Reza','Tehran');
INSERT INTO orders VALUES (1,1,120.5),(2,1,80),(3,2,42),(4,3,10);
CREATE VIEW big AS SELECT * FROM orders WHERE total > 50;"""); c.commit()`
	if out, err := exec.Command("python3", "-c", script, filepath.Join(ws, name)).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
}

func TestDocumentReaderReadsSQLite(t *testing.T) {
	r := newRegistry(t, true, nil)
	makeSQLite(t, r, "x", "shop.db")
	out := call(t, r, "x", ToolDocumentReader, map[string]any{"action": "read", "filename": "shop.db", "from_page": 1, "to_page": 3})
	for _, want := range []string{"2 tables, 1 views", "- table customers: 3 rows", "- table orders: 4 rows",
		"Columns: id INTEGER PRIMARY KEY, name TEXT NOT NULL, city TEXT", "| 1 | Ali | Tehran |", "Foreign key: customer_id → customers(id)"} {
		if !strings.Contains(out, want) {
			t.Errorf("misses %q:\n%s", want, out)
		}
	}
	_ = r.Machine.WriteFile("x", "Thumbs.db", []byte("not a database at all, just bytes"))
	if out := call(t, r, "x", ToolDocumentReader, map[string]any{"action": "outline", "filename": "Thumbs.db"}); !strings.Contains(out, "not a SQLite database") {
		t.Errorf("another kind of .db: %s", out)
	}
}

// SQL joins a database, a CSV and a sheet of a workbook, read-only.
func TestQueryData(t *testing.T) {
	r := newRegistry(t, true, nil)
	makeSQLite(t, r, "x", "shop.db")
	_ = r.Machine.WriteFile("x", "cities.csv", []byte("City Name,Population\nTehran,\"9,000,000\"\nShiraz,۱۸۰۰۰۰۰\n"))
	var buf bytes.Buffer
	_ = writeXLSX(&buf, []xlsxSheet{{Name: "اهداف فروش", Header: true, Rows: [][]any{{"city", "goal"}, {"Tehran", 150.0}, {"Shiraz", 40.0}}}})
	_ = r.Machine.WriteFile("x", "goals.xlsx", buf.Bytes())
	files := []string{"shop.db", "cities.csv", "goals.xlsx"}

	out := call(t, r, "x", ToolQueryData, map[string]any{"files": files})
	for _, want := range []string{"cities (from cities.csv, 2 rows): City_Name TEXT, Population INTEGER", "goals_اهداف_فروش (from goals.xlsx › اهداف فروش, 2 rows)",
		"customers (from shop.db, 3 rows): id INTEGER, name TEXT, city TEXT"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema misses %q:\n%s", want, out)
		}
	}
	sql := `SELECT c.city, SUM(o.total) AS sold, g.goal, p.Population FROM orders o JOIN customers c ON c.id = o.customer_id
		JOIN goals_اهداف_فروش g ON g.city = c.city JOIN cities p ON p.City_Name = c.city GROUP BY c.city ORDER BY sold DESC`
	out = call(t, r, "x", ToolQueryData, map[string]any{"files": files, "sql": sql, "output": "report.xlsx"})
	if !strings.Contains(out, "| city | sold | goal | Population |") || !strings.Contains(out, "| Tehran | 210.5 | 150 | 9000000 |") ||
		!strings.Contains(out, "| Shiraz | 42 | 40 | 1800000 |") || !strings.Contains(out, "(2 rows)") || !strings.Contains(out, "Saved the whole result to report.xlsx") {
		t.Fatalf("join:\n%s", out)
	}
	data, _, _ := r.Machine.ReadFile("x", "report.xlsx", 1<<20)
	if rows := readBack(t, data)[0].Rows; len(rows) != 3 || rows[1][1] != "210.5" {
		t.Errorf("the saved result: %v", rows)
	}
	if out := call(t, r, "x", ToolQueryData, map[string]any{"files": []string{"shop.db"}, "sql": "DELETE FROM orders"}); !strings.Contains(out, "error: SQL error") {
		t.Errorf("read-only: %s", out)
	}
	if out := call(t, r, "x", ToolQueryData, map[string]any{"files": []string{"shop.db"}, "sql": "SELECT * FROM big", "max_rows": 1}); !strings.Contains(out, "(1 of 2 rows shown)") {
		t.Errorf("views and the row limit: %s", out)
	}
	if out := call(t, r, "x", ToolQueryData, map[string]any{"files": []string{"shop.db"}, "sql": "SELEC nonsense"}); !strings.Contains(out, "call without sql to see the tables") {
		t.Errorf("a bad query explains itself: %s", out)
	}
	if _, err := r.Machine.Stat("x", ".run/data"); err == nil {
		t.Error("the scratch files go")
	}
}
