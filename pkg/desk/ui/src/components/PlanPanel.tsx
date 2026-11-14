import { useMemo } from "react";
import { local, took } from "../format";
import { useNow } from "../hooks";
import { Icon } from "../icons";
import { setState, useStore } from "../store";
import type { PlanItem } from "../types";

export function usePanelPlan(): PlanItem[] {
  const running = useStore((s) => s.live.running);
  const livePlan = useStore((s) => s.live.plan);
  const blocks = useStore((s) => s.blocks);
  return useMemo(() => {
    if (running && livePlan && livePlan.length) return livePlan;
    for (let i = blocks.length - 1; i >= 0; i--) {
      const b = blocks[i];
      if (b.kind === "divider" || b.kind === "user") break;
      if (b.kind === "plan") {
        try {
          return (JSON.parse(b.text || "[]") as PlanItem[]) || [];
        } catch {
          return [];
        }
      }
    }
    return [];
  }, [running, livePlan, blocks]);
}

export function togglePlan(open?: boolean) {
  setState((s) => {
    const next = open ?? !s.planOpen;
    local.set("planOpen", next);
    return { planOpen: next };
  });
}

export function PlanPanel() {
  const plan = usePanelPlan();
  const open = useStore((s) => s.planOpen);
  const running = useStore((s) => s.live.running);
  const started = useStore((s) => s.live.started || 0);
  const now = useNow(running);
  if (!plan.length || !open) return null;
  const done = plan.filter((i) => i.status === "done").length;
  const current = plan.find((i) => i.status === "in_progress");
  return (
    <aside className="planside" aria-label="Plan">
      <div className="ps-head">
        <span className="ps-title">Plan</span>
        <span className="ps-count">
          {done} of {plan.length}
        </span>
        <button type="button" className="icon-btn sm" title="Hide the plan" aria-label="Hide the plan" onClick={() => togglePlan(false)}>
          <Icon name="x" size={14} />
        </button>
      </div>
      <div className="ps-bar">
        <span style={{ width: `${(100 * done) / plan.length}%` }} />
      </div>
      <ol className="ps-list">
        {plan.map((it, i) => {
          const st = it.status === "done" ? "done" : it.status === "in_progress" ? "now" : "todo";
          return (
            <li key={i} className={"ps-item " + st}>
              <span className="ps-mark">
                {st === "done" ? <Icon name="check" size={11} /> : st === "now" && running ? <span className="spin" /> : null}
              </span>
              <span className="ps-text" dir="auto">
                {it.text}
              </span>
            </li>
          );
        })}
      </ol>
      <div className="ps-foot">
        {running
          ? current
            ? "Working on step " + (plan.indexOf(current) + 1) + " · " + took(now - started)
            : "Working · " + took(now - started)
          : done === plan.length
            ? "All steps done"
            : done + " of " + plan.length + " steps done"}
      </div>
    </aside>
  );
}
