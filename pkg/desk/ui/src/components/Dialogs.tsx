import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import {
  addFolder,
  answer,
  chooseModel,
  closeDialog,
  compact,
  errorText,
  fail,
  newChat,
  openDialog,
  openProject,
  setTheme,
  stop,
  toast,
  toggleMode,
} from "../actions";
import { server } from "../api";
import { shortPath } from "../format";
import { useMod, useProjectName } from "../hooks";
import { Icon } from "../icons";
import type { IconName } from "../icons";
import { getState, setState, useStore } from "../store";
import type { Theme } from "../store";
import type { Ask, Connection, Listing, ProviderChoice } from "../types";

function Modal({ kind, dismiss, children }: { kind: string; dismiss: boolean; children: ReactNode }) {
  return (
    <div
      className={"overlay " + kind}
      onMouseDown={(e) => {
        if (dismiss && e.target === e.currentTarget) closeDialog();
      }}
    >
      <div className={"dialog " + kind} role="dialog" aria-modal="true">
        {children}
      </div>
    </div>
  );
}

export function Dialogs() {
  const d = useStore((s) => s.dialog);
  if (!d) return null;
  switch (d.kind) {
    case "ask":
      return (
        <Modal kind="ask" dismiss={false}>
          <AskDialog ask={d.ask} />
        </Modal>
      );
    case "palette":
      return (
        <Modal kind="palette" dismiss>
          <Palette key={d.mode} mode={d.mode} />
        </Modal>
      );
    case "folder":
      return (
        <Modal kind="folder" dismiss>
          <FolderPicker />
        </Modal>
      );
    case "settings":
      return (
        <Modal kind="settings" dismiss>
          <Settings />
        </Modal>
      );
  }
}

function AskDialog({ ask }: { ask: Ask }) {
  const name = useProjectName();
  const opened = useRef(Date.now());
  useEffect(() => {
    opened.current = Date.now();
    (document.activeElement as HTMLElement | null)?.blur();
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        answer("deny");
      } else if (e.key === "Enter" && !(document.activeElement instanceof HTMLButtonElement)) {
        e.preventDefault();
        if (Date.now() - opened.current > 700) answer("once");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ask.id]);
  return (
    <>
      <div className="ask-head">
        <span className="ask-icon">
          <Icon name="alert" size={16} />
        </span>
        <div>
          <h3>{ask.title || "Allow this?"}</h3>
          <div className="sub">Macron wants to run this in {name}.</div>
        </div>
      </div>
      <pre className="command">{ask.command}</pre>
      {(ask.findings || []).map((f) => (
        <div key={f} className="finding">
          {f}
        </div>
      ))}
      {ask.guardian && <div className="guardian">{ask.guardian}</div>}
      <div className="bar">
        <div className="left">
          <button type="button" className="btn primary" onClick={() => answer("once")}>
            Allow once
          </button>
          <button type="button" className="btn" onClick={() => answer("session")}>
            Allow this session
          </button>
          <button type="button" className="btn" onClick={() => answer("always")}>
            Always allow
          </button>
        </div>
        <button type="button" className="btn danger" onClick={() => answer("deny")}>
          Deny
        </button>
      </div>
      <div className="foot-keys">
        <kbd>Enter</kbd> allow once <kbd>Esc</kbd> deny
      </div>
    </>
  );
}

type Entry = { id: string; group: string; label: string; hint?: string; icon: IconName; on?: boolean; keys?: string[]; run: () => void };

function useList(count: number) {
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (sel >= count) setSel(Math.max(0, count - 1));
  }, [count, sel]);
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(".sel");
    el?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const move = (e: KeyboardEvent) => {
    if (!count) return false;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setSel((i) => (i + (e.key === "ArrowDown" ? 1 : count - 1)) % count);
      return true;
    }
    return false;
  };
  return { sel, setSel, listRef, move };
}

