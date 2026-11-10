---
name: telegram-widgets
description: "Use before making a Telegram widget: an interactive mini app inside a message (dashboard, form, calculator, game, live status)."
version: 1.0.0
category: telegram
---

# Telegram widgets

A widget is one HTML document that Telegram runs inside the message bubble. `send_widget` sends it; its handler script on this server answers the widget's requests; `update_widget` pushes new data into it. Widgets work in the private chat with the owner.

## Steps
1. Write the HTML to a workspace file (e.g. `widgets/status.html`). Keep it small: the limit is 32768 bytes and 4096 bytes after zlib compression. Check: `python3 -c 'import sys,zlib; d=sys.stdin.buffer.read(); print(len(d), len(zlib.compress(d, 9)))' < widgets/status.html`. `send_widget` checks it too and says by how much it is over.
2. If the widget needs anything from the server (fresh numbers, a file, a command's result, saving a choice), write a handler script and test it from the shell first: `echo '{"data":"status"}' | python3 widgets/status.py status`.
3. `send_widget` with `file`, `handler`, a short `title`, and initial `chat_data` if the first view needs data. It answers with the widget's number N.
4. To show new data later: `update_widget` with `widget=N` and `chat_data`. To change the look: edit the HTML file and send it again (that is a new message).

## What the HTML may use
- Inline `<style>` and `<script>` only. Images only as `data:` URLs or inline SVG. No external scripts, styles, fonts or images; no network at all (`fetch`, `XMLHttpRequest`, `WebSocket` throw); no navigation; no keyboard; no form controls (`input`, `select`, `textarea`, buttons as form controls), `iframe`, `video` or `audio`. Make buttons from `div`s with click listeners. `alert`, `confirm` and `prompt` do nothing.
- Width fills the bubble unless `width` is given; height follows the content between 50 and 600 px and taller content is cut, with no inner scroll: keep it compact. No `vh` units.
- Budget: 2000 DOM nodes, 400 CSS rules, 64 timers, 8 MB memory, a short CPU time per task; blur, filters and transparency are expensive. Over the limits the widget stops.
- `localStorage` holds up to 4 KB per message on the device and is never sent to the bot.
- Code and data are visible to whoever sees the message: never put tokens, passwords or private paths in them.

## The JavaScript side
- `window.TelegramWidget` (undefined outside Telegram, so guard it).
- `TelegramWidget.request(data)` sends 1–64 bytes to the bot and returns a Promise of `{result}`: `result` is the handler's answer (up to 1024 bytes) or null. It rejects with an error whose `name` is `WidgetRequestError` and `code` one of `NOT_ALLOWED`, `BUSY`, `RATE_LIMITED`, `TIMEOUT`, `FAILED`, `INVALID_ANSWER`. At most 5 requests per 5 seconds.
- `TelegramWidget.chatData` and `TelegramWidget.publicData` hold the data `send_widget` and `update_widget` set. `TelegramWidget.onEvent('chatDataChanged', fn)` (also `publicDataChanged`, `themeChanged`, `activated`, `deactivated`) calls `fn` with `this` set to `TelegramWidget` and no arguments: read the new value from it.
- Also: `colorScheme`, `themeParams`, `isActive`, `ready()`, `copyText(text)` and `openLink(url)` on a tap, `HapticFeedback.impactOccurred(style)`, `notificationOccurred(type)`, `selectionChanged()`.

## The handler
- It runs for each request: `python3 FILE DATA` for `.py`, `node` for `.js`, `bash` for `.sh`, `ruby`, `php`; any other file runs directly and needs `chmod +x`. It runs in the workspace with about 11 seconds to finish.
- Input: the request data as its last argument and in `MACRON_WIDGET_DATA`; on stdin a JSON object `{"data", "widget", "title", "chat_data", "public_data", "user": {"id", "name"}}`.
- Output: what it prints is the answer the widget receives as `result` (at most 1024 bytes). To also change the widget's data, print a JSON object with `answer` and `chat_data` and/or `public_data` (strings, up to 1 KB each): the data is pushed to the widget and kept for the next request.
- When it fails, times out or answers too much, the widget receives `{"error": "…"}` and the failure is appended to `.widgets/N.log` in the workspace: read that file to see why.

## Example: a server status panel
`widgets/status.html`:
```html
<style>body{margin:0;font:14px system-ui,sans-serif}.c{padding:12px}.r{display:flex;justify-content:space-between;padding:3px 0}.b{margin-top:10px;padding:8px;border-radius:8px;text-align:center;background:#3390ec;color:#fff}</style>
<div class="c"><div id="rows">…</div><div class="b" id="go">Refresh</div></div>
<script>
var W=window.TelegramWidget,rows=document.getElementById('rows');
function show(t){var d;try{d=JSON.parse(t)}catch(e){rows.textContent=t||'—';return}
rows.innerHTML='';Object.keys(d).forEach(function(k){var r=document.createElement('div');r.className='r';
var a=document.createElement('b');a.textContent=k;var v=document.createElement('span');v.textContent=d[k];
r.appendChild(a);r.appendChild(v);rows.appendChild(r)})}
function load(){if(W)W.request('status').then(function(a){show(a.result)},function(e){rows.textContent='error: '+e.code})}
document.getElementById('go').addEventListener('click',load);
if(W){show(W.chatData);W.onEvent('chatDataChanged',function(){show(this.chatData)});W.ready()}
</script>
```
`widgets/status.py`:
```python
import json, os, shutil, time
du = shutil.disk_usage("/")
print(json.dumps({"load": "%.2f" % os.getloadavg()[0], "disk": "%d%%" % (du.used * 100 // du.total), "at": time.strftime("%H:%M:%S")}))
```
Send it with `send_widget` `file=widgets/status.html`, `handler=widgets/status.py`, `title=Server`, and `chat_data` set to one run of the handler so the first view is filled.
