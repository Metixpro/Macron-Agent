import { ApiError, fileURL, server, setBase } from "./api";
import { local } from "./format";
import { applyLang } from "./i18n";
import type { Lang } from "./i18n";
import { getState, setState } from "./store";
import type { Density, Dialog, MotionPref, RightTab, SettingsSectionId, Theme } from "./store";
import type { FileRef, Mode, Snapshot } from "./types";

export const composer = {
  focus: () => {},
  prepend: (_text: string) => {},
  attachFiles: (_files: File[]) => {},
};

export const follow = { on: true };

export const stream = {
  reconnect: () => {},
};

const scoped = (id: string) => (getState().machine || "local") + ":" + id;

let errorTimer = 0;

// Routine white bottom notifications are intentionally disabled; UI controls reflect state directly.
export function toast(_text: string, _ms = 2600) {
  return;
}

export function clearError() {
  window.clearTimeout(errorTimer);
  setState({ errorAlert: null });
}

export function showError(text: string, ms = 7000) {
  setState({ errorAlert: text });
  window.clearTimeout(errorTimer);
  errorTimer = window.setTimeout(() => setState({ errorAlert: null }), ms);
}

const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export function errorText(err: unknown): string {
  if (err instanceof ApiError) return capital(err.hint ? `${err.message} · ${err.hint}` : err.message);
  return capital(err instanceof Error ? err.message : String(err));
}