function Palette({ mode }: { mode: "root" | "models" }) {
  const projects = useStore((s) => s.projects);
  const current = useStore((s) => s.current);
  const defaultId = useStore((s) => s.defaultId);
  const home = useStore((s) => s.home);
  const appMode = useStore((s) => s.mode);
  const theme = useStore((s) => s.theme);
  const running = useStore((s) => s.live.running);
  const info = useStore((s) => s.info);
  const mod = useMod();
  const [q, setQ] = useState("");
  const [models, setModels] = useState<string[] | null>(null);
  const [active, setActive] = useState(info.model);
  const [error, setError] = useState("");

  useEffect(() => {
    if (mode !== "models") return;
    server
      .models()
      .then((r) => {
        setModels(r.models || []);
        if (r.current) setActive(r.current);
      })
      .catch((e) => {
        setModels([]);
        setError(errorText(e));
      });
  }, [mode]);

  const entries = useMemo<Entry[]>(() => {
    if (mode === "models") {
      const list = (models || []).map<Entry>((m) => ({ id: "m:" + m, group: "Models", label: m, icon: "chip", on: m === active, run: () => chooseModel(m) }));
      const typed = q.trim();
      if (typed && !(models || []).includes(typed)) list.push({ id: "custom", group: "Models", label: `Use “${typed}”`, icon: "chip", run: () => chooseModel(typed) });
      return list;
    }
    const themes: [Theme, string, IconName][] = [
      ["system", "Match the system", "monitor"],
      ["dark", "Dark", "moon"],
      ["light", "Light", "sun"],
    ];
    const list: Entry[] = [
      { id: "new", group: "Actions", label: "New conversation", icon: "edit", keys: [mod, "N"], run: () => newChat() },
      appMode === "plan"
        ? { id: "mode", group: "Actions", label: "Switch to Build mode", hint: "Let Macron make changes", icon: "code", keys: [mod, "Shift", "P"], run: () => toggleMode() }
        : { id: "mode", group: "Actions", label: "Switch to Plan mode", hint: "Plan first, change nothing", icon: "map", keys: [mod, "Shift", "P"], run: () => toggleMode() },
      { id: "model", group: "Actions", label: "Change model…", hint: info.model, icon: "chip", run: () => openDialog({ kind: "palette", mode: "models" }) },
      { id: "folder", group: "Actions", label: "Open folder…", icon: "folder", keys: [mod, "O"], run: () => openDialog({ kind: "folder" }) },
      { id: "compact", group: "Actions", label: "Compact the conversation", hint: "Summarise to free room", icon: "layers", run: () => compact() },
      { id: "settings", group: "Actions", label: "Settings", icon: "sliders", keys: [mod, ","], run: () => openDialog({ kind: "settings" }) },
    ];
    if (running) list.unshift({ id: "stop", group: "Actions", label: "Stop the current job", icon: "square", keys: ["Esc"], run: () => stop() });
    for (const [t, label, icon] of themes) list.push({ id: "t:" + t, group: "Theme", label, icon, on: theme === t, run: () => setTheme(t) });
    for (const p of projects) {
      list.push({
        id: "p:" + p.id,
        group: "Workspaces",
        label: p.id === defaultId ? "Home" : p.name,
        hint: shortPath(p.path, home),
        icon: p.id === defaultId ? "home" : "folder",
        on: p.id === current,
        run: () => openProject(p.id),
      });
    }
    return list;
  }, [mode, models, active, q, mod, appMode, info.model, running, theme, projects, defaultId, current, home]);

  const shown = useMemo(() => {
    const f = q.trim().toLowerCase();
    if (!f || mode === "models") return mode === "models" && f ? entries.filter((e) => e.id === "custom" || e.label.toLowerCase().includes(f)) : entries;
    return entries.filter((e) => (e.label + " " + (e.hint || "") + " " + e.group).toLowerCase().includes(f));
  }, [entries, q, mode]);

  const { sel, setSel, listRef, move } = useList(shown.length);

  const pick = (e: Entry) => {
    const opensDialog = e.id === "model" || e.id === "folder" || e.id === "settings";
    if (!opensDialog) closeDialog();
    e.run();
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (move(e)) return;
    if (e.key === "Enter" && shown[sel]) {
      e.preventDefault();
      pick(shown[sel]);
    } else if (e.key === "Backspace" && !q && mode === "models") {
      openDialog({ kind: "palette", mode: "root" });
    }
  };

  let lastGroup = "";
  return (
    <>
      <div className="palette-input">
        <Icon name="search" size={15} />
        <input
          autoFocus
          spellCheck={false}
          value={q}
          placeholder={mode === "models" ? "Search models, or type any model name" : "Type a command or a workspace"}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={onKey}
        />
        {mode === "models" && info.provider && <span className="scope">{info.provider}</span>}
      </div>
      <div className="palette-list" ref={listRef}>
        {mode === "models" && models === null && <div className="empty-row">Loading models…</div>}
        {error && <div className="empty-row">{error}</div>}
        {models !== null && !shown.length && !error && <div className="empty-row">Nothing matches</div>}
        {shown.map((e, i) => {
          const head = e.group !== lastGroup ? e.group : "";
          lastGroup = e.group;
          return (
            <div key={e.id}>
              {head && mode !== "models" && <div className="group">{head}</div>}
              <button type="button" className={"entry" + (i === sel ? " sel" : "")} onMouseMove={() => i !== sel && setSel(i)} onClick={() => pick(e)}>
                <Icon name={e.icon} size={15} />
                <span className="label">{e.label}</span>
                {e.hint && <span className="hint">{e.hint}</span>}
                <span className="end">
                  {e.on && <Icon name="check" size={14} className="tick" />}
                  {e.keys && e.keys.map((k) => <kbd key={k}>{k}</kbd>)}
                </span>
              </button>
            </div>
          );
        })}
      </div>
      <div className="palette-foot">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> move
        </span>
        <span>
          <kbd>Enter</kbd> choose
        </span>
        <span>
          <kbd>Esc</kbd> close
        </span>
      </div>
    </>
  );
}

