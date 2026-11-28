import { setRightTab } from "../actions";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { setState, useStore } from "../store";

function hostLabel(raw?: string): string {
  if (!raw) return "";
  try {
    return new URL(raw).hostname.replace(/^www\./, "");
  } catch {
    return raw.slice(0, 32);
  }
}

export function BrowserDock() {
  const t = useT();
  const view = useStore((s) => s.browserView);
  const expanded = useStore((s) => s.browserExpanded);
  const dialog = useStore((s) => s.dialog);

  // Only show the floating dock when the browser is open, not already fullscreen,
  // and no full-screen settings modal is blocking the view.
  if (!view?.open || expanded || dialog?.kind === "settings") return null;

  const needsUser = view.challenge?.status === "needs-user" || Boolean(view.dialog);
  const takeover = Boolean(view.takeover);
  const active = Boolean(view.agentActive);
  const teaching = Boolean(view.teaching);
  const host = hostLabel(view.url) || view.title || t("Live Browser");

  const openFull = () => {
    setRightTab("browser");
    setState({ browserExpanded: true });
  };

  return (
    <div
      className={
        "br-dock" +
        (needsUser ? " alert" : takeover ? " user" : teaching ? " teach" : active ? " live" : "")
      }
      role="region"
      aria-label={t("Live Browser")}
    >
      <button type="button" className="br-dock-main" onClick={openFull}>
        <span
          className={"br-dock-dot" + (active || needsUser || teaching ? " pulse" : "")}
          style={view.profile?.color && !needsUser && !teaching ? { background: view.profile.color } : undefined}
          aria-hidden="true"
        />
        <span className="br-dock-text">
          <span className="br-dock-host" dir="ltr">
            {host}
          </span>
          <span className="br-dock-sub">
            {view.dialog
              ? t("Page dialog needs your answer")
              : needsUser
                ? view.challenge?.reason || t("Verification required — Take over")
                : teaching
                  ? `${t("Teaching")} · ${(view.teachSteps || []).length}`
                  : takeover
                    ? t("You have control")
                    : active
                      ? t("Macron is browsing…")
                      : view.profile?.name || t("Live Browser")}
          </span>
        </span>
        <span className="br-dock-open">
          <Icon name="open" size={12} />
        </span>
      </button>
    </div>
  );
}
