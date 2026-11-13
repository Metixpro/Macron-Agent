import { ApiError, server } from "./api";
import { local } from "./format";
import { getState, setState } from "./store";
import type { Dialog, Theme } from "./store";
import type { Mode } from "./types";

export const composer = {
  focus: () => {},
  prepend: (_text: string) => {},
};

export const follow = { on: true };

let toastTimer = 0;

export function toast(text: string, ms = 2600) {
  setState((s) => ({ toast: { text, n: (s.toast?.n || 0) + 1 } }));
  window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => setState({ toast: null }), ms);
}

export function errorText(err: unknown): string {
  if (err instanceof ApiError) return err.hint ? `${err.message} · ${err.hint}` : err.message;
  return err instanceof Error ? err.message : String(err);
}

export function fail(err: unknown) {
  toast(errorText(err), 5000);
}

const drafts: Record<string, string> = local.get("drafts", {});
let draftTimer = 0;

export function draftFor(id: string): string {
  return drafts[id] || "";
}

export function saveDraft(id: string, text: string) {
  if (!id) return;
  drafts[id] = text;
  window.clearTimeout(draftTimer);
  draftTimer = window.setTimeout(() => local.set("drafts", drafts), 400);
}

export async function loadState() {
  const st = await server.state();
  const projects = st.projects || [];
  setState((s) => {
    const ids = projects.map((p) => p.id);
    let current = s.current;
    if (!ids.includes(current)) current = local.get("project", "");
    if (!ids.includes(current)) current = st.default || ids[0] || "";
    return { info: st.info, mode: st.mode, projects, defaultId: st.default, home: st.home || "", current };
  });
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
  local.set("project", id);
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
