import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { newChat, openDialog, openSettings, switchThread, toggleChanges, toggleCompanion, toggleSidebarCollapsed } from "../actions";
import { copyText } from "../format";
import { useMod, useProjectName } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { setState, useStore } from "../store";
import { usePanelPlan, useRightPanelVisible } from "./PlanPanel";
import { useStarBuddyMood } from "./StarBuddy";

export function Header() {
  const t = useT();
  const name = useProjectName();
  const path = useStore((s) => s.projects.find((p) => p.id === s.current)?.path || "");
  const remote = useStore((s) => s.servers.find((x) => x.id === s.machine)?.name || "");
  const threads = useStore((s) => s.threads);
  const activeThread = useStore((s) => s.activeThread);
  const running = useStore((s) => s.live.running);
  const sideCollapsed = useStore((s) => s.sideCollapsed);
  const workspace = useStore((s) => `${s.machine}:${s.current}`);
  const mod = useMod();
  const [copiedPath, setCopiedPath] = useState(false);
  const [closedTabs, setClosedTabs] = useState<Record<string, string[]>>({});
  const tablist = useRef<HTMLDivElement>(null);

  const hidden = closedTabs[workspace] || [];
  const tabs: { id: string; title: string }[] =
    threads.length > 0
      ? threads.filter((x) => x.id === activeThread || !hidden.includes(x.id)).slice(0, 6)
      : [{ id: activeThread || "default", title: "New conversation" }];
  if (activeThread && !tabs.some((x) => x.id === activeThread)) {
    const active = threads.find((x) => x.id === activeThread);
    if (active) tabs.unshift({ id: active.id, title: active.title });
  }

  useEffect(() => {
    setClosedTabs((all) => {
      const hidden = all[workspace];
      if (!hidden?.includes(activeThread)) return all;
      return { ...all, [workspace]: hidden.filter((id) => id !== activeThread) };
    });
  }, [workspace, activeThread]);

  const closeTab = async (index: number) => {
    const closed = tabs[index].id;
    if (closed === activeThread) {
      const next = tabs[index + 1] || tabs[index - 1];
      if (!next || !(await switchThread(next.id))) return;
    }
    setClosedTabs((all) => ({ ...all, [workspace]: [...(all[workspace] || []), closed] }));
  };

  const moveTabFocus = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const rtl = document.documentElement.dir === "rtl";
    const offset = event.key === "ArrowRight" ? (rtl ? -1 : 1) : event.key === "ArrowLeft" ? (rtl ? 1 : -1) : 0;
    if (!offset && event.key !== "Home" && event.key !== "End") return;
    const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + offset + tabs.length) % tabs.length;
    event.preventDefault();
    tablist.current?.querySelectorAll<HTMLButtonElement>("[role=tab]")[next]?.focus();
  };

  const copyWorkspacePath = () => {
    if (!path) return;
    void copyText(path).then(() => {
      setCopiedPath(true);
      window.setTimeout(() => setCopiedPath(false), 1400);
    });
  };

  return (
    <header className="top">
      <button
        type="button"
        className={"icon-btn sm side-toggle" + (sideCollapsed ? " show-desktop" : "")}
        title={sideCollapsed ? `${t("Expand sidebar")} (${mod}+\\)` : t("Workspaces")}
        aria-label={sideCollapsed ? t("Expand sidebar") : t("Workspaces")}
        onClick={() => {
          if (window.innerWidth <= 820) setState((s) => ({ sideOpen: !s.sideOpen }));
          else toggleSidebarCollapsed(false);
        }}
      >
        <Icon name="sidebar" size={15} />
      </button>

      <div className="top-tabbar">
        <div className="top-tabs" role="tablist" aria-label={t("Conversations")} ref={tablist}>
          {tabs.map((th, index) => {
            const isOn = th.id === activeThread || (tabs.length === 1 && !activeThread);
            const label = !th.title || th.title === "New conversation" ? t("New conversation") : th.title;
            return (
              <div key={th.id} className={"top-tab" + (isOn ? " on" : "")}>
                <button
                  type="button"
                  role="tab"
                  tabIndex={isOn ? 0 : -1}
                  aria-selected={isOn}
                  className="top-tab-hit"
                  title={label}
                  onClick={() => {
                    if (!isOn) void switchThread(th.id);
                  }}
                  onKeyDown={(e) => moveTabFocus(e, index)}
                >
                  <span className={"top-tab-dot" + (isOn && running ? " busy" : "")} aria-hidden="true" />
                  <span className="top-tab-title" dir="auto">{label}</span>
                </button>
                {tabs.length > 1 && (
                  <button
                    type="button"
                    className="top-tab-close"
                    title={t("Close tab")}
                    aria-label={t("Close tab")}
                    disabled={isOn && running}
                    onClick={() => void closeTab(index)}
                  >
                    <Icon name="x" size={11} />
                  </button>
                )}
              </div>
            );
          })}
          <button
            type="button"
            className="top-tab-add"
            title={`${t("New conversation")} (${mod}+N)`}
            aria-label={t("New conversation")}
            onClick={() => void newChat()}
          >
            <Icon name="plus" size={14} />
          </button>
        </div>
      </div>

      <div className="top-right">
        {remote && (
          <span className="on-server" title="Macron works on this server">
            <Icon name="server" size={11} />
            {remote}
          </span>
        )}
        {name !== "Home" && (
          <button
            type="button"
            className={"top-ws-chip" + (copiedPath ? " copied" : "")}
            title={copiedPath ? t("Copied") : path ? `${path} · ${t("Copy path")}` : name}
            onClick={copyWorkspacePath}
          >
            <Icon name={copiedPath ? "check" : "folder"} size={12} />
            <span>{copiedPath ? t("Copied") : name}</span>
          </button>
        )}
        <IsolationPill />
        <ChangesToggle />
        <button
          type="button"
          className="icon-btn sm top-cmd-btn"
          title={`${t("Search or run a command")} (${mod}+K)`}
          aria-label={t("Search or run a command")}
          onClick={() => openDialog({ kind: "palette", mode: "root" })}
        >
          <Icon name="search" size={14} />
        </button>
        <CompanionToggle />
      </div>
    </header>
  );
}

