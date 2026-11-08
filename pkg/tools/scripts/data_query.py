import csv
import json
import os
import re
import sqlite3
import sys
import urllib.parse

d = json.load(sys.stdin)
csv.field_size_limit(16 << 20)
MAX_IMPORT = 2_000_000
DIGITS = str.maketrans("۰۱۲۳۴۵۶۷۸۹٠١٢٣٤٥٦٧٨٩٫", "01234567890123456789.")
INT = re.compile(r"^-?(0|[1-9][0-9]{0,17})$")
REAL = re.compile(r"^-?(0|[1-9][0-9]*)?\.[0-9]+$|^-?(0|[1-9][0-9]*)(\.[0-9]+)?[eE][-+]?[0-9]+$")
GROUPED = re.compile(r"^-?[1-9][0-9]{0,2}(,[0-9]{3})+(\.[0-9]+)?$")


def q(name):
    return '"' + str(name).replace('"', '""') + '"'


def ident(name, used, fallback):
    s = re.sub(r"[\x00-\x2f\x3a-\x40\x5b-\x5e\x60\x7b-\x7f\s]+", "_", str(name or "")).strip("_")
    if not s:
        s = fallback
    if s[0].isdigit():
        s = "c_" + s
    base, n = s, 2
    while s.lower() in used:
        s, n = "%s_%d" % (base, n), n + 1
    used.add(s.lower())
    return s


def number(v):
    t = v.strip().translate(DIGITS)
    if GROUPED.match(t):
        t = t.replace(",", "")
    if INT.match(t):
        return int(t)
    if REAL.match(t):
        return float(t)
    return None


def load(con, table, header, rows):
    used = set()
    cols = [ident(h, used, "col_%d" % (i + 1)) for i, h in enumerate(header)]
    width = max([len(cols)] + [len(r) for r in rows])
    while len(cols) < width:
        cols.append(ident("", used, "col_%d" % (len(cols) + 1)))
    types = []
    for i in range(width):
        kind = None
        for r in rows:
            v = r[i] if i < len(r) else None
            if v is None or (isinstance(v, str) and v.strip() == ""):
                continue
            if isinstance(v, bool) or not isinstance(v, (int, float, str)):
                kind = "TEXT"
                break
            n = v if isinstance(v, (int, float)) else number(v)
            if n is None:
                kind = "TEXT"
                break
            kind = "REAL" if isinstance(n, float) or kind == "REAL" else "INTEGER"
        types.append(kind or "TEXT")
    con.execute("CREATE TABLE %s (%s)" % (q(table), ", ".join("%s %s" % (q(c), t) for c, t in zip(cols, types))))

    def conv(r):
        out = []
        for i in range(width):
            v = r[i] if i < len(r) else None
            if isinstance(v, str) and v.strip() == "":
                v = None
            elif types[i] != "TEXT" and isinstance(v, str):
                v = number(v)
            elif isinstance(v, (dict, list)):
                v = json.dumps(v, ensure_ascii=False)
            out.append(v)
        return out

    con.executemany("INSERT INTO %s VALUES (%s)" % (q(table), ",".join("?" * width)), (conv(r) for r in rows))
    return cols


con = sqlite3.connect("file::memory:", uri=True)
tables, attached = [], []
for s in d["sources"]:
    path = s["path"]
    if s["kind"] == "sqlite":
        with open(path, "rb") as f:
            if f.read(16) != b"SQLite format 3\x00":
                print("%s is not a SQLite database" % s["label"], file=sys.stderr)
                sys.exit(2)
        uri = "file:" + urllib.parse.quote(os.path.abspath(path)) + "?mode=ro&immutable=1"
        con.execute("ATTACH DATABASE ? AS %s" % q(s["table"]), (uri,))
        attached.append((s["table"], s["label"]))
        continue
    if s["kind"] == "json":
        with open(path, encoding="utf-8-sig") as f:
            data = json.load(f)
        if isinstance(data, dict):
            data = next((v for v in data.values() if isinstance(v, list)), [data])
        if not isinstance(data, list):
            data = [data]
        if data and all(isinstance(x, dict) for x in data):
            keys = []
            for x in data:
                for k in x:
                    if k not in keys:
                        keys.append(k)
            header, rows = keys, [[x.get(k) for k in keys] for x in data[:MAX_IMPORT]]
        else:
            rows = [x if isinstance(x, list) else [x] for x in data[:MAX_IMPORT]]
            header = []
    else:
        with open(path, encoding="utf-8-sig", errors="replace", newline="") as f:
            reader = csv.reader(f, delimiter=s.get("delim", ","))
            header = next(reader, [])
            rows = []
            for r in reader:
                if len(rows) >= MAX_IMPORT:
                    break
                rows.append(r)
    cols = load(con, s["table"], header, rows)
    tables.append((s["table"], s["label"], cols, len(rows)))

