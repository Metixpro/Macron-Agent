const $ = (id) => document.getElementById(id);

const ui = {
  side: document.querySelector(".side"),
  projects: $("projects"),
  engine: $("engine"),
  pname: $("pname"),
  ppath: $("ppath"),
  mode: $("mode"),
  modelBtn: $("modelBtn"),
  modelName: $("modelName"),
  feed: $("feed"),
  inner: $("feedInner"),
  live: $("live"),
  box: $("box"),
  input: $("input"),
  queued: $("queued"),
  send: $("send"),
  stop: $("stop"),
  usage: $("usage"),
  overlay: $("overlay"),
  toast: $("toast"),
};

const S = {
  info: {},
  mode: "build",
  projects: [],
  defaultId: "",
  current: "",
  blocks: [],
  live: { running: false },
  seq: 0,
  drafts: {},
  lastTurn: null,
  dialog: null,
  askOpen: "",
  unseen: false,
};

const store = {
  get(k, d) {
    try {
      const v = localStorage.getItem("macron." + k);
      return v === null ? d : JSON.parse(v);
    } catch {
      return d;
    }
  },
  set(k, v) {
    try {
      localStorage.setItem("macron." + k, JSON.stringify(v));
    } catch {}
  },
};

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined && text !== null) n.textContent = text;
  return n;
}

function add(parent, ...kids) {
  for (const k of kids) if (k) parent.append(k);
  return parent;
}

class ApiError extends Error {
  constructor(title, hint, detail) {
    super(title);
    this.hint = hint || "";
    this.detail = detail || "";
  }
}

async function api(method, path, body) {
  const opt = { method, headers: { "X-Macron": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opt.headers["Content-Type"] = "application/json";
    opt.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opt);
  } catch {
    throw new ApiError("The Macron engine is not answering", "It may have closed. Start Macron again.");
  }
  const type = res.headers.get("Content-Type") || "";
  const data = type.includes("json") ? await res.json().catch(() => ({})) : { error: (await res.text()).trim() };
  if (!res.ok) throw new ApiError(data.error || "Request failed (" + res.status + ")", data.hint, data.detail);
  return data;
}

function toast(text, ms = 2600) {
  ui.toast.textContent = text;
  ui.toast.classList.add("on");
  clearTimeout(toast.t);
  toast.t = setTimeout(() => ui.toast.classList.remove("on"), ms);
}

function fail(err) {
  toast(err.hint ? err.message + " — " + err.hint : err.message, 4200);
}

