import { useEffect, useState } from "react";
import { useT } from "../i18n";
import { Icon } from "../icons";
import type { BrowserTeachStep } from "../types";

export function BrowserTeachBar({
  startedAt,
  steps,
  busy,
  onSave,
  onCancel,
}: {
  startedAt: number;
  steps: BrowserTeachStep[];
  busy?: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  const [now, setNow] = useState(Date.now());
  const [showSteps, setShowSteps] = useState(false);

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  const elapsedSec = Math.max(0, Math.floor((now - (startedAt || now)) / 1000));
  const mm = String(Math.floor(elapsedSec / 60)).padStart(2, "0");
  const ss = String(elapsedSec % 60).padStart(2, "0");

  return (
    <div className="br-teach-wrap" dir="auto">
      <div className="br-teach-bar">
        <span className="br-teach-status">
          <span className="br-teach-rec" aria-hidden="true" />
          <strong>{t("Teaching Macron")}</strong>
          <span className="br-teach-timer" dir="ltr">
            {mm}:{ss}
          </span>
          <button
            type="button"
            className="br-teach-count"
            onClick={() => setShowSteps((v) => !v)}
            title={t("Show recorded steps")}
          >
            {steps.length} {t("steps")}
            <Icon name={showSteps ? "up" : "down"} size={11} />
          </button>
          <span className="br-teach-privacy">{t("Typed text & passwords are never recorded")}</span>
        </span>
        <div className="br-teach-actions">
          <button type="button" className="btn ghost sm" disabled={busy} onClick={onCancel}>
            {t("Cancel")}
          </button>
          <button type="button" className="btn primary sm" disabled={busy || steps.length === 0} onClick={onSave}>
            <Icon name="sparkles" size={12} />
            <span>{t("Save as Skill")}</span>
          </button>
        </div>
      </div>
      {showSteps && (
        <div className="br-teach-list">
          {steps.length === 0 ? (
            <div className="br-teach-empty">{t("Click or navigate in the browser to record steps…")}</div>
          ) : (
            steps.map((s) => (
              <div key={s.index} className={"br-teach-step" + (s.sensitive ? " sens" : "")}>
                <span className="br-teach-idx" dir="ltr">
                  {s.index}.
                </span>
                <span className="br-teach-desc" dir="auto">
                  {s.description}
                </span>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}
