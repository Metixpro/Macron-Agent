import base64
import json
import mimetypes
import os
import socket
import sys
import threading
import time
from urllib.parse import unquote, urlsplit

CFG = json.load(sys.stdin)
WS = os.path.realpath(os.getcwd())
LOCAL_HOST = "waze.local"
CDN_HOSTS = {"cdn.jsdelivr.net", "fastly.jsdelivr.net", "gcore.jsdelivr.net", "cdnjs.cloudflare.com", "unpkg.com", "esm.sh", "cdn.skypack.dev"}
TYPES = {".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml",
         ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".wasm": "application/wasm", ".woff2": "font/woff2",
         ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
         ".gif": "image/gif", ".webp": "image/webp", ".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".wav": "audio/wav"}


def out(result):
    sys.stdout.write(json.dumps(result, ensure_ascii=False))
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
        "--disable-background-networking", "--disable-component-update",
        "--disable-sync", "--disable-breakpad", "--disable-features=Translate,MediaRouter,OptimizationHints",
        "--js-flags=--max-old-space-size=1024", "--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
        "--font-render-hinting=none", "--force-color-profile=srgb",
    ]
    if CFG.get("offline"):  # a page of local files: nowhere to go
        args += ["--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>"]
    return pw.chromium.launch(executable_path=CFG["chrome"], headless=True, args=args, chromium_sandbox=False, timeout=60000)


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


def fulfill(route, path):
    ext = os.path.splitext(path)[1].lower()
    ctype = TYPES.get(ext) or mimetypes.guess_type(path)[0] or "application/octet-stream"
    with open(path, "rb") as f:
        body = f.read()
    route.fulfill(status=200, body=body, headers={"Content-Type": ctype, "Access-Control-Allow-Origin": "*", "Cache-Control": "no-store"})


def serve_local(route):
    path = unquote(urlsplit(route.request.url).path)
    name = os.path.basename(path)
    target = LIBS.get(name) if path.startswith("/lib/") else None
    target = target or inside(path)
    if not target or not os.path.isfile(target):
        return route.fulfill(status=404, body="not found", headers={"Access-Control-Allow-Origin": "*"})
    fulfill(route, target)


def offline_libs(route):
    u = urlsplit(route.request.url)
    if u.hostname == LOCAL_HOST:
        return serve_local(route)
    name = os.path.basename(u.path)
    if u.hostname in CDN_HOSTS and name in LIBS:
        return fulfill(route, LIBS[name])
    route.continue_()


def local_url(name):
    return f"https://{LOCAL_HOST}/" + "/".join(p for p in name.replace("\\", "/").split("/") if p not in ("", ".", ".."))


def save_png(data, name):
    path = inside(name)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "wb") as f:
        f.write(data)
    return os.path.relpath(path, WS)
