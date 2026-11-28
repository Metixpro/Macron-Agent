import re

from playwright.sync_api import Error as PWError
from playwright.sync_api import TimeoutError as PWTimeout
from playwright.sync_api import sync_playwright

SNAPSHOT = r"""
(max) => {
  const visible = el => { const r = el.getBoundingClientRect(), s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none"; };
  const q = s => JSON.stringify(s);
  const unique = s => { try { return document.querySelectorAll(s).length === 1; } catch (e) { return false; } };
  const label = el => (el.innerText || el.value || el.getAttribute("aria-label") || el.getAttribute("placeholder") || el.title || el.alt || "")
    .trim().replace(/\s+/g, " ").slice(0, 70);
  const path = el => {
    const parts = [];
    for (let e = el; e && e.nodeType === 1 && parts.length < 5; e = e.parentElement) {
      if (e.id && unique("#" + CSS.escape(e.id))) { parts.unshift("#" + CSS.escape(e.id)); break; }
      let i = 1; for (let s = e.previousElementSibling; s; s = s.previousElementSibling) if (s.tagName === e.tagName) i++;
      parts.unshift(e.tagName.toLowerCase() + ":nth-of-type(" + i + ")");
    }
    return parts.join(" > ");
  };
  const selector = el => {
    const tag = el.tagName.toLowerCase();
    if (el.id && unique("#" + CSS.escape(el.id))) return "#" + CSS.escape(el.id);
    for (const a of ["name", "data-testid", "aria-label", "placeholder"]) {
      const v = el.getAttribute(a);
      if (v && unique(tag + "[" + a + "=" + q(v) + "]")) return tag + "[" + a + "=" + q(v) + "]";
    }
    const text = label(el);
    if (text && (tag === "a" || tag === "button" || el.getAttribute("role"))) return "text=" + q(text);
    return path(el);
  };
  const items = [];
  const seen = new Set();
  for (const el of document.querySelectorAll("a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[contenteditable=true],summary")) {
    if (!visible(el)) continue;
    const sel = selector(el);
    if (seen.has(sel)) continue;
    seen.add(sel);
    const it = { tag: el.tagName.toLowerCase(), sel, text: label(el) };
    if (el.type && el.tagName !== "BUTTON") it.type = el.type;
    if (el.href) it.href = el.href.slice(0, 160);
    if (el.tagName === "SELECT") it.options = [...el.options].slice(0, 12).map(o => o.label);
    items.push(it);
    if (items.length >= 70) break;
  }
  const text = document.body ? document.body.innerText.replace(/[ \t]+/g, " ").replace(/\n\s*\n+/g, "\n").trim() : "";
  return { title: document.title, url: location.href, text: text.slice(0, max), more: Math.max(0, text.length - max), items };
}
"""

STATE = inside(".browser/state.json")
LAST = inside(".browser/url")
META = inside(".browser/meta.json")
PREVIEW = inside(".browser/preview.png")
os.makedirs(os.path.dirname(STATE), exist_ok=True)
if CFG.get("new_session"):
    for f in (STATE, LAST):
        if os.path.exists(f):
            os.remove(f)

def target(u):
    u = (u or "").strip()
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:(?!\d)", u):
        sp = urlsplit(u)
        if sp.scheme.lower() not in ("http", "https"):
            raise ValueError("only http(s) pages or workspace files can be opened")
        if is_metadata_host(sp.hostname):
            raise ValueError("access to metadata host is blocked")
        return u
    path = inside(u)
    if path and os.path.isfile(path):
        return local_url(u)
    cand = "https://" + u
    if is_metadata_host(urlsplit(cand).hostname):
        raise ValueError("access to metadata host is blocked")
    return cand


