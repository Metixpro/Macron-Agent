import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, KeyboardEvent } from "react";
import {
  clearError,
  compact,
  composer,
  draftFor,
  effortAbout,
  effortLevels,
  effortName,
  effortNote,
  exportConversation,
  fail,
  initProject,
  newChat,
  openDialog,
  openSettings,
  runShell,
  saveDraft,
  send,
  setMode,
  setTuning,
  stop,
  toggleChanges,
} from "../actions";
import { fileURL, server } from "../api";
import { bytes, formatCost, tokens, took } from "../format";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { setState, useStore } from "../store";
import type { FileRef } from "../types";
import { LiveCard } from "./Live";
import { StarBuddy, useStarBuddyMood } from "./StarBuddy";

type Command = { name: string; hint: string; run: () => void };

const COMMANDS: Command[] = [
  { name: "/new", hint: "Start a new conversation", run: () => newChat() },
  { name: "/search", hint: "Search all conversations across workspaces", run: () => openDialog({ kind: "search" }) },
  { name: "/compact", hint: "Summarise the conversation to free room", run: () => compact() },
  { name: "/status", hint: "Show model, sandbox, context and SpectorDev info", run: () => openDialog({ kind: "status" }) },
  { name: "/export", hint: "Download this conversation as Markdown", run: () => exportConversation() },
  { name: "/init", hint: "Write or update MACRON.md for this project", run: () => void initProject() },
  { name: "/model", hint: "Pick the model", run: () => openDialog({ kind: "palette", mode: "models" }) },
  { name: "/provider", hint: "Add or customize models & providers", run: () => openDialog({ kind: "provider-edit" }) },
  { name: "/routing", hint: "Configure smart light/strong model routing", run: () => openSettings("routing") },
  { name: "/moa", hint: "Configure multi-model voting (or type /moa <prompt>)", run: () => openSettings("moa") },
  { name: "/plan", hint: "Plan first and change nothing", run: () => setMode("plan") },
  { name: "/build", hint: "Let Macron make changes", run: () => setMode("build") },
  { name: "/changes", hint: "View git changes and diff", run: () => toggleChanges(true) },
  { name: "/execution", hint: "Switch execution isolation mode", run: () => openSettings("execution") },
  { name: "/open", hint: "Open another folder", run: () => openDialog({ kind: "folder" }) },
  { name: "/settings", hint: "Approvals, providers and theme", run: () => openSettings("general") },
  { name: "/stop", hint: "Stop the current job", run: () => stop() },
];

function SendStop({
  running,
  hasContent,
  disabled,
  fly,
  onSend,
}: {
  running: boolean;
  hasContent: boolean;
  disabled: boolean;
  fly: number;
  onSend: () => void;
}) {
  const state = running ? (hasContent ? "queue" : "stop") : hasContent && !disabled ? "ready" : "idle";
  const title = state === "stop" ? "Stop (Esc)" : state === "queue" ? "Queue message for Macron's next step (Enter)" : "Send (Enter)";
  return (
    <div className={"actions " + state}>
      <button type="button" className="mini-stop" title="Stop (Esc)" aria-label="Stop" tabIndex={state === "queue" ? 0 : -1} onClick={stop}>
        <span className="sq" />
      </button>
      <button
        type="button"
        className="go"
        disabled={state === "idle" || (disabled && state !== "stop")}
        title={title}
        aria-label={state === "stop" ? "Stop" : "Send"}
        onClick={state === "stop" ? stop : onSend}
      >
        <svg className="ring" viewBox="0 0 44 44" aria-hidden="true">
          <circle cx="22" cy="22" r="20" />
        </svg>
        <span key={fly} className={"glyph send" + (fly ? " fly" : "")}>
          <Icon name="arrowUp" size={15} />
        </span>
        <span className="glyph stop">
          <span className="sq" />
        </span>
      </button>
    </div>
  );
}

