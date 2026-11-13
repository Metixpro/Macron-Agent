import { composer, fail, loadState, openProject, refreshState, toast } from "./actions";
import { projectName } from "./format";
import { getState, patchLive, setState } from "./store";
import type { Ask, Block, LiveSub, LiveTool, PlanItem, ServerEvent, Usage } from "./types";

let source: EventSource | null = null;
let seq = 0;

export function connect() {
  if (source) return;
  source = new EventSource("/api/events");
  source.addEventListener("open", () => {
    seq = 0;
    setState({ online: true });
    resync();
  });
  source.addEventListener("error", () => setState({ online: false }));
  source.addEventListener("message", (m) => {
    let ev: ServerEvent;
    try {
      ev = JSON.parse(m.data);
    } catch {
      return;
    }
    onEvent(ev);
  });
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

const touches = new Set(["user", "state", "done", "ask", "ask_done", "block"]);

function onEvent(ev: ServerEvent) {
  if (seq && ev.seq && ev.seq !== seq + 1) {
    seq = ev.seq;
    resync();
    return;
  }
  seq = ev.seq || seq;
  if (ev.type === "projects" || ev.type === "info") return refreshState();
  if (!ev.project) return;
  const s = getState();
  if (ev.project === s.current) onProjectEvent(ev);
  if (touches.has(ev.type)) refreshState();
  if (ev.type === "ask" && ev.project !== s.current) {
    toast(projectName(s.projects.find((p) => p.id === ev.project), s.defaultId) + " is waiting for your approval", 6000);
  }
}

function append(...blocks: Block[]) {
  if (blocks.length) setState((s) => ({ blocks: [...s.blocks, ...blocks] }));
}

function onProjectEvent(ev: ServerEvent) {
  switch (ev.type) {
    case "user":
      if (ev.taken) patchLive(() => ({ queued: [] }));
      append(ev.block as Block);
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
      patchLive((l) => ({ html: "", thought: "", tools: [...(l.tools || []), ev.tool as LiveTool] }));
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
      setState((s) => ({
        blocks: [...s.blocks, ...blocks],
        live: { running: false, usage: ev.usage as Usage, context: s.live.context },
        lastTook: (ev.took as number) || 0,
        dialog: s.dialog?.kind === "ask" ? null : s.dialog,
        unseen: document.hasFocus() ? s.unseen : true,
      }));
      const requeue = ev.requeue as string[] | undefined;
      if (requeue && requeue.length) composer.prepend(requeue.join("\n\n"));
      return;
    }
  }
}
