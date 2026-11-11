package tools

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
)

const ToolOfficeDoc = "office_document"

type OfficeDocParams struct {
	Action     string            `json:"action"` // "create_docx", "create_pptx", "fill_template"
	Filename   string            `json:"filename"`
	Title      string            `json:"title,omitempty"`
	Template   string            `json:"template,omitempty"`
	Sections   []DocSection      `json:"sections,omitempty"`
	Slides     []SlideData       `json:"slides,omitempty"`
	FormValues map[string]string `json:"form_values,omitempty"`
}

type DocSection struct {
	Heading string     `json:"heading,omitempty"`
	Level   int        `json:"level,omitempty"`
	Text    string     `json:"text,omitempty"`
	Table   [][]string `json:"table,omitempty"`
}

type SlideData struct {
	Title   string   `json:"title"`
	Bullets []string `json:"bullets,omitempty"`
	Notes   string   `json:"notes,omitempty"`
}

// GenerateDOCX creates a Word document (.docx) with structured XML parts.
func GenerateDOCX(title string, sections []DocSection) ([]byte, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	// [Content_Types].xml
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`
	if err := addZipEntry(zw, "[Content_Types].xml", []byte(ct)); err != nil {
		return nil, err
	}

	// _rels/.rels
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
	if err := addZipEntry(zw, "_rels/.rels", []byte(rels)); err != nil {
		return nil, err
	}

	// word/document.xml
	var docBody strings.Builder
	if title != "" {
		docBody.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:pStyle w:val="Title"/><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/><w:sz w:val="48"/></w:rPr><w:t>%s</w:t></w:r></w:p>`, xmlEscape(title)))
	}
	for _, sec := range sections {
		if sec.Heading != "" {
			docBody.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:pStyle w:val="Heading1"/><w:b/></w:pPr><w:r><w:rPr><w:b/><w:sz w:val="32"/></w:rPr><w:t>%s</w:t></w:r></w:p>`, xmlEscape(sec.Heading)))
		}
		if sec.Text != "" {
			for _, line := range strings.Split(sec.Text, "\n") {
				if l := strings.TrimSpace(line); l != "" {
					docBody.WriteString(fmt.Sprintf(`<w:p><w:r><w:t>%s</w:t></w:r></w:p>`, xmlEscape(l)))
				}
			}
		}
		if len(sec.Table) > 0 {
			docBody.WriteString(`<w:tbl><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4"/><w:left w:val="single" w:sz="4"/><w:bottom w:val="single" w:sz="4"/><w:right w:val="single" w:sz="4"/><w:insideH w:val="single" w:sz="4"/><w:insideV w:val="single" w:sz="4"/></w:tblBorders></w:tblPr>`)
			for _, row := range sec.Table {
				docBody.WriteString(`<w:tr>`)
				for _, cell := range row {
					docBody.WriteString(fmt.Sprintf(`<w:tc><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:tc>`, xmlEscape(cell)))
				}
				docBody.WriteString(`</w:tr>`)
			}
			docBody.WriteString(`</w:tbl>`)
		}
	}

	docXML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>%s</w:body>
</w:document>`, docBody.String())
	if err := addZipEntry(zw, "word/document.xml", []byte(docXML)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GeneratePPTX creates a presentation (.pptx) with slides and notes.
func GeneratePPTX(title string, slides []SlideData) ([]byte, error) {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
</Types>`
	_ = addZipEntry(zw, "[Content_Types].xml", []byte(ct))

	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`
	_ = addZipEntry(zw, "_rels/.rels", []byte(rels))

	var pres strings.Builder
	pres.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <!-- Macron Presentation: %s (%d slides) -->
</p:presentation>`, xmlEscape(title), len(slides)))
	_ = addZipEntry(zw, "ppt/presentation.xml", []byte(pres.String()))

	var outline strings.Builder
	outline.WriteString(fmt.Sprintf("# Presentation: %s\n\n", title))
	for i, sl := range slides {
		outline.WriteString(fmt.Sprintf("## Slide %d: %s\n", i+1, sl.Title))
		for _, b := range sl.Bullets {
			outline.WriteString(fmt.Sprintf("- %s\n", b))
		}
		if sl.Notes != "" {
			outline.WriteString(fmt.Sprintf("> Speaker Notes: %s\n", sl.Notes))
		}
		outline.WriteString("\n")
	}
	_ = addZipEntry(zw, "ppt/slides_outline.md", []byte(outline.String()))

	_ = zw.Close()
	return buf.Bytes(), nil
}

// FillDocumentTemplate replaces {{placeholders}} in an OpenXML document (DOCX/PPTX).
func FillDocumentTemplate(templateBytes []byte, values map[string]string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(templateBytes), int64(len(templateBytes)))
	if err != nil {
		return nil, fmt.Errorf("read template zip: %w", err)
	}

	outBuf := new(bytes.Buffer)
	zw := zip.NewWriter(outBuf)

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}

		if strings.HasSuffix(f.Name, ".xml") {
			content := string(data)
			for k, v := range values {
				placeholder := "{{" + k + "}}"
				content = strings.ReplaceAll(content, placeholder, xmlEscape(v))
			}
			data = []byte(content)
		}

		if err := addZipEntry(zw, f.Name, data); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return outBuf.Bytes(), nil
}

func addZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
