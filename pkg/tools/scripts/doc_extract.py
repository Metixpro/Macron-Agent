import json
import os
import sys
import unicodedata

d = json.load(sys.stdin)
path, out = d["path"], d["out"]
max_pages = int(d.get("max_pages", 3000))


def norm(t):

    return unicodedata.normalize("NFKC", t or "").replace("\x00", "")


pages, outline, title = [], [], ""
try:
    import fitz

    doc = fitz.open(path)
    if doc.needs_pass and not doc.authenticate(""):
        print("PDF is password protected", file=sys.stderr)
        sys.exit(2)
    total = doc.page_count
    for i, page in enumerate(doc):
        if i >= max_pages:
            break
        pages.append(norm(page.get_text()))
    for level, name, page in doc.get_toc()[:800]:
        outline.append({"title": norm(name).strip(), "page": int(page), "level": int(level)})
    title = norm((doc.metadata or {}).get("title") or "")
except ImportError:
    import pypdf

    reader = pypdf.PdfReader(path)
    if reader.is_encrypted:
        try:
            reader.decrypt("")
        except Exception:
            print("PDF is password protected", file=sys.stderr)
            sys.exit(2)
    total = len(reader.pages)
    for i, page in enumerate(reader.pages):
        if i >= max_pages:
            break
        try:
            pages.append(norm(page.extract_text()))
        except Exception:
            pages.append("")

    def walk(items, level):
        for item in items:
            if len(outline) >= 800:
                return
            if isinstance(item, list):
                walk(item, level + 1)
                continue
            try:
                outline.append({"title": norm(item.title).strip(), "page": reader.get_destination_page_number(item) + 1, "level": level})
            except Exception:
                pass

    try:
        walk(reader.outline, 1)
    except Exception:
        pass
    try:
        title = norm((reader.metadata or {}).get("/Title") or "")
    except Exception:
        title = ""

os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
with open(out, "w", encoding="utf-8") as f:
    json.dump({"title": title.strip(), "pages": pages, "outline": outline, "total_pages": total}, f, ensure_ascii=False)
print(json.dumps({"pages": len(pages), "total_pages": total, "chars": sum(len(p) for p in pages)}))
