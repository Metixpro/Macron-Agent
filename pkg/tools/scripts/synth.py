import json
import math
import os
import re
import subprocess
import sys
import wave

import numpy as np

d = json.load(sys.stdin)
SR = 44100
seed = int(d.get("seed") or 7)
rng = np.random.default_rng(seed)

SCALES = {
    "major": [0, 200, 400, 500, 700, 900, 1100], "minor": [0, 200, 300, 500, 700, 800, 1000],
    "dorian": [0, 200, 300, 500, 700, 900, 1000], "phrygian": [0, 100, 300, 500, 700, 800, 1000],
    "lydian": [0, 200, 400, 600, 700, 900, 1100], "mixolydian": [0, 200, 400, 500, 700, 900, 1000],
    "harmonic_minor": [0, 200, 300, 500, 700, 800, 1100], "pentatonic_major": [0, 200, 400, 700, 900],
    "pentatonic_minor": [0, 300, 500, 700, 1000], "blues": [0, 300, 500, 600, 700, 1000], "hijaz": [0, 100, 400, 500, 700, 800, 1000],
    "mahur": [0, 200, 400, 500, 700, 900, 1100], "shur": [0, 150, 300, 500, 700, 800, 1000],
    "homayoun": [0, 150, 400, 500, 700, 800, 1000], "chahargah": [0, 150, 400, 500, 700, 850, 1100],
    "segah": [0, 150, 350, 500, 650, 850, 1000], "isfahan": [0, 200, 300, 500, 700, 850, 1100],
}

STYLES = {
    "lofi": dict(bpm=80, scale="major", prog=[1, 4, 0, 5], sevenths=True, drums="boom_bap", bass="lofi", lead="epiano", pad="warm",
                 arp=None, swing=0.12, reverb=0.25, lowpass=5500, crackle=True, wobble=True),
    "ambient": dict(bpm=70, scale="lydian", prog=[0, 1, 0, 4], sevenths=True, drums="none", bass="drone", lead="bell", pad="air",
                    arp=None, swing=0, reverb=0.55, lowpass=9000, sparse=True),
    "edm": dict(bpm=124, scale="minor", prog=[0, 5, 2, 6], sevenths=False, drums="four_on_floor", bass="offbeat", lead="supersaw",
                pad="supersaw", arp="up", swing=0, reverb=0.25, sidechain=True),
    "cinematic": dict(bpm=76, scale="harmonic_minor", prog=[0, 5, 3, 4], sevenths=False, drums="epic", bass="long", lead="strings",
                      pad="strings", arp=None, swing=0, reverb=0.5),
    "chiptune": dict(bpm=140, scale="major", prog=[0, 5, 3, 4], sevenths=False, drums="chip", bass="chip", lead="square", pad=None,
                     arp="updown", swing=0, reverb=0.0),
    "synthwave": dict(bpm=100, scale="minor", prog=[0, 5, 2, 6], sevenths=False, drums="synthwave", bass="eighths", lead="saw",
                      pad="warm", arp="up", swing=0, reverb=0.35),
    "trap": dict(bpm=140, scale="minor", prog=[0, 0, 5, 6], sevenths=False, drums="trap", bass="808", lead="bell", pad="dark",
                 arp=None, swing=0, reverb=0.2),
    "pop": dict(bpm=110, scale="major", prog=[0, 4, 5, 3], sevenths=False, drums="pop", bass="pop", lead="pluck", pad="warm",
                arp=None, swing=0, reverb=0.25),
    "piano": dict(bpm=72, scale="major", prog=[0, 5, 3, 4], sevenths=False, drums="none", bass="piano", lead="piano", pad="piano",
                  arp=None, swing=0, reverb=0.35),
    "persian": dict(bpm=96, scale="shur", prog=[0], sevenths=False, drums="daf", bass="drone", lead="santur", pad=None, arp=None,
                    swing=0, reverb=0.3, meter=6),
    "none": dict(bpm=100, scale="major", prog=[0], drums="none", bass=None, lead=None, pad=None, arp=None, swing=0, reverb=0.2),
}

style_name = (d.get("style") or "lofi").lower()
S = dict(STYLES.get(style_name, STYLES["lofi"]))
for k in ("drums", "bass", "lead", "pad", "arp"):
    if d.get(k) is not None:
        S[k] = None if d[k] in ("none", "", False) else d[k]
