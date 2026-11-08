import json
import sys

import pypdf

d = json.load(sys.stdin)
reader = pypdf.PdfReader(d["path"])
if reader.is_encrypted:
    try:
        reader.decrypt("")
    except Exception:
        print("PDF is password protected", file=sys.stderr)
        sys.exit(2)

total = len(reader.pages)
start = max(1, int(d.get("start", 1)))
end = int(d.get("end", 0)) or total
end = min(total, end, start - 1 + int(d.get("max_pages", 30)))
out = []
for i in range(start - 1, end):
    text = reader.pages[i].extract_text() or ""
    if text.strip():
        out.append("--- Page %d/%d ---\n%s" % (i + 1, total, text))
if start > total:
    out = ["The PDF has %d pages; page %d is past the end." % (total, start)]
elif end < total:
    out.append('--- Showing pages %d-%d of %d. Read on with pages="%d-", or ask document_reader. ---' % (start, end, total, end + 1))
print("\n".join(out) or "(no extractable text - the PDF may consist of scanned images)")
