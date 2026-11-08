import json
import re
import sys

d = json.load(sys.stdin)

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager, ticker

RTL = re.compile("[֐-ࣿיִ-﷿ﹰ-﻿]")

available = {f.name for f in font_manager.fontManager.ttflist}
FONT = next((n for n in ("Vazirmatn", "Vazir", "Noto Sans Arabic", "Noto Naskh Arabic", "DejaVu Sans") if n in available), None)
if FONT:
    plt.rcParams["font.family"] = FONT


def native_shaping():
    # Matplotlib 3.11 lays out Persian itself (joined, right to left); older
    # versions draw isolated letters left to right and need it done for them.
    # Three joined letters are narrower than the same three apart.
    try:
        import arabic_reshaper
        from matplotlib.ft2font import FT2Font

        f = FT2Font(font_manager.findfont(FONT or "DejaVu Sans"))
        f.set_size(24, 72)
        f.set_text("ببب")
        raw = f.get_width_height()[0]
        f.set_text(arabic_reshaper.reshape("ببب"))
        joined = f.get_width_height()[0]
        return abs(raw - joined) <= max(2, joined * 0.05)
    except Exception:
        from matplotlib import ft2font

        return hasattr(ft2font, "LayoutItem")


NATIVE = native_shaping()


def shape(text):
    text = str(text)
    if NATIVE or not RTL.search(text):
        return text
    try:
        import arabic_reshaper
        from bidi.algorithm import get_display

        return get_display(arabic_reshaper.reshape(text))
    except Exception:
        return text


labels_raw = [str(x) for x in d["labels"]]
series = d.get("series") or [{"name": "", "values": d["values"]}]
for s in series:
    s["values"] = [float(v) for v in s["values"]]
kind = d.get("type", "bar")
unit = (d.get("unit") or "").strip()
texts = labels_raw + [d.get("title") or "", d.get("x_label") or "", d.get("y_label") or ""] + [s.get("name") or "" for s in series]
persian = any(RTL.search(t) for t in texts)

FA_DIGITS = str.maketrans("0123456789.,-", "۰۱۲۳۴۵۶۷۸۹٫٬−")


def digits(s):
    return s.translate(FA_DIGITS) if persian else s


def full(v):
    s = f"{v:,.0f}" if abs(v) >= 100 or float(v).is_integer() else f"{v:,.2f}".rstrip("0").rstrip(".")
    return digits(s)


def compact(v):
    a = abs(v)
    for size, fa, en in ((1e12, "هزار میلیارد", "T"), (1e9, "میلیارد", "B"), (1e6, "میلیون", "M"), (1e3, "هزار", "K")):
        if a >= size:
            n = f"{v / size:.1f}".rstrip("0").rstrip(".")
            return digits(n) + (" " + fa if persian else en)
    return full(v)


def with_unit(s):
    if unit in ("%", "٪"):
        return s + ("٪" if persian else "%")
    return f"{s} {unit}" if unit else s


def percent(p):
    s = f"{p:.1f}".rstrip("0").rstrip(".")
    return digits(s) + ("٪" if persian else "%")


def value_label(v):
    return shape(with_unit(compact(v) if abs(v) >= 1e6 else full(v)))


light = d.get("theme") == "light"
if light:
    plt.style.use("default")
    fg, grid, bg = "#1f2937", "#d1d5db", "#ffffff"
else:
    plt.style.use("dark_background")
    fg, grid, bg = "#e5e7eb", "#4b5563", "#0b0f19"
palette = ["#10b981", "#3b82f6", "#f59e0b", "#ec4899", "#8b5cf6", "#06b6d4", "#f97316", "#84cc16", "#ef4444", "#14b8a6"]
labels = [shape(x) for x in labels_raw]
n = len(labels)
multi = len(series) > 1

fig, ax = plt.subplots(figsize=(10, 5.8))
fig.patch.set_facecolor(bg)
ax.set_facecolor(bg)
for side in ("top", "right"):
    ax.spines[side].set_visible(False)
for side in ("left", "bottom"):
    ax.spines[side].set_color(grid)
ax.tick_params(colors=fg)
axis_fmt = ticker.FuncFormatter(lambda v, _: shape(compact(v)))