function fmtTokens(n) {
  if (!n) return "0";
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

function fmtTook(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m " + String(s % 60).padStart(2, "0") + "s";
  return Math.floor(m / 60) + "h " + String(m % 60).padStart(2, "0") + "m";
}

function ago(ms) {
  if (!ms) return "";
  const s = (Date.now() - ms) / 1000;
  if (s < 60) return "now";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  if (s < 86400 * 7) return Math.floor(s / 86400) + "d";
  return new Date(ms).toLocaleDateString();
}

function applyTheme() {
  const pick = store.get("theme", "system");
  const dark = pick === "dark" || (pick === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyTheme);
applyTheme();

function project() {
  return S.projects.find((p) => p.id === S.current);
}

function displayName(p) {
  if (!p) return "Macron";
  return p.id === S.defaultId ? "Home" : p.name;
}

function nearBottom() {
  const f = ui.feed;
  return f.scrollHeight - f.scrollTop - f.clientHeight < 120;
}

function toBottom(force) {
  if (force || S.stick) ui.feed.scrollTop = ui.feed.scrollHeight;
}

const jump = el("button", "jump", "↓");
jump.type = "button";
jump.title = "Jump to the latest";
jump.addEventListener("click", () => {
  S.stick = true;
  toBottom(true);
});
ui.feed.parentElement.insertBefore(jump, ui.live);

ui.feed.addEventListener("scroll", () => {
  S.stick = nearBottom();
  jump.classList.toggle("on", !S.stick);
});

function bidi(root) {
  root.querySelectorAll("p, li, h1, h2, h3, h4, h5, h6, blockquote, td, th").forEach((n) => n.setAttribute("dir", "auto"));
}

function proseEl(html) {
  const d = el("div", "prose");
  d.setAttribute("dir", "auto");
  d.innerHTML = html || "";
  bidi(d);
  return d;
}

function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
  const t = el("textarea");
  t.value = text;
  t.style.position = "fixed";
  t.style.opacity = "0";
  document.body.append(t);
  t.select();
  document.execCommand("copy");
  t.remove();
  return Promise.resolve();
}

function planEl(items, live) {
  const box = el("div", "plan" + (live ? " live-plan" : ""));
  const done = items.filter((i) => i.status === "done").length;
  const head = el("div", "ph");
  add(head, el("span", "", "Plan"), el("span", "", done + " / " + items.length));
  box.append(head);
  for (const it of items) {
    const st = it.status === "done" ? "done" : it.status === "in_progress" ? "now" : "";
    const row = el("div", "it " + st);
    add(row, el("span", "bx", st === "done" ? "✓" : ""), el("span", "tx", it.text));
    row.querySelector(".tx").setAttribute("dir", "auto");
    box.append(row);
  }
  return box;
}

function toolEl(b) {
  const d = el("details", "tool" + (b.failed ? " fail" : ""));
  const s = el("summary");
  add(s, el("span", "st", b.failed ? "✕" : "✓"), el("span", "lb", b.label || b.name), el("span", "ar", b.arg || ""), el("span", "chev", "›"));
  d.append(s);
  if (b.summary && b.summary !== b.label) d.append(el("div", "sum", b.summary));
  d.addEventListener("toggle", () => {
    if (!d.open || d.dataset.filled) return;
    d.dataset.filled = "1";
    if (b.args && b.args !== "{}") d.append(el("div", "args", prettyArgs(b.args)));
    d.append(el("pre", "out", b.output && b.output.trim() ? b.output : "(no output)"));
  });
  return d;
}

function prettyArgs(raw) {
  try {
    const v = JSON.parse(raw);
    if (v && typeof v === "object") {
      const keys = Object.keys(v);
      if (keys.length === 1 && typeof v[keys[0]] === "string") return v[keys[0]];
      return JSON.stringify(v, null, 2);
    }
  } catch {}
  return raw;
}

function blockEl(b) {
  switch (b.kind) {
    case "user": {
      const m = el("div", "msg user");
      const bub = el("div", "bubble", b.text);
      bub.setAttribute("dir", "auto");
      if (b.at) bub.title = new Date(b.at).toLocaleString();
      m.append(bub);
      return m;
    }
    case "assistant": {
      const m = el("div", "msg assistant");
      const body = el("div", "body");
      body.append(proseEl(b.html));
      const acts = el("div", "msg-actions");
      const c = el("button", "act", "Copy");
      c.type = "button";
      c.addEventListener("click", () => copyText(b.text || "").then(() => toast("Copied")));
      acts.append(c);
      if (b.at) acts.append(el("span", "when", new Date(b.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })));
      body.append(acts);
      add(m, el("div", "avatar", "M"), body);
      return m;
    }
    case "tool":
      return toolEl(b);
    case "plan": {
      try {
        return planEl(JSON.parse(b.text) || [], false);
      } catch {
        return null;
      }
    }
    case "divider":
      return el("div", "divider", b.text || "New conversation");
    case "error": {
      const e = el("div", "error");
      add(e, el("div", "et", b.text), b.summary ? el("div", "eh", b.summary) : null);
      if (b.output && b.output !== b.text) {
        const d = el("details");
        add(d, el("summary", "", "Details"), el("div", "", b.output));
        e.append(d);
      }
      return e;
    }
    default: {
      const n = el("div", "note", b.text);
      n.setAttribute("dir", "auto");
      return n;
    }
  }
}

const SUGGEST = [
  ["Explore this folder", "What's in here, and what is it for?", "Look around this folder and tell me what it contains and what it is for."],
  ["Fix something", "Find the bug and fix it", "Something is broken here. Find out what and fix it, then show me what you changed."],
  ["Research", "Dig into a topic with sources", "Research this for me and give me a short answer with sources: "],
  ["Automate", "Script a chore you repeat", "Write a script that automates this for me: "],
];

function emptyEl() {
  const p = project();
  const e = el("div", "empty");
  add(e, el("div", "big", "M"), el("h2", "", p && p.id !== S.defaultId ? displayName(p) : "What should we do?"));
  e.append(el("p", "", p && p.id !== S.defaultId ? "Macron works inside " + p.path : "Ask anything. Open a folder on the left to work on a project."));
  const g = el("div", "sugg");
  for (const [t, d, prompt] of SUGGEST) {
    const b = el("button");
    b.type = "button";
    add(b, el("b", "", t), el("span", "", d));
    b.addEventListener("click", () => {
      ui.input.value = prompt;
      grow();
      ui.input.focus();
      ui.input.setSelectionRange(prompt.length, prompt.length);
    });
    g.append(b);
  }
  e.append(g);
  return e;
}

let streamNode = null;

function streamEl() {
  if (streamNode) return streamNode;
  const m = el("div", "msg assistant streaming");
  const body = el("div", "body");
  body.append(proseEl(""));
  add(m, el("div", "avatar", "M"), body);
  streamNode = m;
  return m;
}

