import { took } from "../format";
import { useNow } from "../hooks";
import { Icon } from "../icons";
import { useStore } from "../store";
import { PlanList } from "./Feed";

export function LiveCard() {
  const live = useStore((s) => s.live);
  const mode = useStore((s) => s.mode);
  const now = useNow(live.running);
  if (!live.running) return null;
  const subs = live.subs || [];
  const tools = (live.tools || []).filter((t) => !(subs.length && t.name === "delegate_task"));
  const plan = live.plan || [];
  const idle = !tools.length && !subs.some((s) => !s.done) && !live.html;
  if (!plan.length && !tools.length && !subs.length && !idle) return null;
  return (
    <div className="live">
      <div className="live-card">
        {plan.length > 0 && <PlanList items={plan} live />}
        {tools.map((t) => (
          <div key={t.id} className="live-step">
            <div className="row">
              <span className="spin" />
              <span className="lb">{t.label || t.name}</span>
              <span className="ar">{t.arg}</span>
              <span className="tm">{took(now - t.started)}</span>
            </div>
            {t.progress && <div className="prog">{t.progress}</div>}
          </div>
        ))}
        {subs.map((s) => (
          <div key={s.id} className={"row agent" + (s.done ? " done" : "")}>
            {s.done ? (
              <span className={s.failed ? "mark bad" : "mark ok"}>
                <Icon name={s.failed ? "x" : "check"} size={13} />
              </span>
            ) : (
              <span className="spin" />
            )}
            <span className="lb">{s.title}</span>
            <span className="ar">{s.done ? (s.failed ? "failed" : "done") : s.step || "starting"}</span>
            <span className="tm">{s.steps ? `${s.steps} ${s.steps === 1 ? "step" : "steps"}` : ""}</span>
          </div>
        ))}
        {idle && (
          <>
            <div className="row thinking">
              <span className="spin" />
              <span className="word">{mode === "plan" ? "Planning" : "Working"}</span>
              <span />
              <span className="tm">{took(now - (live.started || now))}</span>
            </div>
            {live.thought && (
              <div className="thought" dir="auto">
                {live.thought}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
