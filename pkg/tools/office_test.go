package tools

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestGenerateDOCX(t *testing.T) {
	sections := []DocSection{
		{
			Heading: "Introduction",
			Text:    "This is an automated test document.",
			Table: [][]string{
				{"Col 1", "Col 2"},
				{"Row 1", "Data 1"},
			},
		},
	}

	b, err := GenerateDOCX("Annual Report", sections)
	if err != nil {
		t.Fatalf("GenerateDOCX: %v", err)
	}

	// Verify valid zip
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}

	foundDoc := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			foundDoc = true
			rc, _ := f.Open()
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(rc)
			rc.Close()
			if !strings.Contains(buf.String(), "Annual Report") {
				t.Errorf("missing title in document.xml")
			}
			if !strings.Contains(buf.String(), "Annual Report") || !strings.Contains(buf.String(), "Introduction") {
				t.Errorf("missing sections in document.xml")
			}
		}
	}
	if !foundDoc {
		t.Fatal("word/document.xml not found in generated docx")
	}
}

func TestGeneratePPTX(t *testing.T) {
	slides := []SlideData{
		{
			Title:   "Overview",
			Bullets: []string{"Point 1", "Point 2"},
			Notes:   "Remember to highlight point 1",
		},
	}

	b, err := GeneratePPTX("Project Kickoff", slides)
	if err != nil {
		t.Fatalf("GeneratePPTX: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}

	foundPres := false
	for _, f := range zr.File {
		if f.Name == "ppt/presentation.xml" {
			foundPres = true
		}
	}
	if !foundPres {
		t.Fatal("ppt/presentation.xml not found in generated pptx")
	}
}

func TestFillDocumentTemplate(t *testing.T) {
	// Create mock docx template with placeholder
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	xmlContent := `<w:document><w:body><w:p><w:t>Hello {{name}}, welcome to {{city}}!</w:t></w:p></w:body></w:document>`
	_ = addZipEntry(zw, "word/document.xml", []byte(xmlContent))
	_ = zw.Close()

	filled, err := FillDocumentTemplate(buf.Bytes(), map[string]string{
		"name": "Ali",
		"city": "Tehran",
	})
	if err != nil {
		t.Fatalf("FillDocumentTemplate: %v", err)
	}

	zr, _ := zip.NewReader(bytes.NewReader(filled), int64(len(filled)))
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b := new(bytes.Buffer)
			_, _ = b.ReadFrom(rc)
			rc.Close()
			if !strings.Contains(b.String(), "Hello Ali, welcome to Tehran!") {
				t.Errorf("template placeholders not filled: %s", b.String())
			}
		}
	}
}
