import { useEffect, useState } from "react";
import { closeDialog, effortLevels, effortName, errorText, fail, openDialog, refreshState, windowName } from "../actions";
import { server } from "../api";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { setState, useStore } from "../store";
import type { ProviderDetail, ProviderTestResult, ProviderUpsert } from "../types";

const PRESETS = [
  { id: "openai", label: "OpenAI", url: "https://api.openai.com/v1", api: "openai", key: true },
  { id: "anthropic", label: "Anthropic", url: "https://api.anthropic.com/v1/messages", api: "anthropic", key: true },
  { id: "openrouter", label: "OpenRouter", url: "https://openrouter.ai/api/v1", api: "openai", key: true },
  { id: "gemini", label: "Gemini", url: "https://generativelanguage.googleapis.com/v1beta/openai", api: "openai", key: true },
  { id: "deepseek", label: "DeepSeek", url: "https://api.deepseek.com/v1", api: "openai", key: true },
  { id: "responses", label: "Responses API", url: "https://api.openai.com/v1/responses", api: "responses", key: true },
  { id: "ollama", label: "Ollama", url: "http://localhost:11434/v1", api: "openai", key: false },
  { id: "custom", label: "Custom", url: "", api: "", key: true },
] as const;

const API_MODES = [
  ["", "Auto"],
  ["openai", "OpenAI Chat"],
  ["responses", "Responses"],
  ["anthropic", "Anthropic"],
] as const;

const WINDOW_PRESETS = [
  [0, "Auto"],
  [32000, "32k"],
  [64000, "64k"],
  [128000, "128k"],
  [200000, "200k"],
  [256000, "256k"],
  [400000, "400k"],
  [1000000, "1M"],
] as const;

function apiBadge(api?: string): string {
  switch (api) {
    case "anthropic":
      return "Anthropic";
    case "responses":
      return "Responses";
    default:
      return "OpenAI";
  }
}

function cleanProviderError(raw: string): string {
  const s = (raw || "").trim();
  if (!s) return "";
  const statusMatch = s.match(/status\s+(\d{3})/i);
  const statusPrefix = statusMatch ? `HTTP ${statusMatch[1]} · ` : "";
  const brace = s.indexOf("{");
  if (brace >= 0) {
    try {
      const parsed = JSON.parse(s.slice(brace)) as { error?: { message?: string; code?: string }; message?: string };
      const msg = parsed?.error?.message || parsed?.message;
      if (msg) return statusPrefix + msg.trim();
    } catch {
      const m = s.match(/"message"\s*:\s*"([^"]+)"/);
      if (m?.[1]) return statusPrefix + m[1].trim();
    }
  }
  return s;
}

function parseWindowInput(raw: string): number | null {
  const s = raw.trim().toLowerCase().replace(/[,_\s]/g, "");
  if (!s || s === "auto" || s === "0") return 0;
  let mult = 1;
  let numStr = s;
  if (s.endsWith("m")) {
    mult = 1_000_000;
    numStr = s.slice(0, -1);
  } else if (s.endsWith("k")) {
    mult = 1_000;
    numStr = s.slice(0, -1);
  }
  const n = Number(numStr);
  if (!Number.isFinite(n) || n <= 0) return null;
  const val = Math.round(n * mult);
  if (val < 8000 || val > 10_000_000) return null;
  return val;
}