if kind in ("pie", "donut"):
    vals = series[0]["values"]
    colors = [palette[i % len(palette)] for i in range(n)]
    total = sum(vals) or 1
    wedges, _, autotexts = ax.pie(
        vals,
        labels=None,
        colors=colors,
        startangle=90,
        counterclock=False,
        autopct=lambda p: percent(p) if p >= 3 else "",
        pctdistance=0.78 if kind == "donut" else 0.62,
        wedgeprops={"width": 0.42, "edgecolor": bg} if kind == "donut" else {"edgecolor": bg},
        textprops={"color": "white", "fontsize": 10},
    )
    ax.axis("equal")
    legend = [shape(f"{labels_raw[i]} — {with_unit(full(v))}") for i, v in enumerate(vals)]
    ax.legend(wedges, legend, loc="center left", bbox_to_anchor=(1.0, 0.5), frameon=False, labelcolor=fg)
    if kind == "donut":
        ax.text(0, 0, shape(with_unit(compact(total))), ha="center", va="center", fontsize=15, color=fg, fontweight="bold")
elif kind == "scatter":
    for i, s in enumerate(series):
        ax.scatter(range(n), s["values"], s=60, color=palette[i % len(palette)], label=shape(s.get("name") or ""), alpha=0.9)
    ax.set_xticks(range(n), labels)
    ax.yaxis.set_major_formatter(axis_fmt)
    ax.grid(True, linestyle="--", alpha=0.35, color=grid)
elif kind in ("line", "area"):
    for i, s in enumerate(series):
        c = palette[i % len(palette)]
        ax.plot(range(n), s["values"], marker="o" if n <= 40 else None, linewidth=2.5, color=c, label=shape(s.get("name") or ""))
        if kind == "area":
            ax.fill_between(range(n), s["values"], alpha=0.18 if multi else 0.3, color=c)
        if not multi and n <= 14:
            for x, v in enumerate(s["values"]):
                ax.annotate(value_label(v), (x, v), textcoords="offset points", xytext=(0, 9), ha="center", color=fg, fontsize=9)
    ax.set_xticks(range(n), labels)
    ax.yaxis.set_major_formatter(axis_fmt)
    ax.grid(axis="y", linestyle="--", alpha=0.35, color=grid)
    ax.margins(x=0.03, y=0.12)
    if kind == "area" and min(min(s["values"]) for s in series) >= 0:
        ax.set_ylim(bottom=0)
else:  # bar, hbar, stacked_bar
    horizontal = kind == "hbar"
    stacked = kind == "stacked_bar"
    k = len(series)
    width = 0.8 if (stacked or k == 1) else 0.8 / k
    bottom = [0.0] * n
    for i, s in enumerate(series):
        if stacked or k == 1:
            pos = list(range(n))
        else:
            pos = [x - 0.4 + width * (i + 0.5) for x in range(n)]
        colors = palette[i % len(palette)] if multi else [palette[j % len(palette)] for j in range(n)]
        draw = ax.barh if horizontal else ax.bar
        extra = {"left": bottom} if (stacked and horizontal) else ({"bottom": bottom} if stacked else {})
        bars = draw(pos, s["values"], width, color=colors, label=shape(s.get("name") or ""), **extra)
        if (not stacked and k <= 3 and n * k <= 30) or (stacked and i == k - 1):
            shown = [value_label(b + v) for b, v in zip(bottom, s["values"])] if stacked else [value_label(v) for v in s["values"]]
            ax.bar_label(bars, labels=shown, padding=3, color=fg, fontsize=9 if k > 1 else 10)
        if stacked:
            bottom = [b + v for b, v in zip(bottom, s["values"])]
    if horizontal:
        ax.set_yticks(range(n), labels)
        ax.invert_yaxis()
        ax.xaxis.set_major_formatter(axis_fmt)
        ax.grid(axis="x", linestyle="--", alpha=0.35, color=grid)
        ax.margins(x=0.12)
    else:
        ax.set_xticks(range(n), labels)
        ax.yaxis.set_major_formatter(axis_fmt)
        ax.grid(axis="y", linestyle="--", alpha=0.35, color=grid)
        ax.margins(y=0.1)
    ax.set_axisbelow(True)

if kind not in ("pie", "donut", "hbar"):
    longest = max((len(x) for x in labels_raw), default=0)
    if n > 6 or longest * n > 70:
        plt.setp(ax.get_xticklabels(), rotation=35, ha="right" if not persian else "right")
if d.get("x_label"):
    ax.set_xlabel(shape(d["x_label"]), color=fg, labelpad=8)
if d.get("y_label"):
    ax.set_ylabel(shape(d["y_label"]), color=fg, labelpad=8)
if multi and kind not in ("pie", "donut"):
    ax.legend(frameon=False, labelcolor=fg, loc="best")
if d.get("title"):
    ax.set_title(shape(d["title"]), fontsize=15, color=fg, pad=16, fontweight="bold")
fig.tight_layout()
fig.savefig(d["out"], dpi=150, facecolor=bg)
print("OK")
