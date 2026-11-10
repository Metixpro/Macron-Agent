import hashlib
import subprocess

from playwright.sync_api import sync_playwright

VIRTUAL_TIME = r"""
(() => {
  const epoch = Date.now();
  let now = 0, rafs = new Map(), rafId = 0, timers = new Map(), timerId = 0;
  const born = new WeakMap();
  window.requestAnimationFrame = cb => { rafs.set(++rafId, cb); return rafId; };
  window.cancelAnimationFrame = id => rafs.delete(id);
  performance.now = () => now;
  const RealDate = Date;
  window.Date = class extends RealDate {
    constructor(...a) { a.length ? super(...a) : super(epoch + now); }
    static now() { return epoch + now; }
  };
  const later = (fn, ms, args, every) => {
    timers.set(++timerId, { fn, at: now + Math.max(every ? 1 : 0, +ms || 0), every: every ? Math.max(1, +ms || 0) : 0, args });
    return timerId;
  };
  window.setTimeout = (fn, ms, ...args) => later(fn, ms, args, false);
  window.setInterval = (fn, ms, ...args) => later(fn, ms, args, true);
  window.clearTimeout = window.clearInterval = id => timers.delete(id);
  const run = (fn, args) => { try { typeof fn === "function" ? fn(...args) : (0, eval)(fn); } catch (e) { console.error(e && e.stack || e); } };
  window.__macronTick = t => {
    for (let n = 0; n < 5000; n++) {
      let id = 0, next = null;
      for (const [k, v] of timers) if (v.at <= t && (!next || v.at < next.at)) { id = k; next = v; }
      if (!next) break;
      now = next.at;
      if (next.every) next.at += next.every; else timers.delete(id);
      run(next.fn, next.args);
    }
    now = t;
    const due = rafs; rafs = new Map();
    for (const cb of due.values()) run(cb, [t]);
    for (const a of document.getAnimations()) {
      if (!born.has(a)) born.set(a, t);
      a.pause();
      a.currentTime = t - born.get(a);
    }
    return true;
  };
})();
"""

W, H, FPS, DUR = CFG["width"], CFG["height"], CFG["fps"], CFG["duration"]
frames = max(1, round(DUR * FPS))
errors = []
start = time.time()

encode = ["ffmpeg", "-y", "-v", "error", "-f", "image2pipe", "-framerate", str(FPS), "-i", "-"]
if CFG.get("audio"):
    fade = min(1.5, DUR / 4)
    encode += ["-i", CFG["audio"], "-map", "0:v", "-map", "1:a", "-af", f"apad,afade=t=out:st={DUR - fade:.3f}:d={fade:.3f}",
               "-c:a", "aac", "-b:a", "192k"]
encode += ["-t", f"{DUR:.3f}", "-c:v", "libx264", "-preset", "medium", "-crf", str(CFG.get("crf", 18)), "-pix_fmt", "yuv420p",
           "-movflags", "+faststart", CFG["out"]]

with sync_playwright() as pw:
    browser = launch(pw)
    ctx = browser.new_context(viewport={"width": W, "height": H}, device_scale_factor=1, locale="fa-IR")
    ctx.route("**/*", offline_libs)
    page = ctx.new_page()
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.on("console", lambda m: m.type == "error" and errors.append(m.text))
    page.add_init_script(VIRTUAL_TIME)
    try:
        page.goto(local_url(CFG["page"]), wait_until="load", timeout=60000)
        page.evaluate("() => document.fonts ? document.fonts.ready.then(() => true) : true")
    except Exception as e:
        fail(f"the page did not load: {e}")
    cdp = ctx.new_cdp_session(page)
    ff = subprocess.Popen(encode, stdin=subprocess.PIPE, stderr=subprocess.PIPE)
    hashes, every = set(), max(1, frames // 10)
    for i in range(frames):
        page.evaluate("t => window.__macronTick(t)", i * 1000.0 / FPS)
        shot = base64.b64decode(cdp.send("Page.captureScreenshot", {"format": "png", "optimizeForSpeed": True})["data"])
        if i % every == 0:
            hashes.add(hashlib.md5(shot).digest())
        try:
            ff.stdin.write(shot)
        except BrokenPipeError:
            break
    ff.stdin.close()
    err = ff.stderr.read().decode(errors="replace")
    if ff.wait() != 0:
        fail("ffmpeg could not encode the frames: " + err[-400:])
    browser.close()

preview = CFG.get("preview")
if preview:
    marks = sorted({min(frames - 1, round(frames * p)) for p in (0.1, 0.4, 0.7, 0.95)})
    while len(marks) < 4:
        marks.append(marks[-1])
    pick = "+".join(f"eq(n\\,{m})" for m in marks)
    subprocess.run(["ffmpeg", "-y", "-v", "error", "-i", CFG["out"], "-vf", f"select='{pick}',scale=640:-2,tile=2x2", "-frames:v", "1",
                    "-vsync", "vfr", "-q:v", "4", preview], check=False)

seen = []
for e in errors:
    e = e.strip().splitlines()[0][:300] if e.strip() else ""
    if e and e not in seen:
        seen.append(e)
out({"frames": frames, "seconds": round(time.time() - start, 1), "still": len(hashes) == 1 and frames > 1, "errors": seen[:8]})
