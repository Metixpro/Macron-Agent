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
os.makedirs(os.path.dirname(STATE), exist_ok=True)
if CFG.get("new_session"):
    for f in (STATE, LAST):
        if os.path.exists(f):
            os.remove(f)

def target(u):
    u = (u or "").strip()
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:(?!\d)", u):
        if urlsplit(u).scheme.lower() not in ("http", "https"):
            raise ValueError("only http(s) pages or workspace files can be opened")
        return u
    path = inside(u)
    return local_url(u) if path and os.path.isfile(path) else "https://" + u


url = CFG.get("url") or ""
if not url and os.path.exists(LAST):
    url = open(LAST).read().strip()

log, files, errors, dialogs = [], [], [], []
TIMEOUT = int(CFG.get("timeout_ms") or 15000)


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


def act(page, a):
    kind = (a.get("action") or "").lower()
    sel = a.get("selector") or ""
    loc = page.locator(sel).first if sel else None

    def need():
        if loc is None:
            raise ValueError("this action needs a selector")
        return loc

    if kind == "goto":
        resp = page.goto(target(a.get("url")), wait_until="domcontentloaded", timeout=45000)
        settle(page)
        return page.url + status_note(resp)
    if kind == "click":
        need().click(timeout=TIMEOUT, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
        settle(page, 3000, 300)
        return ""
    if kind == "fill":
        need().fill(str(a.get("text", "")), timeout=TIMEOUT)
        return ""
    if kind == "type":
        if loc is not None:
            loc.press_sequentially(str(a.get("text", "")), delay=25, timeout=TIMEOUT)
        else:
            page.keyboard.type(str(a.get("text", "")), delay=25)
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
        need().hover(timeout=TIMEOUT)
        return ""
    if kind == "scroll":
        if loc is not None:
            loc.scroll_into_view_if_needed(timeout=TIMEOUT)
        elif a.get("to") == "bottom":
            page.evaluate("() => window.scrollTo(0, document.body.scrollHeight)")
        elif a.get("to") == "top":
            page.evaluate("() => window.scrollTo(0, 0)")
        else:
            page.mouse.wheel(0, int(a.get("y") or 800))
        page.wait_for_timeout(600)
        return ""
    if kind == "wait":
        if sel:
            loc.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
        elif a.get("text"):
            page.get_by_text(str(a["text"])).first.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
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
        every = page.locator(sel)
        if a.get("attribute"):
            vals = every.evaluate_all("(els, a) => els.map(e => e.getAttribute(a))", a["attribute"])[:n]
        else:
            vals = every.all_inner_texts()[:n]
        vals = [re.sub(r"\s+", " ", str(v or "")).strip()[:600] for v in vals]
        return json.dumps(vals, ensure_ascii=False)
    if kind == "eval":
        val = page.evaluate(str(a.get("script") or ""))
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
            "accept_downloads": True}
    if CFG.get("mobile"):
        opts.update({k: v for k, v in pw.devices["iPhone 13"].items() if k != "default_browser_type"})
    if os.path.exists(STATE):
        opts["storage_state"] = STATE
    ctx = browser.new_context(**opts)
    ctx.set_default_timeout(TIMEOUT)
    ctx.route(f"https://{LOCAL_HOST}/**", serve_local)
    page = ctx.new_page()

    def watch(p):
        p.on("pageerror", lambda e: errors.append(str(e).splitlines()[0][:200]))
        p.on("dialog", lambda d: (dialogs.append(f"{d.type}: {d.message[:200]}"), d.accept()))

    watch(page)
    ctx.on("page", watch)
    try:
        if url:
            resp = page.goto(target(url), wait_until="domcontentloaded", timeout=45000)
            settle(page)
            if note := status_note(resp):
                log.append({"step": 0, "what": "open " + url, "ok": False, "detail": note.strip(" ()")})
        for i, a in enumerate(CFG.get("actions") or []):
            what = (a.get("action") or "?") + (" " + a["selector"] if a.get("selector") else "") + (" " + a["url"] if a.get("url") else "")
            try:
                try:
                    detail = act(page, a)
                except PWError as e:
                    if "context was destroyed" not in str(e) and "navigat" not in str(e):
                        raise
                    settle(page, 8000, 300)
                    detail = act(page, a)
                log.append({"step": i + 1, "what": what, "ok": True, "detail": detail})
            except PWTimeout:
                log.append({"step": i + 1, "what": what, "ok": False, "detail": "timed out: the element was not found or not usable"})
                break
            except (PWError, ValueError) as e:
                log.append({"step": i + 1, "what": what, "ok": False, "detail": str(e).splitlines()[0][:300]})
                break
            if ctx.pages and ctx.pages[-1] is not page:
                page = ctx.pages[-1]
                settle(page)
        if page.url != "about:blank":
            snapshot = page.evaluate(SNAPSHOT, int(CFG.get("max_text") or 3500))
            if CFG.get("screenshot", True):
                shot = name_for(CFG.get("shot") or "screenshot", ".png")
                page.screenshot(path=inside(shot), full_page=bool(CFG.get("full_page")))
                if shot not in files:
                    files.append(shot)
            with open(LAST, "w") as f:
                f.write(page.url)
        ctx.storage_state(path=STATE)
    except (PWError, ValueError) as e:
        log.append({"step": 0, "what": "open " + url, "ok": False, "detail": str(e).splitlines()[0][:300]})
    browser.close()

out({"page": snapshot, "log": log, "files": files, "errors": errors[:6], "dialogs": dialogs[:5]})
