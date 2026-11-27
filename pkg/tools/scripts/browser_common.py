import base64
import ipaddress
import json
import mimetypes
import os
import socket
import sys
import threading
import time
from urllib.parse import unquote, urlsplit

for _stream in (sys.stdout, sys.stderr):
    if hasattr(_stream, "reconfigure"):
        try:
            _stream.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass
if hasattr(sys.stdin, "reconfigure"):
    try:
        sys.stdin.reconfigure(encoding="utf-8")
    except Exception:
        pass

if os.environ.get("MACRON_BROWSER_DAEMON") == "1":
    _init_line = sys.stdin.readline()
    CFG = json.loads(_init_line) if _init_line and _init_line.strip() else {}
else:
    CFG = json.load(sys.stdin)
WS = os.path.realpath(os.getcwd())
LOCAL_HOST = "macron.local"
METADATA_HOSTS = {"metadata.google.internal", "metadata.goog", "instance-data"}
CDN_HOSTS = {"cdn.jsdelivr.net", "fastly.jsdelivr.net", "gcore.jsdelivr.net", "cdnjs.cloudflare.com", "unpkg.com", "esm.sh", "cdn.skypack.dev"}


def is_metadata_host(host):
    h = (host or "").strip().rstrip(".").lower()
    if not h:
        return False
    if "%" in h:
        h = h.split("%", 1)[0]
    if h in METADATA_HOSTS:
        return True
    try:
        addr = ipaddress.ip_address(h)
    except ValueError:
        return False
    if isinstance(addr, ipaddress.IPv6Address) and addr.ipv4_mapped:
        addr = addr.ipv4_mapped
    s = str(addr)
    if s in ("169.254.169.254", "169.254.170.2", "100.100.100.200", "fd00:ec2::254"):
        return True
    return isinstance(addr, ipaddress.IPv4Address) and addr.is_link_local and s.startswith("169.254.169.")
TYPES = {".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml",
         ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".wasm": "application/wasm", ".woff2": "font/woff2",
         ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
         ".gif": "image/gif", ".webp": "image/webp", ".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav"}


def out(result):
    payload = json.dumps(result, ensure_ascii=False)
    try:
        sys.stdout.buffer.write(payload.encode("utf-8"))
        sys.stdout.buffer.flush()
    except AttributeError:
        sys.stdout.write(payload)
        sys.stdout.flush()


def fail(message):
    out({"error": message})
    sys.exit(0)


def forward(sock_path):
    """A TCP port on the loopback that leads to a unix socket (the bot's model bridge)."""
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.bind(("127.0.0.1", 0))
    srv.listen(128)

    def pipe(src, dst):
        try:
            while True:
                data = src.recv(65536)
                if not data:
                    break
                dst.sendall(data)
        except OSError:
            pass
        for s in (src, dst):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass

    def serve():
        while True:
            client, _ = srv.accept()
            upstream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                upstream.connect(sock_path)
            except OSError:
                client.close()
                continue
            threading.Thread(target=pipe, args=(client, upstream), daemon=True).start()
            threading.Thread(target=pipe, args=(upstream, client), daemon=True).start()

    threading.Thread(target=serve, daemon=True).start()
    return srv.getsockname()[1]


def launch(pw):
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
    return pw.chromium.launch(executable_path=CFG["chrome"], headless=True, args=args, chromium_sandbox=False, timeout=60000)


DESKTOP_UA = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"
)


def apply_stealth(ctx, locale="en-US"):
    lang = (locale or "en-US").split(",")[0].strip() or "en-US"
    base = lang.split("-")[0]
    langs = [lang] if lang == base else [lang, base]
    ctx.add_init_script(f"""
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


def clear_live_preview(*paths):
    for p in paths:
        if p and os.path.exists(p):
            try:
                os.remove(p)
            except OSError:
                pass


def inside(path):
    p = os.path.realpath(os.path.join(WS, path.lstrip("/")))
    return p if p == WS or p.startswith(WS + os.sep) else None


def load_libs():
    libs = {}
    for d in CFG.get("libs") or []:
        if os.path.isdir(d):
            for root, _, files in os.walk(d):
                for f in files:
                    libs.setdefault(f, os.path.join(root, f))
    return libs


LIBS = load_libs()


def fulfill(route, path, allow_cors=False):
    ext = os.path.splitext(path)[1].lower()
    ctype = TYPES.get(ext) or mimetypes.guess_type(path)[0] or "application/octet-stream"
    with open(path, "rb") as f:
        body = f.read()
    headers = {"Content-Type": ctype, "Cache-Control": "no-store"}
    if allow_cors:
        headers["Access-Control-Allow-Origin"] = "*"
    route.fulfill(status=200, body=body, headers=headers)


def serve_local(route):
    req = route.request
    headers = {k.lower(): v for k, v in (req.headers or {}).items()}
    sfs = (headers.get("sec-fetch-site") or "").strip().lower()
    origin = (headers.get("origin") or "").strip().lower()
    if sfs == "cross-site" or (origin and origin != f"https://{LOCAL_HOST}"):
        return route.fulfill(status=403, body="forbidden")
    try:
        page_url = req.frame.page.url
    except Exception:
        page_url = ""
    if page_url and req.resource_type != "document":
        pu = urlsplit(page_url)
        if pu.scheme in ("http", "https") and (pu.hostname or "").lower() != LOCAL_HOST:
            return route.fulfill(status=403, body="forbidden")
    path = unquote(urlsplit(req.url).path)
    name = os.path.basename(path)
    target = LIBS.get(name) if path.startswith("/lib/") else None
    target = target or inside(path)
    if not target or not os.path.isfile(target):
        return route.fulfill(status=404, body="not found")
    fulfill(route, target, allow_cors=False)


def offline_libs(route):
    u = urlsplit(route.request.url)
    if u.hostname == LOCAL_HOST:
        return serve_local(route)
    if u.hostname and is_metadata_host(u.hostname):
        return route.abort("blockedbyclient")
    name = os.path.basename(u.path)
    if u.hostname in CDN_HOSTS and name in LIBS:
        return fulfill(route, LIBS[name], allow_cors=True)
    route.continue_()


def browser_route(route):
    u = urlsplit(route.request.url)
    if u.hostname == LOCAL_HOST:
        return serve_local(route)
    if u.hostname and is_metadata_host(u.hostname):
        return route.abort("blockedbyclient")
    route.continue_()


def local_url(name):
    return f"https://{LOCAL_HOST}/" + "/".join(p for p in name.replace("\\", "/").split("/") if p not in ("", ".", ".."))


def save_png(data, name):
    path = inside(name)
    if not path:
        raise ValueError(f"invalid output path outside workspace: {name!r}")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)
    return os.path.relpath(path, WS)
