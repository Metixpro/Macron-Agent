import json
import os
import sqlite3
import sys
import urllib.parse

d = json.load(sys.stdin)
path, out, sample = d["path"], d["out"], int(d.get("sample", 5))

with open(path, "rb") as f:
    if f.read(16) != b"SQLite format 3\x00":
        print("not a SQLite database", file=sys.stderr)
        sys.exit(2)

con = sqlite3.connect("file:" + urllib.parse.quote(os.path.abspath(path)) + "?mode=ro&immutable=1", uri=True)
con.execute("PRAGMA query_only=ON")
try:
    con.execute("PRAGMA trusted_schema=OFF")
except sqlite3.Error:
    pass


def q(name):
    return '"' + name.replace('"', '""') + '"'


def show(v):
    if v is None:
        return "NULL"
    if isinstance(v, bytes):
        return "<blob %d bytes>" % len(v)
    s = str(v).replace("\n", " ").replace("|", "¦")
    return s if len(s) <= 80 else s[:79] + "…"


objects = con.execute(
    "SELECT name, type, sql FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY type, name"
).fetchall()
indexes = con.execute("SELECT count(*) FROM sqlite_master WHERE type='index'").fetchone()[0]

counts = {}
for name, kind, _ in objects:
    try:
        counts[name] = con.execute("SELECT count(*) FROM " + q(name)).fetchone()[0]
    except sqlite3.Error as e:
        counts[name] = "? (%s)" % e

size = os.path.getsize(path)
head = ["SQLite database %s — %.1f MB, %d tables, %d views, %d indexes" % (
    os.path.basename(path), size / 1048576, sum(1 for o in objects if o[1] == "table"),
    sum(1 for o in objects if o[1] == "view"), indexes), ""]
for name, kind, _ in objects[:500]:
    head.append("- %s %s: %s rows" % (kind, name, counts[name]))
head.append("")
head.append("Query it with query_data (SQL, read-only).")
pages, outline = ["\n".join(head)], [{"title": "Overview", "page": 1, "level": 1}]

for name, kind, sql in objects[:500]:
    lines = ["%s %s — %s rows" % (kind.capitalize(), name, counts[name]), "", (sql or "").strip(), ""]
    try:
        cols = con.execute("PRAGMA table_info(" + q(name) + ")").fetchall()
        lines.append("Columns: " + ", ".join(
            "%s %s%s%s" % (c[1], c[2] or "ANY", " NOT NULL" if c[3] else "", " PRIMARY KEY" if c[5] else "") for c in cols))
        for fk in con.execute("PRAGMA foreign_key_list(" + q(name) + ")").fetchall():
            lines.append("Foreign key: %s → %s(%s)" % (fk[3], fk[2], fk[4]))
        cur = con.execute("SELECT * FROM " + q(name) + " LIMIT %d" % sample)
        names = [c[0] for c in cur.description]
        rows = cur.fetchall()
        if rows:
            lines += ["", "First %d rows:" % len(rows), "| " + " | ".join(names) + " |"]
            lines += ["| " + " | ".join(show(v) for v in r) + " |" for r in rows]
    except sqlite3.Error as e:
        lines.append("(could not read: %s)" % e)
    outline.append({"title": name, "page": len(pages) + 1, "level": 1})
    pages.append("\n".join(lines).strip())

os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
with open(out, "w", encoding="utf-8") as f:
    json.dump({"title": os.path.basename(path), "pages": pages, "outline": outline}, f, ensure_ascii=False)
