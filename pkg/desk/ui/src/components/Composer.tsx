import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { compact, composer, draftFor, effortAbout, effortLevels, effortName, effortNote, newChat, openDialog, saveDraft, send, setMode, setTuning, stop } from "../actions";
import { tokens, took } from "../format";
import { Icon } from "../icons";
import { useStore } from "../store";

type Command = { name: string; hint: string; run: () => void };

const COMMANDS: Command[] = [
  { name: "/new", hint: "Start a new conversation", run: () => newChat() },
  { name: "/compact", hint: "Summarise the conversation to free room", run: () => compact() },
  { name: "/model", hint: "Pick the model", run: () => openDialog({ kind: "palette", mode: "models" }) },
  { name: "/plan", hint: "Plan first and change nothing", run: () => setMode("plan") },
  { name: "/build", hint: "Let Macron make changes", run: () => setMode("build") },
  { name: "/open", hint: "Open another folder", run: () => openDialog({ kind: "folder" }) },
  { name: "/settings", hint: "Approvals, providers and theme", run: () => openDialog({ kind: "settings" }) },
  { name: "/stop", hint: "Stop the current job", run: () => stop() },
];

function SendStop({ running, hasText, fly, onSend }: { running: boolean; hasText: boolean; fly: number; onSend: () => void }) {
  const state = running ? (hasText ? "queue" : "stop") : hasText ? "ready" : "idle";
  const title = state === "stop" ? "Stop (Esc)" : state === "queue" ? "Send now; Macron reads it at its next step (Enter)" : "Send (Enter)";
  return (
    <div className={"actions " + state}>
      <button type="button" className="mini-stop" title="Stop (Esc)" aria-label="Stop" tabIndex={state === "queue" ? 0 : -1} onClick={stop}>
        <span className="sq" />
      </button>
      <button type="button" className="go" disabled={state === "idle"} title={title} aria-label={state === "stop" ? "Stop" : "Send"} onClick={state === "stop" ? stop : onSend}>
        <svg className="ring" viewBox="0 0 44 44" aria-hidden="true">
          <circle cx="22" cy="22" r="20" />
        </svg>
        <span key={fly} className={"glyph send" + (fly ? " fly" : "")}>
          <Icon name="arrowUp" size={16} />
        </span>
        <span className="glyph stop">
          <span className="sq" />
        </span>
      </button>
    </div>
  );
}

function Meter() {
  const ctx = useStore((s) => s.live.context);
  const usage = useStore((s) => s.live.usage);
  const last = useStore((s) => s.lastTook);
  if (!ctx || !ctx.window) return null;
  const pct = Math.min(1, ctx.used / ctx.window);
  const parts = [`Context used: ${Math.round(pct * 100)}% of ${tokens(ctx.window)} tokens`];
  if (usage && (usage.in || usage.out)) parts.push(`${tokens(usage.in)} in, ${tokens(usage.out)} out`);
  if (last) parts.push(`last reply took ${took(last)}`);
  return (
    <span className={"meter" + (pct > 0.8 ? " high" : "")} title={parts.join(" · ")}>
      <span className="meter-bar">
        <span style={{ width: `${Math.round(pct * 100)}%` }} />
      </span>
      {Math.round(pct * 100)}%
    </span>
  );
}

function ModeSwitch() {
  const mode = useStore((s) => s.mode);
  return (
    <div className="mode" role="radiogroup" aria-label="Mode">
      {(["build", "plan"] as const).map((m) => (
        <button
          key={m}
          type="button"
          role="radio"
          aria-checked={mode === m}
          className={mode === m ? "on" : ""}
          title={m === "plan" ? "Plan first; nothing changes until you say so" : "Macron can change files and run commands"}
          onClick={() => mode !== m && setMode(m)}
        >
          {m === "plan" ? "Plan" : "Build"}
        </button>
      ))}
    </div>
  );
}

function Gauge({ level }: { level: number }) {
  return (
    <span className="gauge" aria-hidden="true">
      {[1, 2, 3, 4].map((i) => (
        <span key={i} className={i <= level ? "on" : ""} style={{ height: 3 + i * 2 }} />
      ))}
    </span>
  );
}