BPM = float(min(max(float(d.get("bpm") or S["bpm"]), 40), 220))
BEAT = 60.0 / BPM
METER = int(d.get("meter") or S.get("meter", 4))
STEPS = 12 if METER == 6 else 4 * METER
STEP = BEAT / 4 if METER != 6 else BEAT / 2
BAR = STEPS * STEP
DUR = float(min(max(float(d.get("duration") or 30), 2), 180))
N = int(DUR * SR) + SR * 4
scale_name = (d.get("scale") or S["scale"]).lower()
SCALE = SCALES.get(scale_name, SCALES["minor"])
NAMES = {"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11}
energy = {"low": 0.6, "medium": 1.0, "high": 1.4}.get(str(d.get("energy") or "medium").lower(), 1.0)


def pitch_class(name):
    m = re.match(r"^([A-Ga-g])([#b]?)", name.strip())
    if not m:
        return 9
    return (NAMES[m.group(1).upper()] + {"#": 1, "b": -1, "": 0}[m.group(2)]) % 12


KEY = pitch_class(d.get("key") or ("E" if style_name == "persian" else "A" if "minor" in scale_name or scale_name in ("dorian", "phrygian") else "C"))


def hz(midi):
    return 440.0 * 2 ** ((midi - 69) / 12.0)


def degree(deg, octave):
    n = len(SCALE)
    return 12 * (octave + 1) + KEY + 12 * (deg // n) + SCALE[deg % n] / 100.0


def note_midi(tok):
    m = re.match(r"^([A-Ga-g])(#|b|k|s)?(-?\d)$", tok.strip())
    if not m:
        return None
    off = {"#": 1, "b": -1, "k": -0.5, "s": 0.5, None: 0}[m.group(2)]
    return 12 * (int(m.group(3)) + 1) + NAMES[m.group(1).upper()] + off


QUALITY = {"": [0, 4, 7], "m": [0, 3, 7], "min": [0, 3, 7], "maj7": [0, 4, 7, 11], "m7": [0, 3, 7, 10], "7": [0, 4, 7, 10],
           "dim": [0, 3, 6], "aug": [0, 4, 8], "sus2": [0, 2, 7], "sus4": [0, 5, 7], "add9": [0, 4, 7, 14], "m9": [0, 3, 7, 10, 14],
           "maj9": [0, 4, 7, 11, 14], "5": [0, 7], "6": [0, 4, 7, 9], "m6": [0, 3, 7, 9]}
ROMAN = {"i": 0, "ii": 1, "iii": 2, "iv": 3, "v": 4, "vi": 5, "vii": 6}


def parse_chords(text):
    out = []
    for tok in re.split(r"[\s,|\-]+", text.strip()):
        if not tok:
            continue
        tok, _, beats = tok.partition(":")
        bars = float(beats) / METER if beats else 1.0
        low = tok.lower().rstrip("°o+7")
        if low in ROMAN or tok.isdigit():
            deg = ROMAN[low] if low in ROMAN else int(tok) - 1
            notes = [degree(deg + k, 3) for k in (0, 2, 4)] + ([degree(deg + 6, 3)] if tok.endswith("7") else [])
            out.append((tok, notes, bars))
            continue
        m = re.match(r"^([A-G][#b]?)(.*)$", tok)
        if not m or m.group(2) not in QUALITY:
            continue
        root = 48 + pitch_class(m.group(1))
        out.append((tok, [root + i for i in QUALITY[m.group(2)]], bars))
    return out


if d.get("chords"):
    CHORDS = parse_chords(str(d["chords"]))
else:
    CHORDS = []
if not CHORDS:
    for deg in S["prog"]:
        notes = [degree(deg + k, 3) for k in (0, 2, 4)] + ([degree(deg + 6, 3)] if S.get("sevenths") else [])
        CHORDS.append(("I II III IV V VI VII".split()[deg % 7] if len(SCALE) == 7 else str(deg + 1), notes, 1.0))


def chord_name(notes):
    root = round(notes[0]) % 12
    third = (notes[1] - notes[0]) if len(notes) > 1 else 4
    nm = ["C", "C#", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"][root]
    return nm + ("m" if 2.75 <= third <= 3.25 else "" if 3.75 <= third <= 4.25 else "~")


bars_total = int(math.ceil(DUR / BAR))
spans = []
t, i = 0.0, 0
while t < bars_total * BAR:
    _, notes, bars = CHORDS[i % len(CHORDS)]
    length = max(bars, 0.25) * BAR
    if spans and spans[-1][2] == notes:
        spans[-1] = (spans[-1][0], spans[-1][1] + length, notes)
    else:
        spans.append((t, length, notes))
    t += length
    i += 1


def chord_at(at):
    for start, length, notes in spans:
        if start <= at + 1e-6 < start + length:
            return notes
    return spans[-1][2]


timeline = [(b * BAR, BAR, chord_at(b * BAR)) for b in range(bars_total)]


# ----------------------------------------------------------------- sounds

def tvec(n):
    return np.arange(n, dtype=np.float64) / SR


def env(n, a=0.005, dcy=0.1, s=0.7, r=0.1, hold=None):
    hold = n / SR if hold is None else hold
    t = tvec(n)
    e = np.where(t < a, t / max(a, 1e-6), s + (1 - s) * np.exp(-(t - a) / max(dcy, 1e-6)))
    rel = np.clip(1 - (t - hold) / max(r, 1e-6), 0, 1)
    return (e * np.where(t > hold, rel, 1)).astype(np.float64)


def blep(phase, dt):
    out = np.zeros_like(phase)
    lo = phase < dt
    x = phase[lo] / dt
    out[lo] = x + x - x * x - 1
    hi = phase > 1 - dt
    x = (phase[hi] - 1) / dt
    out[hi] = x * x + x + x + 1
    return out


def saw(f, n, detune=0.0, ph=0.0):
    freq = f * 2 ** (detune / 1200)
    phase = (ph + freq * tvec(n)) % 1.0
    return 2 * phase - 1 - blep(phase, freq / SR)


def square(f, n, width=0.5):
    phase = tvec(n) * f % 1.0
    dt = f / SR
    a = 2 * phase - 1 - blep(phase, dt)
    p2 = (phase + 1 - width) % 1.0
    b = 2 * p2 - 1 - blep(p2, dt)
    return (a - b) * 0.5


def sine(f, n, ph=0.0):
    return np.sin(2 * np.pi * f * tvec(n) + ph)


def triangle(f, n):
    phase = tvec(n) * f % 1.0
    return 4 * np.abs(phase - 0.5) - 1


def onepole_fft(x, cutoff, kind="low", order=2):
    X = np.fft.rfft(x)
    f = np.fft.rfftfreq(len(x), 1 / SR)
    if kind == "low":
        g = 1 / np.sqrt(1 + (f / cutoff) ** (2 * order))
    elif kind == "high":
        g = 1 / np.sqrt(1 + (cutoff / np.maximum(f, 1e-3)) ** (2 * order))
    else:
        lo, hi = cutoff
        g = 1 / np.sqrt(1 + (f / hi) ** (2 * order)) / np.sqrt(1 + (lo / np.maximum(f, 1e-3)) ** (2 * order))
    return np.fft.irfft(X * g, len(x))


def pluck(f, dur, bright=0.6, decay_s=1.6):
    n = int(dur * SR)
    p = max(2, int(round(SR / f)))
    real = SR / p
    buf = rng.uniform(-1, 1, p)
    buf = buf * bright + np.convolve(buf, [0.5, 0.5], "same") * (1 - bright)
    k = 10 ** (-3 / (decay_s * real))
    blocks = n * real / f / p + 2
    out = np.empty(int(blocks) * p + p)
    for b in range(int(blocks) + 1):
        out[b * p:(b + 1) * p] = buf
        buf = k * 0.5 * (buf + np.roll(buf, -1))
    idx = np.arange(n) * (f / real)
    return np.interp(idx, np.arange(len(out)), out)


def piano(f, dur, vel=1.0):
    n = int((dur + 1.2) * SR)
    t = tvec(n)
    y = np.zeros(n)
    for k in range(1, 9):
        fk = k * f * math.sqrt(1 + 0.0004 * k * k)
        if fk > 15000:
            break
        y += (1 / k ** 1.2) * np.sin(2 * np.pi * fk * t + rng.uniform(0, 6.28)) * np.exp(-t * (0.9 + 0.7 * k) * (f / 262) ** 0.35)
    ham = onepole_fft(rng.normal(0, 1, min(n, int(0.01 * SR))), 3000) * np.linspace(1, 0, min(n, int(0.01 * SR)))
    y[: len(ham)] += 0.15 * ham
    return y * env(n, 0.002, 10, 1, 0.25, hold=dur) * vel


def epiano(f, dur, vel=1.0):
    n = int((dur + 0.6) * SR)
    t = tvec(n)
    idx = 1.8 * np.exp(-t * 3) + 0.2
    y = np.sin(2 * np.pi * f * t + idx * np.sin(2 * np.pi * f * t)) * np.exp(-t * 1.1)
    y += 0.3 * np.sin(2 * np.pi * 2 * f * t) * np.exp(-t * 3)
    return y * env(n, 0.004, 10, 1, 0.3, hold=dur) * vel


def bell(f, dur, vel=1.0):
    n = int((dur + 2.0) * SR)
    t = tvec(n)
    idx = 3.0 * np.exp(-t * 2.5)
    y = np.sin(2 * np.pi * f * t + idx * np.sin(2 * np.pi * f * 3.5 * t)) * np.exp(-t * 1.5)
    return y * env(n, 0.002, 10, 1, 0.5, hold=dur + 1.2) * vel


def flute(f, dur, vel=1.0):
    n = int((dur + 0.3) * SR)
    t = tvec(n)
    vib = 1 + 0.006 * np.sin(2 * np.pi * 5.2 * t) * np.clip(t / 0.4, 0, 1)
    ph = 2 * np.pi * np.cumsum(f * vib) / SR
    y = np.sin(ph) + 0.25 * np.sin(2 * ph) + 0.08 * np.sin(3 * ph)
    breath = onepole_fft(rng.normal(0, 1, n), (f * 1.5, f * 4), "band") * 0.12
    return (y + breath) * env(n, 0.08, 0.3, 0.85, 0.2, hold=dur) * vel


def synth_voice(kind, f, dur, vel=1.0):
    if kind == "piano":
        return piano(f, dur, vel)
    if kind == "epiano":
        return epiano(f, dur, vel)
    if kind == "bell":
        return bell(f, dur, vel)
    if kind in ("flute", "ney"):
        return flute(f, dur, vel)
    if kind in ("pluck", "santur"):
        y = pluck(f, dur + (1.5 if kind == "santur" else 0.6), 0.85 if kind == "santur" else 0.6, 2.2 if kind == "santur" else 1.2)
        if kind == "santur":
            y = 0.6 * y + 0.4 * pluck(f * 1.003, len(y) / SR, 0.85, 2.2)
        return y * vel
    n = int((dur + 0.4) * SR)
    if kind == "square":
        y = square(f, n, 0.25) * 0.6
        return y * env(n, 0.002, 0.08, 0.6, 0.05, hold=dur) * vel
    if kind == "sine":
        return sine(f, n) * env(n, 0.01, 0.2, 0.8, 0.2, hold=dur) * vel
    if kind == "triangle":
        return triangle(f, n) * env(n, 0.005, 0.1, 0.8, 0.08, hold=dur) * vel
    if kind == "strings":
        y = sum(saw(f, n, dt, rng.uniform()) for dt in (-7, 0, 7)) / 3
        return y * env(n, 0.25, 0.5, 0.85, 0.5, hold=dur) * vel
    if kind == "supersaw":
        y = sum(saw(f, n, dt, rng.uniform()) for dt in (-14, -6, 0, 6, 14)) / 4
        return y * env(n, 0.01, 0.3, 0.7, 0.15, hold=dur) * vel
    y = (saw(f, n, -5) + saw(f, n, 5)) / 2
    return y * env(n, 0.01, 0.2, 0.7, 0.15, hold=dur) * vel


def pad_voice(kind, f, dur):
    n = int((dur + 1.2) * SR)
    if kind == "piano":
        return piano(f, dur, 0.55)
    if kind == "air":
        y = sine(f, n) + 0.3 * sine(f * 2.003, n) + 0.15 * triangle(f * 0.5, n)
        return y * env(n, 1.2, 1, 1, 1.2, hold=dur)
    if kind == "strings":
        y = sum(saw(f, n, dt, rng.uniform()) for dt in (-10, -3, 4, 11)) / 4
        return y * env(n, 0.6, 1, 1, 1.0, hold=dur)
    if kind == "supersaw":
        y = sum(saw(f, n, dt, rng.uniform()) for dt in (-18, -7, 0, 7, 18)) / 5
        return y * env(n, 0.05, 0.5, 0.8, 0.3, hold=dur)
    if kind == "dark":
        y = (saw(f, n, -6) + triangle(f, n)) / 2
        return y * env(n, 0.8, 1, 1, 1.0, hold=dur)
    y = (saw(f, n, -8, rng.uniform()) + saw(f, n, 8, rng.uniform()) + triangle(f, n)) / 3
    return y * env(n, 0.35, 1, 1, 0.8, hold=dur)


def kick(vel=1.0, punch=1.0):
    n = int(0.5 * SR)
    t = tvec(n)
    f = 45 + 110 * np.exp(-t * 28) * punch
    y = np.sin(2 * np.pi * np.cumsum(f) / SR) * np.exp(-t * 7)
    y[: int(0.004 * SR)] += rng.normal(0, 0.3, int(0.004 * SR))
    return np.tanh(y * 1.6) * vel


def snare(vel=1.0):
    n = int(0.3 * SR)
    t = tvec(n)
    body = np.sin(2 * np.pi * 185 * t) * np.exp(-t * 25)
    noise = onepole_fft(rng.normal(0, 1, n), (900, 9000), "band") * np.exp(-t * 16)
    return (0.5 * body + 0.8 * noise) * vel


def clap(vel=1.0):
    n = int(0.35 * SR)
    t = tvec(n)
    e = np.zeros(n)
    for off in (0, 0.011, 0.022):
        i = int(off * SR)
        e[i:] += np.exp(-(t[: n - i]) * 60)
    e += 0.5 * np.exp(-t * 12)
    return onepole_fft(rng.normal(0, 1, n), (900, 6000), "band") * e * 0.9 * vel


def hat(vel=1.0, open_=False):
    n = int((0.35 if open_ else 0.06) * SR)
    t = tvec(n)
    y = onepole_fft(rng.normal(0, 1, n), 7000, "high") * np.exp(-t * (8 if open_ else 70))
    return y * 0.45 * vel


def tom(f=110, vel=1.0):
    n = int(0.8 * SR)
    t = tvec(n)
    fr = f * (0.6 + 0.4 * np.exp(-t * 10))
    return np.sin(2 * np.pi * np.cumsum(fr) / SR) * np.exp(-t * 4) * vel


def boom(vel=1.0):
    n = int(2.5 * SR)
    t = tvec(n)
    fr = 38 + 60 * np.exp(-t * 6)
    y = np.sin(2 * np.pi * np.cumsum(fr) / SR) * np.exp(-t * 1.6)
    y += onepole_fft(rng.normal(0, 1, n), 500) * np.exp(-t * 9) * 0.6
    return np.tanh(y * 1.4) * vel


def daf(low=True, vel=1.0):
    n = int(0.45 * SR)
    t = tvec(n)
    if low:
        y = np.sin(2 * np.pi * 72 * t) * np.exp(-t * 9) + 0.3 * onepole_fft(rng.normal(0, 1, n), 400) * np.exp(-t * 30)
    else:
        y = 0.6 * np.sin(2 * np.pi * 260 * t) * np.exp(-t * 30) + onepole_fft(rng.normal(0, 1, n), (3000, 12000), "band") * np.exp(-t * 9) * 0.7
    return y * vel


def chip_noise(vel=1.0, length=0.08):
    n = int(length * SR)
    steps = rng.choice([-1.0, 1.0], n // 40 + 1)
    return np.repeat(steps, 40)[:n] * np.exp(-tvec(n) * 30) * 0.4 * vel


# ------------------------------------------------------------ arrangement

buses = {k: np.zeros(N) for k in ("drums", "bass", "pad", "lead", "arp", "sfx")}
kicks = []


def place(bus, y, at, gain=1.0):
    i = int(at * SR)
    if i >= N or i < 0:
        return
    j = min(N, i + len(y))
    buses[bus][i:j] += y[: j - i] * gain


def swing(step):
    return STEP * step + (S.get("swing", 0) * STEP if step % 2 == 1 else 0)


PATTERNS = {
    "four_on_floor": {"k": [0, 4, 8, 12], "c": [4, 12], "h": [2, 6, 10, 14], "o": [14]},
    "boom_bap": {"k": [0, 7, 10], "s": [4, 12], "h": [0, 2, 4, 6, 8, 10, 12, 14]},
    "pop": {"k": [0, 8, 10], "s": [4, 12], "h": [0, 2, 4, 6, 8, 10, 12, 14]},
    "synthwave": {"k": [0, 8], "s": [4, 12], "h": [0, 2, 4, 6, 8, 10, 12, 14]},
    "trap": {"k": [0, 7, 11], "c": [8], "h": list(range(16)), "roll": True},
    "breakbeat": {"k": [0, 10], "s": [4, 12], "g": [7, 15], "h": [0, 2, 4, 6, 8, 10, 12, 14]},
    "half_time": {"k": [0, 11], "s": [8], "h": [0, 4, 8, 12]},
    "chip": {"k": [0, 8], "n": [4, 12], "x": [2, 6, 10, 14]},
    "epic": {"b": [0], "t": [6, 10, 12, 14]},
    "daf": {"dl": [0, 6], "dh": [3, 4, 9, 10]},
}


def drums(bar_t, bar_i, bars):
    pat = PATTERNS.get(S.get("drums") or "", None)
    if not pat:
        return
    intro = bar_i == 0 and bars > 3
    last = bar_i == bars - 1
    for st in pat.get("k", []):
        if intro and st != 0:
            continue
        kicks.append(bar_t + swing(st))
        place("drums", kick(0.95, 1.2 if S["drums"] == "four_on_floor" else 1.0), bar_t + swing(st))
    if intro:
        return
    for st in pat.get("s", []):
        place("drums", snare(0.8), bar_t + swing(st))
    for st in pat.get("c", []):
        place("drums", clap(0.85), bar_t + swing(st))
    for st in pat.get("g", []):
        place("drums", snare(0.25), bar_t + swing(st))
    for st in pat.get("h", []):
        if energy < 0.8 and st % 4 == 2:
            continue
        place("drums", hat(0.55 + 0.25 * rng.random()), bar_t + swing(st))
        if pat.get("roll") and st in (12, 14) and (bar_i % 2 == 1 or last):
            for r in range(1, 3):
                place("drums", hat(0.35), bar_t + swing(st) + r * STEP / 3)
    for st in pat.get("o", []):
        place("drums", hat(0.4, True), bar_t + swing(st))
    for st in pat.get("n", []):
        place("drums", chip_noise(0.9, 0.12), bar_t + st * STEP)
    for st in pat.get("x", []):
        place("drums", chip_noise(0.4, 0.03), bar_t + st * STEP)
    for st in pat.get("b", []):
        if bar_i % 2 == 0:
            place("drums", boom(0.9), bar_t + st * STEP)
    for st in pat.get("t", []):
        if bar_i % 2 == 1:
            place("drums", tom(95 + 15 * (st % 3), 0.7), bar_t + st * STEP)
    for st in pat.get("dl", []):
        place("drums", daf(True, 0.95), bar_t + st * STEP)
    for st in pat.get("dh", []):
        place("drums", daf(False, 0.6 + 0.2 * rng.random()), bar_t + st * STEP)


def low_root(notes):
    root = notes[0] - 12
    while root > 45:
        root -= 12
    return root


def bass(bar_t, length):
    kind = S.get("bass")
    if not kind or kind == "drone":
        return
    hits = {"lofi": [(0, 6), (8, 4), (12, 3)], "offbeat": [(2, 2), (6, 2), (10, 2), (14, 2)], "eighths": [(s, 2) for s in range(0, 16, 2)],
            "pop": [(0, 4), (6, 2), (8, 4), (12, 4)], "chip": [(s, 2) for s in range(0, 16, 2)], "long": [(0, 16)],
            "piano": [(0, 8), (8, 8)], "808": [(0, 7), (7, 4), (11, 5)]}.get(kind, [(0, 8), (8, 8)])
    for i, (st, ln) in enumerate(hits):
        at = bar_t + swing(st)
        if at >= bar_t + length:
            break
        root = low_root(chord_at(at))
        f, fifth = hz(root), hz(root + 7)
        dur = ln * STEP
        n = int((dur + 0.15) * SR)
        if kind == "808":
            t = tvec(int((dur + 0.4) * SR))
            fr = f * (1 + 0.6 * np.exp(-t * 30))
            y = np.tanh(2.2 * np.sin(2 * np.pi * np.cumsum(fr) / SR)) * np.exp(-t * 1.4) * env(len(t), 0.003, 10, 1, 0.15, hold=dur)
        elif kind == "chip":
            y = triangle(fifth if i % 2 else f, n) * env(n, 0.002, 0.05, 0.9, 0.02, hold=dur)
        elif kind == "piano":
            y = piano(f * 2, dur, 0.6)
        elif kind == "long":
            y = (saw(f, n, -4) + sine(f, n)) * env(n, 0.3, 1, 1, 0.4, hold=dur) * 0.6
        else:
            y = (sine(f, n) + 0.35 * saw(f, n)) * env(n, 0.005, 0.15, 0.8, 0.06, hold=dur)
        place("bass", y * 0.7, at)


def sustained(start, length, notes):
    if S.get("bass") == "drone":
        root = low_root(notes)
        n = int((length + 0.8) * SR)
        y = (sine(hz(root), n) + 0.5 * sine(hz(root + 7), n) + 0.25 * sine(hz(root + 12), n)) * env(n, 0.8, 1, 1, 0.8, hold=length)
        place("bass", y * 0.28, start)
    kind = S.get("pad")
    if not kind:
        return
    voices = [hz(m) for m in notes]
    if kind == "piano":
        for k in range(0, max(1, int(length / BEAT + 0.5)), 2):
            for f in voices:
                place("pad", piano(f, BEAT * 2, 0.35), start + k * BEAT + rng.uniform(0, 0.012))
        return
    for f in voices:
        place("pad", pad_voice(kind, f, length) * 0.22, start)


def arp(bar_t, length):
    kind = S.get("arp")
    if not kind:
        return
    voice = "square" if style_name == "chiptune" else "pluck"
    for st in range(int(length / STEP + 0.5)):
        at = bar_t + st * STEP
        seq = [m + 12 for m in chord_at(at)]
        seq = seq + [seq[0] + 12]
        if kind == "updown":
            seq = seq + seq[-2:0:-1]
        place("arp", synth_voice(voice, hz(seq[st % len(seq)]), STEP * 0.9, 0.35), at)


RHYTHMS = [[(0, 4), (4, 2), (6, 2), (8, 6), (14, 2)], [(0, 2), (2, 2), (4, 4), (10, 2), (12, 4)], [(0, 6), (6, 2), (8, 8)],
           [(0, 3), (3, 3), (6, 2), (8, 4), (12, 4)], [(2, 2), (4, 2), (6, 4), (10, 6)], [(0, 8), (8, 4), (12, 4)]]
RHYTHMS6 = [[(0, 3), (3, 3), (6, 6)], [(0, 2), (2, 1), (3, 3), (6, 3), (9, 3)], [(0, 6), (6, 2), (8, 1), (9, 3)]]


def chord_degrees(notes):
    out = []
    for m in notes:
        cents = ((m - KEY) % 12) * 100
        out.append(min(range(len(SCALE)), key=lambda g: min(abs(SCALE[g] - cents), 1200 - abs(SCALE[g] - cents))))
    return out


def make_motif(rhythm, notes, start):
    tones = chord_degrees(notes)
    motif, cur = [], start
    for k, (st, ln) in enumerate(rhythm):
        if k == 0 or st % (STEPS // 2) == 0:
            cands = [c + len(SCALE) * o for c in tones for o in (0, 1, 2)]
            cur = min(cands, key=lambda c: abs(c - cur) + rng.random() * 0.8)
        else:
            cur += int(rng.choice([-2, -1, -1, 1, 1, 2, 0], p=[0.1, 0.25, 0.1, 0.25, 0.1, 0.1, 0.1]))
        cur = int(min(max(cur, len(SCALE)), 3 * len(SCALE)))
        motif.append((st, ln, cur))
    return motif


def lead_bars():
    kind = S.get("lead")
    if not kind:
        return
    if d.get("melody"):
        events, beat = [], 0.0
        for tok in str(d["melody"]).split():
            nm, _, ln = tok.partition(":")
            ln = float(ln or 1)
            m = note_midi(nm)
            if m is not None:
                events.append((beat, ln, m))
            beat += ln
        if not events or beat <= 0:
            return
        loops = int(math.ceil(DUR / (beat * BEAT)))
        start = 2 * BAR if bars_total > 6 and S.get("drums") not in (None, "none") else 0
        for lp in range(loops):
            for b, ln, m in events:
                at = start + (lp * beat + b) * BEAT
                if at < DUR:
                    place("lead", synth_voice(kind, hz(m), ln * BEAT * 0.95, 0.5), at)
        return
    rhythms = RHYTHMS6 if METER == 6 else RHYTHMS
    pick = lambda: rhythms[int(rng.integers(len(rhythms)))]
    a, b = pick(), pick()
    sparse = S.get("sparse") or energy < 0.8
    start_deg = len(SCALE) + 2
    motif_a = motif_b = None
    for bi, (bar_t, length, notes) in enumerate(timeline):
        if bi == 0 and bars_total > 3:
            continue
        section = (bi // 2) % 4
        if sparse and bi % 2 == 1:
            continue
        rhythm = a if section in (0, 1, 3) else b
        if section in (0, 1) and motif_a is not None and bi % 2 == 0:
            motif = [(st, ln, deg + (1 if k == len(motif_a) - 1 and section == 1 else 0)) for k, (st, ln, deg) in enumerate(motif_a)]
        elif section == 2 and motif_b is not None:
            motif = motif_b
        else:
            motif = make_motif(rhythm, notes, start_deg)
        if section == 0 and motif_a is None:
            motif_a = motif
        if section == 2 and motif_b is None:
            motif_b = motif
        for st, ln, deg in motif:
            at = bar_t + swing(st)
            if at < DUR:
                place("lead", synth_voice(kind, hz(degree(deg, 3)), ln * STEP * 0.92, 0.42 + 0.1 * rng.random()), at)


for bi, (bar_t, length, notes) in enumerate(timeline):
    drums(bar_t, bi, len(timeline))
    bass(bar_t, length)
    arp(bar_t, length)
for start, length, notes in spans:
    sustained(start, length, notes)
lead_bars()


def sfx(kind, at, dur, vol):
    dur = max(0.05, min(dur, 8))
    n = int(dur * SR)
    t = tvec(n)
    if kind in ("whoosh", "swoosh"):
        noise = rng.normal(0, 1, n)
        shape = np.sin(np.pi * np.clip(t / dur, 0, 1)) ** 2
        parts = np.array_split(np.arange(n), 16)
        y = np.zeros(n)
        for k, idx in enumerate(parts):
            c = 400 + 5000 * math.sin(math.pi * (k + 0.5) / len(parts))
            y[idx] = onepole_fft(noise[idx], (c * 0.5, c * 1.6), "band")
        y *= shape * 1.2
    elif kind == "riser":
        fr = 200 * (2 ** (3 * t / dur))
        y = 0.5 * np.sin(2 * np.pi * np.cumsum(fr) / SR) + onepole_fft(rng.normal(0, 1, n), 3000, "high") * 0.35
        y *= (t / dur) ** 2
    elif kind in ("impact", "hit", "boom"):
        n = max(n, int(1.2 * SR))
        y = np.zeros(n)
        b = boom(1.0)[:n]
        y[: len(b)] += b
        if kind == "hit":
            sn = snare(1.0)[:n]
            y[: len(sn)] += sn
    elif kind == "click":
        n = int(0.03 * SR)
        y = np.sin(2 * np.pi * 1800 * tvec(n)) * np.exp(-tvec(n) * 160)
    elif kind == "pop":
        n = int(0.08 * SR)
        fr = 200 + 700 * np.exp(-tvec(n) * 60)
        y = np.sin(2 * np.pi * np.cumsum(fr) / SR) * np.exp(-tvec(n) * 40)
    elif kind == "ding":
        y = bell(hz(84), 0.4, 0.8)
    elif kind == "glitch":
        y = np.zeros(n)
        for _ in range(int(dur * 25)):
            i = int(rng.integers(0, max(1, n - 800)))
            ln = int(rng.integers(150, 800))
            y[i:i + ln] += (square(rng.uniform(80, 2000), ln) if rng.random() < 0.5 else rng.normal(0, 0.6, ln))[: len(y[i:i + ln])]
    elif kind == "typing":
        y = np.zeros(n)
        for k in np.arange(0, dur, 0.07 + 0.03 * rng.random()):
            m = int(0.02 * SR)
            i = int(k * SR)
            y[i:i + m] += (onepole_fft(rng.normal(0, 1, m), 2500, "high") * np.exp(-tvec(m) * 250))[: len(y[i:i + m])]
    else:
        return False
    place("sfx", y * vol, at)
    return True


placed = 0
for fx in (d.get("sfx") or [])[:60]:
    if isinstance(fx, dict) and sfx(str(fx.get("type", "")).lower(), float(fx.get("at", 0)), float(fx.get("duration", 1.0)),
                                     float(min(max(float(fx.get("volume", 0.8)), 0), 1.5))):
        placed += 1

# ---------------------------------------------------------------- mixing

if S.get("sidechain") and kicks:
    duck = np.ones(N)
    t = tvec(int(0.35 * SR))
    curve = 1 - 0.7 * np.exp(-t / 0.09)
    for k in kicks:
        i = int(k * SR)
        j = min(N, i + len(curve))
        if i < N:
            duck[i:j] = np.minimum(duck[i:j], curve[: j - i])
    for b in ("pad", "bass", "arp"):
        buses[b] *= duck

if S.get("pad") in ("warm", "dark", "strings"):
    buses["pad"] = onepole_fft(buses["pad"], 2400 if S["pad"] != "dark" else 1200)
if S.get("lead") in ("saw", "supersaw", "square", "strings"):
    buses["lead"] = onepole_fft(buses["lead"], 5200)
buses["bass"] = onepole_fft(buses["bass"], 900 if S.get("bass") != "chip" else 6000)

GAIN = {"drums": 0.9, "bass": 0.85, "pad": 0.55, "lead": 0.6, "arp": 0.35, "sfx": 0.9}
PAN = {"drums": 0.0, "bass": 0.0, "pad": 0.0, "lead": 0.12, "arp": -0.25, "sfx": 0.0}
SEND = {"drums": 0.25, "bass": 0.0, "pad": 1.0, "lead": 0.7, "arp": 0.6, "sfx": 0.5}
mix = np.zeros((N, 2))
send = np.zeros(N)
for name, y in buses.items():
    g = GAIN[name]
    p = PAN[name]
    mix[:, 0] += y * g * math.cos((p + 1) * math.pi / 4) * math.sqrt(2)
    mix[:, 1] += y * g * math.sin((p + 1) * math.pi / 4) * math.sqrt(2)
    send += y * g * SEND[name]

lag = int(BEAT * 0.75 * SR)
if (S.get("lead") in ("saw", "pluck", "bell") or style_name in ("synthwave", "edm")) and 0 < lag < N // 2:
    echo = np.zeros(N)
    echo[lag:] = buses["lead"][:-lag] * 0.3 * GAIN["lead"]
    mix[:, 1] += echo
    mix[lag:, 0] += echo[:-lag] * 0.6

wet = float(S.get("reverb", 0.25)) * float(d.get("reverb_scale", 1.0))
if wet > 0:
    ir_n = int(SR * (1.6 + 2.5 * wet))
    it = tvec(ir_n)
    size = 1 << int(math.ceil(math.log2(N + ir_n)))
    S_send = np.fft.rfft(send, size)
    for ch in (0, 1):
        ir = rng.normal(0, 1, ir_n) * np.exp(-it * (5.5 / (1.6 + 2.5 * wet)))
        ir = onepole_fft(ir, 6000)
        ir[: int(0.012 * SR)] = 0
        ir /= np.sqrt(np.sum(ir ** 2)) + 1e-9
        mix[:, ch] += np.fft.irfft(S_send * np.fft.rfft(ir, size), size)[:N] * wet * 0.9

if S.get("wobble"):
    tt = tvec(N)
    warp = tt + 0.0018 * np.sin(2 * np.pi * 0.45 * tt) + 0.0004 * np.sin(2 * np.pi * 5.5 * tt)
    for ch in (0, 1):
        mix[:, ch] = np.interp(warp * SR, np.arange(N), mix[:, ch])
if S.get("lowpass"):
    for ch in (0, 1):
        mix[:, ch] = onepole_fft(mix[:, ch], S["lowpass"], order=1)
if S.get("crackle"):
    pops = np.zeros(N)
    idx = rng.integers(0, N, int(DUR * 6))
    pops[idx] = rng.normal(0, 0.35, len(idx))
    pops = onepole_fft(pops, 3500)
    hiss = onepole_fft(rng.normal(0, 0.006, N), (1500, 7000), "band")
    mix += (pops + hiss)[:, None]

mix = mix[: int(DUR * SR)]
mix -= mix.mean(axis=0)
peak = np.max(np.abs(mix)) + 1e-9
mix = np.tanh(mix / peak * 1.25) / np.tanh(1.25)
fin = min(float(d.get("fade_in", 0.3 if style_name != "none" else 0.0)), DUR / 3)
fout = min(float(d.get("fade_out", 2.0 if style_name != "none" else 0.1)), DUR / 3)
n = len(mix)
if fin > 0:
    k = int(fin * SR)
    mix[:k] *= np.linspace(0, 1, k)[:, None]
if fout > 0:
    k = int(fout * SR)
    mix[n - k:] *= np.linspace(1, 0, k)[:, None] ** 1.5
mix *= 0.84 / (np.max(np.abs(mix)) + 1e-9)

out = d["out"]
wav = out if out.lower().endswith(".wav") else out + ".tmp.wav"
with wave.open(wav, "wb") as w:
    w.setnchannels(2)
    w.setsampwidth(2)
    w.setframerate(SR)
    w.writeframes((mix * 32767).astype("<i2").tobytes())
if wav != out:
    title = d.get("title") or f"{style_name.title()} {int(BPM)} BPM"
    r = subprocess.run(["ffmpeg", "-y", "-v", "error", "-i", wav, "-c:a", "libmp3lame", "-b:a", "192k", "-metadata", f"title={title}",
                        "-metadata", "artist=Macron AI", out], capture_output=True, text=True)
    os.remove(wav)
    if r.returncode != 0:
        print(json.dumps({"error": "ffmpeg: " + r.stderr[-300:]}))
        sys.exit(0)

names = [chord_name(notes) for _, notes, _ in CHORDS]
key_name = ["C", "C#", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"][KEY]
layers = [k for k in ("drums", "bass", "pad", "lead", "arp") if S.get(k) not in (None, "none")]
print(json.dumps({"style": style_name, "bpm": round(BPM), "key": key_name, "scale": scale_name, "meter": f"{METER}/8" if METER == 6 else f"{METER}/4",
                  "chords": names, "bars": len(timeline), "seconds": round(DUR, 2), "layers": layers, "sfx": placed}))
