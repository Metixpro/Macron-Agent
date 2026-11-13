import { ApiError, fileURL, server, setBase } from "./api";
import { local } from "./format";
import { getState, setState } from "./store";
import type { Dialog, Theme } from "./store";
import type { Mode } from "./types";

export const composer = {
  focus: () => {},
  prepend: (_text: string) => {},
};

export const follow = { on: true };

export const stream = {
  reconnect: () => {},
};

const scoped = (id: string) => (getState().machine || "local") + ":" + id;

let toastTimer = 0;

export function toast(text: string, ms = 2600) {
  setState((s) => ({ toast: { text, n: (s.toast?.n || 0) + 1 } }));
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => setState({ toast: null }), ms);
}

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export function errorText(err: unknown): string {
  if (err instanceof ApiError) return capital(err.hint ? `${err.message} · ${err.hint}` : err.message);
  return capital(err instanceof Error ? err.message : String(err));
}

export function fail(err: unknown) {
  toast(errorText(err), 5000);
}

const drafts: Record<string, string> = local.get("drafts", {});
let draftTimer = 0;

export function draftFor(id: string): string {
  return drafts[scoped(id)] || "";
}

export function saveDraft(id: string, text: string) {
  if (!id) return;
  drafts[scoped(id)] = text;
  window.clearTimeout(draftTimer);
  draftTimer = window.setTimeout(() => local.set("drafts", drafts), 400);
}

export async function loadState() {
  const st = await server.state();
  const projects = st.projects || [];
  setState((s) => {
    const ids = projects.map((p) => p.id);
    let current = s.current;
    if (!ids.includes(current)) current = local.get("project:" + (s.machine || "local"), "");
    if (!ids.includes(current)) current = st.default || ids[0] || "";
    return { info: st.info, mode: st.mode, projects, defaultId: st.default, home: st.home || "", current };
  });
  loadTuning();
}

export async function loadTuning() {
  if (!getState().info.model) {
    setState({ tuning: null });
    return;
  }
  try {
    setState({ tuning: await server.tuning() });
  } catch {
    setState({ tuning: null });
  }
}

const effortNames: Record<string, string> = { "": "Auto", low: "Low", medium: "Medium", high: "High", ultracode: "Ultracode" };

export function effortName(level: string): string {
  return effortNames[level] || level;
}

export function windowName(n: number): string {
  if (n >= 1e6) return (n / 1e6).toFixed(n % 1e6 ? 1 : 0) + "M";
  return Math.round(n / 1000) + "k";
}

export const effortLevels = ["", "low", "medium", "high", "ultracode"];

export const effortAbout: Record<string, string> = {
  "": "The model's own default",
  low: "Fast and cheap: chat and quick questions",
  medium: "Balanced: most everyday work",
  high: "Deep: analysis, code and multi-step work",
  ultracode: "The model's maximum for big code and hard problems",
};

export function effortNote(t: { effortSupport: string; effortValue: string; effortParam: string }): string {
  if (t.effortSupport === "no") return "This model takes no effort setting; it runs at its default.";
  if (!t.effortValue) return "Nothing extra is sent; the model decides.";
  const sent = `Sends ${t.effortParam} = ${t.effortValue}.`;
  return t.effortSupport === "maybe" ? sent + " If the model refuses it, Macron steps down by itself." : sent;
}

export async function setTuning(field: "effort" | "window" | "temperature", value: string | number | null, done?: string) {
  try {
    const t = await server.setTuning(field, value);
    setState({ tuning: t });
    if (done) toast(done);
  } catch (e) {
    fail(e);
  }
}

let refreshTimer = 0;

export function refreshState() {
  window.clearTimeout(refreshTimer);
  refreshTimer = window.setTimeout(() => {
    loadState().catch(() => undefined);
  }, 250);
}

let opening = 0;

export async function openProject(id: string) {
  const n = ++opening;
  follow.on = true;
  setState({ current: id, sideOpen: false });
  local.set("project:" + (getState().machine || "local"), id);
  const snap = await server.project(id);
  if (n !== opening) return;
  const blocks = snap.blocks || [];
  const live = snap.live || { running: false };
  setState((s) => ({
    blocks,
    fresh: blocks.length,
    live,
    lastTook: 0,
    dialog: live.ask ? { kind: "ask", ask: live.ask } : s.dialog?.kind === "ask" ? null : s.dialog,
  }));
}

export async function send(text: string): Promise<boolean> {
  follow.on = true;
  try {
    await server.send(getState().current, text);
    return true;
  } catch (e) {
    fail(e);
    return false;
  }
}

export async function stop() {
  const s = getState();
  if (!s.live.running) return;
  try {
    await server.stop(s.current);
  } catch (e) {
    fail(e);
  }
}

export async function newChat() {
  const s = getState();
  if (s.live.running) return toast("Stop the current job first");
  try {
    await server.newChat(s.current);
    toast("New conversation");
    composer.focus();
  } catch (e) {
    fail(e);
  }
}

