import { memo, useEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from "react";
import {
  deleteThread,
  installUpdate,
  loadServers,
  newChat,
  openDialog,
  openProject,
  openSettings,
  removeProject,
  setSideWidth,
  switchMachine,
  switchThread,
  toggleSidebarCollapsed,
} from "../actions";
import { ago, bytes, copyText, shortPath } from "../format";
import { useMod } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { ServerView, Summary, ThreadSummary } from "../types";
import { StarBuddy } from "./StarBuddy";

function serverLine(s: ServerView): string {
  if (s.state === "failed" && s.error) return s.error;
  if (s.state === "connecting") return "Connecting…";
  if (s.state === "installing") return "Installing Macron…";
  if (s.kind === "wsl") return "WSL" + (s.host ? " · " + s.host : "");
  return (s.user ? s.user + "@" : "") + (s.host || "") + (s.port && s.port !== 22 ? ":" + s.port : "");
}

function MachineMenu() {
  const t = useT();
  const machine = useStore((s) => s.machine);
  const servers = useStore((s) => s.servers);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    loadServers();
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
    };
    window.addEventListener("mousedown", away);
    window.addEventListener("keydown", esc, true);
    return () => {
      window.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", esc, true);
    };
  }, [open]);

  const cur = servers.find((x) => x.id === machine);
  const pick = (id: string) => {
    setOpen(false);
    switchMachine(id);
  };

  return (
    <div className="machine" ref={ref}>
      <button type="button" className={"machine-btn" + (open ? " open" : "")} onClick={() => setOpen(!open)} aria-expanded={open}>
        <Icon name={cur ? (cur.kind === "wsl" ? "terminal" : "server") : "monitor"} size={14} />
        <span className="mname">{cur ? cur.name : t("This PC")}</span>
        {cur && <span className={"sdot " + cur.state} />}
        <Icon name="down" size={12} className="mchev" />
      </button>
      {open && (
        <div className="menu" role="menu">
          <button type="button" role="menuitem" className={"mrow" + (machine === "" ? " on" : "")} onClick={() => pick("")}>
            <Icon name="monitor" size={14} />
            <span className="grow">
              <span className="name">{t("This PC")}</span>
              <span className="meta">{t("Work on this computer")}</span>
            </span>
            {machine === "" && <Icon name="check" size={14} />}
          </button>
          {servers.map((s) => (
            <button key={s.id} type="button" role="menuitem" className={"mrow" + (machine === s.id ? " on" : "")} onClick={() => pick(s.id)}>
              <Icon name={s.kind === "wsl" ? "terminal" : "server"} size={14} />
              <span className="grow">
                <span className="name">{s.name}</span>
                <span className={"meta" + (s.state === "failed" ? " bad" : "")}>{serverLine(s)}</span>
              </span>
              {machine === s.id ? <Icon name="check" size={14} /> : <span className={"sdot " + s.state} />}
            </button>
          ))}
          <div className="msep" />
          <button
            type="button"
            role="menuitem"
            className="mrow add"
            onClick={() => {
              setOpen(false);
              openDialog({ kind: "server-add" });
            }}
          >
            <Icon name="plus" size={14} />
            <span className="grow">
              <span className="name">{t("Add a server…")}</span>
            </span>
          </button>
        </div>
      )}
    </div>
  );
}

const ThreadList = memo(function ThreadList({ threads, active }: { threads: ThreadSummary[]; active: string }) {
  const t = useT();
  if (threads.length <= 1) return null;
  return (
    <div className="thread-list" aria-label={t("Threads")}>
      {threads.map((th) => (
        <div key={th.id} className={"thread-row" + (th.id === active ? " on" : "")}>
          <button type="button" className="thread-hit" onClick={() => switchThread(th.id)} title={th.title}>
            <span className={"thread-dot" + (th.id === active ? " on" : "")} aria-hidden="true" />
            <span className="thread-title" dir="auto">
              {th.title === "New conversation" ? t("New conversation") : th.title}
            </span>
          </button>
          <span className="thread-trail">
            {th.updated > 0 && <span className="thread-ago">{ago(th.updated)}</span>}
            <button
              type="button"
              className="thread-del"
              title={t("Remove")}
              aria-label={t("Remove")}
              onClick={(e) => {
                e.stopPropagation();
                deleteThread(th.id);
              }}
            >
              <Icon name="x" size={11} />
            </button>
          </span>
        </div>
      ))}
    </div>
  );
});

const EMPTY_THREADS: ThreadSummary[] = [];