function Meter({ onOpenChange }: { onOpenChange?: (open: boolean) => void }) {
  const tr = useT();
  const ctx = useStore((s) => s.live.context);
  const usage = useStore((s) => s.live.usage);
  const last = useStore((s) => s.lastTook);
  const routingActive = useStore((s) => s.info.routingActive);
  const moaActive = useStore((s) => s.info.moaActive);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  // Notify the parent in the same event handler so the StarBuddy tuck and the
  // popover land in one render batch; an effect would only report the change
  // a render later, leaving a frame where the popover overlaps the buddy.
  const applyOpen = (v: boolean) => {
    setOpen(v);
    onOpenChange?.(v);
  };

  useEffect(() => () => onOpenChange?.(false), [onOpenChange]);

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) applyOpen(false);
    };
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        applyOpen(false);
      }
    };
    window.addEventListener("mousedown", away);
    window.addEventListener("keydown", esc, true);
    return () => {
      window.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", esc, true);
    };
  }, [open]);

  const hasCtx = Boolean(ctx && ctx.window);
  const hasUsage = Boolean(usage && (usage.in || usage.out));
  if (!hasCtx && !hasUsage && !routingActive && !moaActive) return null;

  const pct = hasCtx && ctx ? Math.min(1, ctx.used / ctx.window) : 0;
  const pctNum = Math.round(pct * 100);
  const dash = 44;
  const offset = dash - dash * pct;

  return (
    <div className="meter-group" dir="ltr" ref={ref}>
      {moaActive && (
        <button type="button" className="ai-badge moa" title={`MoA active: ${moaActive}`} onClick={() => openSettings("moa")}>
          <span className="ai-dot" aria-hidden="true" />
          MoA · {moaActive}
        </button>
      )}
      {!moaActive && routingActive && (
        <button type="button" className="ai-badge route" title="Smart light/strong model routing active" onClick={() => openSettings("routing")}>
          <span className="ai-dot" aria-hidden="true" />
          Auto-Route
        </button>
      )}
      {(hasCtx || hasUsage) && (
        <div className="meter-pop-wrap">
          <button
            type="button"
            className={"meter-btn" + (pct > 0.8 ? " high" : "") + (open ? " open" : "")}
            aria-expanded={open}
            title={tr("Context & tokens")}
            onClick={() => applyOpen(!open)}
          >
            {hasCtx && (
              <svg className="ctx-ring" width="16" height="16" viewBox="0 0 18 18" aria-hidden="true">
                <circle className="ctx-track" cx="9" cy="9" r="7" />
                <circle className="ctx-fill" cx="9" cy="9" r="7" strokeDasharray={dash} strokeDashoffset={offset} />
              </svg>
            )}
            <span>{hasCtx ? `${pctNum}%` : `↑${tokens(usage?.in)}`}</span>
          </button>
          {open && (
            <div className="meter-popover" role="dialog" aria-label={tr("Context & tokens")}>
              <div className="mp-head">{tr("Context & tokens")}</div>
              {hasCtx && ctx && (
                <div className="mp-block">
                  <div className="mp-row">
                    <span>{tr("Context")}</span>
                    <strong>
                      {tokens(ctx.used)} / {tokens(ctx.window)} ({pctNum}%)
                    </strong>
                  </div>
                  <div className="mp-bar">
                    <span className={pct > 0.8 ? "high" : ""} style={{ width: `${pctNum}%` }} />
                  </div>
                </div>
              )}
              {hasUsage && usage && (
                <div className="mp-stats">
                  <div className="mp-row">
                    <span>{tr("Input")}</span>
                    <span>{tokens(usage.in)}</span>
                  </div>
                  <div className="mp-row">
                    <span>{tr("Output")}</span>
                    <span>{tokens(usage.out)}</span>
                  </div>
                  {usage.costKnown && typeof usage.cost === "number" && (
                    <div className="mp-row">
                      <span>{tr("Est. cost")}</span>
                      <span>~{formatCost(usage.cost)}</span>
                    </div>
                  )}
                  {last > 0 && (
                    <div className="mp-row">
                      <span>{tr("Last turn")}</span>
                      <span>{took(last)}</span>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ModeSwitch() {
  const t = useT();
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
          title={m === "plan" ? t("Plan first and change nothing") : t("Let Macron make changes")}
          onClick={() => mode !== m && setMode(m)}
        >
          {m === "plan" ? t("Plan") : t("Build")}
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

function EffortMenu({ onOpenChange }: { onOpenChange?: (open: boolean) => void }) {
  const tr = useT();
  const t = useStore((s) => s.tuning);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  // Notify the parent in the same event handler so the StarBuddy tuck and the
  // popover land in one render batch; an effect would only report the change
  // a render later, leaving a frame where the popover overlaps the buddy.
  const applyOpen = (v: boolean) => {
    setOpen(v);
    onOpenChange?.(v);
  };

  useEffect(() => () => onOpenChange?.(false), [onOpenChange]);

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) applyOpen(false);
    };
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        applyOpen(false);
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
      <button
        type="button"
        className={"effort-btn" + (t.effort ? " set" : "") + (open ? " open" : "")}
        title={"Effort: " + effortNote(t)}
        onClick={() => applyOpen(!open)}
        aria-expanded={open}
      >
        <Gauge level={level} />
        <span>{effortName(t.effort)}</span>
      </button>
      {open && (
        <div className="effort-menu" role="menu">
          <div className="em-head">{tr("Reasoning effort")}</div>
          {effortLevels.map((l, i) => (
            <button
              key={l || "auto"}
              type="button"
              role="menuitemradio"
              aria-checked={l === t.effort}
              className={"em-row" + (l === t.effort ? " on" : "")}
              onClick={() => {
                applyOpen(false);
                if (l !== t.effort) setTuning("effort", l || "auto");
              }}
            >
              <Gauge level={i} />
              <span className="grow">
                <span className="name">{effortName(l)}</span>
                <span className="meta">{effortAbout[l]}</span>
              </span>
              {l === t.effort && <Icon name="check" size={13} />}
            </button>
          ))}
          <div className="em-foot">{effortNote(t)}</div>
        </div>
      )}
    </div>
  );
}

function ModelButton() {
  const tr = useT();
  const model = useStore((s) => s.info.model);
  const provider = useStore((s) => s.info.provider);
  return (
    <button
      type="button"
      className={"model" + (model ? "" : " missing")}
      title={provider ? `${provider} · ${model}` : tr("Connect a model")}
      onClick={() => openDialog(model ? { kind: "palette", mode: "models" } : { kind: "model-connect" })}
    >
      <Icon name="chip" size={12} className="model-ico" />
      <span className="model-name" dir="ltr">
        {model || tr("Connect a model")}
      </span>
      <Icon name="down" size={11} />
    </button>
  );
}

function activeAtToken(text: string, caret: number): { start: number; query: string } | null {
  const head = text.slice(0, caret);
  const match = head.match(/(?:^|\s)@([^\s@]*)$/);
  if (!match) return null;
  const query = match[1] || "";
  const start = caret - query.length - 1;
  return { start, query };
}

const EMPTY_QUEUED: string[] = [];

export function Composer() {
  const tr = useT();
  const current = useStore((s) => s.current);
  const online = useStore((s) => s.online);
  const errorAlert = useStore((s) => s.errorAlert);
  const running = useStore((s) => s.live.running);
  const queuedItems = useStore((s) => s.live.queued || EMPTY_QUEUED);
  const mode = useStore((s) => s.mode);

  const [text, setText] = useState("");
  const [caret, setCaret] = useState(0);
  const [sel, setSel] = useState(0);
  const [slashDismissed, setSlashDismissed] = useState(false);
  const [atDismissed, setAtDismissed] = useState(false);
  const [atFiles, setAtFiles] = useState<string[]>([]);
  const [attachments, setAttachments] = useState<FileRef[]>([]);
  const [uploading, setUploading] = useState(0);
  const [fly, setFly] = useState(0);
  const [focused, setFocused] = useState(false);
  const [meterOpen, setMeterOpen] = useState(false);
  const [effortOpen, setEffortOpen] = useState(false);

  const ref = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const idRef = useRef(current);
  const typingTimerRef = useRef<number>(0);

  useEffect(() => {
    idRef.current = current;
    setText(draftFor(current));
    setAttachments([]);
    setAtFiles([]);
  }, [current]);

  useEffect(() => {
    return () => {
      window.clearTimeout(typingTimerRef.current);
      setState({ composerTyping: false });
    };
  }, []);

  const syncTyping = (val: string, pos: number) => {
    const ratio = val.length > 0 ? Math.max(0.1, Math.min(0.9, pos / Math.max(24, val.length))) : 0.5;
    window.clearTimeout(typingTimerRef.current);
    if (val.trim().length > 0) {
      setState({ composerTyping: true, composerCaretRatio: ratio });
      typingTimerRef.current = window.setTimeout(() => {
        setState({ composerTyping: false });
      }, 2400);
    } else {
      setState({ composerTyping: false, composerCaretRatio: 0.5 });
    }
  };

  const uploadFiles = async (files: File[]) => {
    const pid = idRef.current;
    if (!pid || !files.length) return;
    setUploading((n) => n + files.length);
    for (const f of files) {
      try {
        const uploaded = await server.upload(pid, f);
        if (idRef.current === pid) {
          setAttachments((prev) => [...prev, uploaded]);
        }
      } catch (e) {
        fail(e);
      } finally {
        setUploading((n) => Math.max(0, n - 1));
      }
    }
    ref.current?.focus({ preventScroll: true });
  };

  useEffect(() => {
    composer.focus = () => ref.current?.focus({ preventScroll: true });
    composer.prepend = (t: string) =>
      setText((v) => {
        const next = v ? t + "\n\n" + v : t;
        saveDraft(idRef.current, next);
        return next;
      });
    composer.attachFiles = (files: File[]) => {
      void uploadFiles(files);
    };
    ref.current?.focus({ preventScroll: true });
  }, []);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (!text) {
      el.style.height = "";
      return;
    }
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 240) + "px";
  }, [text]);

  const matches = useMemo(
    () => (!slashDismissed && text.startsWith("/") && !/\s/.test(text) ? COMMANDS.filter((c) => c.name.startsWith(text.toLowerCase())) : []),
    [text, slashDismissed],
  );

  const atMatch = useMemo(() => {
    if (atDismissed || matches.length > 0) return null;
    return activeAtToken(text, caret);
  }, [text, caret, atDismissed, matches.length]);

  useEffect(() => {
    if (!atMatch || !current) {
      setAtFiles([]);
      return;
    }
    let alive = true;
    const timer = window.setTimeout(() => {
      server
        .projectFiles(current, atMatch.query)
        .then((r) => {
          if (alive) {
            setAtFiles((r.files || []).slice(0, 12));
            setSel(0);
          }
        })
        .catch(() => {
          if (alive) setAtFiles([]);
        });
    }, 80);
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [current, atMatch?.query, atMatch !== null]);

  const change = (v: string, pos?: number) => {
    const nextPos = pos ?? v.length;
    setSlashDismissed(false);
    setAtDismissed(false);
    setText(v);
    setCaret(nextPos);
    setSel(0);
    saveDraft(current, v);
    syncTyping(v, nextPos);
  };

  const insertFileMention = (file: string) => {
    if (!atMatch) return;
    const before = text.slice(0, atMatch.start);
    const after = text.slice(caret);
    const insert = `@${file} `;
    const next = before + insert + after;
    const nextPos = before.length + insert.length;
    setAtFiles([]);
    setAtDismissed(true);
    setText(next);
    setCaret(nextPos);
    saveDraft(current, next);
    syncTyping(next, nextPos);
    window.requestAnimationFrame(() => {
      if (ref.current) {
        ref.current.focus();
        ref.current.setSelectionRange(nextPos, nextPos);
      }
    });
  };

  const run = (c: Command) => {
    change("");
    c.run();
  };

  const historyRef = useRef<string[]>([]);
  const histIdxRef = useRef<number>(-1);
  const histDraftRef = useRef<string>("");

  const submit = async () => {
    const t = text.trim();
    if ((!t && attachments.length === 0) || online === false) return;
    if (attachments.length === 0) {
      const cmd = COMMANDS.find((c) => c.name === t.toLowerCase());
      if (cmd) return run(cmd);
      if (t.startsWith("!") && t.slice(1).trim()) {
        const sh = t.slice(1).trim();
        historyRef.current = [...historyRef.current.slice(-49), t];
        histIdxRef.current = -1;
        change("");
        setFly((n) => n + 1);
        if (!(await runShell(sh))) change(t);
        return;
      }
    }
    if (t) {
      historyRef.current = [...historyRef.current.slice(-49), t];
      histIdxRef.current = -1;
    }
    const pending = [...attachments];
    change("");
    setAttachments([]);
    setFly((n) => n + 1);
    if (!(await send(t, pending))) {
      change(t);
      setAttachments(pending);
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const items = Array.from(e.clipboardData?.items || []);
    const files: File[] = [];
    for (const item of items) {
      if (item.kind === "file") {
        const f = item.getAsFile();
        if (f) {
          const named = f.name ? f : new File([f], `screenshot-${Date.now()}.png`, { type: f.type || "image/png" });
          files.push(named);
        }
      }
    }
    if (files.length) {
      e.preventDefault();
      void uploadFiles(files);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (atMatch && atFiles.length > 0) {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setAtDismissed(true);
        return;
      }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        e.preventDefault();
        setSel((i) => (i + (e.key === "ArrowDown" ? 1 : atFiles.length - 1)) % atFiles.length);
        return;
      }
      if (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey)) {
        e.preventDefault();
        insertFileMention(atFiles[Math.min(sel, atFiles.length - 1)]);
        return;
      }
    }
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
    if (e.key === "ArrowUp" && !text.includes("\n") && historyRef.current.length > 0) {
      const el = ref.current;
      if (!el || (el.selectionStart === 0 && el.selectionEnd === 0) || histIdxRef.current >= 0) {
        e.preventDefault();
        if (histIdxRef.current < 0) {
          histDraftRef.current = text;
          histIdxRef.current = historyRef.current.length - 1;
        } else if (histIdxRef.current > 0) {
          histIdxRef.current--;
        }
        const v = historyRef.current[histIdxRef.current] || "";
        change(v, v.length);
        return;
      }
    }
    if (e.key === "ArrowDown" && histIdxRef.current >= 0 && !text.includes("\n")) {
      e.preventDefault();
      if (histIdxRef.current < historyRef.current.length - 1) {
        histIdxRef.current++;
        const v = historyRef.current[histIdxRef.current] || "";
        change(v, v.length);
      } else {
        histIdxRef.current = -1;
        const v = histDraftRef.current;
        change(v, v.length);
      }
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void submit();
    }
  };

  const autoMood = useStarBuddyMood();
  const buddyMood = autoMood === "idle" && (focused || text.trim().length > 0) ? "typing" : autoMood;
  const menuOpen = matches.length > 0 || Boolean(atMatch && atFiles.length > 0);
  const popoverOpen = meterOpen || effortOpen;
  const isOffline = online === false;
  const hasContent = text.trim().length > 0 || attachments.length > 0;

  const buddyTip =
    buddyMood === "working"
      ? tr("Working")
      : buddyMood === "streaming"
        ? tr("Writing…")
        : buddyMood === "asking"
          ? tr("Waiting for your approval")
          : buddyMood === "success"
            ? tr("Success")
            : buddyMood === "error"
              ? tr("Error")
              : buddyMood === "offline"
                ? tr("Offline")
                : buddyMood === "typing"
                  ? tr("Listening…")
                  : tr("Ready");

  const placeholder = isOffline
    ? tr("Engine offline — attempting to reconnect…")
    : running
      ? tr("Add to what Macron is doing, or press Esc to stop")
      : mode === "plan"
        ? tr("Describe the goal; Macron plans first and changes nothing")
        : tr("Ask Macron to build, fix, research or automate something");

  const boxClass = [
    "box",
    mode === "plan" ? "plan" : "",
    running ? "busy" : "",
    focused ? "focused" : "",
    hasContent ? "typing" : "",
    uploading > 0 ? "uploading" : "",
    queuedItems.length > 0 ? "has-queue" : "",
    isOffline ? "disabled" : "",
    errorAlert ? "has-error" : "",
  ]
    .filter(Boolean)
    .join(" ");

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
              <span className="cmd" dir="ltr">
                {c.name}
              </span>
              <span className="meta">{tr(c.hint)}</span>
            </button>
          ))}
        </div>
      )}
      {atMatch && atFiles.length > 0 && (
        <div className="slash at-menu" role="listbox">
          {atFiles.map((f, i) => (
            <button
              key={f}
              type="button"
              role="option"
              aria-selected={i === sel}
              className={"item" + (i === sel ? " sel" : "")}
              onMouseDown={(e) => {
                e.preventDefault();
                insertFileMention(f);
              }}
            >
              <Icon name="file" size={13} />
              <span className="at-path" dir="ltr">
                {f}
              </span>
            </button>
          ))}
        </div>
      )}

      <div className="box-stage">
        <StarBuddy variant="peek" mood={buddyMood} tucked={menuOpen || popoverOpen} interactive trackPointer title={`Macron · ${buddyTip}`} />
        <div className={boxClass}>
          <LiveCard />
          {errorAlert && (
            <div className="err-dock" role="alert">
              <Icon name="alert" size={14} />
              <span className="grow" dir="auto">
                {errorAlert}
              </span>
              <button type="button" className="icon-btn sm" title={tr("Dismiss")} aria-label={tr("Dismiss")} onClick={clearError}>
                <Icon name="x" size={13} />
              </button>
            </div>
          )}
          {queuedItems.length > 0 && (
            <div className="queue-strip">
              <span className="queue-pill">
                <Icon name="layers" size={11} />
                {queuedItems.length} {tr("waiting")}
              </span>
              <span className="queue-preview" dir="auto">
                {queuedItems[queuedItems.length - 1]}
              </span>
            </div>
          )}

          {(attachments.length > 0 || uploading > 0) && (
            <div className="att-bar">
              {attachments.map((a) => {
                const isImg = (a.type || "").startsWith("image/");
                return (
                  <span key={a.name} className="att-chip" title={a.base}>
                    {isImg ? <img className="att-thumb" src={fileURL(current, a.name)} alt={a.base} /> : <Icon name="file" size={13} />}
                    <span className="att-name" dir="ltr">
                      {a.base}
                    </span>
                    <span className="att-size" dir="ltr">
                      {bytes(a.size)}
                    </span>
                    <button
                      type="button"
                      className="att-rm"
                      aria-label={tr("Remove")}
                      onClick={() => setAttachments((prev) => prev.filter((x) => x.name !== a.name))}
                    >
                      <Icon name="x" size={11} />
                    </button>
                  </span>
                );
              })}
              {uploading > 0 && (
                <span className="att-chip uploading">
                  <span className="spin" />
                  <span>{tr("Uploading…")}</span>
                </span>
              )}
            </div>
          )}

          <textarea
            ref={ref}
            rows={1}
            dir="auto"
            disabled={isOffline}
            value={text}
            placeholder={placeholder}
            onFocus={() => setFocused(true)}
            onBlur={() => {
              setFocused(false);
              setState({ composerTyping: false });
            }}
            onPaste={onPaste}
            onClick={(e) => setCaret(e.currentTarget.selectionStart || 0)}
            onKeyUp={(e) => setCaret(e.currentTarget.selectionStart || 0)}
            onChange={(e) => change(e.target.value, e.target.selectionStart || 0)}
            onKeyDown={onKey}
          />

          <input
            ref={fileInputRef}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              const files = Array.from(e.target.files || []);
              e.target.value = "";
              if (files.length) void uploadFiles(files);
            }}
          />

          <div className="toolbar">
            <ModeSwitch />
            <button
              type="button"
              className="icon-btn sm attach-btn"
              disabled={isOffline}
              title={tr("Attach files or images")}
              aria-label={tr("Attach files or images")}
              onClick={() => fileInputRef.current?.click()}
            >
              <Icon name="paperclip" size={14} />
            </button>
            <ModelButton />
            <EffortMenu onOpenChange={setEffortOpen} />
            <span className="grow" />
            <Meter onOpenChange={setMeterOpen} />
            <SendStop running={running} hasContent={hasContent} disabled={isOffline} fly={fly} onSend={() => void submit()} />
          </div>
        </div>
      </div>
    </footer>
  );
}
