from playwright.sync_api import sync_playwright

CSS = """
@page { size: A4; margin: 18mm 16mm 20mm 16mm; }
html { font-family: Vazirmatn, Vazir, "Noto Sans Arabic", "DejaVu Sans", sans-serif; font-size: 11pt; color: #1f2937; }
body { line-height: 1.8; margin: 0; }
h1, h2, h3, h4 { color: #0f172a; line-height: 1.4; margin: 1.1em 0 .45em; page-break-after: avoid; }
h1 { font-size: 20pt; border-bottom: 2px solid #10b981; padding-bottom: .25em; }
h2 { font-size: 15pt; border-bottom: 1px solid #e5e7eb; padding-bottom: .2em; }
h3 { font-size: 12.5pt; }
p { margin: .45em 0; text-align: justify; }
.doc-title { font-size: 24pt; margin: 0 0 .2em; border: 0; color: #065f46; }
.doc-date { color: #6b7280; font-size: 9.5pt; margin-bottom: 1.4em; }
table { border-collapse: collapse; width: 100%; margin: .8em 0; font-size: 10pt; page-break-inside: auto; }
th, td { border: 1px solid #d1d5db; padding: 6px 9px; text-align: start; vertical-align: top; }
th { background: #ecfdf5; color: #065f46; }
tr:nth-child(even) td { background: #f9fafb; }
tr { page-break-inside: avoid; }
pre { background: #0f172a; color: #e2e8f0; padding: 10px 12px; border-radius: 6px; font-size: 9pt; line-height: 1.5;
      white-space: pre-wrap; word-break: break-word; direction: ltr; text-align: left; }
code { font-family: "DejaVu Sans Mono", "Liberation Mono", monospace; }
p code, li code, td code { background: #f3f4f6; padding: 1px 4px; border-radius: 4px; font-size: .92em; }
blockquote { margin: .8em 0; padding: .3em 1em; border-inline-start: 4px solid #10b981; background: #f0fdf4; color: #374151; }
ul, ol { margin: .4em 0; padding-inline-start: 1.6em; }
ol[dir=rtl] { list-style-type: persian; }
li { margin: .15em 0; }
hr { border: 0; border-top: 1px solid #e5e7eb; margin: 1.2em 0; }
img { max-width: 100%; max-height: 180mm; display: block; margin: .6em auto; border-radius: 4px; }
a { color: #047857; text-decoration: none; }
"""

FOOTER = '<div style="font-size:8px;width:100%;text-align:center;color:#9ca3af;font-family:sans-serif"><span class="pageNumber"></span> / <span class="totalPages"></span></div>'

title = CFG.get("title") or ""
head = ""
if title:
    import html as _h
    head = f'<h1 class="doc-title" dir="auto">{_h.escape(title)}</h1>'
    if CFG.get("date"):
        head += f'<div class="doc-date" dir="auto">{_h.escape(CFG["date"])}</div>'
lang_dir = "rtl" if CFG.get("rtl") else "ltr"
doc = f'<!doctype html><html dir="{lang_dir}"><head><meta charset="utf-8"><style>{CSS}</style></head><body>{head}{CFG["html"]}</body></html>'

errors = []
with sync_playwright() as pw:
    browser = launch(pw)
    ctx = browser.new_context(locale="fa-IR", java_script_enabled=False)
    ctx.route("**/*", offline_libs)
    page = ctx.new_page()
    page.on("console", lambda m: m.type == "error" and errors.append(m.text))
    try:
        page.set_content(doc, wait_until="load", timeout=60000)
        path = inside(CFG["out"])
        if not path:
            fail("bad output path")
        page.pdf(path=path, format="A4", print_background=True, display_header_footer=True,
                 header_template="<span></span>", footer_template=FOOTER,
                 margin={"top": "18mm", "bottom": "20mm", "left": "16mm", "right": "16mm"})
    except Exception as e:
        fail(str(e)[:400])
    browser.close()
out({"ok": True, "errors": errors[:5]})
