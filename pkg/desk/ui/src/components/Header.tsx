import { openDialog, openSettings, toggleChanges, toggleCompanion, toggleSidebarCollapsed } from "../actions";
import { shortPath } from "../format";
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
  const home = useStore((s) => s.home);
  const remote = useStore((s) => s.servers.find((x) => x.id === s.machine)?.name || "");
  const activeThreadTitle = useStore((s) => {
    const th = s.threads.find((x) => x.id === s.activeThread);
    if (!th || s.threads.length <= 1 || th.title === "New conversation") return "";
    return th.title;
  });
  const sideCollapsed = useStore((s) => s.sideCollapsed);
  const mod = useMod();

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

      <div className="title">
        <h1>{name === "Home" ? t("Home") : name}</h1>
        {activeThreadTitle && (
          <span className="thread-crumb" dir="auto" title={activeThreadTitle}>
            <span className="crumb-sep">/</span>
            {activeThreadTitle}
          </span>
        )}
        {remote && (
          <span className="on-server" title="Macron works on this server">
            <Icon name="server" size={11} />
            {remote}
          </span>
        )}
        <span className="path" title={path} dir="ltr">
          {"\u200E" + shortPath(path, home) + "\u200E"}
        </span>
      </div>

      <div className="top-right">
        <IsolationPill />
        <ChangesToggle />
        <button type="button" className="search" onClick={() => openDialog({ kind: "palette", mode: "root" })}>
          <Icon name="search" size={13} />
          <span className="search-label">{t("Search or run a command")}</span>
          <kbd>{mod} K</kbd>
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
      className={"iso-pill" + (sandboxed ? " on" : "") + (running ? " live" : "")}
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
      onClick={() => toggleCompanion(!visible, plan.length ? "plan" : "activity")}
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