def save_live_preview(page):
    if CFG.get("close_after") or not page or page.url == "about:blank":
        return
    if PREVIEW:
        try:
            page.screenshot(path=PREVIEW, full_page=False)
        except PWError:
            pass
    if META:
        try:
            with open(META, "w", encoding="utf-8") as mf:
                json.dump({"url": page.url, "title": page.title() or "", "updated": int(time.time() * 1000)}, mf, ensure_ascii=False)
        except OSError:
            pass
    try:
        with open(LAST, "w", encoding="utf-8") as lf:
            lf.write(page.url)
    except OSError:
        pass


_actions = CFG.get("actions") or []
_closing_only = bool(_actions) and all((a.get("action") or "").lower() == "close" for a in _actions if isinstance(a, dict))

url = CFG.get("url") or ""
if not url and not _closing_only and os.path.exists(LAST):
    with open(LAST, encoding="utf-8") as lf:
        url = lf.read().strip()

log, files, errors, dialogs = [], [], [], []
TIMEOUT = int(CFG.get("timeout_ms") or 15000)
NAV_TIMEOUT = int(CFG.get("nav_timeout_ms") or max(TIMEOUT, 45000))


def name_for(base, ext):
    base = re.sub(r"[^\w.\-() ]+", "_", base or "").strip(" ._") or "page"
    if not base.lower().endswith(ext):
        base += ext
    return base


def status_note(resp):
    if resp is None or resp.status < 400:
        return ""
    if resp.status == 403 and resp.headers.get("x-macron-proxy"):
        return " (refused: the browser may only reach public websites on web ports)"
    return f" (HTTP {resp.status})"


def settle(page, ms=4000, pause=0):
    try:
        if pause:
            page.wait_for_timeout(pause)
        page.wait_for_load_state("domcontentloaded", timeout=ms)
        page.wait_for_load_state("networkidle", timeout=ms)
    except PWError:
        pass


MOUSE_POS = [683.0, 430.0]


def smooth_move(page, tx, ty, steps=12):
    sx, sy = MOUSE_POS
    steps = max(2, min(int(steps or 12), 40))
    for i in range(1, steps + 1):
        t = i / steps
        # Ease-in-out curve for natural pointer motion
        ease = t * t * (3 - 2 * t)
        page.mouse.move(sx + (tx - sx) * ease, sy + (ty - sy) * ease)
    MOUSE_POS[0], MOUSE_POS[1] = float(tx), float(ty)


def box_center(loc):
    box = loc.bounding_box(timeout=TIMEOUT)
    if not box:
        raise ValueError("element has no visible bounding box")
    return box["x"] + box["width"] / 2.0, box["y"] + box["height"] / 2.0


def resolve_scope(page, a):
    fr = a.get("frame")
    if fr is None or fr == "":
        return page
    frames = page.frames
    if isinstance(fr, int) or (isinstance(fr, str) and fr.isdigit()):
        idx = int(fr)
        if 0 <= idx < len(frames):
            return frames[idx]
        raise ValueError(f"frame index {idx} out of range (0..{len(frames)-1})")
    needle = str(fr).lower()
    for f in frames[1:]:
        if needle in (f.url or "").lower() or needle in (f.name or "").lower():
            return f
    raise ValueError(f"frame {fr!r} not found")


def collect_snapshot(page, max_text):
    snap = page.evaluate(SNAPSHOT, max_text)
    items = list(snap.get("items") or [])
    if len(items) < 70:
        for idx, f in enumerate(page.frames[1:], start=1):
            if idx > 8 or not f.url or f.url.startswith("about:"):
                continue
            try:
                sub = f.evaluate(SNAPSHOT, 800)
            except PWError:
                continue
            for it in sub.get("items") or []:
                it["frame"] = idx
                items.append(it)
                if len(items) >= 70:
                    break
            if len(items) >= 70:
                break
    snap["items"] = items[:70]
    return snap