const Row = memo(function Row({ p, on, isHome, home }: { p: Summary; on: boolean; isHome: boolean; home: string }) {
  const t = useT();
  const threads = useStore((s) => (on ? s.threads : EMPTY_THREADS));
  const activeThread = useStore((s) => s.activeThread);
  const [expanded, setExpanded] = useState(true);
  const [ctxOpen, setCtxOpen] = useState(false);
  const ctxRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!ctxOpen) return;
    const away = (e: MouseEvent) => {
      if (!ctxRef.current?.contains(e.target as Node)) setCtxOpen(false);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        setCtxOpen(false);
      }
    };
    window.addEventListener("mousedown", away);
    window.addEventListener("keydown", esc, true);
    return () => {
      window.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", esc, true);
    };
  }, [ctxOpen]);

  const preview = p.preview || (isHome ? t("General conversations") : shortPath(p.path, home));
  const hasThreads = on && threads.length > 1;

  const onContext = (e: ReactMouseEvent) => {
    e.preventDefault();
    setCtxOpen(true);
  };

  return (
    <div className="proj-group" ref={ctxRef}>
      <div className={"proj" + (on ? " on" : "") + (!isHome ? " removable" : "")} onContextMenu={onContext}>
        <button type="button" className="proj-hit" title={p.path} onClick={() => openProject(p.id)}>
          <span className="pn-wrap">
            {hasThreads ? (
              <span
                className={"proj-tree-chev" + (expanded ? " open" : "")}
                onClick={(e) => {
                  e.stopPropagation();
                  setExpanded((v) => !v);
                }}
                aria-hidden="true"
              >
                <Icon name="right" size={11} />
              </span>
            ) : (
              <span className="proj-ico" aria-hidden="true">
                <Icon name={isHome ? "home" : "folder"} size={13} />
              </span>
            )}
            <span className="pn">{isHome ? t("Home") : p.name}</span>
            {p.asking ? (
              <span className="badge ask" title={t("Waiting for your approval")} />
            ) : p.running ? (
              <span className="badge run" title={t("Working")} />
            ) : on ? (
              <span className="badge active" aria-hidden="true" />
            ) : null}
          </span>
          <span className="ago">{ago(p.updated)}</span>
          <span className="pv" dir="auto">
            {preview}
          </span>
        </button>

        <div className="proj-actions">
          {on && (
            <button
              type="button"
              className="proj-act"
              title={t("New thread")}
              aria-label={t("New thread")}
              onClick={(e) => {
                e.stopPropagation();
                newChat();
              }}
            >
              <Icon name="plus" size={12} />
            </button>
          )}
          {!isHome && (
            <button
              type="button"
              className="x"
              title={t("Remove from list (folder stays)")}
              aria-label={t("Remove")}
              onClick={(e) => {
                e.stopPropagation();
                removeProject(p.id);
              }}
            >
              <Icon name="x" size={12} />
            </button>
          )}
        </div>
      </div>

      {ctxOpen && (
        <div className="ctx-menu" role="menu">
          <button
            type="button"
            role="menuitem"
            className="ctx-row"
            onClick={() => {
              setCtxOpen(false);
              if (!on) void openProject(p.id).then(() => newChat());
              else newChat();
            }}
          >
            <Icon name="edit" size={13} />
            <span>{t("New conversation")}</span>
          </button>
          <button
            type="button"
            role="menuitem"
            className="ctx-row"
            onClick={() => {
              setCtxOpen(false);
              void copyText(p.path);
            }}
          >
            <Icon name="copy" size={13} />
            <span>{t("Copy path")}</span>
          </button>
          {!isHome && (
            <button
              type="button"
              role="menuitem"
              className="ctx-row bad"
              onClick={() => {
                setCtxOpen(false);
                removeProject(p.id);
              }}
            >
              <Icon name="trash" size={13} />
              <span>{t("Remove")}</span>
            </button>
          )}
        </div>
      )}

      {hasThreads && expanded && <ThreadList threads={threads} active={activeThread} />}
    </div>
  );
});

function UpdateCard() {
  const t = useT();
  const upd = useStore((s) => s.update);
  const machine = useStore((s) => s.machine);
  if (machine || !upd || (!upd.available && !upd.installing && !upd.installed && !upd.error)) return null;
  if (upd.error && !upd.available && !upd.installing) return null;
  const pct = Math.max(0, Math.min(100, Math.round(upd.percent || 0)));
  const total = upd.total || upd.assetSize || 0;
  const downloaded = upd.downloaded || 0;
  const speed = upd.speedBps || 0;
  const phaseLabel = upd.step ? t(upd.step) : t("Updating…");
  const telemetry =
    upd.phase === "downloading" && total > 0
      ? `${bytes(downloaded)} / ${bytes(total)}${speed > 0 ? ` · ${bytes(speed)}/s` : ""}`
      : upd.phase === "downloading" && downloaded > 0
        ? `${bytes(downloaded)}${speed > 0 ? ` · ${bytes(speed)}/s` : ""}`
        : "";

  return (
    <div className={"upd-card" + (upd.installed ? " done" : upd.installing ? " busy" : upd.error ? " err" : "")}>
      <div className="upd-top">
        <span className={"upd-dot" + (upd.installed ? " done" : upd.installing ? " busy" : upd.error ? " err" : "")} />
        <span className="grow upd-title">
          {upd.installed
            ? t("Updated — restart Macron")
            : upd.installing
              ? `${t("Updating")} v${upd.latest || ""}`
              : `${t("Update available")} · v${upd.latest}`}
        </span>
        {!upd.installing && !upd.installed && total > 0 && (
          <span className="upd-size" dir="ltr">
            {bytes(total)}
          </span>
        )}
        {upd.installing && (
          <span className="upd-pct" dir="ltr">
            {pct}%
          </span>
        )}
      </div>

      {upd.installing && (
        <div className="upd-prog">
          <div className="upd-bar" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
            <span className={"upd-fill" + (pct < 3 ? " indet" : "")} style={{ width: `${Math.max(pct, 4)}%` }} />
          </div>
          <div className="upd-sub">
            <span className="upd-phase">{phaseLabel}</span>
            {telemetry && (
              <span className="upd-meta" dir="ltr">
                {telemetry}
              </span>
            )}
          </div>
        </div>
      )}

      {!upd.installing && upd.error && (
        <div className="upd-err" dir="auto">
          {upd.error}
        </div>
      )}

      {!upd.installing && !upd.installed && (
        <button type="button" className="upd-btn" onClick={() => installUpdate()}>
          {upd.error ? t("Retry") : t("Update now")}
        </button>
      )}
    </div>
  );
}

