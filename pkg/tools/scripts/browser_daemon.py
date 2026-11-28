import asyncio
import base64
import mimetypes
import re

from playwright.async_api import Error as PWError
from playwright.async_api import TimeoutError as PWTimeout
from playwright.async_api import async_playwright

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
    if (el.closest("[data-macron-overlay]")) continue;
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

CURSOR_SHOW_JS = r"""
([x, y, clicking]) => {
  if (!document.documentElement) return;
  let root = document.getElementById("__macron_cursor");
  if (!root) {
    root = document.createElement("div");
    root.id = "__macron_cursor";
    root.setAttribute("data-macron-overlay", "1");
    root.setAttribute("aria-hidden", "true");
    root.style.cssText = [
      "position:fixed",
      "left:0",
      "top:0",
      "width:18px",
      "height:18px",
      "margin-left:-9px",
      "margin-top:-9px",
      "border-radius:9999px",
      "border:2px solid #38bdf8",
      "background:rgba(56,189,248,0.24)",
      "box-shadow:0 0 0 3px rgba(56,189,248,0.18),0 2px 10px rgba(0,0,0,0.45)",
      "pointer-events:none",
      "z-index:2147483647",
      "transition:transform 28ms linear,width 90ms ease,height 90ms ease,margin 90ms ease,background 90ms ease"
    ].join(";");
    document.documentElement.appendChild(root);
  }
  root.style.display = "block";
  root.style.transform = `translate3d(${Math.round(x)}px, ${Math.round(y)}px, 0) scale(${clicking ? 1.45 : 1})`;
  if (clicking) {
    root.style.background = "rgba(56,189,248,0.48)";
  } else {
    root.style.background = "rgba(56,189,248,0.24)";
  }
}
"""

CURSOR_HIDE_JS = r"""
() => {
  const root = document.getElementById("__macron_cursor");
  if (root) root.remove();
}
"""

STATE = inside(".browser/state.json")
LAST = inside(".browser/url")
META = inside(".browser/meta.json")
PREVIEW = inside(".browser/preview.png")
os.makedirs(os.path.dirname(STATE), exist_ok=True)

_OUT_LOCK = threading.Lock()


def emit(obj):
    line = (json.dumps(obj, ensure_ascii=False) + "\n").encode("utf-8", errors="replace")
    with _OUT_LOCK:
        try:
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
        except AttributeError:
            sys.stdout.write(line.decode("utf-8", errors="replace"))
            sys.stdout.flush()


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


async def async_apply_stealth(ctx, locale="en-US"):
    lang = (locale or "en-US").split(",")[0].strip() or "en-US"
    base = lang.split("-")[0]
    langs = [lang] if lang == base else [lang, base]
    await ctx.add_init_script(f"""
(() => {{
  try {{
    Object.defineProperty(navigator, 'webdriver', {{ get: () => undefined }});
  }} catch (e) {{}}
  try {{
    Object.defineProperty(navigator, 'languages', {{ get: () => {json.dumps(langs)} }});
  }} catch (e) {{}}
  try {{
    window.chrome = window.chrome || {{ runtime: {{}} }};
  }} catch (e) {{}}
  try {{
    if (navigator.permissions && navigator.permissions.query) {{
      const origQuery = navigator.permissions.query.bind(navigator.permissions);
      navigator.permissions.query = (params) =>
        params && params.name === 'notifications'
          ? Promise.resolve({{ state: Notification.permission }})
          : origQuery(params);
    }}
  }} catch (e) {{}}
}})();
""")


async def async_fulfill(route, path, allow_cors=False):
    ext = os.path.splitext(path)[1].lower()
    ctype = TYPES.get(ext) or mimetypes.guess_type(path)[0] or "application/octet-stream"
    with open(path, "rb") as f:
        body = f.read()
    headers = {"Content-Type": ctype, "Cache-Control": "no-store"}
    if allow_cors:
        headers["Access-Control-Allow-Origin"] = "*"
    await route.fulfill(status=200, body=body, headers=headers)


async def async_serve_local(route):
    req = route.request
    headers = {k.lower(): v for k, v in (req.headers or {}).items()}
    sfs = (headers.get("sec-fetch-site") or "").strip().lower()
    origin = (headers.get("origin") or "").strip().lower()
    if sfs == "cross-site" or (origin and origin != f"https://{LOCAL_HOST}"):
        return await route.fulfill(status=403, body="forbidden")
    try:
        page_url = req.frame.page.url
    except Exception:
        page_url = ""
    if page_url and req.resource_type != "document":
        pu = urlsplit(page_url)
        if pu.scheme in ("http", "https") and (pu.hostname or "").lower() != LOCAL_HOST:
            return await route.fulfill(status=403, body="forbidden")
    path = unquote(urlsplit(req.url).path)
    name = os.path.basename(path)
    target_path = LIBS.get(name) if path.startswith("/lib/") else None
    target_path = target_path or inside(path)
    if not target_path or not os.path.isfile(target_path):
        return await route.fulfill(status=404, body="not found")
    await async_fulfill(route, target_path, allow_cors=False)


async def async_browser_route(route):
    u = urlsplit(route.request.url)
    if u.hostname == LOCAL_HOST:
        return await async_serve_local(route)
    if u.hostname and is_metadata_host(u.hostname):
        return await route.abort("blockedbyclient")
    await route.continue_()


