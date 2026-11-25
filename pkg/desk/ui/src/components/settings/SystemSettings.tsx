import { useEffect, useState } from "react";
import { checkUpdate, fail, installUpdate } from "../../actions";
import { server } from "../../api";
import { bytes } from "../../format";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { useStore } from "../../store";
import { StarBuddy } from "../StarBuddy";

export function DiagnosticsSettingsPage() {
  const t = useT();
  const current = useStore((s) => s.current);
  const info = useStore((s) => s.info);
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);

  const load = () => {
    if (!current) {
      setLoading(false);
      return;
    }
    setLoading(true);
    server
      .projectStatus(current)
      .then((r) => setStatus(r.status || ""))
      .catch(() => setStatus(""))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    load();
  }, [current]);

  const exportDiag = async () => {
    setExporting(true);
    try {
      const data = await server.diagnostics();
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const href = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = href;
      a.download = `macron-diagnostics-v${info.version || "2.0.0"}.json`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.setTimeout(() => URL.revokeObjectURL(href), 1500);
    } catch (e) {
      fail(e);
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("Diagnostics")}</h1>
          <p>{t("Session & engine telemetry")}</p>
        </div>
        <div className="left">
          <button type="button" className="btn ghost sm" onClick={load}>
            <Icon name="refresh" size={13} />
            <span>{t("Refresh")}</span>
          </button>
          <button type="button" className="btn primary sm" disabled={exporting} onClick={() => void exportDiag()}>
            <Icon name="download" size={13} />
            <span>{exporting ? t("Exporting…") : t("Export diagnostics")}</span>
          </button>
        </div>
      </header>

      <div className="stg-diag-console">
        {loading ? (
          <div className="empty-row">{t("Loading…")}</div>
        ) : (
          <pre className="status-pre" dir="ltr">
            {status ||
              `Macron Agent ${info.version} — by ${info.creator || "SpectorDev"} (${info.creatorUrl || "https://t.me/SpectorDev"})\n  model       ${info.model || "none"}\n  provider    ${info.provider || "none"}\n  execution   ${info.executionMode || "best-effort"}\n  approvals   ${info.approvals || "smart"}`}
          </pre>
        )}
      </div>
    </div>
  );
}

export function UpdatesSettingsPage() {
  const t = useT();
  const info = useStore((s) => s.info);
  const upd = useStore((s) => s.update);
  const machine = useStore((s) => s.machine);
  const [checking, setChecking] = useState(false);

  const runCheck = async () => {
    setChecking(true);
    await checkUpdate(true);
    setChecking(false);
  };

  const pct = Math.max(0, Math.min(100, Math.round(upd?.percent || 0)));
  const total = upd?.total || upd?.assetSize || 0;
  const downloaded = upd?.downloaded || 0;
  const speed = upd?.speedBps || 0;
  const phaseLabel = upd?.step ? t(upd.step) : t("Updating…");
  const telemetry =
    upd?.phase === "downloading" && total > 0
      ? `${bytes(downloaded)} / ${bytes(total)}${speed > 0 ? ` · ${bytes(speed)}/s` : ""}`
      : upd?.phase === "downloading" && downloaded > 0
        ? `${bytes(downloaded)}${speed > 0 ? ` · ${bytes(speed)}/s` : ""}`
        : "";

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Updates")}</h1>
        <p>GitHub Releases · Metixpro/Macron-Agent</p>
      </header>

      {machine ? (
        <div className="empty-row">Updates are managed on the local machine.</div>
      ) : (
        <div className="set-list">
          <div className="set-row static upd-row">
            <span className={"state" + (upd?.installing ? " warn" : upd?.available ? " warn" : upd?.installed || (upd && !upd.error) ? " ok" : "")} />
            <span className="grow">
              <span className="name">
                Macron v{info.version || upd?.current}
                {upd?.available && upd.latest ? ` → v${upd.latest}` : ""}
                {upd?.available && total > 0 && !upd.installing && (
                  <span className="tag" dir="ltr">
                    {bytes(total)}
                  </span>
                )}
              </span>
              <span className={"meta" + (upd?.error && !upd?.installing ? " bad-text" : "")}>
                {upd?.installing
                  ? `${phaseLabel} · ${pct}%`
                  : upd?.installed
                    ? t("Updated — restart Macron")
                    : upd?.error
                      ? upd.error
                      : upd?.available
                        ? t("Update available")
                        : upd
                          ? t("Up to date")
                          : "GitHub Releases · Metixpro/Macron-Agent"}
              </span>
            </span>
            {upd?.available && !upd.installed ? (
              <button type="button" className="btn primary sm" disabled={upd.installing} onClick={() => void installUpdate()}>
                {upd.installing ? `${pct}%` : upd.error ? t("Retry") : t("Install update")}
              </button>
            ) : (
              <button type="button" className="btn ghost sm" disabled={checking || upd?.installing} onClick={() => void runCheck()}>
                {checking ? t("Checking…") : t("Check for updates")}
              </button>
            )}
          </div>
          {upd?.installing && (
            <div className="upd-set-prog">
              <div className="upd-bar" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
                <span className={"upd-fill" + (pct < 3 ? " indet" : "")} style={{ width: `${Math.max(pct, 4)}%` }} />
              </div>
              <div className="upd-sub">
                <span className="upd-phase">{phaseLabel}</span>
                <span className="upd-meta" dir="ltr">
                  {telemetry ? `${telemetry} · ${pct}%` : `${pct}%`}
                </span>
              </div>
            </div>
          )}
          {upd?.available && upd.notes && (
            <details className="upd-notes" open>
              <summary>{t("Release notes")}</summary>
              <pre dir="auto">{upd.notes}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

export function AboutSettingsPage() {
  const t = useT();
  const info = useStore((s) => s.info);
  const creator = info.creator || "SpectorDev";
  const url = info.creatorUrl || "https://t.me/SpectorDev";

  return (
    <div className="stg-page">
      <div className="stg-about-hero">
        <StarBuddy variant="stage" size="xl" interactive trackPointer title="Macron StarBuddy" />
        <h1>Macron Agent</h1>
        <div className="stg-about-ver" dir="ltr">
          v{info.version || "2.0.0"} · {creator}
        </div>
        <p>{t("Developed by SpectorDev Team")} · {t("MIT License")}</p>
        <div className="stg-about-meta" dir="ltr">
          <span>{info.platform || "desktop"}</span>
          {info.host && <span>{info.host}</span>}
          {info.sandboxBackend && <span>{info.sandboxBackend}</span>}
        </div>
        <div className="stg-about-links">
          <a className="btn ghost sm" href={url} target="_blank" rel="noreferrer" dir="ltr">
            <Icon name="open" size={13} />
            <span>@SpectorDev</span>
          </a>
        </div>
      </div>
    </div>
  );
}
