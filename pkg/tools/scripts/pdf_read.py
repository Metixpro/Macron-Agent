import json
import sys
import unicodedata

d = json.load(sys.stdin)
path = d["path"]


def norm(t):

    return unicodedata.normalize("NFKC", t or "").replace("\x00", "")


def locked():
    print("PDF is password protected", file=sys.stderr)
    sys.exit(2)


try:

    import fitz

    doc = fitz.open(path)
    if doc.needs_pass and not doc.authenticate(""):
        locked()
    total = doc.page_count

    def page_text(i):
        return doc[i].get_text()

except ImportError:
    import pypdf

    reader = pypdf.PdfReader(path)
    if reader.is_encrypted:
        try:
            reader.decrypt("")
        except Exception:
            locked()
    total = len(reader.pages)

    def page_text(i):
        return reader.pages[i].extract_text()


start = max(1, int(d.get("start", 1)))
end = int(d.get("end", 0)) or total
end = min(total, end, start - 1 + int(d.get("max_pages", 30)))
out = []
for i in range(start - 1, end):
    try:
        text = norm(page_text(i))
    except Exception:
        text = ""
    if text.strip():
        out.append("--- Page %d/%d ---\n%s" % (i + 1, total, text.rstrip("\n")))
if start > total:
    out = ["The PDF has %d pages; page %d is past the end." % (total, start)]
elif end < total:
    out.append('--- Showing pages %d-%d of %d. Read on with pages="%d-", or ask document_reader. ---' % (start, end, total, end + 1))
print("\n".join(out) or "(no extractable text - the PDF may consist of scanned images)")