export function fail(err: unknown) {
  showError(errorText(err), 7000);
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

export async function setTuning(field: "effort" | "window" | "temperature", value: string | number | null, _done?: string) {
  try {
    const t = await server.setTuning(field, value);
    setState({ tuning: t });
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

function applySnapshot(snap: Snapshot) {
  const blocks = snap.blocks || [];
  const live = snap.live || { running: false };
  setState((s) => ({
    blocks,
    fresh: blocks.length,
    live,
    lastTook: 0,
    activeThread: snap.thread || s.activeThread,
    threads: snap.threads || s.threads,
    dialog: live.ask ? { kind: "ask", ask: live.ask } : s.dialog?.kind === "ask" ? null : s.dialog,
  }));
}

let opening = 0;

export async function openProject(id: string) {
  const n = ++opening;
  follow.on = true;
  setState({ current: id, sideOpen: false, changes: null });
  local.set("project:" + (getState().machine || "local"), id);
  const snap = await server.project(id);
  if (n !== opening) return;
  applySnapshot(snap);
  loadChanges(id);
}

export async function loadChanges(id = getState().current) {
  if (!id) return;
  try {
    const ch = await server.changes(id);
    if (getState().current === id) setState({ changes: ch });
  } catch {
    // Ignore errors when remote engine is older.
  }
}

export async function revertFile(path: string): Promise<boolean> {
  const id = getState().current;
  if (!id) return false;
  try {
    await server.revert(id, path);
    await loadChanges(id);
    return true;
  } catch (e) {
    fail(e);
    return false;
  }
}

export function setRightTab(tab: RightTab) {
  if (tab === "changes") {
    local.set("changesOpen", true);
    local.set("companionOpen", true);
    setState({ rightTab: "changes", changesOpen: true, companionOpen: true });
    loadChanges();
  } else if (tab === "plan") {
    local.set("planOpen", true);
    local.set("companionOpen", true);
    setState({ rightTab: "plan", planOpen: true, companionOpen: true });
  } else {
    local.set("companionOpen", true);
    setState({ rightTab: "activity", companionOpen: true });
  }
}

export function toggleChanges(open?: boolean) {
  setState((s) => {
    const next = open ?? !(s.changesOpen && s.rightTab === "changes");
    local.set("changesOpen", next);
    if (next) {
      local.set("companionOpen", true);
      loadChanges(s.current);
      return { changesOpen: true, companionOpen: true, rightTab: "changes" };
    }
    return { changesOpen: false, companionOpen: false };
  });
}

export function toggleCompanion(open?: boolean, tab?: RightTab) {
  setState((s) => {
    const currentlyOpen = s.companionOpen || s.changesOpen || (s.planOpen && s.rightTab === "plan" && Boolean(s.live.plan?.length));
    const next = open ?? !currentlyOpen;
    local.set("companionOpen", next);
    if (!next) {
      local.set("planOpen", false);
      local.set("changesOpen", false);
      return { companionOpen: false, planOpen: false, changesOpen: false };
    }
    const nextTab = tab ?? s.rightTab ?? "activity";
    if (nextTab === "changes") {
      local.set("changesOpen", true);
      loadChanges(s.current);
      return { companionOpen: true, changesOpen: true, rightTab: "changes" };
    }
    if (nextTab === "plan") {
      local.set("planOpen", true);
      return { companionOpen: true, planOpen: true, rightTab: "plan" };
    }
    return { companionOpen: true, rightTab: "activity" };
  });
}

export function toggleSidebarCollapsed(next?: boolean) {
  setState((s) => {
    const collapsed = next ?? !s.sideCollapsed;
    local.set("sideCollapsed", collapsed);
    return { sideCollapsed: collapsed };
  });
}

export function setSideWidth(width: number) {
  const clamped = Math.max(216, Math.min(360, Math.round(width)));
  local.set("sideWidth", clamped);
  setState({ sideWidth: clamped });
}

export async function send(text: string, attachments?: FileRef[]): Promise<boolean> {
  follow.on = true;
  clearError();
  try {
    await server.send(getState().current, text, attachments);
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
  if (s.live.running) return showError("Stop the current job first");
  try {
    const res = await server.newChat(s.current);
    if (res.snapshot) applySnapshot(res.snapshot);
    composer.focus();
  } catch (e) {
    fail(e);
  }
}

export async function switchThread(tid: string) {
  const s = getState();
  if (s.live.running) return showError("Stop the current job first");
  if (tid === s.activeThread) return;
  try {
    const snap = await server.switchThread(s.current, tid);
    applySnapshot(snap);
    composer.focus();
  } catch (e) {
    fail(e);
  }
}

export async function deleteThread(tid: string) {
  const s = getState();
  if (s.live.running) return showError("Stop the current job first");
  try {
    const res = await server.deleteThread(s.current, tid);
    if (res.snapshot) applySnapshot(res.snapshot);
    else setState({ activeThread: res.active, threads: res.threads });
  } catch (e) {
    fail(e);
  }
}

export async function compact() {
  const s = getState();
  if (s.live.running) return showError("Wait for the current job to finish");
  try {
    await server.compact(s.current);
  } catch (e) {
    fail(e);
  }
}

export async function runShell(command: string) {
  const s = getState();
  const cmd = command.trim();
  if (!cmd || !s.current) return false;
  if (s.live.running) {
    showError("Wait for the current job to finish");
    return false;
  }
  try {
    await server.runShell(s.current, cmd);
    return true;
  } catch (e) {
    fail(e);
    return false;
  }
}

const INIT_PROMPT =
  "Study this project (its layout, how it is built, tested and run, its conventions) and write a concise MACRON.md at its root for future sessions: what the project is, the exact build, test and run commands, the code style, and what to be careful about. If MACRON.md or AGENTS.md already exists, improve it instead of starting over. Keep it short and factual.";

export async function initProject() {
  return send(INIT_PROMPT);
}

export function exportConversation() {
  const s = getState();
  const proj = s.projects.find((p) => p.id === s.current);
  const title = proj?.name || "macron-chat";
  const lines: string[] = [
    `# ${title}`,
    "",
    `> Exported from Macron Agent ${s.info.version} (SpectorDev — https://t.me/SpectorDev)`,
    "",
  ];
  for (const b of s.blocks) {
    if (b.kind === "user" && b.text) {
      lines.push("## User", "", b.text, "");
    } else if (b.kind === "assistant" && b.text) {
      lines.push("## Macron", "", b.text, "");
    } else if (b.kind === "tool") {
      lines.push(`### Tool: ${b.label || b.name || "tool"}${b.arg ? " — `" + b.arg + "`" : ""}`, "");
      if (b.output) lines.push("```", b.output, "```", "");
    } else if (b.kind === "note" && b.text) {
      lines.push(`> ${b.text}`, "");
    }
  }
  const blob = new Blob([lines.join("\n")], { type: "text/markdown;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
  a.href = url;
  a.download = `macron-${title.replace(/[^a-zA-Z0-9_-]+/g, "-")}-${stamp}.md`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export async function setMode(mode: Mode) {
  try {
    const r = await server.setMode(mode);
    setState({ mode: r.mode, info: r.info });
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

export function setDensity(density: Density) {
  local.set("density", density);
  applyDensity(density);
  setState({ density });
}

export function applyDensity(density: Density) {
  document.documentElement.dataset.density = density;
}

export function setMotion(motion: MotionPref) {
  local.set("motion", motion);
  applyMotion(motion);
  setState({ motion });
}

export function applyMotion(motion: MotionPref) {
  document.documentElement.dataset.motion = motion;
}

export function openSettings(section?: SettingsSectionId) {
  openDialog({ kind: "settings", section });
}

export function setLang(lang: Lang) {
  local.set("lang", lang);
  applyLang(lang);
  setState({ lang });
}

export async function checkUpdate(force = false) {
  try {
    const st = await server.checkUpdate(force);
    setState({ update: st });
    return st;
  } catch {
    return null;
  }
}

export async function installUpdate() {
  setState((s) => ({ update: s.update ? { ...s.update, installing: true, error: "" } : null }));
  try {
    const st = await server.installUpdate();
    setState({ update: st });
  } catch (e) {
    setState((s) => ({ update: s.update ? { ...s.update, installing: false, error: errorText(e) } : null }));
    fail(e);
  }
}

export async function loadServers() {
  try {
    const r = await server.servers();
    setState({ servers: r.servers || [], wsl: r.wsl });
  } catch {
    return;
  }
}

export async function connectServer(id: string, secrets: { password?: string; passphrase?: string; trust?: string } = {}): Promise<boolean> {
  setState((s) => ({ servers: s.servers.map((x) => (x.id === id ? { ...x, state: "connecting", error: "" } : x)) }));
  try {
    const r = await server.connectServer(id, secrets);
    await loadServers();
    if (r.need) {
      openDialog({ kind: "server-auth", id, need: r.need, fingerprint: r.fingerprint, message: r.message });
      return false;
    }
    return true;
  } catch (e) {
    await loadServers();
    const msg = errorText(e);
    if (/not installed|too old/i.test(msg)) {
      openDialog({ kind: "server-install", id, update: /too old/i.test(msg) });
      return false;
    }
    fail(e);
    return false;
  }
}

export async function installOnServer(id: string, secrets: { password?: string; passphrase?: string; trust?: string } = {}): Promise<string> {
  const r = await server.installServer(id, secrets);
  await loadServers();
  if (r.need) {
    openDialog({ kind: "server-auth", id, need: r.need, fingerprint: r.fingerprint, message: r.message, install: true });
    return "";
  }
  setState({ dialog: null });
  await switchMachine(id);
  return r.output || "";
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
  setState({
    machine: id,
    projects: [],
    blocks: [],
    fresh: 0,
    current: "",
    changes: null,
    threads: [],
    activeThread: "default",
    live: { running: false },
    dialog: null,
    online: null,
    sideOpen: false,
    lastTook: 0,
  });
  stream.reconnect();
}

export async function authServer(id: string, secrets: { password?: string; passphrase?: string; trust?: string }, install = false) {
  if (install) {
    openDialog({ kind: "server-install", id, update: false, secrets });
    return;
  }
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
