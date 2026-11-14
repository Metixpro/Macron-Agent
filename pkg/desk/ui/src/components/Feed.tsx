import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { follow, onServer, openDialog, openFile, saveFile, toast } from "../actions";
import { fileURL } from "../api";
import { bidi, bytes, clock, copyText, prettyArgs, shortPath } from "../format";
import { useMod, useProjectName } from "../hooks";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { Block, FileRef, PlanItem } from "../types";

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
      window.setTimeout(() => (btn.textContent = "Copy"), 1400);
    });
  };
  return <div ref={ref} className={streaming ? "prose streaming" : "prose"} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
}

export function PlanList({ items, live }: { items: PlanItem[]; live?: boolean }) {
  const done = items.filter((i) => i.status === "done").length;
  return (
    <div className={live ? "plan live" : "plan"}>
      <div className="plan-head">
        <span>Plan</span>
        <span className="plan-count">
          {done} of {items.length}
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

const ToolRow = memo(function ToolRow({ b }: { b: Block }) {
  const [open, setOpen] = useState(false);
  const args = open && b.args && b.args !== "{}" ? prettyArgs(b.args) : "";
  return (
    <div className={"tool" + (b.failed ? " fail" : "") + (open ? " open" : "")}>
      <button type="button" className="tool-row" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="st">
          <Icon name={b.failed ? "x" : "check"} size={13} />
        </span>
        <span className="lb">{b.label || b.name}</span>
        <span className="ar">{b.arg}</span>
        <span className="chev">
          <Icon name="right" size={12} />
        </span>
      </button>
      {!open && b.failed && b.summary && <div className="why">{b.summary}</div>}
      {open && (
        <div className="detail">
          {args && args.trim() !== (b.arg || "").trim() && <div className="args">{args}</div>}
          <pre className="out">{b.output && b.output.trim() ? b.output : "(no output)"}</pre>
        </div>
      )}
    </div>
  );
});

function Steps({ items, fresh, live }: { items: Block[]; fresh: boolean; live: boolean }) {
  const [open, setOpen] = useState(false);
  if (items.length <= 3) {
    return (
      <div className={"steps" + (fresh ? " enter" : "")}>
        {items.map((b, i) => (
          <ToolRow key={i + ":" + b.at} b={b} />
        ))}
      </div>
    );
  }
  const failed = items.filter((b) => b.failed).length;
  const last = items[items.length - 1];
  const tail = !open && live ? 3 : 0;
  const shown = open ? items : items.slice(items.length - tail);
  const from = items.length - shown.length;
  return (
    <div className={"steps grouped" + (open ? " open" : "") + (tail ? " live" : "") + (fresh ? " enter" : "")}>
      <button type="button" className="tool-row head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="st">
          <Icon name="steps" size={13} />
        </span>
        <span className="lb">{tail ? items.length - tail + " earlier steps" : items.length + " steps"}</span>
        <span className="ar">
          {failed > 0 && <em>{failed} failed{open || tail ? "" : " · "}</em>}
          {open || tail ? "" : (last.label || last.name) + " " + (last.arg || "")}
        </span>
        <span className="chev">
          <Icon name="right" size={12} />
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
  return (
    <div className="msg-actions">
      <button type="button" className="act" title="Copy the answer" onClick={() => copyText(b.text || "").then(() => toast("Copied"))}>
        <Icon name="copy" size={13} />
        <span>Copy</span>
      </button>
      {b.at > 0 && <span className="when">{clock(b.at)}</span>}
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
          <Icon name={kind.icon} size={18} />
        </span>
        <span className="file-meta">
          <span className="file-name" dir="auto" title={f.name}>
            {f.base}
          </span>
          <span className="file-size">
            {kind.label} · {bytes(f.size)}
          </span>
        </span>
        <span className="file-actions">
          {!remote && !runs && (
            <button type="button" className="btn sm" onClick={() => openFile(f.name)}>
              <Icon name="open" size={14} />
              Open
            </button>
          )}
          {!remote && (
            <button type="button" className="icon-btn sm" title="Show in folder" aria-label="Show in folder" onClick={() => openFile(f.name, true)}>
              <Icon name="folder" size={15} />
            </button>
          )}
          <button type="button" className="icon-btn sm" title="Save a copy" aria-label="Save a copy" onClick={() => saveFile(f.name, f.base)}>
            <Icon name="save" size={15} />
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

const BlockView = memo(function BlockView({ b, fresh }: { b: Block; fresh: boolean }) {
  const enter = fresh ? " enter" : "";
  switch (b.kind) {
    case "user":
      return (
        <div className={"msg user" + enter}>
          <div className="bubble" dir="auto" title={b.at ? new Date(b.at).toLocaleString() : undefined}>
            {b.text}
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
      return <div className={"block" + enter}>{items.length > 0 && <PlanList items={items} />}</div>;
    }
    case "file":
      return b.file ? <FileCard f={b.file} fresh={fresh} /> : null;
    case "divider":
      return (
        <div className={"divider" + enter}>
          <span>{b.text || "New conversation"}</span>
        </div>
      );
    case "error":
      return (
        <div className={"error" + enter}>
          <div className="et">{b.text}</div>
          {b.summary && <div className="eh">{b.summary}</div>}
          {b.output && b.output !== b.text && (
            <details>
              <summary>Details</summary>
              <div>{b.output}</div>
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
  const html = useStore((s) => s.live.html);
  if (!html) return null;
  return (
    <div className="msg assistant">
      <Prose html={html} streaming />
    </div>
  );
}

function Intro() {
  const name = useProjectName();
  const isHome = useStore((s) => s.current === s.defaultId);
  const path = useStore((s) => shortPath(s.projects.find((p) => p.id === s.current)?.path || "", s.home));
  const mod = useMod();
  const noModel = useStore((s) => s.online === true && !s.info.model);
  const keys: [string, string[]][] = [
    ["Command palette", [mod, "K"]],
    ["New conversation", [mod, "N"]],
    ["Plan before changing anything", [mod, "Shift", "P"]],
    ["Open another folder", [mod, "O"]],
    ["Stop the current job", ["Esc"]],
  ];
  return (
    <div className="intro">
      <div className="intro-name">{name}</div>
      <div className="intro-path">{"‎" + path}</div>
      {noModel && (
        <div className="setup-card">
          <div>
            <div className="setup-title">Connect a model to start</div>
            <div className="setup-text">Macron needs a language model: OpenAI, Anthropic, Gemini, OpenRouter, a local Ollama, or any compatible provider.</div>
          </div>
          <button type="button" className="btn primary" onClick={() => openDialog({ kind: "model-connect" })}>
            Connect a model
          </button>
        </div>
      )}
      <p>
        {isHome
          ? "Conversations that don't belong to a project live here. Open a folder to give Macron a project to work in."
          : "Macron reads and changes files in this folder, runs commands here, and looks things up when it needs to."}
      </p>
      <dl className="keys">
        {keys.map(([label, combo]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>
              {combo.map((k) => (
                <kbd key={k}>{k}</kbd>
              ))}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

export function Feed() {
  const blocks = useStore((s) => s.blocks);
  const fresh = useStore((s) => s.fresh);
  const running = useStore((s) => s.live.running);
  const current = useStore((s) => s.current);
  const scroller = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const [away, setAway] = useState(false);
  const items = useMemo(() => group(blocks), [blocks]);

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
                <BlockView key={it.i + ":" + it.b.at} b={it.b} fresh={it.i >= fresh} />
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
          title="Jump to the latest"
          onClick={() => {
            follow.on = true;
            setAway(false);
            const el = scroller.current;
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
          }}
        >
          <Icon name="arrowDown" size={15} />
        </button>
      )}
    </>
  );
}