function setStream(html) {
  if (!html) {
    if (streamNode) streamNode.remove();
    streamNode = null;
    return;
  }
  const n = streamEl();
  const prose = n.querySelector(".prose");
  prose.innerHTML = html;
  bidi(prose);
  if (!n.isConnected) ui.inner.append(n);
}

function renderFeed() {
  streamNode = null;
  ui.inner.replaceChildren();
  if (!S.blocks.length && !S.live.running) {
    ui.inner.append(emptyEl());
    return;
  }
  const frag = document.createDocumentFragment();
  for (const b of S.blocks) {
    const n = blockEl(b);
    if (n) frag.append(n);
  }
  ui.inner.append(frag);
  if (S.live.html) setStream(S.live.html);
}

function appendBlock(b) {
  S.blocks.push(b);
  const empty = ui.inner.querySelector(".empty");
  if (empty) empty.remove();
  const n = blockEl(b);
  if (!n) return;
  if (streamNode && streamNode.isConnected) ui.inner.insertBefore(n, streamNode);
  else ui.inner.append(n);
}

function renderLive() {
  const L = S.live;
  ui.box.parentElement.classList.toggle("running", !!L.running);
  ui.live.replaceChildren();
  ui.queued.textContent = L.queued && L.queued.length ? L.queued.length + " queued" : "";
  if (!L.running) return;
  const card = el("div", "live-card");
  if (L.plan && L.plan.length) card.append(planEl(L.plan, true));
  for (const t of L.tools || []) {
    const row = el("div", "live-row");
    const tm = el("span", "tm", fmtTook(Date.now() - t.started));
    tm.dataset.since = t.started;
    add(row, el("span", "spin"), el("span", "lb", t.label || t.name), el("span", "ar", t.arg || ""), tm);
    card.append(row);
    if (t.progress) card.append(el("div", "live-prog", t.progress));
  }
  if (L.subs && L.subs.length) {
    const subs = el("div", "live-subs");
    for (const s of L.subs) {
      const c = el("span", "chip" + (s.failed ? " failed" : s.done ? " done" : ""));
      add(c, el("span", "s"), el("span", "", s.title), s.step && !s.done ? el("span", "st", s.step) : null);
      c.title = s.steps + " steps";
      subs.append(c);
    }
    card.append(subs);
  }
  if (!(L.tools && L.tools.length) && !L.html) {
    const row = el("div", "thinking");
    const dots = el("span", "dots");
    add(dots, el("span"), el("span"), el("span"));
    const tm = el("span", "tm", fmtTook(Date.now() - (L.started || Date.now())));
    tm.dataset.since = L.started || Date.now();
    add(row, dots, el("span", "", S.mode === "plan" ? "Planning…" : "Working…"), tm);
    card.append(row);
    if (L.thought) {
      const th = el("div", "thought", L.thought);
      th.setAttribute("dir", "auto");
      card.append(th);
    }
  }
  if (card.childElementCount) ui.live.append(card);
}

setInterval(() => {
  ui.live.querySelectorAll("[data-since]").forEach((n) => (n.textContent = fmtTook(Date.now() - Number(n.dataset.since))));
}, 1000);

function renderUsage() {
  const L = S.live;
  const parts = [];
  if (L.context && L.context.window) parts.push("context " + Math.round((100 * L.context.used) / L.context.window) + "%");
  if (S.lastTurn) parts.push("last reply " + fmtTook(S.lastTurn.took));
  const u = L.usage || S.usage;
  if (u && (u.in || u.out)) parts.push(fmtTokens(u.in) + " in · " + fmtTokens(u.out) + " out");
  ui.usage.textContent = parts.join(" · ");
}

function renderHeader() {
  const p = project();
  ui.pname.textContent = displayName(p);
  ui.ppath.textContent = p ? p.path : "";
  document.title = (S.unseen ? "● " : "") + (p && p.id !== S.defaultId ? displayName(p) + " — Macron" : "Macron");
  ui.mode.querySelectorAll("button").forEach((b) => b.classList.toggle("on", b.dataset.mode === S.mode));
  ui.box.classList.toggle("plan", S.mode === "plan");
  ui.input.placeholder = S.mode === "plan" ? "Describe what you want — Macron plans first and changes nothing" : "Ask Macron anything… build, fix, research, automate";
  const i = S.info;
  ui.modelName.textContent = i.model || "No model";
  ui.modelBtn.title = i.provider ? i.provider + (i.effort ? " · " + i.effort : "") : "Pick a model";
  if (ui.engine.classList.contains("ok")) ui.engine.querySelector(".engine-text").textContent = "Engine ready" + (i.version ? " · " + i.version : "");
}