allowed_pragmas = {"table_info", "table_xinfo", "index_list", "index_info", "foreign_key_list", "table_list"}


def auth(action, a1, a2, db, trigger):
    if action in (sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, sqlite3.SQLITE_FUNCTION, getattr(sqlite3, "SQLITE_RECURSIVE", 33)):
        return sqlite3.SQLITE_OK
    if action == sqlite3.SQLITE_PRAGMA and (a1 or "").lower() in allowed_pragmas:
        return sqlite3.SQLITE_OK
    return sqlite3.SQLITE_DENY


sql = (d.get("sql") or "").strip()
if not sql:
    print("Tables (SQLite SQL; quote names with spaces in double quotes):")
    for name, label, cols, n in tables:
        types = {r[1]: r[2] for r in con.execute("PRAGMA table_info(%s)" % q(name))}
        print("- %s (from %s, %d rows): %s" % (name, label, n, ", ".join("%s %s" % (c, types.get(c, "")) for c in cols)))
    for alias, label in attached:
        for (t,) in con.execute("SELECT name FROM %s.sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%%' ORDER BY name" % q(alias)):
            cols = con.execute("PRAGMA %s.table_info(%s)" % (q(alias), q(t))).fetchall()
            try:
                n = con.execute("SELECT count(*) FROM %s.%s" % (q(alias), q(t))).fetchone()[0]
            except sqlite3.Error:
                n = "?"
            print("- %s (from %s, %s rows): %s" % (t, label, n, ", ".join("%s %s" % (c[1], c[2] or "") for c in cols)))
    sys.exit(0)

con.set_authorizer(auth)
try:
    cur = con.execute(sql)
except sqlite3.Error as e:
    print("SQL error: %s" % e, file=sys.stderr)
    sys.exit(3)
if cur.description is None:
    print("(the statement returned no rows)")
    sys.exit(0)
names = [c[0] for c in cur.description]
max_rows = int(d.get("max_rows", 50))
out_csv = d.get("out_csv")
writer = None
if out_csv:
    os.makedirs(os.path.dirname(out_csv) or ".", exist_ok=True)
    fh = open(out_csv, "w", encoding="utf-8-sig", newline="")
    writer = csv.writer(fh)
    writer.writerow(names)


def show(v):
    if v is None:
        return "NULL"
    if isinstance(v, bytes):
        return "<blob %d bytes>" % len(v)
    if isinstance(v, float):
        v = repr(round(v, 10)).rstrip("0").rstrip(".") if v == v and abs(v) < 1e15 else repr(v)
    s = str(v).replace("\n", " ").replace("|", "¦")
    return s if len(s) <= 60 else s[:59] + "…"


shown, total, capped = [], 0, False
for row in cur:
    total += 1
    if len(shown) < max_rows:
        shown.append(row)
    if writer:
        writer.writerow(["" if v is None else v for v in row])
    if total >= 5_000_000:
        capped = True
        break
if writer:
    fh.close()
print("| " + " | ".join(names) + " |")
print("|" + "---|" * len(names))
for r in shown:
    print("| " + " | ".join(show(v) for v in r) + " |")
if total > len(shown):
    print("(%d of %s rows shown)" % (len(shown), "%d+" % total if capped else total))
else:
    print("(%d rows)" % total)
