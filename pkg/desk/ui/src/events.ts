import { apiBase, server } from "./api";
import { checkUpdate, composer, fail, loadBrowser, loadChanges, loadServers, loadState, noteBrowserVisit, openProject, refreshState, showError, stream, switchMachine } from "./actions";
import { getState, patchLive, setState } from "./store";
import type { Ask, Block, BrowserState, LiveDraft, LiveSub, LiveTool, PlanItem, ServerEvent, UpdateStatus, Usage } from "./types";

let source: EventSource | null = null;
let seq = 0;
let updateChecked = false;

export function connect() {
  source?.close();
  const es = new EventSource(apiBase() + "/api/events");
  source = es;
  es.addEventListener("open", () => {
    if (source !== es) return;
    seq = 0;
    setState({ online: true });
    resync();
    if (!getState().machine) {
      loadServers();
      if (!updateChecked) {
        updateChecked = true;
        window.setTimeout(() => void checkUpdate(false), 6000);
      }
    }
  });
  es.addEventListener("error", () => {
    if (source !== es) return;
    setState({ online: false });
    if (es.readyState === EventSource.CLOSED) lost(es);
  });
  es.addEventListener("message", (m) => {
    if (source !== es) return;
    let ev: ServerEvent;
    try {
      ev = JSON.parse(m.data);
    } catch {
      return;
    }
    onEvent(ev);
  });
}

stream.reconnect = connect;

async function lost(es: EventSource) {
  const machine = getState().machine;
  if (machine) {
    await loadServers();
    const srv = getState().servers.find((x) => x.id === machine);
    if (!srv || srv.state !== "ready") {
      showError(`Lost the connection to ${srv?.name || "the server"}`, 6000);
      await switchMachine("");
      return;
    }
  }
  window.setTimeout(() => {
    if (source === es) connect();
  }, 2000);
}

async function resync() {
  try {
    await loadState();
    const id = getState().current;
    if (id) await openProject(id);
  } catch (e) {
    fail(e);
  }
}

const touches = new Set(["user", "state", "done", "ask", "ask_done", "block", "thread"]);

function onEvent(ev: ServerEvent) {
  if (seq && ev.seq && ev.seq !== seq + 1) {
    seq = ev.seq;
    resync();
    return;
  }
  seq = ev.seq || seq;
  if (ev.type === "projects" || ev.type === "info") return refreshState();
  if (ev.type === "servers") return void loadServers();
  if (ev.type === "update" && ev.update) {
    setState({ update: ev.update as UpdateStatus });
    return;
  }
  if (!ev.project) return;
  if (ev.type === "browser") noteBrowserVisit(ev.project);
  const s = getState();
  if (ev.project === s.current) onProjectEvent(ev);
  if (touches.has(ev.type)) refreshState();
}

function append(...blocks: Block[]) {
  if (blocks.length) setState((s) => ({ blocks: [...s.blocks, ...blocks] }));
}

function refreshThreads(projectId: string) {
  server
    .threads(projectId)
    .then((r) => {
      if (getState().current === projectId) {
        setState({ activeThread: r.active, threads: r.threads || [] });
      }
    })
    .catch(() => undefined);
}

function onProjectEvent(ev: ServerEvent) {
  switch (ev.type) {
    case "user":
      if (ev.taken) patchLive(() => ({ queued: [] }));
      append(ev.block as Block);
      if (ev.project) refreshThreads(ev.project);
      return;
    case "thread":
      if (ev.project) refreshThreads(ev.project);
      return;
    case "state":
      setState((s) => ({
        live: {
          running: Boolean(ev.running),
          started: ev.started as number,
          tools: [],
          subs: [],
          plan: [],
          queued: [],
          html: "",
          usage: s.live.usage,
          context: s.live.context,
        },
      }));
      return;
    case "queued":
      patchLive(() => ({ queued: (ev.queued as string[]) || [] }));
      return;
    case "text":
      patchLive(() => ({ html: ev.html as string }));
      return;
    case "thought":
      patchLive(() => ({ thought: ev.text as string }));
      return;
    case "block": {
      const b = ev.block as Block;
      if (b.kind === "assistant") patchLive(() => ({ html: "" }));
      append(b);
      return;
    }
    case "tool_start":
      patchLive((l) => ({ html: "", thought: "", draft: null, tools: [...(l.tools || []), ev.tool as LiveTool] }));
      return;
    case "tool_draft":
      patchLive(() => ({ draft: ev.draft as LiveDraft }));
      return;
    case "tool_progress":
      patchLive((l) => ({ tools: (l.tools || []).map((t) => (t.id === ev.id ? { ...t, progress: ev.line as string } : t)) }));
      return;
    case "tool_end":
      setState((s) => ({
        blocks: [...s.blocks, ev.block as Block],
        live: { ...s.live, tools: (s.live.tools || []).filter((t) => t.id !== ev.id) },
      }));
      return;
    case "plan":
      patchLive(() => ({ plan: (ev.items as PlanItem[]) || [] }));
      return;
    case "browser":
      if (ev.browser) setState({ browserView: ev.browser as BrowserState });
      else if (ev.project) void loadBrowser(ev.project);
      return;
    case "sub": {
      const sub = ev.sub as LiveSub;
      patchLive((l) => {
        const subs = [...(l.subs || [])];
        const i = subs.findIndex((x) => x.id === sub.id);
        if (i >= 0) subs[i] = sub;
        else subs.push(sub);
        return { subs };
      });
      return;
    }
    case "context":
      patchLive(() => ({ context: { used: ev.used as number, window: ev.window as number } }));
      return;
    case "ask": {
      const ask = ev.ask as Ask;
      setState((s) => ({ live: { ...s.live, ask }, dialog: { kind: "ask", ask } }));
      return;
    }
    case "ask_done":
      setState((s) => ({
        live: { ...s.live, ask: null },
        dialog: s.dialog?.kind === "ask" && s.dialog.ask.id === ev.id ? null : s.dialog,
      }));
      return;
    case "done": {
      const blocks = (ev.blocks as Block[] | null) || [];
      const ctx = (ev.context as { used: number; window: number } | undefined) || undefined;
      const hadError = blocks.some((b) => b.kind === "error");
      setState((s) => ({
        blocks: [...s.blocks, ...blocks],
        live: { running: false, usage: ev.usage as Usage, context: ctx ?? s.live.context },
        lastTook: (ev.took as number) || 0,
        lastTurnResult: { ok: !hadError, at: Date.now() },
        dialog: s.dialog?.kind === "ask" ? null : s.dialog,
        unseen: document.hasFocus() ? s.unseen : true,
      }));
      const requeue = ev.requeue as string[] | undefined;
      if (requeue && requeue.length) composer.prepend(requeue.join("\n\n"));
      if (ev.project) {
        loadChanges(ev.project);
        refreshThreads(ev.project);
      }
      return;
    }
  }
}