function renderProjects() {
  ui.projects.replaceChildren();
  for (const p of S.projects) {
    const b = el("button", "proj" + (p.id === S.current ? " on" : ""));
    b.type = "button";
    b.title = p.path;
    const badge = el("span", "badge" + (p.asking ? " ask" : p.running ? " run" : ""));
    add(b, el("span", "pn", displayName(p)), badge);
    const pv = el("span", "pv", p.preview || (p.id === S.defaultId ? "General chat" : p.path));
    pv.setAttribute("dir", "auto");
    b.append(pv);
    if (p.updated && !p.running && !p.asking) badge.replaceWith(el("span", "ago", ago(p.updated)));
    b.addEventListener("click", () => {
      openProject(p.id);
      ui.side.classList.remove("open");
    });
    if (p.id !== S.defaultId) {
      const x = el("span", "x", "×");
      x.title = "Remove from the list (files stay)";
      x.addEventListener("click", (e) => {
        e.stopPropagation();
        removeProject(p);
      });
      b.append(x);
    }
    ui.projects.append(b);
  }
}

async function loadState() {
  const st = await api("GET", "/api/state");
  S.info = st.info || {};
  S.mode = st.mode || "build";
  S.projects = st.projects || [];
  S.defaultId = st.default || "";
  if (!S.projects.find((p) => p.id === S.current)) S.current = store.get("project", "");
  if (!S.projects.find((p) => p.id === S.current)) S.current = S.defaultId || (S.projects[0] && S.projects[0].id) || "";
  renderProjects();
  renderHeader();
}

let stateTimer = 0;
function refreshState() {
  clearTimeout(stateTimer);
  stateTimer = setTimeout(() => loadState().catch(() => {}), 250);
}

async function openProject(id) {
  if (id !== S.current) {
    S.drafts[S.current] = ui.input.value;
    store.set("drafts", S.drafts);
  }
  S.current = id;
  store.set("project", id);
  const snap = await api("GET", "/api/projects/" + id);
  if (S.current !== id) return;
  S.blocks = snap.blocks || [];
  S.live = snap.live || { running: false };
  S.usage = S.live.usage;
  S.lastTurn = null;
  ui.input.value = S.drafts[id] || "";
  grow();
  renderProjects();
  renderHeader();
  renderFeed();
  renderLive();
  renderUsage();
  S.stick = true;
  toBottom(true);
  if (S.live.ask) showAsk(S.live.ask);
  else closeAsk();
}

async function removeProject(p) {
  try {
    await api("DELETE", "/api/projects/" + p.id);
    if (S.current === p.id) S.current = S.defaultId;
    await loadState();
    await openProject(S.current);
  } catch (e) {
    fail(e);
  }
}

function grow() {
  ui.input.style.height = "auto";
  ui.input.style.height = Math.min(ui.input.scrollHeight, 240) + "px";
  ui.send.disabled = !ui.input.value.trim();
}

const COMMANDS = [
  ["/new", "Start a new conversation", () => newChat()],
  ["/compact", "Summarise the conversation to free room", () => compact()],
  ["/model", "Pick a model", () => modelPicker()],
  ["/plan", "Plan first, change nothing", () => setMode("plan")],
  ["/build", "Let Macron make changes", () => setMode("build")],
  ["/open", "Open a folder", () => folderPicker()],
  ["/settings", "Approvals, providers, theme", () => settings()],
  ["/stop", "Stop the current job", () => stop()],
];

const slash = el("div", "slash");
ui.box.parentElement.insertBefore(slash, ui.box);
let slashSel = 0;

function slashMatches() {
  const v = ui.input.value;
  if (!v.startsWith("/") || /\s/.test(v)) return [];
  return COMMANDS.filter((c) => c[0].startsWith(v.toLowerCase()));
}

function renderSlash() {
  const list = slashMatches();
  slash.replaceChildren();
  slash.classList.toggle("on", list.length > 0);
  slashSel = Math.min(slashSel, Math.max(list.length - 1, 0));
  list.forEach((c, i) => {
    const b = el("button", "item" + (i === slashSel ? " sel" : ""));
    b.type = "button";
    add(b, el("span", "cmd", c[0]), el("span", "meta", c[1]));
    b.addEventListener("mousedown", (e) => {
      e.preventDefault();
      runCommand(c);
    });
    slash.append(b);
  });
}

function runCommand(c) {
  ui.input.value = "";
  grow();
  renderSlash();
  c[2]();
}

async function send() {
  const text = ui.input.value.trim();
  if (!text) return;
  const cmd = COMMANDS.find((c) => c[0] === text.toLowerCase());
  if (cmd) return runCommand(cmd);
  ui.input.value = "";
  S.drafts[S.current] = "";
  store.set("drafts", S.drafts);
  grow();
  renderSlash();
  S.stick = true;
  try {
    await api("POST", "/api/projects/" + S.current + "/send", { text });
  } catch (e) {
    ui.input.value = text;
    grow();
    fail(e);
  }
}

