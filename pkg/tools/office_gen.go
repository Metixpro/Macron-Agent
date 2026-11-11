package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolOfficeDoc = "office_document"

const officeScript = `
import json, re, sys
spec = json.load(sys.stdin)
RTL = re.compile(r"[֐-ࣿיִ-ﻼ]")

def rtl_of(text):
    return spec.get("rtl") if spec.get("rtl") is not None else bool(RTL.search(text or ""))

def docx_rtl(par, on):
    if not on:
        return
    from docx.oxml.ns import qn
    from docx.oxml import OxmlElement
    from docx.enum.text import WD_ALIGN_PARAGRAPH
    ppr = par._p.get_or_add_pPr()
    ppr.append(OxmlElement("w:bidi"))
    par.alignment = WD_ALIGN_PARAGRAPH.RIGHT
    for run in par.runs:
        rpr = run._r.get_or_add_rPr()
        rpr.append(OxmlElement("w:rtl"))

def add_runs(par, text):
    parts = re.split(r"(\*\*[^*]+\*\*)", text or "")
    for p in parts:
        if p.startswith("**") and p.endswith("**") and len(p) > 4:
            par.add_run(p[2:-2]).bold = True
        elif p:
            par.add_run(p)

def make_docx():
    import docx
    from docx.shared import Inches
    d = docx.Document()
    if spec.get("title"):
        h = d.add_heading(spec["title"], 0)
        docx_rtl(h, rtl_of(spec["title"]))
    for b in spec.get("blocks") or []:
        if b.get("heading"):
            h = d.add_heading(b["heading"], min(max(int(b.get("level") or 1), 1), 4))
            docx_rtl(h, rtl_of(b["heading"]))
        if b.get("text"):
            for line in str(b["text"]).split("\n"):
                if line.strip():
                    p = d.add_paragraph()
                    add_runs(p, line)
                    docx_rtl(p, rtl_of(line))
        for item in b.get("bullets") or []:
            p = d.add_paragraph(style="List Bullet")
            add_runs(p, str(item))
            docx_rtl(p, rtl_of(str(item)))
        for item in b.get("numbered") or []:
            p = d.add_paragraph(style="List Number")
            add_runs(p, str(item))
            docx_rtl(p, rtl_of(str(item)))
        rows = b.get("table") or []
        if rows:
            cols = max(len(r) for r in rows)
            t = d.add_table(rows=len(rows), cols=cols)
            t.style = "Table Grid"
            for i, r in enumerate(rows):
                for j in range(cols):
                    cell = t.cell(i, j)
                    cell.text = str(r[j]) if j < len(r) else ""
                    for p in cell.paragraphs:
                        if i == 0:
                            for run in p.runs:
                                run.bold = True
                        docx_rtl(p, rtl_of(cell.text))
        if b.get("image"):
            d.add_picture(b["image"], width=Inches(float(b.get("width_inches") or 6)))
        if b.get("page_break"):
            d.add_page_break()
    d.save(spec["out"])
    print("paragraphs=%d tables=%d" % (len(d.paragraphs), len(d.tables)))

def make_pptx():
    from pptx import Presentation
    from pptx.util import Inches, Pt
    from pptx.enum.text import PP_ALIGN
    prs = Presentation()
    prs.slide_width, prs.slide_height = Inches(13.333), Inches(7.5)
    def align(tf, text):
        if rtl_of(text):
            for p in tf.paragraphs:
                p.alignment = PP_ALIGN.RIGHT
    if spec.get("title"):
        s = prs.slides.add_slide(prs.slide_layouts[0])
        s.shapes.title.text = spec["title"]
        align(s.shapes.title.text_frame, spec["title"])
        if spec.get("subtitle") and len(s.placeholders) > 1:
            s.placeholders[1].text = spec["subtitle"]
    for sl in spec.get("blocks") or []:
        bullets = [str(x) for x in (sl.get("bullets") or [])]
        layout = prs.slide_layouts[1] if bullets or not sl.get("image") else prs.slide_layouts[5]
        s = prs.slides.add_slide(layout)
        s.shapes.title.text = str(sl.get("title") or "")
        align(s.shapes.title.text_frame, s.shapes.title.text)
        if bullets and len(s.placeholders) > 1:
            tf = s.placeholders[1].text_frame
            tf.text = bullets[0]
            for x in bullets[1:]:
                tf.add_paragraph().text = x
            for p in tf.paragraphs:
                for r in p.runs:
                    r.font.size = Pt(24)
            align(tf, " ".join(bullets))
        if sl.get("image"):
            left = Inches(7.2) if bullets else Inches(1.5)
            width = Inches(5.6) if bullets else Inches(10.3)
            s.shapes.add_picture(sl["image"], left, Inches(1.6), width=width)
        if sl.get("notes"):
            s.notes_slide.notes_text_frame.text = str(sl["notes"])
    prs.save(spec["out"])
    print("slides=%d" % len(prs.slides))

def fill():
    values = {str(k): str(v) for k, v in (spec.get("values") or {}).items()}
    def sub(text):
        for k, v in values.items():
            text = text.replace("{{" + k + "}}", v)
        return text
    def fix(par):
        full = "".join(r.text for r in par.runs)
        if "{{" in full and par.runs:
            new = sub(full)
            if new != full:
                par.runs[0].text = new
                for r in par.runs[1:]:
                    r.text = ""
                return 1
        return 0
    n = 0
    src = spec["template"]
    if src.lower().endswith(".docx"):
        import docx
        d = docx.Document(src)
        pars = list(d.paragraphs)
        for t in d.tables:
            for row in t.rows:
                for c in row.cells:
                    pars.extend(c.paragraphs)
        for s in d.sections:
            pars.extend(s.header.paragraphs)
            pars.extend(s.footer.paragraphs)
        n = sum(fix(p) for p in pars)
        d.save(spec["out"])
    elif src.lower().endswith(".pptx"):
        from pptx import Presentation
        prs = Presentation(src)
        for s in prs.slides:
            for sh in s.shapes:
                if sh.has_text_frame:
                    n += sum(fix(p) for p in sh.text_frame.paragraphs)
        prs.save(spec["out"])
    else:
        raise SystemExit("templates are .docx or .pptx")
    left = sorted(set(re.findall(r"\{\{\s*([^}]+?)\s*\}\}", open(spec["out"], "rb").read().decode("latin-1"))))
    print("replaced=%d" % n + (" left=" + ",".join(left) if left else ""))

try:
    {"docx": make_docx, "pptx": make_pptx, "fill": fill}[spec["kind"]]()
except ImportError as e:
    raise SystemExit("missing Python package: %s (it is in the tools venv: run install.sh again, or pip install python-docx python-pptx)" % e.name)
`