class Daemon:
    def __init__(self, pw, browser):
        self.pw = pw
        self.browser = browser
        self.ctx = None
        self.active_page = None
        self.attached_page = None
        self.cdp = None
        self.locale = CFG.get("locale") or "en-US"
        self.mobile = bool(CFG.get("mobile"))
        self.vw = 390 if self.mobile else 1366
        self.vh = 844 if self.mobile else 860
        self.timeout_ms = int(CFG.get("timeout_ms") or 15000)
        self.nav_timeout_ms = int(CFG.get("nav_timeout_ms") or max(self.timeout_ms, 45000))
        self.mouse_pos = [self.vw / 2.0, self.vh / 2.0]

        self.paused = False
        self.agent_active = False
        self.resume_event = asyncio.Event()
        self.resume_event.set()
        self.tool_task = None
        self.tool_lock = asyncio.Lock()

        self.errors = []
        self.dialogs = []
        self.watched_pages = set()

        self.last_frame_at = 0.0
        self.pending_frame = None
        self.flush_task = None
        self.last_preview_at = 0.0
        self.last_meta_key = ("", "", 0)
        self.should_exit = False
        self.state_timer = None
        self.frame_watchdog = None

    async def create_context(self, reset_state=False):
        if reset_state:
            for f in (STATE, LAST):
                if os.path.exists(f):
                    try:
                        os.remove(f)
                    except OSError:
                        pass
        if self.cdp is not None:
            try:
                await self.cdp.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await self.cdp.detach()
            except Exception:
                pass
            self.cdp = None
        if self.ctx is not None:
            try:
                await self.ctx.close()
            except Exception:
                pass
            self.ctx = None
        self.watched_pages.clear()

        opts = {
            "viewport": {"width": 1366, "height": 860},
            "locale": self.locale,
            "timezone_id": "Asia/Tehran",
            "accept_downloads": True,
            "user_agent": DESKTOP_UA,
        }
        if self.mobile:
            dev = dict(self.pw.devices["iPhone 13"])
            dev.pop("default_browser_type", None)
            opts.update(dev)
        vp = opts.get("viewport") or {"width": 1366, "height": 860}
        self.vw = int(vp.get("width") or 1366)
        self.vh = int(vp.get("height") or 860)
        self.mouse_pos = [self.vw / 2.0, self.vh / 2.0]

        if not reset_state and os.path.exists(STATE):
            opts["storage_state"] = STATE
        self.ctx = await self.browser.new_context(**opts)
        await async_apply_stealth(self.ctx, self.locale)
        self.ctx.set_default_timeout(self.timeout_ms)
        await self.ctx.route("**/*", async_browser_route)
        self.ctx.on("page", self._on_new_page)

        page = await self.ctx.new_page()
        self._watch_page(page)
        self.active_page = page
        await self.attach_screencast(page)
        return page

    def _watch_page(self, page):
        pid = id(page)
        if pid in self.watched_pages:
            return
        self.watched_pages.add(pid)
        page.on("pageerror", lambda e: self.errors.append(str(e).splitlines()[0][:200]))

        async def _accept_dialog(d):
            self.dialogs.append(f"{d.type}: {d.message[:200]}")
            try:
                await d.accept()
            except Exception:
                pass

        page.on("dialog", lambda d: asyncio.create_task(_accept_dialog(d)))
        page.on("framenavigated", lambda fr: self._on_frame_navigated(page, fr))
        page.on("load", lambda: asyncio.create_task(self.on_page_changed(page, screenshot=True)))
        page.on("close", lambda: asyncio.create_task(self._on_page_closed(page)))

    def _on_new_page(self, page):
        self._watch_page(page)
        asyncio.create_task(self._handle_new_tab(page))

    async def _handle_new_tab(self, page):
        if not self.agent_active or self.paused:
            self.active_page = page
            try:
                await page.bring_to_front()
            except Exception:
                pass
            await self.attach_screencast(page)
        await self.emit_state()

    def _on_frame_navigated(self, page, frame):
        if page is self.active_page and frame is page.main_frame:
            self._schedule_state_emit(0.08)

    async def _on_page_closed(self, page):
        self.watched_pages.discard(id(page))
        if page is self.active_page and self.ctx is not None:
            remaining = [p for p in self.ctx.pages if not p.is_closed()]
            if remaining:
                self.active_page = remaining[-1]
                await self.attach_screencast(self.active_page)
                await self.push_snapshot_frame()
            else:
                self.active_page = None
        await self.emit_state()

    async def on_page_changed(self, page, screenshot=False):
        if page is self.active_page:
            await self.persist_meta_and_preview(save_shot=screenshot)
            await self.emit_state()
            await self.push_snapshot_frame()

    def _schedule_state_emit(self, delay=0.12):
        if self.state_timer is not None and not self.state_timer.done():
            return

        async def _job():
            await asyncio.sleep(delay)
            await self.persist_meta_and_preview(save_shot=False)
            await self.emit_state()

        self.state_timer = asyncio.create_task(_job())

    async def _safe_ack(self, cdp, session_id):
        try:
            await cdp.send("Page.screencastFrameAck", {"sessionId": session_id})
        except Exception:
            pass

    async def attach_screencast(self, page):
        if page is None or page.is_closed() or self.ctx is None:
            return
        if page is self.attached_page and self.cdp is not None:
            return
        if self.flush_task is not None and not self.flush_task.done():
            self.flush_task.cancel()
        self.pending_frame = None

        old = self.cdp
        if old is not None:
            try:
                await old.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await old.detach()
            except Exception:
                pass
            self.cdp = None
        self.attached_page = None

        try:
            my_cdp = await self.ctx.new_cdp_session(page)
        except Exception:
            return
        self.cdp = my_cdp
        self.attached_page = page

        def on_frame(ev):
            asyncio.create_task(self._safe_ack(my_cdp, ev.get("sessionId")))
            if my_cdp is not self.cdp:
                return
            data = ev.get("data")
            if not data:
                return
            now = time.monotonic()
            if now - self.last_frame_at >= 0.045:
                self.last_frame_at = now
                self.pending_frame = None
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
            else:
                self.pending_frame = data
                if self.flush_task is None or self.flush_task.done():
                    self.flush_task = asyncio.create_task(self._flush_trailing_frame(my_cdp))

        my_cdp.on("Page.screencastFrame", on_frame)
        try:
            await my_cdp.send(
                "Page.startScreencast",
                {
                    "format": "jpeg",
                    "quality": 78,
                    "maxWidth": self.vw,
                    "maxHeight": self.vh,
                    "everyNthFrame": 1,
                },
            )
        except Exception:
            pass

    async def _flush_trailing_frame(self, my_cdp):
        try:
            await asyncio.sleep(0.05)
            if my_cdp is self.cdp and self.pending_frame:
                data = self.pending_frame
                self.pending_frame = None
                self.last_frame_at = time.monotonic()
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
        except asyncio.CancelledError:
            pass

    async def push_snapshot_frame(self):
        if self.cdp is None or self.active_page is None or self.active_page.is_closed():
            return
        if self.active_page.url == "about:blank":
            return
        try:
            res = await self.cdp.send("Page.captureScreenshot", {"format": "jpeg", "quality": 78})
            data = res.get("data")
            if data:
                self.last_frame_at = time.monotonic()
                self.pending_frame = None
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
        except Exception:
            pass

    async def persist_meta_and_preview(self, save_shot=False):
        page = self.active_page
        if page is None or page.is_closed() or page.url == "about:blank":
            return
        try:
            url = page.url or ""
            title = await page.title()
        except Exception:
            return
        updated = int(time.time() * 1000)
        key = (url, title, len(self.ctx.pages) if self.ctx else 0)
        if key != self.last_meta_key:
            self.last_meta_key = key
            if META:
                try:
                    with open(META, "w", encoding="utf-8") as mf:
                        json.dump({"url": url, "title": title, "updated": updated}, mf, ensure_ascii=False)
                except OSError:
                    pass
            try:
                with open(LAST, "w", encoding="utf-8") as lf:
                    lf.write(url)
            except OSError:
                pass
        now = time.monotonic()
        if save_shot and PREVIEW and (now - self.last_preview_at >= 0.6):
            self.last_preview_at = now
            try:
                await page.screenshot(path=PREVIEW, full_page=False)
            except Exception:
                pass

    async def state_payload(self):
        tabs = []
        open_flag = False
        url = ""
        title = ""
        if self.ctx is not None:
            for idx, p in enumerate(self.ctx.pages):
                if p.is_closed():
                    continue
                try:
                    p_url = p.url or ""
                    p_title = await p.title()
                except Exception:
                    p_url, p_title = "", ""
                is_active = p is self.active_page
                tabs.append({"index": idx, "url": p_url, "title": p_title, "active": is_active})
                if p_url and p_url != "about:blank":
                    open_flag = True
                if is_active:
                    url = p_url
                    title = p_title
        if not url and tabs:
            url = tabs[0]["url"]
            title = tabs[0]["title"]
        if url and url != "about:blank":
            open_flag = True
        return {
            "open": open_flag,
            "url": "" if url == "about:blank" else url,
            "title": title,
            "updated": int(time.time() * 1000),
            "paused": self.paused,
            "agent_active": self.agent_active,
            "viewport": {"width": self.vw, "height": self.vh},
            "tabs": tabs,
        }

    async def emit_state(self):
        st = await self.state_payload()
        emit({"event": "state", "state": st})
        return st

    async def save_storage_state(self):
        if self.ctx is None:
            return
        try:
            await self.ctx.storage_state(path=STATE)
        except Exception:
            pass

    async def show_cursor(self, page, x, y, clicking=False):
        if page is None or page.is_closed():
            return
        try:
            await page.evaluate(CURSOR_SHOW_JS, [x, y, bool(clicking)])
        except Exception:
            pass

    async def hide_cursor(self, page):
        if page is None or page.is_closed():
            return
        try:
            await page.evaluate(CURSOR_HIDE_JS)
        except Exception:
            pass

    async def settle(self, page, ms=4000, pause=0):
        try:
            if pause:
                await page.wait_for_timeout(pause)
            await page.wait_for_load_state("domcontentloaded", timeout=ms)
            await page.wait_for_load_state("networkidle", timeout=ms)
        except PWError:
            pass

    async def smooth_move(self, page, tx, ty, steps=12):
        sx, sy = self.mouse_pos
        steps = max(2, min(int(steps or 12), 40))
        for i in range(1, steps + 1):
            await self.wait_if_paused()
            t = i / steps
            ease = t * t * (3 - 2 * t)
            nx = sx + (tx - sx) * ease
            ny = sy + (ty - sy) * ease
            await self.show_cursor(page, nx, ny, clicking=False)
            await page.mouse.move(nx, ny)
        self.mouse_pos[0], self.mouse_pos[1] = float(tx), float(ty)

    async def wait_if_paused(self):
        if not self.resume_event.is_set():
            await self.hide_cursor(self.active_page)
            await self.resume_event.wait()

    async def close_session(self):
        clear_live_preview(PREVIEW, META, LAST)
        if self.cdp is not None:
            try:
                await self.cdp.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await self.cdp.detach()
            except Exception:
                pass
            self.cdp = None
        self.attached_page = None
        if self.ctx is not None:
            try:
                await self.ctx.close()
            except Exception:
                pass
            self.ctx = None
        self.active_page = None
        await self.emit_state()
        self.should_exit = True

    def _schedule_input_frame(self):
        prev_at = self.last_frame_at
        if self.frame_watchdog is not None and not self.frame_watchdog.done():
            self.frame_watchdog.cancel()

        async def _check():
            try:
                await asyncio.sleep(0.085)
                if self.last_frame_at == prev_at:
                    await self.push_snapshot_frame()
            except asyncio.CancelledError:
                pass

        self.frame_watchdog = asyncio.create_task(_check())

    async def handle_input(self, msg):
        page = self.active_page
        cdp = self.cdp
        if page is None or page.is_closed() or cdp is None:
            return {"ok": False, "error": "no active browser tab"}
        await self.hide_cursor(page)
        kind = (msg.get("type") or "").lower()
        x = float(msg.get("x") or self.mouse_pos[0])
        y = float(msg.get("y") or self.mouse_pos[1])
        x = max(0.0, min(float(self.vw - 1), x))
        y = max(0.0, min(float(self.vh - 1), y))
        button = (msg.get("button") or "left").lower()
        if button not in ("left", "middle", "right"):
            button = "left"
        click_count = max(1, min(int(msg.get("clickCount") or 1), 3))
        modifiers = int(msg.get("modifiers") or 0)
        buttons_mask = 1 if button == "left" else 2 if button == "right" else 4

        try:
            if kind == "wheel":
                dx = float(msg.get("deltaX") or 0.0)
                dy = float(msg.get("deltaY") or 0.0)
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mouseWheel",
                        "x": x,
                        "y": y,
                        "deltaX": dx,
                        "deltaY": dy,
                        "modifiers": modifiers,
                    },
                )
            elif kind == "mouse_move":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mouseMoved",
                        "x": x,
                        "y": y,
                        "modifiers": modifiers,
                    },
                )
            elif kind == "mouse_down":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mousePressed",
                        "x": x,
                        "y": y,
                        "button": button,
                        "buttons": buttons_mask,
                        "clickCount": click_count,
                        "modifiers": modifiers,
                    },
                )
            elif kind == "mouse_up":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mouseReleased",
                        "x": x,
                        "y": y,
                        "button": button,
                        "buttons": 0,
                        "clickCount": click_count,
                        "modifiers": modifiers,
                    },
                )
            elif kind == "click":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mouseMoved",
                        "x": x,
                        "y": y,
                        "modifiers": modifiers,
                    },
                )
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mousePressed",
                        "x": x,
                        "y": y,
                        "button": button,
                        "buttons": buttons_mask,
                        "clickCount": click_count,
                        "modifiers": modifiers,
                    },
                )
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {
                        "type": "mouseReleased",
                        "x": x,
                        "y": y,
                        "button": button,
                        "buttons": 0,
                        "clickCount": click_count,
                        "modifiers": modifiers,
                    },
                )
            elif kind == "insert_text":
                txt = str(msg.get("text") or "")
                if txt:
                    await cdp.send("Input.insertText", {"text": txt})
            elif kind == "press":
                key = str(msg.get("key") or "Enter")
                await page.keyboard.press(key)
            elif kind == "key_down":
                key = str(msg.get("key") or "")
                if key:
                    await page.keyboard.down(key)
            elif kind == "key_up":
                key = str(msg.get("key") or "")
                if key:
                    await page.keyboard.up(key)
            else:
                return {"ok": False, "error": f"unknown input type {kind!r}"}
        except Exception as e:
            return {"ok": False, "error": str(e).splitlines()[0][:200]}

        self._schedule_input_frame()
        if kind != "mouse_move":
            self._schedule_state_emit(0.15)
        return {"ok": True}

    async def handle_nav(self, msg):
        action = (msg.get("action") or "").lower()
        if action == "close":
            await self.save_storage_state()
            await self.close_session()
            return {"ok": True, "closed": True, "state": await self.state_payload()}

        if self.ctx is None:
            await self.create_context(reset_state=False)
        page = self.active_page
        if page is None or page.is_closed():
            page = await self.ctx.new_page()
            self._watch_page(page)
            self.active_page = page
            await self.attach_screencast(page)

        try:
            if action in ("goto", "open"):
                dest = target(msg.get("url"))
                await page.goto(dest, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                await self.settle(page, 2500, 120)
            elif action == "new_tab":
                dest = target(msg.get("url") or "https://www.google.com")
                new_p = await self.ctx.new_page()
                self._watch_page(new_p)
                self.active_page = new_p
                await self.attach_screencast(new_p)
                await new_p.goto(dest, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                await self.settle(new_p, 2500, 120)
            elif action == "switch_tab":
                idx = int(msg.get("index") if msg.get("index") is not None else 0)
                pages = self.ctx.pages
                if idx < 0 or idx >= len(pages):
                    raise ValueError(f"tab index {idx} out of range")
                self.active_page = pages[idx]
                await self.active_page.bring_to_front()
                await self.attach_screencast(self.active_page)
            elif action == "close_tab":
                pages = self.ctx.pages
                if len(pages) <= 1:
                    await self.save_storage_state()
                    await self.close_session()
                    return {"ok": True, "closed": True, "state": await self.state_payload()}
                idx = msg.get("index")
                if idx is not None:
                    i = int(idx)
                    if i < 0 or i >= len(pages):
                        raise ValueError(f"tab index {i} out of range")
                    target_p = pages[i]
                else:
                    target_p = page
                closing_active = target_p is self.active_page
                await target_p.close()
                remaining = [p for p in self.ctx.pages if not p.is_closed()]
                if not remaining:
                    await self.save_storage_state()
                    await self.close_session()
                    return {"ok": True, "closed": True, "state": await self.state_payload()}
                if closing_active:
                    self.active_page = remaining[-1]
                    await self.active_page.bring_to_front()
                    await self.attach_screencast(self.active_page)
            elif action == "back":
                await page.go_back(timeout=self.nav_timeout_ms)
                await self.settle(page, 2000, 100)
            elif action == "forward":
                await page.go_forward(timeout=self.nav_timeout_ms)
                await self.settle(page, 2000, 100)
            elif action == "reload":
                await page.reload(timeout=self.nav_timeout_ms)
                await self.settle(page, 2500, 120)
            else:
                return {"ok": False, "error": f"unknown nav action {action!r}"}
        except (PWError, ValueError) as e:
            return {"ok": False, "error": str(e).splitlines()[0][:300], "state": await self.emit_state()}

        await self.persist_meta_and_preview(save_shot=True)
        await self.save_storage_state()
        await self.push_snapshot_frame()
        st = await self.emit_state()
        return {"ok": True, "state": st}

    async def run_tool(self, cfg):
        async with self.tool_lock:
            self.agent_active = True
            self.errors = []
            self.dialogs = []
            files = []
            log = []
            snapshot = {}
            closed = False
            await self.emit_state()
            try:
                await self.wait_if_paused()
                new_session = bool(cfg.get("new_session"))
                want_mobile = bool(cfg.get("mobile"))
                req_locale = cfg.get("locale") or self.locale
                if cfg.get("timeout_ms"):
                    self.timeout_ms = int(cfg["timeout_ms"])
                if cfg.get("nav_timeout_ms"):
                    self.nav_timeout_ms = int(cfg["nav_timeout_ms"])

                if self.ctx is None or new_session or want_mobile != self.mobile or req_locale != self.locale:
                    self.mobile = want_mobile
                    self.locale = req_locale
                    await self.create_context(reset_state=new_session)
                else:
                    self.ctx.set_default_timeout(self.timeout_ms)

                if self.active_page is None or self.active_page.is_closed():
                    if self.ctx.pages:
                        self.active_page = self.ctx.pages[-1]
                        await self.attach_screencast(self.active_page)
                    else:
                        p = await self.ctx.new_page()
                        self._watch_page(p)
                        self.active_page = p
                        await self.attach_screencast(p)

                page = self.active_page
                actions = cfg.get("actions") or []
                closing_only = bool(actions) and all((a.get("action") or "").lower() == "close" for a in actions if isinstance(a, dict))
                url = (cfg.get("url") or "").strip()
                if not url and not closing_only and (not page.url or page.url == "about:blank") and os.path.exists(LAST):
                    with open(LAST, encoding="utf-8") as lf:
                        url = lf.read().strip()

                if url:
                    try:
                        resp = await page.goto(target(url), wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                        await self.settle(page)
                        await self.persist_meta_and_preview(save_shot=True)
                        await self.push_snapshot_frame()
                        await self.emit_state()
                        if note := status_note(resp):
                            log.append({"step": 0, "what": "open " + url, "ok": False, "detail": note.strip(" ()")})
                    except (PWError, ValueError) as e:
                        log.append({"step": 0, "what": "open " + url, "ok": False, "detail": str(e).splitlines()[0][:300]})

                for i, a in enumerate(actions):
                    await self.wait_if_paused()
                    if not isinstance(a, dict):
                        continue
                    what = (a.get("action") or "?") + (" " + a["selector"] if a.get("selector") else "") + (" " + a["url"] if a.get("url") else "")
                    try:
                        try:
                            detail = await self._act_step(page, a, files)
                        except PWError as e:
                            if "context was destroyed" not in str(e) and "navigat" not in str(e):
                                raise
                            await self.settle(page, 8000, 300)
                            await self.wait_if_paused()
                            detail = await self._act_step(page, a, files)

                        if detail == "__closed__":
                            closed = True
                            await self.save_storage_state()
                            await self.close_session()
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": "closed"})
                            break
                        if detail == "__tab_closed__":
                            remaining = [p for p in self.ctx.pages if not p.is_closed()]
                            if remaining:
                                self.active_page = remaining[-1]
                                page = self.active_page
                                await self.attach_screencast(page)
                                await self.settle(page)
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                            await self.persist_meta_and_preview(save_shot=True)
                            await self.emit_state()
                            continue
                        if isinstance(detail, str) and detail.startswith("__switch__:"):
                            idx = int(detail.split(":", 1)[1])
                            self.active_page = self.ctx.pages[idx]
                            page = self.active_page
                            await page.bring_to_front()
                            await self.attach_screencast(page)
                            await self.settle(page)
                            await self.push_snapshot_frame()
                            await self.emit_state()
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                            continue

                        log.append({"step": i + 1, "what": what, "ok": True, "detail": detail})
                        if (a.get("action") or "").lower() in ("goto", "click", "click_at", "drag", "drag_at", "hold", "press", "scroll", "back", "forward", "reload", "switch_tab", "fill", "type"):
                            await self.persist_meta_and_preview(save_shot=False)
                            await self.emit_state()
                    except PWTimeout:
                        log.append({"step": i + 1, "what": what, "ok": False, "detail": "timed out: the element was not found or not usable"})
                        break
                    except (PWError, ValueError) as e:
                        log.append({"step": i + 1, "what": what, "ok": False, "detail": str(e).splitlines()[0][:300]})
                        break

                    if self.ctx.pages and self.ctx.pages[-1] is not page and not self.ctx.pages[-1].is_closed() and (a.get("action") or "").lower() not in ("tabs", "switch_tab"):
                        self.active_page = self.ctx.pages[-1]
                        page = self.active_page
                        await self.attach_screencast(page)
                        await self.settle(page)
                        await self.emit_state()

                await self.wait_if_paused()
                await self.hide_cursor(page)

                if not closed and page and not page.is_closed() and page.url != "about:blank":
                    snapshot = await self.collect_snapshot(page, int(cfg.get("max_text") or 3500))
                    close_after = bool(cfg.get("close_after"))
                    if not close_after:
                        await self.persist_meta_and_preview(save_shot=True)
                    if cfg.get("screenshot", True):
                        shot = name_for(cfg.get("shot") or "screenshot", ".png")
                        shot_path = inside(shot)
                        if shot_path:
                            await page.screenshot(path=shot_path, full_page=bool(cfg.get("full_page")))
                            if shot not in files:
                                files.append(shot)
                    await self.save_storage_state()
                    await self.push_snapshot_frame()
                    if close_after:
                        await self.close_session()
                elif not closed:
                    await self.save_storage_state()
            except asyncio.CancelledError:
                await self.hide_cursor(self.active_page)
                log.append({"step": len(log), "what": "cancelled", "ok": False, "detail": "cancelled"})
                raise
            finally:
                await self.hide_cursor(self.active_page)
                self.agent_active = False
                if not self.should_exit:
                    await self.emit_state()

            return {
                "page": snapshot,
                "log": log,
                "files": files,
                "errors": self.errors[:6],
                "dialogs": self.dialogs[:5],
            }

    async def collect_snapshot(self, page, max_text):
        snap = await page.evaluate(SNAPSHOT, max_text)
        items = list(snap.get("items") or [])
        if len(items) < 70:
            for idx, f in enumerate(page.frames[1:], start=1):
                if idx > 8 or not f.url or f.url.startswith("about:"):
                    continue
                try:
                    sub = await f.evaluate(SNAPSHOT, 800)
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

    async def _resolve_scope(self, page, a):
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

    async def _box_center(self, loc):
        box = await loc.bounding_box(timeout=self.timeout_ms)
        if not box:
            raise ValueError("element has no visible bounding box")
        return box["x"] + box["width"] / 2.0, box["y"] + box["height"] / 2.0

    async def _act_step(self, page, a, files):
        kind = (a.get("action") or "").lower()
        scope = await self._resolve_scope(page, a)
        sel = a.get("selector") or ""
        loc = scope.locator(sel).first if sel else None

        def need():
            if loc is None:
                raise ValueError("this action needs a selector")
            return loc

        if kind == "goto":
            resp = await page.goto(target(a.get("url")), wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
            await self.settle(page)
            await self.push_snapshot_frame()
            return page.url + status_note(resp)

        if kind == "click":
            el = need()
            try:
                await el.scroll_into_view_if_needed(timeout=self.timeout_ms)
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 8)
                await self.show_cursor(page, cx, cy, clicking=True)
            except Exception:
                pass
            await el.click(timeout=self.timeout_ms, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
            await self.settle(page, 3000, 260)
            await self.hide_cursor(page)
            return ""

        if kind == "click_at":
            tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
            await self.smooth_move(page, tx, ty, 10)
            await self.show_cursor(page, tx, ty, clicking=True)
            await page.mouse.click(tx, ty, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
            await self.settle(page, 3000, 260)
            await self.hide_cursor(page)
            return ""

        if kind == "move_at":
            tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
            await self.smooth_move(page, tx, ty, int(a.get("steps") or 12))
            await page.wait_for_timeout(200)
            return ""

        if kind in ("drag", "drag_at"):
            if loc is not None and a.get("to_selector"):
                sx, sy = await self._box_center(need())
                tx, ty = await self._box_center(scope.locator(a["to_selector"]).first)
            elif loc is not None and (a.get("to_x") is not None or a.get("to_y") is not None):
                sx, sy = await self._box_center(need())
                tx = float(a.get("to_x") if a.get("to_x") is not None else sx)
                ty = float(a.get("to_y") if a.get("to_y") is not None else sy)
            else:
                sx, sy = float(a.get("x") or 0), float(a.get("y") or 0)
                tx, ty = float(a.get("to_x") or sx), float(a.get("to_y") or sy)
            await self.smooth_move(page, sx, sy, 10)
            await page.mouse.down()
            await self.smooth_move(page, tx, ty, int(a.get("steps") or 20))
            await page.mouse.up()
            await self.settle(page, 2500, 220)
            await self.hide_cursor(page)
            return ""

        if kind == "hold":
            if loc is not None:
                tx, ty = await self._box_center(need())
            else:
                tx, ty = float(a.get("x") or self.mouse_pos[0]), float(a.get("y") or self.mouse_pos[1])
            await self.smooth_move(page, tx, ty, 10)
            await self.show_cursor(page, tx, ty, clicking=True)
            await page.mouse.down()
            await page.wait_for_timeout(max(100, min(int(a.get("ms") or 1500), 15000)))
            await page.mouse.up()
            await self.settle(page, 3000, 260)
            await self.hide_cursor(page)
            return ""

        if kind == "tabs":
            out_tabs = []
            for idx, p in enumerate(self.ctx.pages):
                if p.is_closed():
                    continue
                try:
                    out_tabs.append({"i": idx, "url": p.url, "title": await p.title()})
                except Exception:
                    out_tabs.append({"i": idx, "url": "", "title": ""})
            return json.dumps(out_tabs, ensure_ascii=False)

        if kind == "switch_tab":
            idx = a.get("index")
            if idx is not None:
                i = int(idx)
                if 0 <= i < len(self.ctx.pages):
                    return f"__switch__:{i}"
                raise ValueError(f"tab index {i} out of range")
            needle = str(a.get("url") or a.get("title") or "").lower()
            for i, p in enumerate(self.ctx.pages):
                try:
                    ptitle = await p.title()
                except Exception:
                    ptitle = ""
                if needle and (needle in (p.url or "").lower() or needle in (ptitle or "").lower()):
                    return f"__switch__:{i}"
            raise ValueError("matching tab not found")

        if kind == "close_tab":
            if len([p for p in self.ctx.pages if not p.is_closed()]) > 1:
                await page.close()
                return "__tab_closed__"
            return "__closed__"

        if kind == "close":
            return "__closed__"

        if kind == "fill":
            el = need()
            txt = str(a.get("text", ""))
            try:
                await el.scroll_into_view_if_needed(timeout=self.timeout_ms)
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 6)
            except Exception:
                pass
            if len(txt) <= 120:
                try:
                    tag_info = await el.evaluate("el => ({tag: el.tagName.toLowerCase(), type: (el.type||'').toLowerCase(), editable: el.isContentEditable})")
                    tname = tag_info.get("tag")
                    itype = tag_info.get("type")
                    if (tname == "textarea" or (tname == "input" and itype in ("", "text", "search", "email", "url", "tel", "password", "number")) or tag_info.get("editable")):
                        await el.fill("", timeout=self.timeout_ms)
                        await el.press_sequentially(txt, delay=14, timeout=self.timeout_ms)
                        return ""
                except Exception:
                    pass
            await el.fill(txt, timeout=self.timeout_ms)
            return ""

        if kind == "type":
            delay = max(0, min(int(a.get("delay_ms") or 18), 200))
            txt = str(a.get("text", ""))
            if loc is not None:
                try:
                    await loc.scroll_into_view_if_needed(timeout=self.timeout_ms)
                    cx, cy = await self._box_center(loc)
                    await self.smooth_move(page, cx, cy, 6)
                except Exception:
                    pass
                if a.get("clear"):
                    await loc.fill("", timeout=self.timeout_ms)
                await loc.press_sequentially(txt, delay=delay, timeout=self.timeout_ms)
            else:
                await page.keyboard.type(txt, delay=delay)
            return ""

        if kind == "press":
            key = a.get("key") or "Enter"
            if loc is not None:
                await loc.press(key, timeout=self.timeout_ms)
            else:
                await page.keyboard.press(key)
            await self.settle(page, 3000, 260)
            return ""

        if kind == "select":
            el = need()
            v = a.get("value") or a.get("text") or ""
            try:
                got = await el.select_option(value=v, timeout=self.timeout_ms)
            except PWError:
                got = await el.select_option(label=v, timeout=self.timeout_ms)
            return ", ".join(got)

        if kind in ("check", "uncheck"):
            el = need()
            await getattr(el, kind)(timeout=self.timeout_ms)
            return ""

        if kind == "hover":
            el = need()
            try:
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 8)
            except Exception:
                pass
            await el.hover(timeout=self.timeout_ms)
            return ""

        if kind == "scroll":
            if loc is not None:
                await loc.scroll_into_view_if_needed(timeout=self.timeout_ms)
            elif a.get("to") == "bottom":
                await scope.evaluate("() => window.scrollTo({top: document.body.scrollHeight, behavior: 'smooth'})")
            elif a.get("to") == "top":
                await scope.evaluate("() => window.scrollTo({top: 0, behavior: 'smooth'})")
            else:
                await page.mouse.wheel(0, int(a.get("y") or 800))
            await page.wait_for_timeout(450)
            return ""

        if kind == "wait":
            if sel:
                await loc.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
            elif a.get("text"):
                await scope.get_by_text(str(a["text"])).first.wait_for(state="visible", timeout=min(int(a.get("ms") or 20000), 30000))
            else:
                ms = min(int(a.get("ms") or 1500), 15000)
                await page.wait_for_timeout(ms)
            return ""

        if kind == "screenshot":
            name = name_for(a.get("name") or "screenshot", ".png")
            path = inside(name)
            await self.hide_cursor(page)
            if loc is not None:
                await loc.screenshot(path=path, timeout=self.timeout_ms)
            else:
                await page.screenshot(path=path, full_page=bool(a.get("full_page")))
            files.append(name)
            return name

        if kind == "pdf":
            name = name_for(a.get("name") or "page", ".pdf")
            await self.hide_cursor(page)
            await page.emulate_media(media="screen")
            await page.pdf(path=inside(name), print_background=True, format=a.get("format") or "A4")
            files.append(name)
            return name

        if kind == "extract":
            need()
            n = min(int(a.get("limit") or 30), 100)
            every = scope.locator(sel)
            if a.get("attribute"):
                vals = (await every.evaluate_all("(els, a) => els.map(e => e.getAttribute(a))", a["attribute"]))[:n]
            else:
                vals = (await every.all_inner_texts())[:n]
            vals = [re.sub(r"\s+", " ", str(v or "")).strip()[:600] for v in vals]
            return json.dumps(vals, ensure_ascii=False)

        if kind == "eval":
            val = await scope.evaluate(str(a.get("script") or ""))
            return json.dumps(val, ensure_ascii=False, default=str)[:4000]

        if kind in ("back", "forward", "reload"):
            fn = "go_back" if kind == "back" else "go_forward" if kind == "forward" else "reload"
            await getattr(page, fn)(timeout=30000)
            await self.settle(page)
            await self.push_snapshot_frame()
            return page.url

        if kind == "upload":
            paths = [inside(f) for f in (a.get("files") or [])]
            if not paths or not all(p and os.path.isfile(p) for p in paths):
                raise ValueError("files must be names of files in the workspace")
            await need().set_input_files(paths, timeout=self.timeout_ms)
            return ""

        if kind == "download":
            async with page.expect_download(timeout=90000) as d_info:
                await need().click(timeout=self.timeout_ms)
            dl = await d_info.value
            name = name_for(a.get("name") or dl.suggested_filename, os.path.splitext(dl.suggested_filename)[1] or ".bin")
            await dl.save_as(inside(name))
            files.append(name)
            return name

        raise ValueError(f"unknown action {kind!r}")


async def launch_async(pw):
    args = [
        "--no-sandbox", "--disable-dev-shm-usage", "--no-first-run", "--mute-audio", "--hide-scrollbars",
        "--disable-blink-features=AutomationControlled",
        "--disable-background-networking", "--disable-component-update",
        "--disable-sync", "--disable-breakpad", "--disable-features=Translate,MediaRouter,OptimizationHints",
        "--js-flags=--max-old-space-size=1024", "--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
        "--font-render-hinting=none", "--force-color-profile=srgb",
    ]
    if CFG.get("offline"):
        args += ["--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>"]
    return await pw.chromium.launch(executable_path=CFG["chrome"], headless=True, args=args, chromium_sandbox=False, timeout=60000)


async def run_daemon():
    loop = asyncio.get_running_loop()
    cmd_queue = asyncio.Queue()

    def stdin_reader():
        try:
            while True:
                line = sys.stdin.readline()
                if not line:
                    loop.call_soon_threadsafe(cmd_queue.put_nowait, None)
                    break
                s = line.strip()
                if not s:
                    continue
                try:
                    msg = json.loads(s)
                except Exception:
                    continue
                loop.call_soon_threadsafe(cmd_queue.put_nowait, msg)
        except Exception:
            try:
                loop.call_soon_threadsafe(cmd_queue.put_nowait, None)
            except Exception:
                pass

    threading.Thread(target=stdin_reader, daemon=True).start()

    async with async_playwright() as pw:
        browser = await launch_async(pw)
        daemon = Daemon(pw, browser)
        await daemon.create_context(reset_state=bool(CFG.get("new_session")))
        if CFG.get("url"):
            try:
                await daemon.active_page.goto(target(CFG["url"]), wait_until="domcontentloaded", timeout=daemon.nav_timeout_ms)
                await daemon.settle(daemon.active_page, 2500, 120)
                await daemon.persist_meta_and_preview(save_shot=True)
                await daemon.push_snapshot_frame()
            except Exception:
                pass
        ready_state = await daemon.state_payload()
        emit({"event": "ready", "state": ready_state})

        async def dispatch_tool(msg_id, cfg):
            try:
                res = await daemon.run_tool(cfg)
                emit({"id": msg_id, "ok": True, "result": res, "state": await daemon.state_payload(), "closed": daemon.should_exit})
            except asyncio.CancelledError:
                emit({"id": msg_id, "ok": False, "error": "cancelled", "state": await daemon.state_payload()})
            except Exception as e:
                emit({"id": msg_id, "ok": False, "error": str(e).splitlines()[0][:300], "state": await daemon.state_payload()})
            finally:
                daemon.tool_task = None
                if daemon.should_exit:
                    await cmd_queue.put(None)

        try:
            while True:
                msg = await cmd_queue.get()
                if msg is None:
                    break
                msg_id = msg.get("id")
                op = (msg.get("op") or "").lower()

                if op == "pause":
                    daemon.paused = bool(msg.get("paused"))
                    if daemon.paused:
                        daemon.resume_event.clear()
                        await daemon.hide_cursor(daemon.active_page)
                    else:
                        daemon.resume_event.set()
                    st = await daemon.emit_state()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": st})
                    continue

                if op == "cancel_tool":
                    if daemon.tool_task is not None and not daemon.tool_task.done():
                        daemon.paused = False
                        daemon.resume_event.set()
                        daemon.tool_task.cancel()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": await daemon.state_payload()})
                    continue

                if op == "input":
                    res = await daemon.handle_input(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "snapshot_frame":
                    await daemon.push_snapshot_frame()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True})
                    continue

                if op == "status":
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": await daemon.state_payload()})
                    continue

                if op == "nav":
                    res = await daemon.handle_nav(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    if daemon.should_exit:
                        break
                    continue

                if op == "tool":
                    cfg = msg.get("cfg") or {}
                    daemon.tool_task = asyncio.create_task(dispatch_tool(msg_id, cfg))
                    continue

                if msg_id is not None:
                    emit({"id": msg_id, "ok": False, "error": f"unknown op {op!r}"})
        finally:
            if daemon.tool_task is not None and not daemon.tool_task.done():
                daemon.tool_task.cancel()
            await daemon.save_storage_state()
            try:
                await browser.close()
            except Exception:
                pass


asyncio.run(run_daemon())