async function stop() {
  if (!S.live.running) return;
  try {
    await api("POST", "/api/projects/" + S.current + "/stop");
  } catch (e) {
    fail(e);
  }
}

async function newChat() {
  if (S.live.running) return toast("Stop the current job first");
  try {
    await api("POST", "/api/projects/" + S.current + "/new");
    toast("New conversation");
    ui.input.focus();
  } catch (e) {
    fail(e);
  }
}

async function compact() {
  if (S.live.running) return toast("Wait for the current job to finish");
  toast("Compacting…", 60000);
  try {
    await api("POST", "/api/projects/" + S.current + "/compact");
    toast("Conversation compacted");
  } catch (e) {
    fail(e);
  }
}

async function setMode(mode) {
  try {
    const st = await api("POST", "/api/mode", { mode });
    S.mode = st.mode;
    renderHeader();
    toast(mode === "plan" ? "Plan mode — Macron plans and changes nothing" : "Build mode — Macron can make changes");
  } catch (e) {
    fail(e);
  }
}

ui.input.addEventListener("input", () => {
  grow();
  slashSel = 0;
  renderSlash();
  S.drafts[S.current] = ui.input.value;
  clearTimeout(ui.input.t);
  ui.input.t = setTimeout(() => store.set("drafts", S.drafts), 400);
});

ui.input.addEventListener("keydown", (e) => {
  if (e.isComposing || e.keyCode === 229) return;
  const list = slashMatches();
  if (list.length) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      slashSel = (slashSel + (e.key === "ArrowDown" ? 1 : list.length - 1)) % list.length;
      renderSlash();
      return;
    }
    if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
      e.preventDefault();
      runCommand(list[slashSel]);
      return;
    }
  }
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    send();
  }
});

ui.input.addEventListener("blur", () => setTimeout(() => slash.classList.remove("on"), 120));
ui.send.addEventListener("click", send);
ui.stop.addEventListener("click", stop);
$("newChat").addEventListener("click", newChat);
$("toggleSide").addEventListener("click", () => ui.side.classList.toggle("open"));
$("addFolder").addEventListener("click", () => folderPicker());
$("openSettings").addEventListener("click", () => settings());
ui.modelBtn.addEventListener("click", () => modelPicker());
ui.mode.addEventListener("click", (e) => {
  const b = e.target.closest("button[data-mode]");
  if (b && b.dataset.mode !== S.mode) setMode(b.dataset.mode);
});

ui.inner.addEventListener("click", (e) => {
  const c = e.target.closest(".code .copy");
  if (!c) return;
  const code = c.closest(".code").querySelector("pre");
  copyText(code ? code.innerText : "").then(() => {
    c.textContent = "Copied";
    setTimeout(() => (c.textContent = "Copy"), 1400);
  });
});

document.addEventListener("keydown", (e) => {
  const mod = e.ctrlKey || e.metaKey;
  if (e.key === "Escape") {
    if (S.askOpen) return;
    if (S.dialog) return closeDialog();
    if (ui.side.classList.contains("open")) return ui.side.classList.remove("open");
    if (S.live.running) return stop();
  }
  if (S.askOpen) return;
  if (mod && e.key.toLowerCase() === "n") {
    e.preventDefault();
    newChat();
  } else if (mod && e.key.toLowerCase() === "k") {
    e.preventDefault();
    modelPicker();
  } else if (mod && e.key.toLowerCase() === "o") {
    e.preventDefault();
    folderPicker();
  } else if (mod && e.key === ",") {
    e.preventDefault();
    settings();
  } else if (mod && e.shiftKey && e.key.toLowerCase() === "p") {
    e.preventDefault();
    setMode(S.mode === "plan" ? "build" : "plan");
  } else if (!S.dialog && !mod && !e.altKey && e.key.length === 1 && document.activeElement === document.body) {
    ui.input.focus();
  }
});

window.addEventListener("focus", () => {
  if (S.unseen) {
    S.unseen = false;
    renderHeader();
  }
});

function openDialog(cls, build) {
  closeDialog();
  const d = el("div", "dialog " + cls);
  d.setAttribute("role", "dialog");
  d.setAttribute("aria-modal", "true");
  build(d);
  ui.overlay.replaceChildren(d);
  ui.overlay.classList.add("on");
  S.dialog = d;
  return d;
}

function closeDialog() {
  if (S.askOpen) return;
  ui.overlay.classList.remove("on");
  ui.overlay.replaceChildren();
  S.dialog = null;
  ui.input.focus();
}

ui.overlay.addEventListener("mousedown", (e) => {
  if (e.target === ui.overlay && !S.askOpen) closeDialog();
});

function btn(text, cls, fn) {
  const b = el("button", "btn " + (cls || ""), text);
  b.type = "button";
  b.addEventListener("click", fn);
  return b;
}

