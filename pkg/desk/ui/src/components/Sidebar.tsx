import { memo } from "react";
import { openDialog, openProject, removeProject } from "../actions";
import { ago, shortPath } from "../format";
import { useMod } from "../hooks";
import { Icon } from "../icons";
import { useStore } from "../store";
import type { Summary } from "../types";

const Row = memo(function Row({ p, on, isHome, home }: { p: Summary; on: boolean; isHome: boolean; home: string }) {
  const preview = p.preview || (isHome ? "General conversations" : shortPath(p.path, home));
  return (
    <div className={"proj" + (on ? " on" : "")}>
      <button type="button" className="proj-hit" title={p.path} onClick={() => openProject(p.id)}>
        <span className="pn">{isHome ? "Home" : p.name}</span>
        {p.asking ? (
          <span className="badge ask" title="Waiting for your approval" />
        ) : p.running ? (
          <span className="badge run" title="Working" />
        ) : (
          <span className="ago">{ago(p.updated)}</span>
        )}
        <span className="pv" dir="auto">
          {preview}
        </span>
      </button>
      {!isHome && (
        <button type="button" className="x" title="Remove from the list (the folder stays)" onClick={() => removeProject(p.id)}>
          <Icon name="x" size={14} />
        </button>
      )}
    </div>
  );
});

export function Sidebar() {
  const projects = useStore((s) => s.projects);
  const current = useStore((s) => s.current);
  const defaultId = useStore((s) => s.defaultId);
  const home = useStore((s) => s.home);
  const online = useStore((s) => s.online);
  const version = useStore((s) => s.info.version);
  const mod = useMod();
  return (
    <aside className="side">
      <div className="brand">Macron</div>
      <button type="button" className="nav" onClick={() => openDialog({ kind: "folder" })}>
        <Icon name="plus" />
        <span>Open folder</span>
        <kbd>{mod} O</kbd>
      </button>
      <div className="side-label">Workspaces</div>
      <nav className="projects">
        {projects.map((p) => (
          <Row key={p.id} p={p} on={p.id === current} isHome={p.id === defaultId} home={home} />
        ))}
      </nav>
      <div className="side-foot">
        <button type="button" className="nav" onClick={() => openDialog({ kind: "settings" })}>
          <Icon name="sliders" />
          <span>Settings</span>
          <kbd>{mod} ,</kbd>
        </button>
        <div className={"engine" + (online === true ? " ok" : online === false ? " bad" : "")}>
          <span className="dot" />
          <span>{online === null ? "Connecting…" : online ? "Engine ready" + (version ? " · " + version : "") : "Reconnecting…"}</span>
        </div>
      </div>
    </aside>
  );
}
