import json
import os
import re
import sys

from reportlab.lib.pagesizes import A4
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfgen import canvas

d = json.load(sys.stdin)
RTL = re.compile("[֐-ࣿיִ-﷿ﹰ-﻿]")


def shape(s):
    if not RTL.search(s):
        return s
    try:
        import arabic_reshaper
        from bidi.algorithm import get_display

        return get_display(arabic_reshaper.reshape(s))
    except Exception:
        return s


FONT = "Helvetica"
for path in (
    "/usr/share/fonts/truetype/vazirmatn/Vazirmatn-Regular.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/TTF/DejaVuSans.ttf",
    "/usr/share/fonts/truetype/noto/NotoSansArabic-Regular.ttf",
):
    if os.path.exists(path):
        pdfmetrics.registerFont(TTFont("Body", path))
        FONT = "Body"
        break


def safe(s):

    return s if FONT != "Helvetica" else s.encode("latin-1", "replace").decode("latin-1")


SIZE, LEADING = 11, 16
W, H = A4
M = 50
usable = W - 2 * M

c = canvas.Canvas(d["out"], pagesize=A4)
if d.get("title"):
    c.setTitle(d["title"][:200])
y = H - M


def newpage():
    global y
    c.showPage()
    y = H - M


def width(text, size):
    return pdfmetrics.stringWidth(safe(shape(text)), FONT, size)


def draw(line, size, rtl):
    global y
    if y < M + size:
        newpage()
    c.setFont(FONT, size)
    shaped = safe(shape(line))
    if rtl:
        c.drawRightString(W - M, y, shaped)
    else:
        c.drawString(M, y, shaped)
    y -= LEADING if size == SIZE else size * 1.5


if d.get("title"):
    draw(d["title"], 16, bool(RTL.search(d["title"])))
    y -= 6

for para in d["text"].replace("\r\n", "\n").split("\n"):
    rtl = bool(RTL.search(para))
    if not para.strip():
        y -= LEADING / 2
        continue
    line = ""
    for word in para.split(" "):
        cand = (line + " " + word).strip() if line else word
        if line and width(cand, SIZE) > usable:
            draw(line, SIZE, rtl)
            line = word
        else:
            line = cand
    if line:
        draw(line, SIZE, rtl)

c.save()
print("OK")
