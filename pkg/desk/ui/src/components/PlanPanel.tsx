import { useMemo } from "react";
import { openDialog, setRightTab, toggleChanges, toggleCompanion } from "../actions";
import { bytes, formatCost, local, tokens, took } from "../format";
import { useNow } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { setState, useStore } from "../store";
import type { Block, PlanItem } from "../types";
import { ChangesBody } from "./ChangesPanel";
import { StarBuddy, useStarBuddyMood } from "./StarBuddy";

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
    const next = open ?? !(s.planOpen && s.rightTab === "plan");
    local.set("planOpen", next);
    local.set("companionOpen", next);
    if (next) return { planOpen: true, companionOpen: true, rightTab: "plan" };
    return { planOpen: false, companionOpen: false };
  });
}

export function useRightPanelVisible(): boolean {
  const plan = usePanelPlan();
  const planOpen = useStore((s) => s.planOpen);
  const changesOpen = useStore((s) => s.changesOpen);
  const companionOpen = useStore((s) => s.companionOpen);
  const rightTab = useStore((s) => s.rightTab);
  if (companionOpen) return true;
  if (rightTab === "changes" && changesOpen) return true;
  if (plan.length > 0 && planOpen) return true;
  if (changesOpen) return true;
  return false;
}

function moodLabel(mood: string, t: (k: string) => string): string {
  switch (mood) {
    case "working":
      return t("Working");
    case "streaming":
      return t("Streaming");
    case "asking":
      return t("Approval");
    case "typing":
      return t("Typing");
    case "success":
      return t("Success");
    case "error":
      return t("Error");
    case "offline":
      return t("Offline");
    case "speaking":
      return t("Speaking");
    default:
      return t("Idle");
  }
}

