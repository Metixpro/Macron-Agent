import { bytes, took } from "../format";
import { useNow } from "../hooks";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { useStore } from "../store";
import { PlanList } from "./Feed";
import { StarBuddy } from "./StarBuddy";

export function LiveCard() {
  const t = useT();
  const live = useStore((s) => s.live);
  const mode = useStore((s) => s.mode);
  const now = useNow(live.running);
  if (!live.running) return null;

  const subs = live.subs || [];
  const tools = (live.tools || []).filter((x) => !(subs.length && x.name === "delegate_task"));
  const plan = live.plan || [];
  const draft = live.draft;
  const idle = !tools.length && !subs.some((s) => !s.done) && !draft;

  return (
    <div className="live">
      <div className="live-card">
        {plan.length > 0 && <PlanList items={plan} live />}
        {draft && (
          <div className="live-step draft">
            <div className="row">
              <span className="spin" />
              <span className="lb">{draft.label || draft.name}</span>
              <span className="ar" dir="ltr">
                {draft.arg}
              </span>
              <span className="tm" dir="ltr">
                {draft.size > 0 ? "writing · " + bytes(draft.size) : "preparing"}
              </span>
            </div>
          </div>
        )}
        {tools.map((item) => (
          <div key={item.id} className="live-step">
            <div className="row">
              <span className="spin" />
              <span className="lb">{item.label || item.name}</span>
              <span className="ar" dir="ltr">
                {item.arg}
              </span>
              <span className="tm" dir="ltr">
                {took(now - item.started)}
              </span>
            </div>
            {item.progress && (
              <div className="prog" dir="ltr">
                {item.progress}
              </div>
            )}
          </div>
        ))}
        {subs.map((s) => (
          <div key={s.id} className={"row agent" + (s.done ? " done" : "")}>
            {s.done ? (
              <span className={s.failed ? "mark bad" : "mark ok"}>
                <Icon name={s.failed ? "x" : "check"} size={12} />
              </span>
            ) : (
              <span className="spin" />
            )}
            <span className="lb" dir="auto">
              {s.title}
            </span>
            <span className="ar" dir="auto">
              {s.done ? (s.failed ? t("failed") : "done") : s.step || "starting"}
            </span>
            <span className="tm" dir="ltr">
              {s.steps ? `${s.steps} ${s.steps === 1 ? t("step") : t("steps")}` : ""}
            </span>
          </div>
        ))}
        {idle && (
          <>
            <div className="row thinking">
              <StarBuddy size="xs" mood={live.html ? "streaming" : "working"} />
              <span className="word">{mode === "plan" ? t("Planning") : t("Working")}</span>
              <span />
              <span className="tm" dir="ltr">
                {took(now - (live.started || now))}
              </span>
            </div>
            {live.thought && !live.html && (
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