export function ProvidersSection() {
  const t = useT();
  const info = useStore((s) => s.info);
  const [providers, setProviders] = useState<ProviderDetail[] | null>(null);
  const [failover, setFailover] = useState<boolean>(info.failover ?? true);
  const [testing, setTesting] = useState<Record<number, boolean>>({});
  const [testResults, setTestResults] = useState<Record<number, ProviderTestResult>>({});
  const [confirmDel, setConfirmDel] = useState<number | null>(null);
  const [localServers, setLocalServers] = useState<{ id: string; name: string; baseUrl: string; models: string[] }[] | null>(null);
  const [scanningLocal, setScanningLocal] = useState(false);

  const load = () =>
    server
      .providers()
      .then((r) => {
        setProviders(r.providers || []);
        if (typeof r.failover === "boolean") setFailover(r.failover);
      })
      .catch(() => setProviders([]));

  const scanLocal = async () => {
    setScanningLocal(true);
    try {
      const r = await server.localProviders();
      setLocalServers(r.servers || []);
    } catch {
      setLocalServers([]);
    } finally {
      setScanningLocal(false);
    }
  };

  useEffect(() => {
    load();
    void scanLocal();
  }, [info.provider, info.model, info.effort, info.contextWindow]);

  const activate = async (id: number) => {
    try {
      const r = await server.setProvider(id);
      setState({ info: r.info });
      load();
    } catch (e) {
      fail(e);
    }
  };

  const toggleEnabled = async (p: ProviderDetail) => {
    try {
      await server.updateProvider(p.id, { enabled: !p.enabled });
      await refreshState();
      load();
    } catch (e) {
      fail(e);
    }
  };

  const runTest = async (p: ProviderDetail) => {
    setTesting((m) => ({ ...m, [p.id]: true }));
    try {
      const res = await server.testProvider(p.id);
      setTestResults((m) => ({ ...m, [p.id]: res }));
    } catch (e) {
      setTestResults((m) => ({ ...m, [p.id]: { ok: false, latencyMs: 0, error: errorText(e) } }));
    } finally {
      setTesting((m) => ({ ...m, [p.id]: false }));
    }
  };

  const remove = async (id: number) => {
    try {
      await server.deleteProvider(id);
      setConfirmDel(null);
      await refreshState();
      load();
    } catch (e) {
      fail(e);
    }
  };

  const toggleFailover = async () => {
    const next = !failover;
    setFailover(next);
    try {
      const r = await server.setFailover(next);
      setFailover(r.failover);
      setState({ info: r.info.info });
    } catch (e) {
      setFailover(!next);
      fail(e);
    }
  };

  return (
    <>
      <div className="set-group">{t("Models & custom providers")}</div>
      <div className="set-list pv-list">
        {providers === null && <div className="empty-row">{t("Loading…")}</div>}
        {providers && !providers.length && <div className="empty-row">{t("No model connected yet.")}</div>}
        {(providers || []).map((p) => {
          const tr = testResults[p.id];
          const isTesting = Boolean(testing[p.id]);
          const win = p.contextWindow ? windowName(p.contextWindow) : p.autoWindow ? `${windowName(p.autoWindow)} auto` : "";
          return (
            <div key={p.id} className={"pv-card" + (p.active ? " on" : "") + (!p.enabled ? " off" : "")}>
              <div className="pv-top">
                <button
                  type="button"
                  className="pv-select"
                  disabled={p.active}
                  title={p.active ? t("Active provider") : t("Switch to this provider")}
                  onClick={() => activate(p.id)}
                >
                  <span className={"radio" + (p.active ? " on" : "")} />
                  <span className="pv-ident">
                    <span className="pv-name-row">
                      <span className="name">{p.name}</span>
                      <span className="pv-pill api">{apiBadge(p.resolvedApi || p.api)}</span>
                      {p.active && <span className="pv-pill active">{t("Active")}</span>}
                      {!p.enabled && <span className="pv-pill muted">{t("Disabled")}</span>}
                      {p.cooling && <span className="pv-pill warn">{t("Cooling")}</span>}
                    </span>
                    <span className="meta pv-model" dir="ltr">
                      {p.model || "no model chosen"}
                    </span>
                  </span>
                </button>
                <div className="pv-actions">
                  <button type="button" className="btn ghost sm" disabled={isTesting} onClick={() => runTest(p)}>
                    {isTesting ? t("Testing…") : t("Test")}
                  </button>
                  <button type="button" className="btn ghost sm" onClick={() => openDialog({ kind: "provider-edit", provider: p })}>
                    {t("Customize")}
                  </button>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={p.enabled}
                    title={p.enabled ? t("Disable") : t("Enable")}
                    className={"switch" + (p.enabled ? " on" : "")}
                    onClick={() => toggleEnabled(p)}
                  >
                    <span />
                  </button>
                  {confirmDel === p.id ? (
                    <button type="button" className="btn danger sm" onClick={() => remove(p.id)}>
                      {t("Confirm")}
                    </button>
                  ) : (
                    <button type="button" className="icon-btn" title={t("Remove")} onClick={() => setConfirmDel(p.id)}>
                      <Icon name="x" size={14} />
                    </button>
                  )}
                </div>
              </div>
              <div className="pv-chips" dir="ltr">
                {p.effort && <span className="pv-chip">effort: {effortName(p.effort)}</span>}
                {win && <span className="pv-chip">ctx: {win}</span>}
                {p.temperature !== null && p.temperature !== undefined && <span className="pv-chip">temp: {p.temperature}</span>}
                <span className="pv-chip">{p.vision !== false ? "vision" : "no-vision"}</span>
                <span className="pv-chip">{p.noStream ? "no-stream" : "stream"}</span>
                {typeof p.priority === "number" && <span className="pv-chip">prio: {p.priority}</span>}
                {(p.avgLatencyMs || 0) > 0 && <span className="pv-chip">{p.avgLatencyMs}ms</span>}
                {(p.requests || 0) > 0 && (
                  <span className="pv-chip">
                    {p.requests} req{p.failures ? ` · ${p.failures} err` : ""}
                  </span>
                )}
              </div>
              {tr && (
                <div className={"pv-test-res" + (tr.ok ? " ok" : " bad")} dir="ltr">
                  {tr.ok ? `✓ ${tr.latencyMs}ms — ${tr.reply || "OK"}` : `✗ ${cleanProviderError(tr.error || "Test failed")}`}
                </div>
              )}
              {!tr && p.lastError && (
                <div className="pv-test-res bad" dir="ltr">
                  {cleanProviderError(p.lastError)}
                </div>
              )}
            </div>
          );
        })}
        <button type="button" className="set-row" onClick={() => openDialog({ kind: "provider-edit" })}>
          <Icon name="plus" size={14} />
          <span className="grow">
            <span className="name">{t("Add custom provider or model…")}</span>
          </span>
        </button>
        {localServers && localServers.length > 0 && (
          <div className="local-detect">
            <div className="local-detect-head">
              <span>{t("Detected local model servers")}</span>
              <button type="button" className="btn ghost sm" disabled={scanningLocal} onClick={() => void scanLocal()}>
                {scanningLocal ? t("Scanning…") : t("Rescan")}
              </button>
            </div>
            {localServers.map((ls) => (
              <div key={ls.id} className="local-detect-row">
                <div className="grow">
                  <div className="name">
                    {ls.name} <span className="pv-pill api">{ls.models.length} models</span>
                  </div>
                  <div className="meta" dir="ltr">
                    {ls.baseUrl}
                    {ls.models[0] ? ` · ${ls.models[0]}` : ""}
                  </div>
                </div>
                <button
                  type="button"
                  className="btn ghost sm"
                  onClick={async () => {
                    if (!ls.models.length) {
                      openDialog({ kind: "provider-edit" });
                      return;
                    }
                    try {
                      const r = await server.connectModel({ name: ls.name, url: ls.baseUrl, key: "", model: ls.models[0] });
                      setState({ info: r.info });
                      load();
                    } catch (e) {
                      fail(e);
                    }
                  }}
                >
                  {t("Connect")}
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
      <section className="set">
        <div className="set-text">
          <div className="set-title">{t("Auto-failover")}</div>
          <div className="set-help">{t("Switch automatically to the next enabled provider if the active one fails.")}</div>
        </div>
        <div className="set-control">
          <button type="button" role="switch" aria-checked={failover} className={"switch" + (failover ? " on" : "")} onClick={toggleFailover}>
            <span />
          </button>
        </div>
      </section>
    </>
  );
}

export function ProviderEditor({ provider }: { provider?: ProviderDetail }) {
  const t = useT();
  const isEdit = Boolean(provider);
  const [preset, setPreset] = useState<string>(isEdit ? "custom" : "openai");
  const [name, setName] = useState<string>(provider?.name || "OpenAI");
  const [url, setUrl] = useState<string>(provider?.baseUrl || "https://api.openai.com/v1");
  const [api, setApi] = useState<string>(provider?.api || "");
  const [key, setKey] = useState<string>("");
  const [clearKey, setClearKey] = useState<boolean>(false);
  const [model, setModel] = useState<string>(provider?.model || "");
  const [effort, setEffort] = useState<string>(provider?.effort || "");
  const [windowInput, setWindowInput] = useState<string>(provider?.contextWindow ? String(provider.contextWindow) : "");
  const [tempInput, setTempInput] = useState<string>(provider?.temperature !== null && provider?.temperature !== undefined ? String(provider.temperature) : "");
  const [vision, setVision] = useState<boolean>(provider?.vision !== false);
  const [stream, setStream] = useState<boolean>(!provider?.noStream);
  const [enabled, setEnabled] = useState<boolean>(provider?.enabled !== false);
  const [priority, setPriority] = useState<string>(provider?.priority !== undefined ? String(provider.priority) : "");

  const [models, setModels] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const tempLimit = api === "anthropic" || (!api && url.toLowerCase().includes("anthropic")) ? 1.0 : 2.0;

  const pickPreset = (id: (typeof PRESETS)[number]["id"]) => {
    const next = PRESETS.find((x) => x.id === id) || PRESETS[0];
    setPreset(id);
    if (!isEdit) {
      setName(id === "custom" ? "" : next.label);
    }
    setUrl(next.url);
    setApi(next.api);
    setModels(null);
    setError("");
  };

  const loadModels = async () => {
    setLoading(true);
    setError("");
    try {
      let list: string[] = [];
      if (isEdit && provider && !key.trim() && url.trim() === (provider.baseUrl || "")) {
        const r = await server.providerModels(provider.id);
        list = r.models || [];
      } else {
        const r = await server.listModels(url.trim(), key.trim());
        list = r.models || [];
      }
      setModels(list.slice().sort());
      if (!list.length) setError(t("The provider did not list any models; type the model's name."));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  };

  const buildPayload = (skipTest: boolean): ProviderUpsert | null => {
    const cleanName = name.trim();
    const cleanUrl = url.trim();
    const cleanModel = model.trim();
    if (!cleanName) {
      setError(t("Enter a name for this provider."));
      return null;
    }
    if (!cleanUrl || !cleanModel) {
      setError(t("API address and model name are required."));
      return null;
    }
    const parsedWin = parseWindowInput(windowInput);
    if (parsedWin === null) {
      setError(t("Context window must be Auto (empty) or between 8,000 and 10,000,000 (e.g. 128k, 256k, 1M)."));
      return null;
    }
    let tempVal: number | null = null;
    let clearTemp = false;
    if (tempInput.trim() === "" || tempInput.trim().toLowerCase() === "auto") {
      clearTemp = true;
    } else {
      const n = Number(tempInput.trim());
      if (!Number.isFinite(n) || n < 0 || n > tempLimit) {
        setError(`Temperature must be between 0 and ${tempLimit}.`);
        return null;
      }
      tempVal = n;
    }
    const payload: ProviderUpsert = {
      name: cleanName,
      baseUrl: cleanUrl,
      model: cleanModel,
      api: api || "auto",
      effort: effort || "auto",
      contextWindow: parsedWin,
      vision,
      noStream: !stream,
      enabled,
      clearTemp,
      activate: !isEdit,
      skipTest,
    };
    if (!clearTemp && tempVal !== null) payload.temperature = tempVal;
    if (clearKey) payload.clearKey = true;
    else if (key.trim()) payload.apiKey = key.trim();
    if (priority.trim() !== "") {
      const pr = Number(priority.trim());
      if (Number.isFinite(pr) && pr >= 0) payload.priority = Math.round(pr);
    }
    return payload;
  };

  const submit = async (skipTest: boolean) => {
    setError("");
    const payload = buildPayload(skipTest);
    if (!payload) return;
    setBusy(true);
    try {
      if (isEdit && provider) {
        const r = await server.updateProvider(provider.id, payload);
        setState({ info: r.info.info });
      } else {
        const r = await server.addProvider(payload);
        setState({ info: r.info.info });
      }
      await refreshState();
      openDialog({ kind: "settings", section: "providers" });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const shown = (models || []).filter((m) => m.toLowerCase().includes(model.trim().toLowerCase())).slice(0, 200);
  const currentWinNum = parseWindowInput(windowInput);

  return (
    <form
      className="pv-editor"
      onSubmit={(e) => {
        e.preventDefault();
        void submit(isEdit);
      }}
    >
      <div className="settings-head pv-ed-head">
        <h3>{isEdit ? t("Customize model & provider") : t("Add custom provider")}</h3>
        <button type="button" className="icon-btn" title="Close (Esc)" onClick={closeDialog}>
          <Icon name="x" />
        </button>
      </div>
      <div className="pv-ed-body">
        {!isEdit && (
          <div className="presets">
            {PRESETS.map((x) => (
              <button key={x.id} type="button" className={"preset" + (x.id === preset ? " on" : "")} onClick={() => pickPreset(x.id)}>
                {x.label}
              </button>
            ))}
          </div>
        )}
        <div className="pv-grid2">
          <label className="field">
            <span>{t("Provider name")}</span>
            <input autoFocus={!isEdit} spellCheck={false} placeholder="e.g. My DeepSeek, Work Claude" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="field">
            <span>{t("Priority")}</span>
            <input type="number" min={0} step={10} dir="ltr" placeholder="Auto (10, 20…)" value={priority} onChange={(e) => setPriority(e.target.value)} />
          </label>
        </div>

        <label className="field">
          <span>{t("API format")}</span>
          <div className="choice" role="radiogroup">
            {API_MODES.map(([v, label]) => (
              <button key={v || "auto"} type="button" role="radio" aria-checked={api === v} className={api === v ? "on" : ""} onClick={() => setApi(v)}>
                {label}
              </button>
            ))}
          </div>
        </label>

        <label className="field">
          <span>{t("API address")}</span>
          <input spellCheck={false} dir="ltr" placeholder="https://api.example.com/v1" value={url} onChange={(e) => setUrl(e.target.value)} />
        </label>

        <label className="field">
          <span className="field-head">
            <span>
              {t("API key")}
              {isEdit && provider?.apiKeySet && !clearKey && <span className="meta"> ({provider.apiKeyMasked || "saved"})</span>}
            </span>
            {isEdit && provider?.apiKeySet && (
              <button type="button" className="link-btn inline" onClick={() => setClearKey(!clearKey)}>
                {clearKey ? t("Keep existing key") : t("Clear key")}
              </button>
            )}
          </span>
          <input
            type="password"
            spellCheck={false}
            dir="ltr"
            disabled={clearKey}
            placeholder={isEdit && provider?.apiKeySet ? t("Leave blank to keep existing key") : t("Optional for local endpoints like Ollama")}
            value={key}
            onChange={(e) => setKey(e.target.value)}
          />
        </label>

        <label className="field">
          <span className="field-head">
            <span>{t("Model")}</span>
            <button type="button" className="link-btn inline" disabled={loading || !url.trim()} onClick={loadModels}>
              {loading ? t("Loading…") : models ? t("Reload models") : t("Fetch models")}
            </button>
          </span>
          <input spellCheck={false} dir="ltr" placeholder="gpt-4o, claude-sonnet-4-5, deepseek-reasoner…" value={model} onChange={(e) => setModel(e.target.value)} />
        </label>
        {models && shown.length > 0 && (
          <div className="model-list">
            {shown.map((m) => (
              <button key={m} type="button" className={"entry" + (m === model ? " sel" : "")} onClick={() => setModel(m)}>
                <span className="label" dir="ltr">
                  {m}
                </span>
                {m === model && (
                  <span className="end">
                    <Icon name="check" size={14} />
                  </span>
                )}
              </button>
            ))}
          </div>
        )}

        <div className="pv-subhead">{t("Per-model customization")}</div>

        <label className="field">
          <span>{t("Effort")}</span>
          <div className="choice" role="radiogroup">
            {effortLevels.map((l) => (
              <button key={l || "auto"} type="button" role="radio" aria-checked={effort === l} className={effort === l ? "on" : ""} onClick={() => setEffort(l)}>
                {l === "ultracode" ? "Ultra" : effortName(l)}
              </button>
            ))}
          </div>
        </label>

        <div className="pv-grid2">
          <label className="field">
            <span>{t("Context window (tokens)")}</span>
            <input spellCheck={false} dir="ltr" placeholder="Auto (e.g. 128k, 256k, 1M)" value={windowInput} onChange={(e) => setWindowInput(e.target.value)} />
            <div className="pv-quick-pills" dir="ltr">
              {WINDOW_PRESETS.map(([val, lbl]) => (
                <button
                  key={lbl}
                  type="button"
                  className={"pv-qpill" + (currentWinNum === val ? " on" : "")}
                  onClick={() => setWindowInput(val === 0 ? "" : String(val))}
                >
                  {lbl}
                </button>
              ))}
            </div>
          </label>
          <label className="field">
            <span>
              {t("Temperature")} (0–{tempLimit})
            </span>
            <input spellCheck={false} dir="ltr" placeholder="Auto (model default)" value={tempInput} onChange={(e) => setTempInput(e.target.value)} />
            <div className="pv-quick-pills" dir="ltr">
              {["", "0", "0.2", "0.5", "0.7", "1"].map((v) => (
                <button key={v || "auto"} type="button" className={"pv-qpill" + (tempInput.trim() === v ? " on" : "")} onClick={() => setTempInput(v)}>
                  {v || "Auto"}
                </button>
              ))}
            </div>
          </label>
        </div>

        <div className="pv-toggles">
          <label className="pv-toggle-item">
            <span>{t("Vision (images)")}</span>
            <button type="button" role="switch" aria-checked={vision} className={"switch" + (vision ? " on" : "")} onClick={() => setVision(!vision)}>
              <span />
            </button>
          </label>
          <label className="pv-toggle-item">
            <span>{t("Streaming (SSE)")}</span>
            <button type="button" role="switch" aria-checked={stream} className={"switch" + (stream ? " on" : "")} onClick={() => setStream(!stream)}>
              <span />
            </button>
          </label>
          <label className="pv-toggle-item">
            <span>{t("Enabled")}</span>
            <button type="button" role="switch" aria-checked={enabled} className={"switch" + (enabled ? " on" : "")} onClick={() => setEnabled(!enabled)}>
              <span />
            </button>
          </label>
        </div>

        {error && <div className="form-error">{error}</div>}
      </div>

      <div className="bar">
        <button type="button" className="btn" onClick={() => openDialog({ kind: "settings", section: "providers" })}>
          {t("Cancel")}
        </button>
        <div className="left">
          {!isEdit && (
            <button type="button" className="btn ghost" disabled={busy || !name.trim() || !url.trim() || !model.trim()} onClick={() => void submit(true)}>
              {t("Save without testing")}
            </button>
          )}
          <button type="submit" className="btn primary" disabled={busy || !name.trim() || !url.trim() || !model.trim()}>
            {busy ? t("Saving…") : isEdit ? t("Save changes") : t("Test and connect")}
          </button>
        </div>
      </div>
    </form>
  );
}