function ActivityBody() {
  const t = useT();
  const live = useStore((s) => s.live);
  const blocks = useStore((s) => s.blocks);
  const now = useNow(live.running);

  const recentTools = useMemo(() => {
    const out: Block[] = [];
    for (let i = blocks.length - 1; i >= 0; i--) {
      const b = blocks[i];
      if (b.kind === "user" || b.kind === "divider") break;
      if (b.kind === "tool") out.push(b);
      if (out.length >= 16) break;
    }
    return out.reverse();
  }, [blocks]);

  const activeTools = (live.tools || []).filter((x) => !((live.subs || []).length && x.name === "delegate_task"));
  const subs = live.subs || [];
  const hasAnything = Boolean(live.draft || activeTools.length || subs.length || live.thought || recentTools.length);

  if (!hasAnything) {
    return (
      <div className="comp-empty">
        <div className="comp-empty-title">{t("No active tasks yet")}</div>
        <p>{t("When Macron runs tools, plans steps, or spawns sub-agents, live progress appears here.")}</p>
      </div>
    );
  }

  return (
    <div className="comp-activity">
      {(live.draft || activeTools.length > 0) && (
        <section className="comp-sec">
          <div className="comp-sec-head">{t("Active operations")}</div>
          {live.draft && (
            <div className="comp-op live">
              <span className="spin" />
              <span className="comp-op-main">
                <span className="comp-op-lb">{live.draft.label || live.draft.name}</span>
                {live.draft.arg && (
                  <span className="comp-op-arg" dir="ltr">
                    {live.draft.arg}
                  </span>
                )}
              </span>
              <span className="comp-op-tm" dir="ltr">
                {live.draft.size > 0 ? bytes(live.draft.size) : "…"}
              </span>
            </div>
          )}
          {activeTools.map((tool) => (
            <div key={tool.id} className="comp-op live">
              <span className="spin" />
              <span className="comp-op-main">
                <span className="comp-op-lb">{tool.label || tool.name}</span>
                {tool.arg && (
                  <span className="comp-op-arg" dir="ltr">
                    {tool.arg}
                  </span>
                )}
                {tool.progress && (
                  <span className="comp-op-prog" dir="ltr">
                    {tool.progress}
                  </span>
                )}
              </span>
              <span className="comp-op-tm" dir="ltr">
                {took(now - tool.started)}
              </span>
            </div>
          ))}
        </section>
      )}

      {live.thought && (
        <section className="comp-sec">
          <div className="comp-thought" dir="auto">
            {live.thought}
          </div>
        </section>
      )}

      {subs.length > 0 && (
        <section className="comp-sec">
          <div className="comp-sec-head">
            <span>{t("Sub-agents")}</span>
            <span className="comp-sec-cnt" dir="ltr">
              {subs.length}
            </span>
          </div>
          {subs.map((s) => (
            <div key={s.id} className={"comp-op" + (s.done ? " done" : " live")}>
              {s.done ? (
                <span className={"comp-mark " + (s.failed ? "bad" : "ok")}>
                  <Icon name={s.failed ? "x" : "check"} size={11} />
                </span>
              ) : (
                <span className="spin" />
              )}
              <span className="comp-op-main">
                <span className="comp-op-lb" dir="auto">
                  {s.title}
                </span>
                <span className="comp-op-arg" dir="auto">
                  {s.done ? (s.failed ? t("failed") : "done") : s.step || "running"}
                </span>
              </span>
              {s.steps > 0 && (
                <span className="comp-op-tm" dir="ltr">
                  {s.steps}
                </span>
              )}
            </div>
          ))}
        </section>
      )}

      {recentTools.length > 0 && (
        <section className="comp-sec">
          <div className="comp-sec-head">
            <span>{t("Recent tool calls")}</span>
            <span className="comp-sec-cnt" dir="ltr">
              {recentTools.length}
            </span>
          </div>
          <div className="comp-timeline">
            {recentTools.map((b, i) => (
              <details key={i + ":" + b.at} className={"comp-tl-item" + (b.failed ? " fail" : "")}>
                <summary>
                  <span className={"comp-mark " + (b.failed ? "bad" : "ok")}>
                    <Icon name={b.failed ? "x" : "check"} size={10} />
                  </span>
                  <span className="comp-op-lb">{b.label || b.name}</span>
                  <span className="comp-op-arg" dir="ltr">
                    {b.arg}
                  </span>
                </summary>
                {b.output && (
                  <pre className="comp-tl-out" dir="ltr">
                    {b.output.slice(0, 1200)}
                  </pre>
                )}
              </details>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

export function PlanPanel() {
  const t = useT();
  const plan = usePanelPlan();
  const visible = useRightPanelVisible();
  const rightTab = useStore((s) => s.rightTab);
  const changes = useStore((s) => s.changes);
  const live = useStore((s) => s.live);
  const info = useStore((s) => s.info);
  const mood = useStarBuddyMood();
  const running = live.running;
  const started = live.started || 0;
  const now = useNow(running);

  if (!visible) return null;

  const hasPlan = plan.length > 0;
  const activeTab =
    rightTab === "changes"
      ? "changes"
      : rightTab === "plan" && hasPlan
        ? "plan"
        : rightTab === "activity"
          ? "activity"
          : hasPlan
            ? "plan"
            : "activity";

  const done = plan.filter((i) => i.status === "done").length;
  const current = plan.find((i) => i.status === "in_progress");
  const dirtyCount = changes?.files?.length || 0;
  const ctx = live.context;
  const usage = live.usage;
  const ctxPct = ctx && ctx.window ? Math.min(100, Math.round((ctx.used / ctx.window) * 100)) : 0;

  const close = () => {
    toggleCompanion(false);
    if (activeTab === "changes") toggleChanges(false);
  };

  return (
    <aside className={"planside companion" + (activeTab === "changes" ? " changes-mode" : "")} aria-label={t("Agent Companion")}>
      {/* Living Companion Header with StarBuddy */}
      <div className="comp-header">
        <div className="comp-hero">
          <StarBuddy variant="stage" size="md" interactive trackPointer title={`StarBuddy · ${moodLabel(mood, t)}`} />
          <div className="comp-ident">
            <div className="comp-title-row">
              <span className="comp-title">{t("Agent Companion")}</span>
              <span className={`comp-mood-pill mood-${mood}`}>{moodLabel(mood, t)}</span>
            </div>
            <div className="comp-sub" dir="ltr">
              {info.model || "No model"}
              {running && started > 0 ? ` · ${took(now - started)}` : ""}
            </div>
          </div>
        </div>
        <button type="button" className="icon-btn sm" title={t("Hide Agent Companion")} aria-label={t("Hide Agent Companion")} onClick={close}>
          <Icon name="x" size={14} />
        </button>
      </div>

      {/* Inline Pending Approval Card */}
      {live.ask && (
        <div className="comp-ask">
          <div className="comp-ask-top">
            <Icon name="alert" size={14} />
            <span>{t("Pending approval")}</span>
          </div>
          <div className="comp-ask-cmd" dir="ltr">
            {live.ask.command}
          </div>
          <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "ask", ask: live.ask! })}>
            {t("Review request")}
          </button>
        </div>
      )}

      {/* Real Context & Token Telemetry (Progressive Disclosure: only if real data exists) */}
      {(ctx?.window || usage?.in || usage?.out) && (
        <div className="comp-telemetry" dir="ltr">
          {ctx && ctx.window > 0 && (
            <div className="comp-ctx">
              <div className="comp-ctx-row">
                <span>Context</span>
                <span>
                  {tokens(ctx.used)} / {tokens(ctx.window)} ({ctxPct}%)
                </span>
              </div>
              <div className="comp-ctx-bar">
                <span className={ctxPct > 80 ? "high" : ""} style={{ width: `${ctxPct}%` }} />
              </div>
            </div>
          )}
          {usage && (usage.in > 0 || usage.out > 0) && (
            <div className="comp-tok-row">
              <span>↑ {tokens(usage.in)}</span>
              <span>↓ {tokens(usage.out)}</span>
              {usage.costKnown && typeof usage.cost === "number" && <span className="comp-cost">~{formatCost(usage.cost)}</span>}
            </div>
          )}
        </div>
      )}

      {/* Segmented Progressive Disclosure Tabs */}
      <div className="ps-tabs comp-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === "activity"}
          className={"ps-tab" + (activeTab === "activity" ? " on" : "")}
          onClick={() => setRightTab("activity")}
        >
          {t("Activity")}
        </button>
        {hasPlan && (
          <button
            type="button"
            role="tab"
            aria-selected={activeTab === "plan"}
            className={"ps-tab" + (activeTab === "plan" ? " on" : "")}
            onClick={() => setRightTab("plan")}
          >
            {t("Plan")}
            <span className="ps-badge" dir="ltr">
              {done}/{plan.length}
            </span>
          </button>
        )}
        <button
          type="button"
          role="tab"
          aria-selected={activeTab === "changes"}
          className={"ps-tab" + (activeTab === "changes" ? " on" : "")}
          onClick={() => setRightTab("changes")}
        >
          {t("Changes")}
          {dirtyCount > 0 && (
            <span className="ps-badge" dir="ltr">
              {dirtyCount}
            </span>
          )}
        </button>
      </div>

      {/* Tab Body */}
      {activeTab === "plan" && hasPlan ? (
        <div className="comp-plan-wrap">
          <div className="ps-bar">
            <span style={{ width: `${(100 * done) / plan.length}%` }} />
          </div>
          <ol className="ps-list">
            {plan.map((it, i) => {
              const st = it.status === "done" ? "done" : it.status === "in_progress" ? "now" : "todo";
              return (
                <li key={i} className={"ps-item " + st}>
                  <span className="ps-mark">
                    {st === "done" ? <Icon name="check" size={10} /> : st === "now" && running ? <span className="spin" /> : null}
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
                : t("Working") + " · " + took(now - started)
              : done === plan.length
                ? t("All steps done")
                : done + " of " + plan.length + " steps done"}
          </div>
        </div>
      ) : activeTab === "changes" ? (
        <ChangesBody />
      ) : (
        <ActivityBody />
      )}
    </aside>
  );
}