function crumbs(path: string, sep: string): { label: string; path: string }[] {
  const out: { label: string; path: string }[] = [];
  const parts = path.split(/[\\/]+/).filter(Boolean);
  if (sep === "/") {
    out.push({ label: "/", path: "/" });
    let acc = "";
    for (const p of parts) {
      acc += "/" + p;
      out.push({ label: p, path: acc });
    }
    return out;
  }
  let acc = "";
  parts.forEach((p, i) => {
    acc = i === 0 ? p + "\\" : acc.replace(/\\$/, "") + "\\" + p;
    out.push({ label: p, path: acc });
  });
  return out;
}

function FolderPicker() {
  const [listing, setListing] = useState<Listing | null>(null);
  const [input, setInput] = useState("");
  const [error, setError] = useState("");
  const dirs = listing?.dirs || [];
  const { sel, setSel, listRef, move } = useList(dirs.length);
  const [moved, setMoved] = useState(false);
  const pathRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const el = pathRef.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [listing]);

  const go = async (path: string) => {
    try {
      const r = await server.fs(path);
      setListing(r);
      setInput(r.path);
      setSel(0);
      setMoved(false);
      setError("");
    } catch (e) {
      setError(errorText(e));
    }
  };

  useEffect(() => {
    go("");
  }, []);

  const join = (name: string) => (listing ? listing.path.replace(/[\\/]+$/, "") + listing.sep + name : name);

  const choose = async (path: string) => {
    closeDialog();
    try {
      await addFolder(path);
    } catch (e) {
      fail(e);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (move(e)) {
      setMoved(true);
      return;
    }
    if (e.key !== "Enter" || !listing) return;
    e.preventDefault();
    if (e.ctrlKey || e.metaKey) return choose(listing.path);
    if (input.trim() && input.trim() !== listing.path) return void go(input.trim());
    if (moved && dirs[sel]) return void go(join(dirs[sel]));
    choose(listing.path);
  };

  const name = listing ? listing.path.split(/[\\/]+/).filter(Boolean).pop() || listing.path : "";
  return (
    <>
      <h3>Open a folder</h3>
      <div className="sub">Macron works inside the folder you choose. Each folder keeps its own conversation.</div>
      <div className="path-input">
        <Icon name="folder" size={15} />
        <input ref={pathRef} autoFocus spellCheck={false} dir="ltr" value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={onKey} placeholder="Type or paste a path" />
      </div>
      {listing && (
        <div className="crumbs">
          {crumbs(listing.path, listing.sep).map((c, i, all) => (
            <span key={c.path}>
              <button type="button" className={i === all.length - 1 ? "here" : ""} onClick={() => go(c.path)}>
                {c.label}
              </button>
              {i < all.length - 1 && c.label !== "/" && <span className="slash-sep">/</span>}
            </span>
          ))}
        </div>
      )}
      <div className="folder-list" ref={listRef}>
        {error && <div className="empty-row">{error}</div>}
        {listing && !dirs.length && !error && <div className="empty-row">No folders inside</div>}
        {dirs.map((d, i) => (
          <button
            key={d}
            type="button"
            className={"entry" + (moved && i === sel ? " sel" : "")}
            onClick={() => go(join(d))}
            onDoubleClick={() => choose(join(d))}
          >
            <Icon name="folder" size={15} />
            <span className="label">{d}</span>
            <span className="end">
              <Icon name="right" size={12} />
            </span>
          </button>
        ))}
      </div>
      <div className="bar">
        <div className="left">
          {listing?.home && (
            <button type="button" className="btn ghost" onClick={() => go(listing.home)}>
              <Icon name="home" size={14} />
              Home
            </button>
          )}
          {listing?.parent && (
            <button type="button" className="btn ghost" onClick={() => go(listing.parent)}>
              <Icon name="arrowUp" size={14} />
              Up
            </button>
          )}
          {(listing?.roots || [])
            .filter((r) => r !== "/" && r !== listing?.path)
            .map((r) => (
              <button key={r} type="button" className="btn ghost" onClick={() => go(r)}>
                <Icon name="drive" size={14} />
                {r}
              </button>
            ))}
        </div>
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={!listing} onClick={() => listing && choose(listing.path)}>
            Open {name ? `“${name}”` : ""}
          </button>
        </div>
      </div>
    </>
  );
}