func (r *Registry) OfficeDocument(ctx context.Context, sessionID, action, filename string, args map[string]any) (string, error) {
	filename = strings.TrimSpace(filename)
	ext := strings.ToLower(path.Ext(filename))
	spec := map[string]any{"out": filename, "title": strArg(args, "title"), "subtitle": strArg(args, "subtitle"), "blocks": args["blocks"]}
	if v, ok := args["rtl"].(bool); ok {
		spec["rtl"] = v
	}
	switch action {
	case "create_docx", "":
		if ext != ".docx" {
			return "", fmt.Errorf("filename must end in .docx")
		}
		spec["kind"] = "docx"
	case "create_pptx":
		if ext != ".pptx" {
			return "", fmt.Errorf("filename must end in .pptx")
		}
		spec["kind"] = "pptx"
	case "fill_template":
		tpl := strArg(args, "template")
		if tpl == "" {
			return "", fmt.Errorf("template is required")
		}
		if strings.ToLower(path.Ext(tpl)) != ext {
			return "", fmt.Errorf("the output must have the template's type (%s)", path.Ext(tpl))
		}
		spec["kind"], spec["template"], spec["values"] = "fill", tpl, args["values"]
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
	if spec["kind"] != "fill" {
		if b, ok := args["blocks"].([]any); !ok || len(b) == 0 {
			return "", fmt.Errorf("blocks is required: the document's content (docx: headings, text, bullets, tables, images; pptx: one object per slide)")
		}
	}
	in, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{"python3", "-c", officeScript}, Stdin: in, Timeout: 2 * time.Minute})
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s", firstRunes(strings.TrimSpace(lastLines(res.Stderr+"\n"+res.Error, 6)), 800))
	}
	return fmt.Sprintf("Saved %s (%s); check it with document_reader before sending.", filename, strings.TrimSpace(res.Stdout)), nil
}