function showAsk(a) {
  S.askOpen = "";
  closeDialog();
  S.askOpen = a.id;
  const answer = async (choice) => {
    const id = a.id;
    S.askOpen = "";
    closeDialog();
    try {
      await api("POST", "/api/projects/" + S.current + "/answer", { id, choice });
      if (choice === "always") toast("Allowed from now on");
    } catch (e) {
      fail(e);
    }
  };
  openDialog("ask", (d) => {
    add(d, el("div", "warn-ico", "!"), el("h3", "", a.title || "Allow this?"));
    d.append(el("div", "sub", "Macron wants to run this in " + displayName(project()) + "."));
    d.append(el("pre", "", a.command));
    for (const f of a.findings || []) {
      const r = el("div", "finding");
      add(r, el("span", "", "⚠"), el("span", "", f));
      d.append(r);
    }
    if (a.guardian) d.append(el("div", "guardian", a.guardian));
    const row = el("div", "row");
    const left = el("div", "left");
    const once = btn("Allow once", "primary", () => answer("once"));
    add(left, once, btn("Allow this session", "", () => answer("session")), btn("Always allow", "", () => answer("always")));
    add(row, left, btn("Deny", "danger", () => answer("deny")));
    d.append(row);
    d.append(el("div", "help keys", "Enter allows once · Esc denies"));
    const opened = Date.now();
    d.tabIndex = -1;
    d.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        answer("deny");
      } else if (e.key === "Enter" && e.target === d) {
        e.preventDefault();
        if (Date.now() - opened > 700) answer("once");
      }
    });
    setTimeout(() => d.focus(), 30);
  });
}

function closeAsk(id) {
  if (!S.askOpen || (id && S.askOpen !== id)) return;
  S.askOpen = "";
  closeDialog();
}

async function modelPicker() {
  const d = openDialog("picker", (d) => {
    add(d, el("h3", "", "Model"), el("div", "sub", S.info.provider ? "From " + S.info.provider : "Pick the model Macron thinks with"));
    const q = el("input");
    q.placeholder = "Search or type a model name";
    q.setAttribute("spellcheck", "false");
    const list = el("div", "list");
    list.append(el("div", "meta", "Loading models…"));
    add(d, q, list);
    setTimeout(() => q.focus(), 30);
  });
  let models = [];
  let current = S.info.model;
  try {
    const r = await api("GET", "/api/models");
    models = r.models || [];
    current = r.current || current;
  } catch (e) {
    if (S.dialog !== d) return;
    d.querySelector(".list").replaceChildren(el("div", "meta", e.message + (e.hint ? " — " + e.hint : "")));
  }
  if (S.dialog !== d) return;
  const q = d.querySelector("input");
  const list = d.querySelector(".list");
  let sel = 0;
  const pick = async (name) => {
    closeDialog();
    try {
      const st = await api("POST", "/api/model", { name });
      S.info = st.info;
      renderHeader();
      toast("Model: " + name);
    } catch (e) {
      fail(e);
    }
  };
  const draw = () => {
    const f = q.value.trim().toLowerCase();
    const shown = models.filter((m) => m.toLowerCase().includes(f)).slice(0, 300);
    if (f && !models.includes(q.value.trim())) shown.push(q.value.trim());
    sel = Math.min(sel, Math.max(shown.length - 1, 0));
    list.replaceChildren();
    if (!shown.length) list.append(el("div", "meta", models.length ? "No match" : "The provider did not list any models. Type a name."));
    shown.forEach((m, i) => {
      const b = el("button", "item" + (i === sel ? " sel" : ""));
      b.type = "button";
      add(b, el("span", "", m), m === current ? el("span", "tick", "✓") : !models.includes(m) ? el("span", "meta", "use this name") : null);
      b.addEventListener("click", () => pick(m));
      list.append(b);
    });
    const s = list.querySelector(".sel");
    if (s) s.scrollIntoView({ block: "nearest" });
    return shown;
  };
  let shown = draw();
  q.addEventListener("input", () => {
    sel = 0;
    shown = draw();
  });
  q.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!shown.length) return;
      sel = (sel + (e.key === "ArrowDown" ? 1 : shown.length - 1)) % shown.length;
      shown = draw();
    } else if (e.key === "Enter" && shown[sel]) {
      e.preventDefault();
      pick(shown[sel]);
    }
  });
}