def act(page, ctx, a):
    kind = (a.get("action") or "").lower()
    scope = resolve_scope(page, a)
    sel = a.get("selector") or ""
    loc = scope.locator(sel).first if sel else None

    def need():
        if loc is None:
            raise ValueError("this action needs a selector")
        return loc

    if kind == "goto":
        resp = page.goto(target(a.get("url")), wait_until="domcontentloaded", timeout=NAV_TIMEOUT)
        settle(page)
        return page.url + status_note(resp)
    if kind == "click":
        el = need()
        try:
            cx, cy = box_center(el)
            smooth_move(page, cx, cy, 8)
        except Exception:
            pass
        el.click(timeout=TIMEOUT, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
        settle(page, 3000, 300)
        return ""
    if kind == "click_at":
        tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
        smooth_move(page, tx, ty, 10)
        page.mouse.click(tx, ty, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
        settle(page, 3000, 300)
        return ""
    if kind == "move_at":
        tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
        smooth_move(page, tx, ty, int(a.get("steps") or 12))
        page.wait_for_timeout(250)
        return ""
    if kind in ("drag", "drag_at"):
        if loc is not None and a.get("to_selector"):
            sx, sy = box_center(need())
            tx, ty = box_center(scope.locator(a["to_selector"]).first)
        elif loc is not None and (a.get("to_x") is not None or a.get("to_y") is not None):
            sx, sy = box_center(need())
            tx = float(a.get("to_x") if a.get("to_x") is not None else sx)
            ty = float(a.get("to_y") if a.get("to_y") is not None else sy)
        else:
            sx, sy = float(a.get("x") or 0), float(a.get("y") or 0)
            tx, ty = float(a.get("to_x") or sx), float(a.get("to_y") or sy)
        smooth_move(page, sx, sy, 10)
        page.mouse.down()
        smooth_move(page, tx, ty, int(a.get("steps") or 20))
        page.mouse.up()
        settle(page, 2500, 250)
        return ""
    if kind == "hold":
        if loc is not None:
            tx, ty = box_center(need())
        else:
            tx, ty = float(a.get("x") or MOUSE_POS[0]), float(a.get("y") or MOUSE_POS[1])
        smooth_move(page, tx, ty, 10)
        page.mouse.down()
        page.wait_for_timeout(max(100, min(int(a.get("ms") or 1500), 15000)))
        page.mouse.up()
        settle(page, 3000, 300)
        return ""
    if kind == "tabs":
        return json.dumps([{"i": idx, "url": p.url, "title": p.title()} for idx, p in enumerate(ctx.pages)], ensure_ascii=False)
    if kind == "switch_tab":
        idx = a.get("index")
        if idx is not None:
            i = int(idx)
            if 0 <= i < len(ctx.pages):
                return f"__switch__:{i}"
            raise ValueError(f"tab index {i} out of range")
        needle = str(a.get("url") or a.get("title") or "").lower()
        for i, p in enumerate(ctx.pages):
            if needle and (needle in (p.url or "").lower() or needle in (p.title() or "").lower()):
                return f"__switch__:{i}"
        raise ValueError("matching tab not found")
    if kind == "close_tab":
        if len(ctx.pages) > 1:
            page.close()
            return "__tab_closed__"
        clear_live_preview(PREVIEW, META, LAST)
        return "__closed__"
    if kind == "close":
        clear_live_preview(PREVIEW, META, LAST)
        return "__closed__"
    if kind == "fill":
        need().fill(str(a.get("text", "")), timeout=TIMEOUT)
        return ""
    if kind == "type":
        delay = max(0, min(int(a.get("delay_ms") or 22), 200))
        if loc is not None:
            if a.get("clear"):
                loc.fill("", timeout=TIMEOUT)
            loc.press_sequentially(str(a.get("text", "")), delay=delay, timeout=TIMEOUT)
        else:
            page.keyboard.type(str(a.get("text", "")), delay=delay)
        return ""
    if kind == "press":
        key = a.get("key") or "Enter"
        loc.press(key, timeout=TIMEOUT) if loc is not None else page.keyboard.press(key)
        settle(page, 3000, 300)
        return ""
    if kind == "select":
        v = a.get("value") or a.get("text") or ""
        try:
            got = need().select_option(value=v, timeout=TIMEOUT)
        except PWError:
            got = loc.select_option(label=v, timeout=TIMEOUT)
        return ", ".join(got)
    if kind in ("check", "uncheck"):
        getattr(need(), kind)(timeout=TIMEOUT)
        return ""
    if kind == "hover":
        el = need()
        try:
            cx, cy = box_center(el)
            smooth_move(page, cx, cy, 8)
        except Exception:
            pass
        el.hover(timeout=TIMEOUT)
        return ""
    if kind == "scroll":
        if loc is not None:
            loc.scroll_into_view_if_needed(timeout=TIMEOUT)
        elif a.get("to") == "bottom":
            scope.evaluate("() => window.scrollTo(0, document.body.scrollHeight)")
        elif a.get("to") == "top":
            scope.evaluate("() => window.scrollTo(0, 0)")
        else:
            page.mouse.wheel(0, int(a.get("y") or 800))
        page.wait_for_timeout(600)
        return ""
    if kind == "wait":
        if sel:
            loc.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
        elif a.get("text"):
            scope.get_by_text(str(a["text"])).first.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
        else:
            page.wait_for_timeout(min(int(a.get("ms") or 1500), 15000))
        return ""
    if kind == "screenshot":
        name = name_for(a.get("name") or "screenshot", ".png")
        path = inside(name)
        if loc is not None:
            loc.screenshot(path=path, timeout=TIMEOUT)
        else:
            page.screenshot(path=path, full_page=bool(a.get("full_page")))
        files.append(name)
        return name
    if kind == "pdf":
        name = name_for(a.get("name") or "page", ".pdf")
        page.emulate_media(media="screen")
        page.pdf(path=inside(name), print_background=True, format=a.get("format") or "A4")
        files.append(name)
        return name
    if kind == "extract":
        need()
        n = min(int(a.get("limit") or 30), 100)
        every = scope.locator(sel)
        if a.get("attribute"):
            vals = every.evaluate_all("(els, a) => els.map(e => e.getAttribute(a))", a["attribute"])[:n]
        else:
            vals = every.all_inner_texts()[:n]
        vals = [re.sub(r"\s+", " ", str(v or "")).strip()[:600] for v in vals]
        return json.dumps(vals, ensure_ascii=False)
    if kind == "eval":
        val = scope.evaluate(str(a.get("script") or ""))
        return json.dumps(val, ensure_ascii=False, default=str)[:4000]
    if kind in ("back", "forward", "reload"):
        getattr(page, "go_back" if kind == "back" else "go_forward" if kind == "forward" else "reload")(timeout=30000)
        settle(page)
        return page.url
    if kind == "upload":
        paths = [inside(f) for f in (a.get("files") or [])]
        if not paths or not all(p and os.path.isfile(p) for p in paths):
            raise ValueError("files must be names of files in the workspace")
        need().set_input_files(paths, timeout=TIMEOUT)
        return ""
    if kind == "download":
        with page.expect_download(timeout=90000) as d:
            need().click(timeout=TIMEOUT)
        dl = d.value
        name = name_for(a.get("name") or dl.suggested_filename, os.path.splitext(dl.suggested_filename)[1] or ".bin")
        dl.save_as(inside(name))
        files.append(name)
        return name
    raise ValueError(f"unknown action {kind!r}")


snapshot = {}
with sync_playwright() as pw:
    browser = launch(pw)
    opts = {"viewport": {"width": 1366, "height": 860}, "locale": CFG.get("locale") or "en-US", "timezone_id": "Asia/Tehran",
            "accept_downloads": True, "user_agent": DESKTOP_UA}
    if CFG.get("mobile"):
        opts.update({k: v for k, v in pw.devices["iPhone 13"].items() if k != "default_browser_type"})
    if os.path.exists(STATE):
        opts["storage_state"] = STATE
    ctx = browser.new_context(**opts)
    apply_stealth(ctx, CFG.get("locale") or "en-US")
    ctx.set_default_timeout(TIMEOUT)
    ctx.route("**/*", browser_route)
    page = ctx.new_page()

    def watch(p):
        p.on("pageerror", lambda e: errors.append(str(e).splitlines()[0][:200]))
        p.on("dialog", lambda d: (dialogs.append(f"{d.type}: {d.message[:200]}"), d.accept()))

    watch(page)
    ctx.on("page", watch)
    closed = False
    try:
        if url:
            resp = page.goto(target(url), wait_until="domcontentloaded", timeout=NAV_TIMEOUT)
            settle(page)
            if note := status_note(resp):
                log.append({"step": 0, "what": "open " + url, "ok": False, "detail": note.strip(" ()")})
        for i, a in enumerate(CFG.get("actions") or []):
            what = (a.get("action") or "?") + (" " + a["selector"] if a.get("selector") else "") + (" " + a["url"] if a.get("url") else "")
            try:
                try:
                    detail = act(page, ctx, a)
                except PWError as e:
                    if "context was destroyed" not in str(e) and "navigat" not in str(e):
                        raise
                    settle(page, 8000, 300)
                    detail = act(page, ctx, a)
                if detail == "__closed__":
                    closed = True
                    log.append({"step": i + 1, "what": what, "ok": True, "detail": "closed"})
                    break
                if detail == "__tab_closed__":
                    if ctx.pages:
                        page = ctx.pages[-1]
                        settle(page)
                    log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                    continue
                if isinstance(detail, str) and detail.startswith("__switch__:"):
                    idx = int(detail.split(":", 1)[1])
                    page = ctx.pages[idx]
                    settle(page)
                    log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                    continue
                log.append({"step": i + 1, "what": what, "ok": True, "detail": detail})
                if (a.get("action") or "").lower() in ("goto", "click", "click_at", "drag", "drag_at", "hold", "press", "scroll", "back", "forward", "reload", "switch_tab"):
                    save_live_preview(page)
            except PWTimeout:
                log.append({"step": i + 1, "what": what, "ok": False, "detail": "timed out: the element was not found or not usable"})
                break
            except (PWError, ValueError) as e:
                log.append({"step": i + 1, "what": what, "ok": False, "detail": str(e).splitlines()[0][:300]})
                break
            if ctx.pages and ctx.pages[-1] is not page and (a.get("action") or "").lower() not in ("tabs", "switch_tab"):
                page = ctx.pages[-1]
                settle(page)
        if not closed and page.url != "about:blank":
            snapshot = collect_snapshot(page, int(CFG.get("max_text") or 3500))
            if not CFG.get("close_after"):
                if PREVIEW:
                    try:
                        page.screenshot(path=PREVIEW, full_page=False)
                    except PWError:
                        pass
                if META:
                    try:
                        with open(META, "w", encoding="utf-8") as mf:
                            json.dump({"url": page.url, "title": snapshot.get("title") or "", "updated": int(time.time() * 1000)}, mf, ensure_ascii=False)
                    except OSError:
                        pass
            if CFG.get("screenshot", True):
                shot = name_for(CFG.get("shot") or "screenshot", ".png")
                shot_path = inside(shot)
                if shot_path:
                    page.screenshot(path=shot_path, full_page=bool(CFG.get("full_page")))
                    if shot not in files:
                        files.append(shot)
            if not CFG.get("close_after"):
                with open(LAST, "w", encoding="utf-8") as f:
                    f.write(page.url)
        ctx.storage_state(path=STATE)
        if CFG.get("close_after"):
            clear_live_preview(PREVIEW, META, LAST)
    except (PWError, ValueError) as e:
        log.append({"step": 0, "what": "open " + url, "ok": False, "detail": str(e).splitlines()[0][:300]})
    browser.close()

out({"page": snapshot, "log": log, "files": files, "errors": errors[:6], "dialogs": dialogs[:5]})