export async function compact() {
  const s = getState();
  if (s.live.running) return toast("Wait for the current job to finish");
  toast("Compacting the conversation…", 120000);
  try {
    await server.compact(s.current);
    toast("Conversation compacted");
  } catch (e) {
    fail(e);
  }
}

export async function setMode(mode: Mode) {
  try {
    const r = await server.setMode(mode);
    setState({ mode: r.mode, info: r.info });
    toast(mode === "plan" ? "Plan mode: Macron plans and changes nothing" : "Build mode: Macron can make changes");
  } catch (e) {
    fail(e);
  }
}

export function toggleMode() {
  return setMode(getState().mode === "plan" ? "build" : "plan");
}

export async function chooseModel(name: string) {
  try {
    const r = await server.setModel(name);
    setState({ info: r.info });
    toast("Model: " + name);
  } catch (e) {
    fail(e);
  }
}

export function openDialog(d: Dialog) {
  setState({ dialog: d });
}

export function closeDialog() {
  if (getState().dialog?.kind === "ask") return;
  setState({ dialog: null });
  composer.focus();
}

export async function answer(choice: "once" | "session" | "always" | "deny") {
  const s = getState();
  const d = s.dialog;
  if (d?.kind !== "ask") return;
  setState({ dialog: null });
  composer.focus();
  try {
    await server.answer(s.current, d.ask.id, choice);
    if (choice === "always") toast("Allowed from now on");
  } catch (e) {
    fail(e);
  }
}

export async function addFolder(path: string) {
  const p = await server.addProject(path);
  await loadState();
  await openProject(p.id);
}

export async function removeProject(id: string) {
  try {
    await server.removeProject(id);
    if (getState().current === id) setState((s) => ({ current: s.defaultId }));
    await loadState();
    await openProject(getState().current);
  } catch (e) {
    fail(e);
  }
}

export function setTheme(theme: Theme) {
  local.set("theme", theme);
  setState({ theme });
}

export function applyTheme(theme: Theme) {
  const dark = theme === "dark" || (theme === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
}

export async function loadServers() {
  try {
    const r = await server.servers();
    setState({ servers: r.servers || [], wsl: r.wsl });
  } catch {
    return;
  }
}

function serverName(id: string): string {
  return getState().servers.find((x) => x.id === id)?.name || "the server";
}

export async function connectServer(id: string, secrets: { password?: string; passphrase?: string; trust?: string } = {}): Promise<boolean> {
  const name = serverName(id);
  setState((s) => ({ servers: s.servers.map((x) => (x.id === id ? { ...x, state: "connecting", error: "" } : x)) }));
  toast(`Connecting to ${name}…`, 120000);
  try {
    const r = await server.connectServer(id, secrets);
    await loadServers();
    if (r.need) {
      setState({ toast: null });
      openDialog({ kind: "server-auth", id, need: r.need, fingerprint: r.fingerprint, message: r.message });
      return false;
    }
    toast(`Connected to ${name}`);
    return true;
  } catch (e) {
    await loadServers();
    fail(e);
    return false;
  }
}

export async function switchMachine(id: string) {
  const s = getState();
  if (id === s.machine) {
    setState({ sideOpen: false });
    return;
  }
  if (id) {
    const srv = s.servers.find((x) => x.id === id);
    if (!srv) return;
    if (srv.state !== "ready" && !(await connectServer(id))) return;
  }
  setBase(id ? "/remote/" + encodeURIComponent(id) : "");
  setState({ machine: id, projects: [], blocks: [], fresh: 0, current: "", live: { running: false }, dialog: null, online: null, sideOpen: false, lastTook: 0 });
  stream.reconnect();
}

export async function authServer(id: string, secrets: { password?: string; passphrase?: string; trust?: string }) {
  setState({ dialog: null });
  if (await connectServer(id, secrets)) await switchMachine(id);
}

export async function addServer(spec: { kind: "ssh" | "wsl"; name?: string; host?: string; user?: string; port?: number; key?: string }) {
  const v = await server.addServer(spec);
  await loadServers();
  setState({ dialog: null });
  await switchMachine(v.id);
}

export async function disconnectServer(id: string) {
  if (getState().machine === id) await switchMachine("");
  try {
    await server.disconnectServer(id);
  } catch (e) {
    fail(e);
  }
  await loadServers();
}

export async function removeServer(id: string) {
  if (getState().machine === id) await switchMachine("");
  try {
    await server.removeServer(id);
  } catch (e) {
    fail(e);
  }
  await loadServers();
}

export function onServer(): boolean {
  const s = getState();
  return s.servers.some((x) => x.id === s.machine);
}

export function saveFile(name: string, base?: string) {
  const a = document.createElement("a");
  a.href = fileURL(getState().current, name, true);
  a.download = base || name.split(/[\\/]/).pop() || "file";
  document.body.append(a);
  a.click();
  a.remove();
}

export async function openFile(name: string, folder = false) {
  if (onServer()) {
    saveFile(name);
    return;
  }
  try {
    await server.reveal(getState().current, name, folder);
  } catch (e) {
    fail(e);
  }
}
