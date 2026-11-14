import { newChat, openDialog } from "../actions";
import { shortPath } from "../format";
import { useMod, useProjectName } from "../hooks";
import { Icon } from "../icons";
import { togglePlan, usePanelPlan } from "./PlanPanel";
import { setState, useStore } from "../store";

export function Header() {
  const name = useProjectName();
  const path = useStore((s) => s.projects.find((p) => p.id === s.current)?.path || "");
  const home = useStore((s) => s.home);
  const remote = useStore((s) => s.servers.find((x) => x.id === s.machine)?.name || "");
  const mod = useMod();
  return (
    <header className="top">
      <button type="button" className="icon-btn side-toggle" title="Workspaces" onClick={() => setState((s) => ({ sideOpen: !s.sideOpen }))}>
        <Icon name="menu" />
      </button>
      <div className="title">
        <h1>{name}</h1>
        {remote && (
          <span className="on-server" title="Macron works on this server">
            <Icon name="server" size={12} />
            {remote}
          </span>
        )}
        <span className="path" title={path}>
          {"‎" + shortPath(path, home) + "‎"}
        </span>
      </div>
      <PlanToggle />
      <button type="button" className="search" onClick={() => openDialog({ kind: "palette", mode: "root" })}>
        <Icon name="search" size={14} />
        <span>Search or run a command</span>
        <kbd>{mod} K</kbd>
      </button>
      <button type="button" className="icon-btn" title={`New conversation (${mod}+N)`} onClick={newChat}>
        <Icon name="edit" />
      </button>
    </header>
  );
}

function PlanToggle() {
  const plan = usePanelPlan();
  const open = useStore((s) => s.planOpen);
  if (!plan.length) return null;
  const done = plan.filter((i) => i.status === "done").length;
  return (
    <button type="button" className={"plan-toggle" + (open ? " on" : "")} title={open ? "Hide the plan" : "Show the plan"} onClick={() => togglePlan()}>
      <Icon name="steps" size={14} />
      <span>
        {done}/{plan.length}
      </span>
    </button>
  );
}
