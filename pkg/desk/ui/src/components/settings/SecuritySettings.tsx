import { fail } from "../../actions";
import { server } from "../../api";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { getState, setState, useStore } from "../../store";

export function ExecutionSettingsPage() {
  const t = useT();
  const info = useStore((s) => s.info);

  const isLinux = !info.platform || info.platform === "linux";
  const isWindows = info.platform === "windows";
  const backend = info.sandboxBackend || "";
  const executionMode = (info.executionMode || "best-effort") as "sandbox" | "best-effort" | "local";

  const applyMode = async (m: "sandbox" | "best-effort" | "local") => {
    try {
      const r = await server.setExecutionMode(m);
      setState({ info: r.info });
    } catch (e) {
      fail(e);
    }
  };

  const modes: { id: "sandbox" | "best-effort" | "local"; title: string; badge: string; desc: string }[] = [
    {
      id: "sandbox",
      title: t("Sandbox"),
      badge: isWindows || backend === "windows-job" ? "Windows Job-Object" : isLinux ? "bubblewrap" : "Strict",
      desc: t(
        isWindows || backend === "windows-job"
          ? "Strict Windows Job-Object sandbox (process, memory, clipboard & secret-env isolation)."
          : isLinux
            ? "Strict bubblewrap isolation (fail-closed if unavailable)."
            : "Strict sandbox isolation (Linux bubblewrap / Windows Job-Object).",
      ),
    },
    {
      id: "best-effort",
      title: t("Best-effort"),
      badge: "Recommended",
      desc: t(
        info.sandboxed
          ? isWindows || backend === "windows-job"
            ? "Windows Job-Object sandbox active; falls back to local if unavailable."
            : "Sandbox active; falls back to local if unavailable."
          : isWindows
            ? "Uses native Windows Job-Object sandbox when enabled."
            : isLinux
              ? "Unsandboxed; uses bubblewrap when available."
              : "Unsandboxed; uses platform sandbox when available.",
      ),
    },
    {
      id: "local",
      title: t("Local"),
      badge: "Direct host",
      desc: t("Sandbox bypassed. Commands run directly on the host."),
    },
  ];

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Execution & Sandbox")}</h1>
        <p>{t("Execution mode")}</p>
      </header>

      <div className={"stg-status-banner" + (info.sandboxed ? " ok" : "")}>
        <Icon name="shield" size={16} />
        <div>
          <div className="stg-sb-title">
            {info.sandboxed ? t("Sandbox active") : `${t("Execution mode")}: ${executionMode}`}
          </div>
          <div className="stg-sb-sub" dir="ltr">
            {info.platform || "local"}
            {backend ? ` · ${backend}` : ""}
          </div>
        </div>
      </div>

      <div className="stg-option-cards" role="radiogroup" aria-label={t("Execution mode")}>
        {modes.map((m) => (
          <button
            key={m.id}
            type="button"
            role="radio"
            aria-checked={executionMode === m.id}
            className={"stg-opt-card" + (executionMode === m.id ? " on" : "")}
            onClick={() => void applyMode(m.id)}
          >
            <div className="stg-opt-top">
              <span className="stg-opt-title">{m.title}</span>
              <span className="pv-pill api">{m.badge}</span>
              <span className="stg-opt-check">{executionMode === m.id && <Icon name="check" size={14} />}</span>
            </div>
            <p className="stg-opt-desc">{m.desc}</p>
          </button>
        ))}
      </div>
    </div>
  );
}

export function ApprovalsSettingsPage() {
  const t = useT();
  const info = useStore((s) => s.info);
  const approvals = (info.approvals || "smart") as "smart" | "manual" | "off";

  const applyApprovals = async (m: "smart" | "manual" | "off") => {
    try {
      const r = await server.setApprovals(m);
      setState({ info: r.info });
    } catch (e) {
      fail(e);
    }
  };

  const toggleYolo = async () => {
    try {
      const r = await server.setYolo(!getState().info.yolo);
      setState({ info: r.info });
    } catch (e) {
      fail(e);
    }
  };

  const policies: { id: "smart" | "manual" | "off"; title: string; desc: string }[] = [
    {
      id: "smart",
      title: t("Smart"),
      desc: t("Safe commands run on their own. Anything risky waits for you."),
    },
    {
      id: "manual",
      title: t("Always ask"),
      desc: t("Every command waits for your OK."),
    },
    {
      id: "off",
      title: t("Never ask"),
      desc: t("Nothing waits. Only for machines you can afford to break."),
    },
  ];

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Approvals & YOLO")}</h1>
        <p>{t("Command approvals")}</p>
      </header>

      <div className="stg-option-cards" role="radiogroup" aria-label={t("Command approvals")}>
        {policies.map((p) => (
          <button
            key={p.id}
            type="button"
            role="radio"
            aria-checked={approvals === p.id}
            className={"stg-opt-card" + (approvals === p.id ? " on" : "")}
            onClick={() => void applyApprovals(p.id)}
          >
            <div className="stg-opt-top">
              <span className="stg-opt-title">{p.title}</span>
              <span className="stg-opt-check">{approvals === p.id && <Icon name="check" size={14} />}</span>
            </div>
            <p className="stg-opt-desc">{p.desc}</p>
          </button>
        ))}
      </div>

      <section className={"stg-danger-zone" + (info.yolo ? " active" : "")}>
        <div className="stg-dz-label">
          <Icon name="alert" size={14} />
          <span>{t("Danger zone")}</span>
        </div>
        <div className="stg-row-setting">
          <div>
            <div className="stg-row-title">{t("YOLO for 30 minutes")}</div>
            <div className="stg-row-help">
              {t(info.yolo ? "On. Commands run without asking until it runs out." : "Let Macron run everything without asking, for a while.")}
            </div>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={info.yolo}
            className={"switch" + (info.yolo ? " on" : "")}
            onClick={() => void toggleYolo()}
          >
            <span />
          </button>
        </div>
      </section>
    </div>
  );
}