function IsolationPill() {
  const t = useT();
  const info = useStore((s) => s.info);
  const running = useStore((s) => s.live.running);
  if (!info.version) return null;
  const sandboxed = Boolean(info.sandboxed);
  const backend = info.sandboxBackend === "windows-job" ? "Windows Job" : info.sandboxBackend === "bwrap" ? "bwrap" : "";
  const mode = info.executionMode || "best-effort";
  const label = sandboxed ? t("Sandbox") : mode === "local" ? t("Local") : t("Best-effort");
  const title = sandboxed
    ? `${t("Sandbox active")}${backend ? ` (${backend})` : ""} — ${t("Click to configure")}`
    : `${t("Execution mode")}: ${label} — ${t("Click to configure")}`;
  return (
    <button
      type="button"
      className={"iso-pill" + (sandboxed ? " on" : "") + (running ? " busy" : "")}
      title={title}
      onClick={() => openSettings("execution")}
    >
      <span className="iso-dot" aria-hidden="true" />
      <span>{label}</span>
    </button>
  );
}

function ChangesToggle() {
  const t = useT();
  const changes = useStore((s) => s.changes);
  const open = useStore((s) => s.changesOpen && s.rightTab === "changes");
  const count = changes?.files?.length || 0;
  if (!changes?.git || count === 0) return null;
  return (
    <button
      type="button"
      className={"plan-toggle" + (open ? " on" : "")}
      title={t("Show changes")}
      onClick={() => toggleChanges()}
    >
      <Icon name="gitBranch" size={13} />
      <span>{count}</span>
    </button>
  );
}

function CompanionToggle() {
  const t = useT();
  const visible = useRightPanelVisible();
  const plan = usePanelPlan();
  const mood = useStarBuddyMood();
  const done = plan.filter((i) => i.status === "done").length;

  return (
    <button
      type="button"
      className={"companion-toggle" + (visible ? " on" : "") + ` mood-${mood}`}
      title={visible ? t("Hide Agent Companion") : t("Open Agent Companion")}
      aria-label={visible ? t("Hide Agent Companion") : t("Open Agent Companion")}
      aria-pressed={visible}
      onClick={() => toggleCompanion(!visible)}
    >
      <span className="comp-dot" aria-hidden="true" />
      {plan.length > 0 ? (
        <span className="comp-count" dir="ltr">
          {done}/{plan.length}
        </span>
      ) : (
        <Icon name="panelRight" size={14} />
      )}
    </button>
  );
}
