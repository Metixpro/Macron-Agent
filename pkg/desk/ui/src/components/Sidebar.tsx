import { memo, useEffect, useRef, useState } from "react";
import { deleteThread, installUpdate, loadServers, openDialog, openProject, removeProject, switchMachine, switchThread } from "../actions";
import { ago, bytes, shortPath } from "../format";
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
        <Icon name={cur ? (cur.kind === "wsl" ? "terminal" : "server") : "monitor"} size={15} />
        <span className="mname">{cur ? cur.name : t("This PC")}</span>
        {cur && <span className={"sdot " + cur.state} />}
        <Icon name="down" size={12} />
      </button>
      {open && (
        <div className="menu" role="menu">
          <button type="button" role="menuitem" className={"mrow" + (machine === "" ? " on" : "")} onClick={() => pick("")}>
            <Icon name="monitor" size={15} />
            <span className="grow">
              <span className="name">{t("This PC")}</span>
              <span className="meta">{t("Work on this computer")}</span>
            </span>
            {machine === "" && <Icon name="check" size={14} />}
          </button>
          {servers.map((s) => (
            <button key={s.id} type="button" role="menuitem" className={"mrow" + (machine === s.id ? " on" : "")} onClick={() => pick(s.id)}>
              <Icon name={s.kind === "wsl" ? "terminal" : "server"} size={15} />
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
            <Icon name="plus" size={15} />
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
              <Icon name="x" size={12} />
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
  const preview = p.preview || (isHome ? t("General conversations") : shortPath(p.path, home));
  return (
    <>
      <div className={"proj" + (on ? " on" : "") + (!isHome ? " removable" : "")}>
        <button type="button" className="proj-hit" title={p.path} onClick={() => openProject(p.id)}>
          <span className="pn-wrap">
            {p.asking ? (
              <span className="badge ask" title="Waiting for your approval" />
            ) : p.running ? (
              <span className="badge run" title={t("Working")} />
            ) : on ? (
              <span className="badge active" aria-hidden="true" />
            ) : null}
            <span className="pn">{isHome ? t("Home") : p.name}</span>
          </span>
          <span className="ago">{ago(p.updated)}</span>
          <span className="pv" dir="auto">
            {preview}
          </span>
        </button>
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
            <Icon name="x" size={13} />
          </button>
        )}
      </div>
      {on && <ThreadList threads={threads} active={activeThread} />}
    </>
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

      {!upd.installing && upd.error && <div className="upd-err" dir="auto">{upd.error}</div>}

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
  const mod = useMod();
  return (
    <aside className="side">
      <div className="brand">
        <StarBuddy size="sm" />
        <span className="brand-name">Macron</span>
      </div>
      <MachineMenu />
      <button type="button" className="nav" onClick={() => openDialog({ kind: "folder" })}>
        <Icon name="plus" />
        <span>{t("Open folder")}</span>
        <kbd>{mod} O</kbd>
      </button>
      <button type="button" className="nav" onClick={() => openDialog({ kind: "search" })}>
        <Icon name="search" size={15} />
        <span>{t("Search chats")}</span>
        <kbd>{mod} ⇧ F</kbd>
      </button>
      <div className="side-label">{t("Workspaces")}</div>
      <nav className="projects">
        {projects.map((p) => (
          <Row key={p.id} p={p} on={p.id === current} isHome={p.id === defaultId} home={home} />
        ))}
      </nav>
      <div className="side-foot">
        <UpdateCard />
        <button type="button" className="nav" onClick={() => openDialog({ kind: "settings" })}>
          <Icon name="sliders" />
          <span>{t("Settings")}</span>
          <kbd>{mod} ,</kbd>
        </button>
        <div className={"engine" + (online === true ? " ok" : online === false ? " bad" : "")}>
          <span className="dot" />
          <span>{online === null ? t("Connecting…") : online ? t("Engine ready") + (version ? " · " + version : "") : t("Reconnecting…")}</span>
        </div>
        <a className="side-credit" href="https://t.me/SpectorDev" target="_blank" rel="noreferrer" dir="ltr">
          SpectorDev · MIT
        </a>
      </div>
    </aside>
  );
}
