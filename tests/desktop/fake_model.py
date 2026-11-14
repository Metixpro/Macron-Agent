import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = open(sys.argv[2], "a", encoding="utf-8")

def call(name, args, n):
    return {"id": "call_%d_%d" % (n, int(time.time() * 1000) % 100000), "type": "function", "function": {"name": name, "arguments": json.dumps(args, ensure_ascii=False)}}

CRLF = "def add(a, b):\r\n    return a + b + 1\r\n\r\n\r\ndef name():\r\n    return 'macron'\r\n"
PLAN = [{"text": "Check the folder", "status": "in_progress"}, {"text": "Write and fix calc.py", "status": "pending"}, {"text": "Hand over the result", "status": "pending"}]
DONE = [dict(i, status="done") for i in PLAN]

SCRIPT = [
    ("", [("update_plan", {"items": PLAN}), ("bash_execute", {"command": "Get-ChildItem -Name; Write-Output 'ps ok'; Write-Output 'سلام دنیا'; $PSVersionTable.PSVersion.ToString()"})]),
    ("", [("write_file", {"filename": "calc.py", "content": CRLF})]),
    ("", [("apply_patch", {"patch": "--- a/calc.py\n+++ b/calc.py\n@@\n def add(a, b):\n-    return a + b + 1\n+    return a + b\n"})]),
    ("", [("edit_file", {"filename": "calc.py", "old_text": "def name():\n    return 'macron'", "new_text": "def name():\n    return 'Macron'"})]),
    ("", [("bash_execute", {"command": "python3 -c \"import calc; print('sum', calc.add(2, 3), calc.name())\""})]),
    ("", [("bash_execute", {"command": "git --version; Start-Sleep -Milliseconds 300; Write-Output 'slept'"})]),
    ("", [("bash_execute", {"command": "Write-Output 'before'; exit 3"})]),
    ("", [("run_code", {"language": "python", "code": "print(6 * 7)"})]),
    ("", [("bash_execute", {"command": "Start-Sleep 2; Write-Output 'bgdone'", "background": True})]),
    ("", [("shell_jobs", {"action": "wait", "timeout_seconds": 20})]),
    ("", [("bash_execute", {"command": "$env:MACRON_PROBE = 'kept'; Set-Location ..; Write-Output (Get-Location).Path"})]),
    ("", [("bash_execute", {"command": "Write-Output \"probe=$env:MACRON_PROBE\"; Write-Output (Get-Location).Path"})]),
    ("", [("read_file", {"files": ["calc.py"]})]),
    ("", [("update_plan", {"items": DONE}), ("send_file", {"filename": "calc.py", "caption": "فایل نهایی"})]),
    ("## Done\n\n![calc](calc.py)\n\nAll steps ran.", []),
]

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        body = json.dumps({"data": [{"id": "fake-model"}]}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        msgs = req.get("messages", [])
        system = " ".join(m.get("content", "") if isinstance(m.get("content"), str) else "" for m in msgs if m.get("role") == "system")
        last = 0
        for i, m in enumerate(msgs):
            c = m.get("content")
            if m.get("role") == "user" and isinstance(c, str) and "smoke" in c.lower():
                last = i
        k = sum(1 for m in msgs[last + 1:] if m.get("role") == "assistant" and m.get("tool_calls"))
        LOG.write(json.dumps({"k": k, "tools": len(req.get("tools", [])), "stream": req.get("stream")}) + "\n"); LOG.flush()
        content, calls = "ok", []
        if "safety guardian" in system:
            content = json.dumps({"risk": "safe", "why": "smoke"})
        elif not req.get("tools"):
            content = "Summary: smoke test."
        elif k < len(SCRIPT):
            content, cs = SCRIPT[k]
            calls = [call(nm, a, j) for j, (nm, a) in enumerate(cs)]
        else:
            content = "Done."
        if req.get("stream"):
            self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
            def emit(d):
                self.wfile.write(b"data: " + json.dumps(d).encode() + b"\n\n"); self.wfile.flush()
            emit({"choices": [{"index": 0, "delta": {"role": "assistant"}}]})
            if content:
                emit({"choices": [{"index": 0, "delta": {"content": content}}]})
            for j, c in enumerate(calls):
                emit({"choices": [{"index": 0, "delta": {"tool_calls": [dict(index=j, **c)]}}]})
            emit({"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls" if calls else "stop"}]})
            emit({"choices": [], "usage": {"prompt_tokens": 1200, "completion_tokens": 80, "total_tokens": 1280}})
            self.wfile.write(b"data: [DONE]\n\n"); self.wfile.flush()
            return
        msg = {"role": "assistant", "content": content}
        if calls:
            msg["tool_calls"] = calls
        body = json.dumps({"choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls" if calls else "stop"}], "usage": {"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)

ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