function EffortMenu() {
  const t = useStore((s) => s.tuning);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setOpen(false);
      }
    };
    window.addEventListener("mousedown", away);
    window.addEventListener("keydown", esc, true);
    return () => {
      window.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", esc, true);
    };
  }, [open]);
  if (!t || !t.model) return null;
  const level = effortLevels.indexOf(t.effort);
  return (
    <div className="effort" ref={ref}>
      <button type="button" className={"effort-btn" + (t.effort ? " set" : "")} title={"Effort: " + effortNote(t)} onClick={() => setOpen(!open)} aria-expanded={open}>
        <Gauge level={level} />
        <span>{effortName(t.effort)}</span>
      </button>
      {open && (
        <div className="effort-menu" role="menu">
          <div className="em-head">How hard the model thinks</div>
          {effortLevels.map((l, i) => (
            <button
              key={l || "auto"}
              type="button"
              role="menuitemradio"
              aria-checked={l === t.effort}
              className={"em-row" + (l === t.effort ? " on" : "")}
              onClick={() => {
                setOpen(false);
                if (l !== t.effort) setTuning("effort", l || "auto", "Effort: " + effortName(l));
              }}
            >
              <Gauge level={i} />
              <span className="grow">
                <span className="name">{effortName(l)}</span>
                <span className="meta">{effortAbout[l]}</span>
              </span>
              {l === t.effort && <Icon name="check" size={14} />}
            </button>
          ))}
          <div className="em-foot">{effortNote(t)}</div>
        </div>
      )}
    </div>
  );
}

function ModelButton() {
  const model = useStore((s) => s.info.model);
  const provider = useStore((s) => s.info.provider);
  return (
    <button
      type="button"
      className={"model" + (model ? "" : " missing")}
      title={provider ? `${provider} · change the model` : "Connect a model"}
      onClick={() => openDialog(model ? { kind: "palette", mode: "models" } : { kind: "model-connect" })}
    >
      <span>{model || "Connect a model"}</span>
      <Icon name="down" size={12} />
    </button>
  );
}

export function Composer() {
  const current = useStore((s) => s.current);
  const running = useStore((s) => s.live.running);
  const queued = useStore((s) => s.live.queued?.length || 0);
  const mode = useStore((s) => s.mode);
  const [text, setText] = useState("");
  const [sel, setSel] = useState(0);
  const [slashDismissed, setSlashDismissed] = useState(false);
  const [fly, setFly] = useState(0);
  const ref = useRef<HTMLTextAreaElement>(null);
  const idRef = useRef(current);

  useEffect(() => {
    idRef.current = current;
    setText(draftFor(current));
  }, [current]);

  useEffect(() => {
    composer.focus = () => ref.current?.focus({ preventScroll: true });
    composer.prepend = (t: string) =>
      setText((v) => {
        const next = v ? t + "\n\n" + v : t;
        saveDraft(idRef.current, next);
        return next;
      });
    ref.current?.focus({ preventScroll: true });
  }, []);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 260) + "px";
  }, [text]);

  const matches = useMemo(() => (!slashDismissed && text.startsWith("/") && !/\s/.test(text) ? COMMANDS.filter((c) => c.name.startsWith(text.toLowerCase())) : []), [text, slashDismissed]);

  const change = (v: string) => {
    setSlashDismissed(false);
    setText(v);
    setSel(0);
    saveDraft(current, v);
  };

  const run = (c: Command) => {
    change("");
    c.run();
  };

  const submit = async () => {
    const t = text.trim();
    if (!t) return;
    const cmd = COMMANDS.find((c) => c.name === t.toLowerCase());
    if (cmd) return run(cmd);
    change("");
    setFly((n) => n + 1);
    if (!(await send(t))) change(t);
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (matches.length) {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setSlashDismissed(true);
        return;
      }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setSel((i) => (i + (e.key === "ArrowDown" ? 1 : matches.length - 1)) % matches.length);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        run(matches[Math.min(sel, matches.length - 1)]);
        return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  const placeholder = running
    ? "Add to what Macron is doing, or press Esc to stop"
    : mode === "plan"
      ? "Describe the goal; Macron plans first and changes nothing"
      : "Ask Macron to build, fix, research or automate something";

  return (
    <footer className="composer">
      {matches.length > 0 && (
        <div className="slash" role="listbox">
          {matches.map((c, i) => (
            <button
              key={c.name}
              type="button"
              role="option"
              aria-selected={i === sel}
              className={"item" + (i === sel ? " sel" : "")}
              onMouseDown={(e) => {
                e.preventDefault();
                run(c);
              }}
            >
              <span className="cmd">{c.name}</span>
              <span className="meta">{c.hint}</span>
            </button>
          ))}
        </div>
      )}
      <div className={"box" + (mode === "plan" ? " plan" : "") + (running ? " busy" : "")}>
        <textarea ref={ref} rows={1} dir="auto" value={text} placeholder={placeholder} onChange={(e) => change(e.target.value)} onKeyDown={onKey} />
        <div className="toolbar">
          <ModeSwitch />
          <ModelButton />
          <EffortMenu />
          <span className="grow" />
          {queued > 0 && <span className="queued">{queued} waiting</span>}
          <Meter />
          <SendStop running={running} hasText={text.trim().length > 0} fly={fly} onSend={submit} />
        </div>
      </div>
    </footer>
  );
}
