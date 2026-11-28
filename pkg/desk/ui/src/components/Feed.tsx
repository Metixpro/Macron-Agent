import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { follow, onServer, openDialog, openFile, saveFile } from "../actions";
import { fileURL } from "../api";
import { bidi, bytes, clock, copyText, prettyArgs, shortPath, took } from "../format";
import { useProjectName } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { Block, FileRef, PlanItem } from "../types";
import { StarBuddy } from "./StarBuddy";

function wireFiles(root: HTMLElement, project: string) {
  root.querySelectorAll<HTMLImageElement>("img[data-file]").forEach((img) => {
    const name = img.dataset.file || "";
    img.src = fileURL(project, name);
    img.loading = "lazy";
    img.title = name;
    img.onerror = () => {
      const chip = document.createElement("a");
      chip.className = "file-chip missing";
      chip.dataset.file = name;
      chip.textContent = img.alt || name;
      img.replaceWith(chip);
    };
  });
  root.querySelectorAll<HTMLAnchorElement>("a[data-file]").forEach((a) => {
    a.href = fileURL(project, a.dataset.file || "", true);
    a.title = a.dataset.file || "";
  });
}

export function Prose({ html, streaming }: { html: string; streaming?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const project = useStore((s) => s.current);
  useLayoutEffect(() => {
    if (!ref.current) return;
    bidi(ref.current);
    wireFiles(ref.current, project);
  }, [html, project]);

  const onClick = (e: MouseEvent<HTMLDivElement>) => {
    const file = (e.target as HTMLElement).closest<HTMLElement>("[data-file]");
    if (file) {
      e.preventDefault();
      openFile(file.dataset.file || "");
      return;
    }
    const btn = (e.target as HTMLElement).closest<HTMLElement>(".copy");
    if (!btn) return;
    const pre = btn.closest(".code")?.querySelector("pre");
    copyText(pre ? pre.innerText : "").then(() => {
      btn.textContent = "Copied";
      btn.classList.add("copied");
      window.setTimeout(() => {
        btn.textContent = "Copy";
        btn.classList.remove("copied");
      }, 1500);
    });
  };

  return <div ref={ref} className={streaming ? "prose streaming" : "prose"} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
}

export function PlanList({ items, live }: { items: PlanItem[]; live?: boolean }) {
  const t = useT();
  const done = items.filter((i) => i.status === "done").length;
  return (
    <div className={live ? "plan live" : "plan"}>
      <div className="plan-head">
        <span>{t("Plan")}</span>
        <span className="plan-count" dir="ltr">
          {done}/{items.length}
        </span>
        <span className="plan-bar">
          <span style={{ width: `${items.length ? (100 * done) / items.length : 0}%` }} />
        </span>
      </div>
      {items.map((it, i) => {
        const st = it.status === "done" ? "done" : it.status === "in_progress" ? "now" : "";
        return (
          <div key={i} className={"plan-item " + st}>
            <span className="check">{st === "done" && <Icon name="check" size={10} />}</span>
            <span className="tx" dir="auto">
              {it.text}
            </span>
          </div>
        );
      })}
    </div>
  );
}

function isShellTool(b: Block): boolean {
  const n = (b.name || "").toLowerCase();
  return n === "run_command" || n === "shell" || n === "bash" || n === "exec" || n === "terminal" || n === "run_shell";
}

const ToolRow = memo(function ToolRow({ b }: { b: Block }) {
  const [open, setOpen] = useState(false);
  const args = open && b.args && b.args !== "{}" ? prettyArgs(b.args) : "";
  const isTerm = isShellTool(b);

  return (
    <div className={"tool" + (b.failed ? " fail" : "") + (open ? " open" : "") + (isTerm ? " term" : "")}>
      <button type="button" className="tool-row" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="st">
          <Icon name={b.failed ? "x" : isTerm ? "terminal" : "check"} size={12} />
        </span>
        <span className="lb">{b.label || b.name}</span>
        <span className="ar" dir="ltr">
          {b.arg}
        </span>
        <span className="chev">
          <Icon name="right" size={11} />
        </span>
      </button>
      {!open && b.failed && b.summary && (
        <div className="why" dir="auto">
          {b.summary}
        </div>
      )}
      {open && (
        <div className={"detail" + (isTerm ? " term-detail" : "")}>
          {isTerm && b.arg ? (
            <div className="term-cmd" dir="ltr">
              <span className="term-prompt">$</span>
              <span>{b.arg}</span>
            </div>
          ) : (
            args &&
            args.trim() !== (b.arg || "").trim() && (
              <div className="args" dir="ltr">
                {args}
              </div>
            )
          )}
          <pre className="out" dir="ltr">
            {b.output && b.output.trim() ? b.output : "(no output)"}
          </pre>
        </div>
      )}
    </div>
  );
});

function Steps({ items, fresh, live }: { items: Block[]; fresh: boolean; live: boolean }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const failed = items.filter((b) => b.failed).length;
  const spanMs = items.length > 1 && items[0].at && items[items.length - 1].at ? Math.max(0, items[items.length - 1].at - items[0].at) : 0;

  if (items.length <= 2) {
    return (
      <div className={"steps" + (fresh ? " enter" : "")}>
        {items.map((b, i) => (
          <ToolRow key={i + ":" + b.at} b={b} />
        ))}
      </div>
    );
  }

  const last = items[items.length - 1];
  const tail = !open && live ? 2 : 0;
  const shown = open ? items : items.slice(items.length - tail);
  const from = items.length - shown.length;

  return (
    <div className={"steps grouped" + (open ? " open" : "") + (tail ? " live" : "") + (fresh ? " enter" : "")}>
      <button type="button" className="tool-row head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="st">
          <Icon name={failed > 0 ? "alert" : "steps"} size={12} />
        </span>
        <span className="lb">
          {tail ? `${items.length - tail} ${t("earlier steps")}` : `${items.length} ${t("steps")}`}
        </span>
        <span className="ar" dir="auto">
          {failed > 0 && (
            <em>
              {failed} {t("failed")}
              {open || tail ? "" : " · "}
            </em>
          )}
          {open || tail ? "" : `${last.label || last.name} ${last.arg || ""}`}
        </span>
        {spanMs >= 1000 && (
          <span className="steps-dur" dir="ltr">
            {took(spanMs)}
          </span>
        )}
        <span className="chev">
          <Icon name="right" size={11} />
        </span>
      </button>
      {shown.length > 0 && (
        <div className="steps-body">
          {shown.map((b, i) => (
            <ToolRow key={from + i + ":" + b.at} b={b} />
          ))}
        </div>
      )}
    </div>
  );
}

function Actions({ b }: { b: Block }) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <div className="msg-actions">
      <button
        type="button"
        className={"act" + (copied ? " copied" : "")}
        title={t("Copy")}
        onClick={() =>
          copyText(b.text || "").then(() => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        <Icon name={copied ? "check" : "copy"} size={12} />
        <span>{copied ? t("Copied") : t("Copy")}</span>
      </button>
      {b.at > 0 && (
        <span className="when" dir="ltr">
          {clock(b.at)}
        </span>
      )}
    </div>
  );
}

function fileKind(f: FileRef): { icon: "image" | "film" | "music" | "archive" | "code" | "file"; label: string } {
  const t = f.type || "";
  const ext = (f.base.split(".").pop() || "").toLowerCase();
  if (t.startsWith("image/")) return { icon: "image", label: ext.toUpperCase() + " image" };
  if (t.startsWith("video/")) return { icon: "film", label: ext.toUpperCase() + " video" };
  if (t.startsWith("audio/")) return { icon: "music", label: ext.toUpperCase() + " audio" };
  if (["zip", "rar", "7z", "tar", "gz", "tgz", "xz", "bz2"].includes(ext)) return { icon: "archive", label: ext.toUpperCase() + " archive" };
  if (["py", "js", "ts", "go", "rs", "java", "c", "cpp", "cs", "rb", "php", "sh", "ps1", "json", "yaml", "yml", "html", "css", "sql"].includes(ext))
    return { icon: "code", label: ext.toUpperCase() + " file" };
  return { icon: "file", label: ext ? ext.toUpperCase() + " file" : "File" };
}

const RUNS = new Set(["exe", "com", "bat", "cmd", "ps1", "psm1", "vbs", "vbe", "js", "jse", "wsf", "wsh", "msi", "msp", "scr", "lnk", "url", "reg", "hta", "cpl", "jar", "py", "pyw", "sh", "command", "app", "appimage", "run", "desktop"]);

function FileCard({ f, fresh }: { f: FileRef; fresh: boolean }) {
  const project = useStore((s) => s.current);
  const remote = useStore((s) => s.servers.some((x) => x.id === s.machine));
  const [broken, setBroken] = useState(false);
  const kind = fileKind(f);
  const src = fileURL(project, f.name);
  const media = !broken && kind.icon;
  const runs = RUNS.has((f.base.split(".").pop() || "").toLowerCase());
  return (
    <div className={"file-card" + (fresh ? " enter" : "")}>
      {media === "image" && (
        <button type="button" className="file-preview" title={remote ? "Save" : "Open"} onClick={() => (onServer() ? saveFile(f.name, f.base) : openFile(f.name))}>
          <img src={src} alt={f.caption || f.base} loading="lazy" onError={() => setBroken(true)} />
        </button>
      )}
      {media === "film" && <video className="file-media" src={src} controls preload="metadata" onError={() => setBroken(true)} />}
      {media === "music" && <audio className="file-media" src={src} controls preload="metadata" onError={() => setBroken(true)} />}
      <div className="file-row">
        <span className="file-icon">
          <Icon name={kind.icon} size={17} />
        </span>
        <span className="file-meta">
          <span className="file-name" dir="auto" title={f.name}>
            {f.base}
          </span>
          <span className="file-size" dir="ltr">
            {kind.label} · {bytes(f.size)}
          </span>
        </span>
        <span className="file-actions">
          {!remote && !runs && (
            <button type="button" className="btn ghost sm" onClick={() => openFile(f.name)}>
              <Icon name="open" size={13} />
              Open
            </button>
          )}
          {!remote && (
            <button type="button" className="icon-btn sm" title="Show in folder" aria-label="Show in folder" onClick={() => openFile(f.name, true)}>
              <Icon name="folder" size={14} />
            </button>
          )}
          <button type="button" className="icon-btn sm" title="Save a copy" aria-label="Save a copy" onClick={() => saveFile(f.name, f.base)}>
            <Icon name="save" size={14} />
          </button>
        </span>
      </div>
      {f.caption && (
        <div className="file-caption" dir="auto">
          {f.caption}
        </div>
      )}
    </div>
  );
}

function UserAttachments({ items }: { items?: FileRef[] }) {
  const project = useStore((s) => s.current);
  if (!items || !items.length) return null;
  return (
    <div className="user-atts">
      {items.map((a) => {
        const isImg = (a.type || "").startsWith("image/");
        return isImg ? (
          <a key={a.name} className="uatt-img" href={fileURL(project, a.name)} target="_blank" rel="noreferrer" title={a.base}>
            <img src={fileURL(project, a.name)} alt={a.base} loading="lazy" />
          </a>
        ) : (
          <button key={a.name} type="button" className="uatt-file" onClick={() => openFile(a.name)} title={a.base}>
            <Icon name="file" size={13} />
            <span dir="ltr">{a.base}</span>
            <span className="uatt-size">{bytes(a.size)}</span>
          </button>
        );
      })}
    </div>
  );
}

const BlockView = memo(function BlockView({ b, fresh, panel }: { b: Block; fresh: boolean; panel?: boolean }) {
  const t = useT();
  const enter = fresh ? " enter" : "";
  switch (b.kind) {
    case "user":
      return (
        <div className={"msg user" + enter}>
          <div className="bubble" dir="auto" title={b.at ? new Date(b.at).toLocaleString() : undefined}>
            <UserAttachments items={b.attachments} />
            <div className="bubble-text">{b.text}</div>
          </div>
        </div>
      );
    case "assistant":
      return (
        <div className={"msg assistant" + enter}>
          <Prose html={b.html || ""} />
          <Actions b={b} />
        </div>
      );
    case "plan": {
      let items: PlanItem[] = [];
      try {
        items = JSON.parse(b.text || "[]") || [];
      } catch {
        return null;
      }
      return <div className={"block" + enter + (panel ? " panel-plan" : "")}>{items.length > 0 && <PlanList items={items} />}</div>;
    }
    case "file":
      return b.file ? <FileCard f={b.file} fresh={fresh} /> : null;
    case "divider":
      return (
        <div className={"divider" + enter}>
          <span>{b.text === "New conversation" || !b.text ? t("New conversation") : b.text}</span>
        </div>
      );
    case "error":
      return (
        <div className={"error" + enter}>
          <div className="err-head">
            <Icon name="alert" size={14} />
            <div className="et" dir="auto">
              {b.text}
            </div>
          </div>
          {b.summary && (
            <div className="eh" dir="auto">
              {b.summary}
            </div>
          )}
          {b.output && b.output !== b.text && (
            <details>
              <summary>{t("Details")}</summary>
              <pre dir="ltr">{b.output}</pre>
            </details>
          )}
        </div>
      );
    default:
      return (
        <div className={"note" + enter} dir="auto">
          {b.text}
        </div>
      );
  }
});

type Item = { kind: "block"; b: Block; i: number } | { kind: "steps"; items: Block[]; i: number };

function group(blocks: Block[]): Item[] {
  const out: Item[] = [];
  blocks.forEach((b, i) => {
    const last = out[out.length - 1];
    if (b.kind === "tool" && last && last.kind === "steps") last.items.push(b);
    else if (b.kind === "tool") out.push({ kind: "steps", items: [b], i });
    else out.push({ kind: "block", b, i });
  });
  return out;
}

function Stream() {
  const t = useT();
  const html = useStore((s) => s.live.html);
  const running = useStore((s) => s.live.running);
  const tools = useStore((s) => s.live.tools);
  const activeTool = tools && tools.length > 0 ? tools[tools.length - 1] : null;
  if (!html && !running) return null;
  if (!html) {
    return (
      <div className="msg assistant live-stream">
        <div className="stream-thinking">
          <span className="wave-dots" aria-hidden="true">
            <i />
            <i />
            <i />
          </span>
          <span className="shimmer-text" dir="auto">
            {activeTool ? `${activeTool.label || activeTool.name}…` : t("Working") + "…"}
          </span>
        </div>
      </div>
    );
  }
  return (
    <div className="msg assistant live-stream">
      <Prose html={html} streaming />
    </div>
  );
}

function Intro() {
  const t = useT();
  const name = useProjectName();
  const fullPath = useStore((s) => s.projects.find((p) => p.id === s.current)?.path || "");
  const path = useStore((s) => shortPath(s.projects.find((p) => p.id === s.current)?.path || "", s.home));
  const noModel = useStore((s) => s.online === true && !s.info.model);
  const [copiedPath, setCopiedPath] = useState(false);

  const displayName = name === "Home" ? t("Home") : name;

  return (
    <div className="intro">
      <div className="intro-hero">
        <StarBuddy variant="stage" size="lg" interactive trackPointer title="Macron StarBuddy" />
        <p className="intro-ready">
          {displayName} {t("is ready.")}
        </p>
        <h2 className="intro-headline">
          {t("Ask, then watch it")} <span className="intro-accent">{t("work.")}</span>
        </h2>
        {path && (
          <button
            type="button"
            className={"intro-path" + (copiedPath ? " copied" : "")}
            title={copiedPath ? t("Copied") : `${fullPath || path} · ${t("Copy path")}`}
            dir="ltr"
            onClick={() => {
              void copyText(fullPath || path).then(() => {
                setCopiedPath(true);
                window.setTimeout(() => setCopiedPath(false), 1400);
              });
            }}
          >
            <Icon name={copiedPath ? "check" : "folder"} size={11} />
            <span>{copiedPath ? t("Copied") : "\u200E" + path}</span>
          </button>
        )}
      </div>

      {noModel && (
        <div className="intro-connect">
          <button type="button" className="btn primary intro-connect-btn" onClick={() => openDialog({ kind: "model-connect" })}>
            <Icon name="key" size={14} />
            <span>{t("Connect a model")}</span>
          </button>
          <span className="intro-connect-hint">{t("OpenAI, Anthropic, Gemini, OpenRouter, or local Ollama")}</span>
        </div>
      )}
    </div>
  );
}

export function Feed() {
  const t = useT();
  const blocks = useStore((s) => s.blocks);
  const fresh = useStore((s) => s.fresh);
  const running = useStore((s) => s.live.running);
  const current = useStore((s) => s.current);
  const scroller = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const [away, setAway] = useState(false);
  const items = useMemo(() => group(blocks), [blocks]);

  const panelPlan = useMemo(() => {
    for (let i = blocks.length - 1; i >= 0; i--) {
      if (blocks[i].kind === "divider" || blocks[i].kind === "user") return -1;
      if (blocks[i].kind === "plan") return i;
    }
    return -1;
  }, [blocks]);

  useLayoutEffect(() => {
    follow.on = true;
    setAway(false);
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [current]);

  useEffect(() => {
    const el = scroller.current;
    const content = inner.current;
    if (!el || !content) return;
    const ro = new ResizeObserver(() => {
      if (follow.on) el.scrollTop = el.scrollHeight;
    });
    ro.observe(el);
    ro.observe(content);
    return () => ro.disconnect();
  }, []);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    follow.on = near;
    setAway(!near);
  };

  const empty = !blocks.length && !running;
  return (
    <>
      <div className="feed" ref={scroller} onScroll={onScroll}>
        <div className="feed-inner" ref={inner}>
          {empty ? (
            <Intro />
          ) : (
            items.map((it, n) =>
              it.kind === "steps" ? (
                <Steps key={"s" + it.i + ":" + it.items[0].at} items={it.items} fresh={it.i >= fresh} live={running && n === items.length - 1} />
              ) : (
                <BlockView key={it.i + ":" + it.b.at} b={it.b} fresh={it.i >= fresh} panel={it.i === panelPlan} />
              ),
            )
          )}
          <Stream />
        </div>
      </div>
      {away && !empty && (
        <button
          type="button"
          className="jump"
          title={t("Jump to latest")}
          aria-label={t("Jump to latest")}
          onClick={() => {
            follow.on = true;
            setAway(false);
            const el = scroller.current;
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
          }}
        >
          <Icon name="arrowDown" size={14} />
        </button>
      )}
    </>
  );
}