function Choice<T extends string>({ value, options, onPick }: { value: T; options: [T, string][]; onPick: (v: T) => void }) {
  return (
    <div className="choice" role="radiogroup">
      {options.map(([v, label]) => (
        <button key={v} type="button" role="radio" aria-checked={v === value} className={v === value ? "on" : ""} onClick={() => v !== value && onPick(v)}>
          {label}
        </button>
      ))}
    </div>
  );
}

function Section({ title, help, children }: { title: string; help?: string; children: ReactNode }) {
  return (
    <section className="set">
      <div className="set-text">
        <div className="set-title">{title}</div>
        {help && <div className="set-help">{help}</div>}
      </div>
      <div className="set-control">{children}</div>
    </section>
  );
}

function Settings() {
  const info = useStore((s) => s.info);
  const theme = useStore((s) => s.theme);
  const mod = useMod();
  const [providers, setProviders] = useState<ProviderChoice[] | null>(null);
  const [connections, setConnections] = useState<Connection[] | null>(null);

  useEffect(() => {
    server.providers().then((r) => setProviders(r.providers || [])).catch(() => setProviders([]));
    server.connections().then((r) => setConnections(r.connections || [])).catch(() => setConnections([]));
  }, [info.provider]);

  const apply = async (fn: () => Promise<{ info: typeof info }>, done?: string) => {
    try {
      const r = await fn();
      setState({ info: r.info });
      if (done) toast(done);
    } catch (e) {
      fail(e);
    }
  };

  const approvals = (info.approvals || "smart") as "smart" | "manual" | "off";
  const approvalHelp = {
    smart: "Safe commands run on their own. Anything risky waits for you.",
    manual: "Every command waits for your OK.",
    off: "Nothing waits. Only for machines you can afford to break.",
  }[approvals];

  return (
    <>
      <div className="settings-head">
        <h3>Settings</h3>
        <button type="button" className="icon-btn" title="Close (Esc)" onClick={closeDialog}>
          <Icon name="x" />
        </button>
      </div>
      <div className="settings-body">
        <div className="set-group">General</div>
        <Section title="Appearance">
          <Choice<Theme>
            value={theme}
            options={[
              ["system", "System"],
              ["light", "Light"],
              ["dark", "Dark"],
            ]}
            onPick={setTheme}
          />
        </Section>
        <div className="set-group">Safety</div>
        <Section title="Command approvals" help={approvalHelp}>
          <Choice
            value={approvals}
            options={[
              ["smart", "Smart"],
              ["manual", "Always ask"],
              ["off", "Never ask"],
            ]}
            onPick={(m) => apply(() => server.setApprovals(m))}
          />
        </Section>
        <Section title="YOLO for 30 minutes" help={info.yolo ? "On. Commands run without asking until it runs out." : "Let Macron run everything without asking, for a while."}>
          <button
            type="button"
            role="switch"
            aria-checked={info.yolo}
            className={"switch" + (info.yolo ? " on" : "")}
            onClick={() => apply(() => server.setYolo(!getState().info.yolo), info.yolo ? "YOLO is off" : "YOLO is on for 30 minutes")}
          >
            <span />
          </button>
        </Section>
        <div className="set-group">Model</div>
        <div className="set-list">
          {providers === null && <div className="empty-row">Loading…</div>}
          {providers && !providers.length && <div className="empty-row">No provider yet. Run «macron setup» in a terminal to connect one.</div>}
          {(providers || []).map((p) => (
            <button
              key={p.id}
              type="button"
              className={"set-row" + (p.active ? " on" : "")}
              disabled={p.active}
              onClick={() => apply(() => server.setProvider(p.id), "Provider: " + p.name)}
            >
              <span className="radio" />
              <span className="grow">
                <span className="name">{p.name}</span>
                <span className="meta">{p.model || "no model chosen"}</span>
              </span>
              {!p.enabled && <span className="meta">disabled</span>}
            </button>
          ))}
        </div>
        <div className="set-group">Connections</div>
        <div className="set-list">
          {connections === null && <div className="empty-row">Loading…</div>}
          {(connections || []).map((c) => (
            <div key={c.id} className="set-row static">
              <span className={"state" + (c.ready ? " ok" : "")} />
              <span className="grow">
                <span className="name">{c.name}</span>
                {c.capability && <span className="meta">{c.capability}</span>}
              </span>
              <span className="meta">{c.ready ? "Connected" : "Not set up"}</span>
            </div>
          ))}
        </div>
        <div className="set-group">Keyboard</div>
        <dl className="keys compact">
          {(
            [
              ["Send", ["Enter"]],
              ["New line", ["Shift", "Enter"]],
              ["Stop", ["Esc"]],
              ["Command palette", [mod, "K"]],
              ["New conversation", [mod, "N"]],
              ["Plan mode", [mod, "Shift", "P"]],
              ["Open folder", [mod, "O"]],
            ] as [string, string[]][]
          ).map(([label, combo]) => (
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
        <div className="about">
          Macron {info.version} · {info.platform}
          {info.host ? " · " + info.host : ""}
        </div>
      </div>
    </>
  );
}