export function Sidebar() {
  const t = useT();
  const projects = useStore((s) => s.projects);
  const current = useStore((s) => s.current);
  const defaultId = useStore((s) => s.defaultId);
  const home = useStore((s) => s.home);
  const online = useStore((s) => s.online);
  const version = useStore((s) => s.info.version);
  const sideWidth = useStore((s) => s.sideWidth);
  const lang = useStore((s) => s.lang);
  const mod = useMod();
  const [resizing, setResizing] = useState(false);

  const onResizeStart = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const startX = e.clientX;
    const startW = sideWidth;
    const isRTL = lang === "fa";
    setResizing(true);
    const onMove = (ev: PointerEvent) => {
      const delta = isRTL ? startX - ev.clientX : ev.clientX - startX;
      setSideWidth(startW + delta);
    };
    const onUp = () => {
      setResizing(false);
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
  };

  return (
    <aside className={"side" + (resizing ? " resizing" : "")}>
      <div className="brand">
        <div className="brand-id">
          <StarBuddy size="sm" interactive />
          <span className="brand-name">Macron</span>
        </div>
        <button
          type="button"
          className="icon-btn sm side-collapse-btn"
          title={`${t("Collapse sidebar")} (${mod}+\\)`}
          aria-label={t("Collapse sidebar")}
          onClick={() => toggleSidebarCollapsed(true)}
        >
          <Icon name="sidebar" size={14} />
        </button>
      </div>

      <MachineMenu />

      <div className="side-quick">
        <button type="button" className="quick-primary" title={`${t("New conversation")} (${mod}+N)`} onClick={newChat}>
          <Icon name="edit" size={14} />
          <span>{t("New conversation")}</span>
        </button>
        <button
          type="button"
          className="icon-btn sm quick-ico"
          title={`${t("Search chats")} (${mod}+Shift+F)`}
          aria-label={t("Search chats")}
          onClick={() => openDialog({ kind: "search" })}
        >
          <Icon name="search" size={14} />
        </button>
        <button
          type="button"
          className="icon-btn sm quick-ico"
          title={`${t("Open folder")} (${mod}+O)`}
          aria-label={t("Open folder")}
          onClick={() => openDialog({ kind: "folder" })}
        >
          <Icon name="plus" size={14} />
        </button>
      </div>

      <div className="side-label">
        <span>{t("Workspaces")}</span>
        <span className="side-count">{projects.length}</span>
      </div>

      <nav className="projects" aria-label={t("Workspaces")}>
        {projects.map((p) => (
          <Row key={p.id} p={p} on={p.id === current} isHome={p.id === defaultId} home={home} />
        ))}
      </nav>

      <div className="side-foot">
        <UpdateCard />
        <button type="button" className="nav" onClick={() => openSettings("general")}>
          <Icon name="sliders" size={15} />
          <span>{t("Settings")}</span>
          <kbd>{mod} ,</kbd>
        </button>
        <div className="side-meta-row">
          <div className={"engine" + (online === true ? " ok" : online === false ? " bad" : "")}>
            <span className="dot" />
            <span>{online === null ? t("Connecting…") : online ? t("Engine ready") + (version ? " · " + version : "") : t("Reconnecting…")}</span>
          </div>
          <a className="side-credit" href="https://t.me/SpectorDev" target="_blank" rel="noreferrer" dir="ltr">
            SpectorDev
          </a>
        </div>
      </div>

      <div
        className="side-resizer"
        role="separator"
        aria-orientation="vertical"
        title="Drag to resize sidebar (double-click to reset)"
        onPointerDown={onResizeStart}
        onDoubleClick={() => setSideWidth(256)}
      />
    </aside>
  );
}