async function folderPicker(start) {
  let cur = null;
  const d = openDialog("picker folders", (d) => {
    add(d, el("h3", "", "Open a folder"), el("div", "sub", "Macron works inside the folder you pick. Each folder keeps its own conversation."));
    const q = el("input");
    q.placeholder = "Paste a path, or browse below";
    q.setAttribute("spellcheck", "false");
    q.setAttribute("dir", "ltr");
    add(d, q, el("div", "roots"), el("div", "crumbs"), el("div", "list"));
    const row = el("div", "row");
    add(row, btn("Cancel", "", closeDialog), btn("Open this folder", "primary", () => choose()));
    d.append(row);
    q.addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        go(q.value.trim());
      }
    });
    setTimeout(() => q.focus(), 30);
  });
  const choose = async () => {
    if (!cur) return;
    try {
      const p = await api("POST", "/api/projects", { path: cur.path });
      closeDialog();
      await loadState();
      await openProject(p.id);
    } catch (e) {
      fail(e);
    }
  };
  const go = async (path) => {
    try {
      const r = await api("GET", "/api/fs?path=" + encodeURIComponent(path || ""));
      if (S.dialog !== d) return;
      cur = r;
      d.querySelector("input").value = r.path;
      d.querySelector(".crumbs").textContent = r.path;
      const roots = d.querySelector(".roots");
      roots.replaceChildren();
      if (r.home) roots.append(btn("Home", "", () => go(r.home)));
      if (r.parent) roots.append(btn("↑ Up", "", () => go(r.parent)));
      for (const root of r.roots || []) if (root !== "/" || r.path !== "/") roots.append(btn(root, "", () => go(root)));
      const list = d.querySelector(".list");
      list.replaceChildren();
      if (!(r.dirs || []).length) list.append(el("div", "meta", "No folders inside"));
      for (const name of r.dirs || []) {
        const b = el("button", "item");
        b.type = "button";
        add(b, el("span", "", "📁  " + name), el("span", "meta", "›"));
        b.addEventListener("click", () => go(r.path.replace(/[\\/]+$/, "") + r.sep + name));
        b.addEventListener("dblclick", () => {
          cur = { path: r.path.replace(/[\\/]+$/, "") + r.sep + name };
          choose();
        });
        list.append(b);
      }
    } catch (e) {
      fail(e);
    }
  };
  go(start || "");
}

async function settings() {
  const d = openDialog("settings", (d) => {
    add(d, el("h3", "", "Settings"), el("div", "sub", "Changes apply right away."));
  });
  const group = (title, help) => {
    const g = el("div", "grp");
    g.append(el("div", "gh", title));
    const o = el("div", "opts");
    g.append(o);
    if (help) g.append(el("div", "help", help));
    d.append(g);
    return o;
  };
  const opt = (parent, text, on, fn) => {
    const b = el("button", "opt" + (on ? " on" : ""), text);
    b.type = "button";
    b.addEventListener("click", fn);
    parent.append(b);
    return b;
  };
  const draw = () => {
    d.querySelectorAll(".grp").forEach((g) => g.remove());
    const theme = store.get("theme", "system");
    const t = group("Appearance");
    for (const [k, label] of [["system", "System"], ["dark", "Dark"], ["light", "Light"]])
      opt(t, label, theme === k, () => {
        store.set("theme", k);
        applyTheme();
        draw();
      });
    const a = group("Command approvals", {
      smart: "Safe commands run on their own; risky ones ask you first.",
      manual: "Every command waits for your OK.",
      off: "Nothing asks. Only for machines you can afford to break.",
    }[S.info.approvals || "smart"]);
    for (const [k, label] of [["smart", "Smart"], ["manual", "Ask every time"], ["off", "Never ask"]])
      opt(a, label, (S.info.approvals || "smart") === k, async () => {
        try {
          const st = await api("POST", "/api/approvals", { mode: k });
          S.info = st.info;
          draw();
        } catch (e) {
          fail(e);
        }
      });
    const y = group("YOLO for 30 minutes", S.info.yolo ? "On — commands run without asking until it times out." : "Let Macron run everything without asking for a while.");
    opt(y, S.info.yolo ? "Turn off" : "Turn on", S.info.yolo, async () => {
      try {
        const st = await api("POST", "/api/yolo", { on: !S.info.yolo });
        S.info = st.info;
        draw();
      } catch (e) {
        fail(e);
      }
    });
    const pv = group("Model provider");
    pv.append(el("span", "help", "Loading…"));
    api("GET", "/api/providers")
      .then((r) => {
        pv.replaceChildren();
        const list = r.providers || [];
        if (!list.length) pv.append(el("span", "help", "None yet. Connect one with «macron setup» in a terminal."));
        for (const p of list)
          opt(pv, p.name + (p.model ? " · " + p.model : ""), p.active, async () => {
            try {
              const st = await api("POST", "/api/provider", { id: p.id });
              S.info = st.info;
              renderHeader();
              draw();
              toast("Provider: " + p.name);
            } catch (e) {
              fail(e);
            }
          });
      })
      .catch((e) => pv.replaceChildren(el("span", "help", e.message)));
    const cn = group("Connections");
    cn.classList.remove("opts");
    api("GET", "/api/connections")
      .then((r) => {
        for (const c of r.connections || []) {
          const row = el("div", "conn");
          add(row, el("span", "", c.name + (c.capability ? " — " + c.capability : "")), el("span", c.ready ? "ok" : "no", c.ready ? "connected" : "not set"));
          cn.append(row);
        }
      })
      .catch(() => {});
    const ab = group("About");
    ab.classList.remove("opts");
    const i = S.info;
    ab.append(el("div", "help", "Macron " + (i.version || "") + " · " + (i.platform || "") + (i.host ? " · " + i.host : "")));
    ab.append(el("div", "help keys", "Enter send · Shift+Enter new line · Esc stop · Ctrl+N new · Ctrl+K model · Ctrl+O open folder · Ctrl+Shift+P plan mode · / commands"));
  };
  draw();
}

