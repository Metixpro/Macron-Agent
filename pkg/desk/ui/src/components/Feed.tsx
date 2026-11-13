import { memo, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { follow, toast } from "../actions";
import { bidi, clock, copyText, prettyArgs, shortPath } from "../format";
import { useMod, useProjectName } from "../hooks";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { Block, PlanItem } from "../types";

export function Prose({ html, streaming }: { html: string; streaming?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (ref.current) bidi(ref.current);
  }, [html]);
  const onClick = (e: MouseEvent<HTMLDivElement>) => {
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

function Steps({ items, fresh }: { items: Block[]; fresh: boolean }) {
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
  return (
    <div className={"steps grouped" + (open ? " open" : "") + (fresh ? " enter" : "")}>
      <button type="button" className="tool-row head" onClick={() => setOpen(!open)} aria-expanded={open}>
        <span className="st">
          <Icon name="steps" size={13} />
        </span>
        <span className="lb">{items.length} steps</span>
        <span className="ar">
          {failed > 0 && <em>{failed} failed · </em>}
          {open ? "" : (last.label || last.name) + " " + (last.arg || "")}
        </span>
        <span className="chev">
          <Icon name="right" size={12} />
        </span>
      </button>
      {open && (
        <div className="steps-body">
          {items.map((b, i) => (
            <ToolRow key={i + ":" + b.at} b={b} />
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
            items.map((it) =>
              it.kind === "steps" ? (
                <Steps key={"s" + it.i + ":" + it.items[0].at} items={it.items} fresh={it.i >= fresh} />
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