function onProjectEvent(ev) {
  const L = S.live;
  switch (ev.type) {
    case "user":
      if (ev.taken) L.queued = [];
      appendBlock(ev.block);
      S.stick = true;
      break;
    case "state":
      S.live = { running: ev.running, started: ev.started, tools: [], subs: [], plan: [], queued: [], usage: L.usage, context: L.context };
      setStream("");
      break;
    case "queued":
      L.queued = ev.queued || [];
      break;
    case "text":
      L.html = ev.html;
      setStream(ev.html);
      break;
    case "thought":
      L.thought = ev.text;
      break;
    case "block":
      if (ev.block.kind === "assistant") {
        L.html = "";
        setStream("");
      }
      appendBlock(ev.block);
      break;
    case "tool_start":
      L.html = "";
      L.thought = "";
      setStream("");
      (L.tools = L.tools || []).push(ev.tool);
      break;
    case "tool_progress": {
      const t = (L.tools || []).find((t) => t.id === ev.id);
      if (t) t.progress = ev.line;
      break;
    }
    case "tool_end":
      L.tools = (L.tools || []).filter((t) => t.id !== ev.id);
      appendBlock(ev.block);
      break;
    case "plan":
      L.plan = ev.items || [];
      break;
    case "sub": {
      L.subs = L.subs || [];
      const i = L.subs.findIndex((s) => s.id === ev.sub.id);
      if (i >= 0) L.subs[i] = ev.sub;
      else L.subs.push(ev.sub);
      break;
    }
    case "context":
      L.context = { used: ev.used, window: ev.window };
      break;
    case "ask":
      L.ask = ev.ask;
      showAsk(ev.ask);
      break;
    case "ask_done":
      L.ask = null;
      closeAsk(ev.id);
      break;
    case "done":
      setStream("");
      for (const b of ev.blocks || []) appendBlock(b);
      S.live = { running: false, usage: ev.usage, context: L.context };
      S.usage = ev.usage;
      S.lastTurn = { took: ev.took };
      closeAsk();
      if (ev.requeue && ev.requeue.length) {
        ui.input.value = ev.requeue.join("\n\n") + (ui.input.value ? "\n\n" + ui.input.value : "");
        grow();
      }
      if (!document.hasFocus()) {
        S.unseen = true;
        renderHeader();
      }
      break;
    default:
      return;
  }
  renderLive();
  renderUsage();
  toBottom();
}

function onEvent(ev) {
  if (S.seq && ev.seq && ev.seq !== S.seq + 1) {
    S.seq = ev.seq;
    resync();
    return;
  }
  S.seq = ev.seq || S.seq;
  if (ev.type === "projects" || ev.type === "info") return refreshState();
  if (!ev.project) return;
  if (ev.project === S.current) onProjectEvent(ev);
  if (["user", "state", "done", "ask", "ask_done", "block"].includes(ev.type)) refreshState();
  if (ev.type === "ask" && ev.project !== S.current) {
    const p = S.projects.find((p) => p.id === ev.project);
    toast(displayName(p) + " needs your approval", 5000);
  }
}

async function resync() {
  try {
    await loadState();
    if (S.current) await openProject(S.current);
  } catch (e) {
    fail(e);
  }
}

function setEngine(ok, text) {
  ui.engine.classList.toggle("ok", ok);
  ui.engine.classList.toggle("bad", !ok);
  ui.engine.querySelector(".engine-text").textContent = text;
}

function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("open", () => {
    S.seq = 0;
    setEngine(true, "Engine ready" + (S.info.version ? " · " + S.info.version : ""));
    resync();
  });
  es.addEventListener("error", () => setEngine(false, "Reconnecting to the engine…"));
  es.addEventListener("message", (m) => {
    let ev;
    try {
      ev = JSON.parse(m.data);
    } catch {
      return;
    }
    onEvent(ev);
  });
}

S.drafts = store.get("drafts", {});
S.stick = true;
grow();
connect();
ui.input.focus();
