import { useEffect, useMemo, useState } from "react";
import {
  effortLevels,
  effortName,
  effortNote,
  errorText,
  fail,
  openDialog,
  refreshState,
  setTuning,
  windowName,
} from "../../actions";
import { server } from "../../api";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { setState, useStore } from "../../store";
import type { MemoryEntry, MoaState, ProviderDetail, ProviderTestResult, RoutingConfig, RoutingTestResult } from "../../types";

function ProviderMark({ name, api }: { name: string; api?: string }) {
  const n = (name + " " + (api || "")).toLowerCase();
  if (n.includes("anthropic") || n.includes("claude")) {
    return (
      <span className="pv-mark anthropic" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none">
          <path d="M14.5 5L20 19h-2.8l-1.3-3.4H11L14.5 5zM9.2 5L4 19h2.7l3.9-10.4L9.2 5z" fill="currentColor" />
        </svg>
      </span>
    );
  }
  if (n.includes("openai") || n.includes("gpt") || n.includes("responses")) {
    return (
      <span className="pv-mark openai" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.8">
          <circle cx="12" cy="12" r="7.5" />
          <path d="M12 4.5v15M5.5 8.2l13 7.6M18.5 8.2l-13 7.6" />
        </svg>
      </span>
    );
  }
  if (n.includes("gemini") || n.includes("google")) {
    return (
      <span className="pv-mark gemini" aria-hidden="true">
        <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
          <path d="M12 2.5C13.2 8.2 15.8 10.8 21.5 12C15.8 13.2 13.2 15.8 12 21.5C10.8 15.8 8.2 13.2 2.5 12C8.2 10.8 10.8 8.2 12 2.5Z" />
        </svg>
      </span>
    );
  }
  if (n.includes("deepseek")) {
    return (
      <span className="pv-mark deepseek" aria-hidden="true">
        DS
      </span>
    );
  }
  if (n.includes("openrouter")) {
    return (
      <span className="pv-mark openrouter" aria-hidden="true">
        OR
      </span>
    );
  }
  if (n.includes("ollama") || n.includes("local") || n.includes("lm studio")) {
    return (
      <span className="pv-mark local" aria-hidden="true">
        <Icon name="terminal" size={14} />
      </span>
    );
  }
  return (
    <span className="pv-mark custom" aria-hidden="true">
      {(name || "M").slice(0, 2).toUpperCase()}
    </span>
  );
}

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
      const parsed = JSON.parse(s.slice(brace)) as { error?: { message?: string }; message?: string };
      const msg = parsed?.error?.message || parsed?.message;
      if (msg) return statusPrefix + msg.trim();
    } catch {
      const m = s.match(/"message"\s*:\s*"([^"]+)"/);
      if (m?.[1]) return statusPrefix + m[1].trim();
    }
  }
  return s;
}

export function ProvidersSettingsPage() {
  const t = useT();
  const info = useStore((s) => s.info);
  const [providers, setProviders] = useState<ProviderDetail[] | null>(null);
  const [failover, setFailover] = useState<boolean>(info.failover ?? true);
  const [query, setQuery] = useState("");
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

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return providers || [];
    return (providers || []).filter((p) => `${p.name} ${p.model} ${p.baseUrl || ""} ${p.resolvedApi || p.api || ""}`.toLowerCase().includes(q));
  }, [providers, query]);

  const activeProv = useMemo(() => (providers || []).find((p) => p.active), [providers]);

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
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("Models & Providers")}</h1>
          <p>{t("Models & custom providers")}</p>
        </div>
        <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "provider-edit" })}>
          <Icon name="plus" size={14} />
          <span>{t("Add provider")}</span>
        </button>
      </header>

      {/* Active Model Spotlight */}
      {info.model ? (
        <div className="pv-spotlight">
          <ProviderMark name={info.provider || info.model} api={activeProv?.resolvedApi || activeProv?.api} />
          <div className="grow">
            <div className="pv-spot-label">{t("Active model")}</div>
            <div className="pv-spot-model" dir="ltr">
              {info.model}
            </div>
            <div className="pv-spot-meta" dir="ltr">
              {info.provider}
              {info.contextWindow ? ` · ${windowName(info.contextWindow)} ctx` : ""}
              {activeProv?.avgLatencyMs ? ` · ${activeProv.avgLatencyMs}ms avg` : ""}
            </div>
          </div>
          <div className="pv-spot-actions">
            <button type="button" className="btn ghost sm" onClick={() => openDialog({ kind: "palette", mode: "models" })}>
              {t("Pick the model")}
            </button>
            {activeProv && (
              <button type="button" className="btn ghost sm" onClick={() => openDialog({ kind: "provider-edit", provider: activeProv })}>
                {t("Configure model")}
              </button>
            )}
          </div>
        </div>
      ) : (
        <div className="stg-empty-banner">
          <div>
            <div className="stg-empty-title">{t("No active model connected")}</div>
            <p>{t("Macron needs a language model: OpenAI, Anthropic, Gemini, OpenRouter, a local Ollama, or any compatible provider.")}</p>
          </div>
          <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "model-connect" })}>
            {t("Connect a model")}
          </button>
        </div>
      )}

      {/* Search + Auto-Failover bar */}
      <div className="pv-toolbar">
        <div className="set-search grow">
          <Icon name="search" size={13} />
          <input type="text" value={query} placeholder={t("Filter models or providers…")} onChange={(e) => setQuery(e.target.value)} />
        </div>
        <div className="pv-failover-inline">
          <span>{t("Auto-failover")}</span>
          <button type="button" role="switch" aria-checked={failover} className={"switch" + (failover ? " on" : "")} onClick={toggleFailover}>
            <span />
          </button>
        </div>
      </div>

      {/* Providers List */}
      <div className="pv-grid">
        {providers === null && <div className="empty-row">{t("Loading…")}</div>}
        {providers && !filtered.length && <div className="empty-row">{t("No model connected yet.")}</div>}
        {filtered.map((p) => {
          const tr = testResults[p.id];
          const isTesting = Boolean(testing[p.id]);
          const win = p.contextWindow ? windowName(p.contextWindow) : p.autoWindow ? `${windowName(p.autoWindow)} auto` : "";
          const statusCls = !p.enabled ? "off" : p.cooling ? "warn" : p.lastError ? "bad" : p.active ? "ok" : "ready";

          return (
            <div key={p.id} className={"pv-card v2" + (p.active ? " on" : "") + (!p.enabled ? " off" : "")}>
              <div className="pv-top">
                <button
                  type="button"
                  className="pv-select"
                  disabled={p.active}
                  title={p.active ? t("Active provider") : t("Switch to this provider")}
                  onClick={() => activate(p.id)}
                >
                  <ProviderMark name={p.name} api={p.resolvedApi || p.api} />
                  <span className="pv-ident">
                    <span className="pv-name-row">
                      <span className="name">{p.name}</span>
                      <span className={`pv-status-dot ${statusCls}`} aria-hidden="true" />
                      <span className="pv-pill api">{apiBadge(p.resolvedApi || p.api)}</span>
                      {p.active && <span className="pv-pill active">{t("Active")}</span>}
                      {!p.enabled && <span className="pv-pill muted">{t("Disabled")}</span>}
                      {p.cooling && <span className="pv-pill warn">{t("Cooling")}</span>}
                    </span>
                    <span className="meta pv-model" dir="ltr">
                      {p.model || "no model chosen"}
                      {p.apiKeySet ? ` · key ${p.apiKeyMasked || "••••"}` : ""}
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
                    <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => setConfirmDel(p.id)}>
                      <Icon name="trash" size={13} />
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
      </div>

      {/* Detected Local Servers */}
      {localServers && localServers.length > 0 && (
        <section className="local-detect">
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
        </section>
      )}
    </div>
  );
}

export function RoutingSettingsPage() {
  const t = useT();
  const [providers, setProviders] = useState<ProviderDetail[]>([]);
  const [routing, setRouting] = useState<RoutingConfig | null>(null);
  const [testText, setTestText] = useState("");
  const [testRes, setTestRes] = useState<RoutingTestResult | null>(null);
  const [testing, setTesting] = useState(false);

  useEffect(() => {
    server.providers().then((r) => setProviders((r.providers || []).filter((p) => p.enabled))).catch(() => setProviders([]));
    server.routing().then(setRouting).catch(() => setRouting(null));
  }, []);

  const updateRouting = async (patch: Parameters<typeof server.saveRouting>[0]) => {
    try {
      const next = await server.saveRouting(patch);
      setRouting(next);
      await refreshState();
    } catch (e) {
      fail(e);
    }
  };

  const toggleKind = (kind: string) => {
    if (!routing) return;
    const set = new Set(routing.kinds || []);
    if (set.has(kind)) set.delete(kind);
    else set.add(kind);
    void updateRouting({ kinds: Array.from(set), kindsSet: true });
  };

  const runRouteTest = async () => {
    if (!testText.trim()) return;
    setTesting(true);
    try {
      const r = await server.testRouting(testText.trim());
      setTestRes(r);
    } catch (e) {
      fail(e);
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Smart Routing")}</h1>
        <p>{t("Route simple prompts to your active model and heavy tasks (code, reasoning, research) to a strong model.")}</p>
      </header>

      {/* Visual Routing Flow Diagram */}
      <div className="stg-flow-diagram" aria-hidden="true">
        <div className="flow-node">
          <Icon name="message" size={14} />
          <span>{t("Prompt arrives")}</span>
        </div>
        <span className="flow-arrow">→</span>
        <div className={"flow-node accent" + (routing?.active ? " on" : "")}>
          <Icon name="route" size={14} />
          <span>{t("Classifier")}</span>
        </div>
        <span className="flow-arrow">→</span>
        <div className="flow-branch">
          <div className="flow-node subtle">
            <span>{t("Light model")}</span>
            <em dir="ltr">{routing?.lightProviderName || "Active"}</em>
          </div>
          <div className={"flow-node subtle" + (routing?.strongProviderId ? " on" : "")}>
            <span>{t("Strong model")}</span>
            <em dir="ltr">{routing?.strongProviderName || "—"}</em>
          </div>
        </div>
      </div>

      {routing && (
        <div className="rm-box v2">
          <div className="rm-row">
            <div className="grow">
              <div className="set-title">
                {t("Automatic light / strong routing")}
                {routing.active ? <span className="pv-pill active">{t("Active")}</span> : <span className="pv-pill muted">{t("Inactive")}</span>}
              </div>
              <div className="set-help">
                {t("Route simple prompts to your active model and heavy tasks (code, reasoning, research) to a strong model.")}
              </div>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={routing.enabled}
              className={"switch" + (routing.enabled ? " on" : "")}
              onClick={() => void updateRouting({ enabled: !routing.enabled })}
            >
              <span />
            </button>
          </div>

          <div className="rm-grid">
            <label className="field">
              <span>{t("Strong model provider")}</span>
              <select
                className="rm-select"
                value={String(routing.strongProviderId || 0)}
                onChange={(e) => void updateRouting({ strongProviderId: Number(e.target.value) })}
              >
                <option value="0">{t("None (use active model only)")}</option>
                {providers.map((p) => (
                  <option key={p.id} value={String(p.id)}>
                    {p.name} — {p.model}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>{t("Routing sensitivity")}</span>
              <div className="choice" role="radiogroup">
                {(
                  [
                    ["cheap", t("Cheap")],
                    ["balanced", t("Balanced")],
                    ["quality", t("Quality")],
                  ] as const
                ).map(([val, label]) => (
                  <button
                    key={val}
                    type="button"
                    role="radio"
                    aria-checked={routing.sensitivity === val}
                    className={routing.sensitivity === val ? "on" : ""}
                    onClick={() => void updateRouting({ sensitivity: val })}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </label>
          </div>

          <div className="rm-kinds-wrap">
            <div className="rm-label">{t("Send these task kinds to the strong model")}</div>
            <div className="rm-kinds" dir="ltr">
              {(routing.allKinds || []).map((k) => {
                const on = (routing.kinds || []).includes(k);
                return (
                  <button key={k} type="button" className={"pv-qpill" + (on ? " on" : "")} onClick={() => toggleKind(k)}>
                    {k}
                  </button>
                );
              })}
            </div>
          </div>

          <div className="rm-test">
            <input
              type="text"
              placeholder={t("Test routing with a sample prompt…")}
              value={testText}
              onChange={(e) => setTestText(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), void runRouteTest())}
            />
            <button type="button" className="btn ghost sm" disabled={testing || !testText.trim()} onClick={() => void runRouteTest()}>
              {testing ? t("Testing…") : t("Test")}
            </button>
          </div>
          {testRes && (
            <div className={"pv-test-res " + (testRes.strong ? "ok" : "")} dir="ltr">
              {testRes.strong ? "→ Strong" : "→ Regular"}
              {testRes.targetProvider ? ` (${testRes.targetProvider})` : ""} · kind: {testRes.kind}
              {testRes.hard ? " · hard" : ""}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function MoaSettingsPage() {
  const t = useT();
  const [providers, setProviders] = useState<ProviderDetail[]>([]);
  const [moa, setMoa] = useState<MoaState | null>(null);
  const [addingMoa, setAddingMoa] = useState(false);
  const [mName, setMName] = useState("");
  const [mAgg, setMAgg] = useState("");
  const [mRefs, setMRefs] = useState<string[]>([]);
  const [mFanout, setMFanout] = useState("user_turn");
  const [mPrivacy, setMPrivacy] = useState("");
  const [mErr, setMErr] = useState("");
  const [savingMoa, setSavingMoa] = useState(false);

  useEffect(() => {
    server.providers().then((r) => setProviders((r.providers || []).filter((p) => p.enabled))).catch(() => setProviders([]));
    server.moa().then(setMoa).catch(() => setMoa(null));
  }, []);

  const toggleRef = (name: string) => {
    setMRefs((prev) => (prev.includes(name) ? prev.filter((x) => x !== name) : [...prev, name]));
  };

  const savePreset = async () => {
    setMErr("");
    if (!mName.trim() || !mAgg || mRefs.length === 0) {
      setMErr(t("Enter a preset name, pick an aggregator, and choose at least one reference model."));
      return;
    }
    setSavingMoa(true);
    try {
      const st = await server.saveMoaPreset({
        name: mName.trim(),
        aggregator: { provider: mAgg },
        references: mRefs.map((p) => ({ provider: p })),
        fanout: mFanout,
        privacyFilter: mPrivacy,
        enabled: true,
      });
      setMoa(st);
      setAddingMoa(false);
      setMName("");
      setMRefs([]);
      await refreshState();
    } catch (e) {
      setMErr(errorText(e));
    } finally {
      setSavingMoa(false);
    }
  };

  const setMoaActive = async (name: string) => {
    try {
      const st = await server.updateMoaConfig({ active: name });
      setMoa(st);
      await refreshState();
    } catch (e) {
      fail(e);
    }
  };

  const setMoaDefault = async (name: string) => {
    try {
      const st = await server.updateMoaConfig({ defaultPreset: name });
      setMoa(st);
    } catch (e) {
      fail(e);
    }
  };

  const delPreset = async (name: string) => {
    try {
      const st = await server.deleteMoaPreset(name);
      setMoa(st);
      await refreshState();
    } catch (e) {
      fail(e);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("Mixture of Agents")}</h1>
          <p>{t("No MoA presets yet. Combine multiple models to vote and synthesize better answers.")}</p>
        </div>
        {!addingMoa && (
          <button
            type="button"
            className="btn primary sm"
            onClick={() => {
              setAddingMoa(true);
              if (!mAgg && providers[0]) setMAgg(providers[0].name);
            }}
          >
            <Icon name="plus" size={14} />
            <span>{t("Create preset")}</span>
          </button>
        )}
      </header>

      {/* Visual MoA Synthesis Architecture */}
      <div className="stg-flow-diagram" aria-hidden="true">
        <div className="flow-node">
          <Icon name="network" size={14} />
          <span>{t("Advisors")}</span>
        </div>
        <span className="flow-arrow">→</span>
        <div className={"flow-node accent" + (moa?.active ? " on" : "")}>
          <Icon name="sparkles" size={14} />
          <span>{t("Aggregator")}</span>
        </div>
        <span className="flow-arrow">→</span>
        <div className="flow-node subtle">
          <Icon name="check" size={14} />
          <span>{t("Final answer")}</span>
        </div>
      </div>

      <div className="pv-grid">
        {moa && moa.presets.length === 0 && !addingMoa && (
          <div className="empty-row">{t("No MoA presets yet. Combine multiple models to vote and synthesize better answers.")}</div>
        )}
        {(moa?.presets || []).map((p) => {
          const isActive = moa?.active === p.name;
          const isDef = moa?.defaultPreset === p.name;
          const hasProbs = (p.problems || []).length > 0;
          return (
            <div key={p.name} className={"pv-card v2" + (isActive ? " on" : "")}>
              <div className="pv-top">
                <div className="pv-ident">
                  <div className="pv-name-row">
                    <span className="name">{p.name}</span>
                    {isActive && <span className="pv-pill active">{t("Always on")}</span>}
                    {isDef && <span className="pv-pill api">{t("Default")}</span>}
                    {hasProbs && <span className="pv-pill warn">{t("Needs setup")}</span>}
                  </div>
                  <div className="pv-chips moa-chips" dir="ltr">
                    <span className="pv-chip">agg: {p.aggregator.provider}</span>
                    <span className="pv-chip">refs: {p.references.map((r) => r.provider).join(", ")}</span>
                    <span className="pv-chip">{p.fanout}</span>
                  </div>
                </div>
                <div className="pv-actions">
                  {!isDef && (
                    <button type="button" className="btn ghost sm" onClick={() => void setMoaDefault(p.name)}>
                      {t("Set default")}
                    </button>
                  )}
                  <button
                    type="button"
                    className={"btn sm " + (isActive ? "primary" : "ghost")}
                    disabled={hasProbs}
                    onClick={() => void setMoaActive(isActive ? "" : p.name)}
                  >
                    {isActive ? t("Turn off") : t("Always on")}
                  </button>
                  <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void delPreset(p.name)}>
                    <Icon name="trash" size={13} />
                  </button>
                </div>
              </div>
              {hasProbs && (
                <div className="pv-test-res bad" dir="ltr">
                  {(p.problems || []).join(" · ")}
                </div>
              )}
            </div>
          );
        })}

        {addingMoa && (
          <div className="rm-box v2">
            <div className="pv-grid2">
              <label className="field">
                <span>{t("Preset name")}</span>
                <input placeholder="e.g. Council" value={mName} onChange={(e) => setMName(e.target.value)} />
              </label>
              <label className="field">
                <span>{t("Aggregator model")}</span>
                <select className="rm-select" value={mAgg} onChange={(e) => setMAgg(e.target.value)}>
                  <option value="">{t("Choose provider…")}</option>
                  {providers.map((p) => (
                    <option key={p.id} value={p.name}>
                      {p.name} ({p.model})
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <div className="rm-kinds-wrap">
              <div className="rm-label">{t("Reference models (advisors)")}</div>
              <div className="rm-kinds" dir="ltr">
                {providers.map((p) => {
                  const on = mRefs.includes(p.name);
                  return (
                    <button key={p.id} type="button" className={"pv-qpill" + (on ? " on" : "")} onClick={() => toggleRef(p.name)}>
                      {p.name}
                    </button>
                  );
                })}
              </div>
            </div>
            <div className="pv-grid2">
              <label className="field">
                <span>{t("Fanout mode")}</span>
                <select className="rm-select" value={mFanout} onChange={(e) => setMFanout(e.target.value)}>
                  <option value="user_turn">Once per user turn</option>
                  <option value="per_iteration">Every reasoning step</option>
                </select>
              </label>
              <label className="field">
                <span>{t("Privacy filter")}</span>
                <select className="rm-select" value={mPrivacy} onChange={(e) => setMPrivacy(e.target.value)}>
                  <option value="">None</option>
                  <option value="display">Scrub displayed advice</option>
                  <option value="full">Scrub prompts & advice</option>
                </select>
              </label>
            </div>
            {mErr && <div className="form-error">{mErr}</div>}
            <div className="soul-bar">
              <button type="button" className="btn ghost sm" onClick={() => setAddingMoa(false)}>
                {t("Cancel")}
              </button>
              <button type="button" className="btn primary sm" disabled={savingMoa} onClick={() => void savePreset()}>
                {savingMoa ? t("Saving…") : t("Create preset")}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function ParametersSettingsPage() {
  const t = useT();
  const tuning = useStore((s) => s.tuning);

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Model Parameters")}</h1>
        <p>{tuning?.model ? tuning.model : t("Connect a provider to unlock model parameters.")}</p>
      </header>

      {!tuning || !tuning.model ? (
        <div className="stg-empty-banner">
          <div>
            <div className="stg-empty-title">{t("No active model connected")}</div>
            <p>{t("Connect a provider to unlock model parameters.")}</p>
          </div>
          <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "model-connect" })}>
            {t("Connect a model")}
          </button>
        </div>
      ) : (
        <section className="stg-block">
          <div className="stg-row-setting">
            <div>
              <div className="stg-row-title">{t("Effort")}</div>
              <div className="stg-row-help">{effortNote(tuning)}</div>
            </div>
            <div className="choice" role="radiogroup">
              {effortLevels.map((l) => {
                const v = l || "auto";
                return (
                  <button
                    key={v}
                    type="button"
                    role="radio"
                    aria-checked={(tuning.effort || "auto") === v}
                    className={(tuning.effort || "auto") === v ? "on" : ""}
                    onClick={() => void setTuning("effort", v)}
                  >
                    {l === "ultracode" ? "Ultra" : effortName(l)}
                  </button>
                );
              })}
            </div>
          </div>

          <div className="stg-row-setting">
            <div>
              <div className="stg-row-title">{t("Context window")}</div>
              <div className="stg-row-help">
                {tuning.window
                  ? "Set by you. A window larger than the model really has makes requests fail."
                  : `Worked out from the model's name: ${windowName(tuning.autoWindow)} tokens.`}
              </div>
            </div>
            <div className="choice" role="radiogroup" dir="ltr">
              {(
                [
                  ["0", "Auto"],
                  ...(["128000", "200000", "400000", "1000000"].includes(String(tuning.window)) || !tuning.window
                    ? []
                    : [[String(tuning.window), windowName(tuning.window)] as [string, string]]),
                  ["128000", "128k"],
                  ["200000", "200k"],
                  ["400000", "400k"],
                  ["1000000", "1M"],
                ] as [string, string][]
              ).map(([v, lbl]) => (
                <button
                  key={v}
                  type="button"
                  role="radio"
                  aria-checked={String(tuning.window) === v}
                  className={String(tuning.window) === v ? "on" : ""}
                  onClick={() => void setTuning("window", Number(v))}
                >
                  {lbl}
                </button>
              ))}
            </div>
          </div>

          <div className="stg-row-setting">
            <div>
              <div className="stg-row-title">{t("Temperature")}</div>
              <div className="stg-row-help">Lower answers are exact and repeatable, higher ones are more varied.</div>
            </div>
            <div className="choice" role="radiogroup" dir="ltr">
              {(
                [
                  ["auto", "Auto"],
                  ["0", "0"],
                  ["0.2", "0.2"],
                  ["0.5", "0.5"],
                  ["0.7", "0.7"],
                  ["1", "1"],
                  ...(tuning.tempLimit >= 2 ? [["1.5", "1.5"] as [string, string]] : []),
                ] as [string, string][]
              ).map(([v, lbl]) => {
                const cur = tuning.temperature === null ? "auto" : String(tuning.temperature);
                return (
                  <button
                    key={v}
                    type="button"
                    role="radio"
                    aria-checked={cur === v}
                    className={cur === v ? "on" : ""}
                    onClick={() => void setTuning("temperature", v === "auto" ? null : Number(v))}
                  >
                    {lbl}
                  </button>
                );
              })}
            </div>
          </div>
        </section>
      )}
    </div>
  );
}

export function SoulSettingsPage() {
  const t = useT();
  const [text, setText] = useState("");
  const [path, setPath] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setLoading(true);
    server
      .getSoul()
      .then((r) => {
        setText(r.text || "");
        setPath(r.path || "");
      })
      .catch((e) => fail(e))
      .finally(() => setLoading(false));
  }, []);

  const save = async () => {
    if (!text.trim()) return;
    setSaving(true);
    setSaved(false);
    try {
      const r = await server.saveSoul(text);
      setText(r.text || text);
      setSaved(true);
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  };

  const reset = async () => {
    setSaving(true);
    setSaved(false);
    try {
      const r = await server.resetSoul();
      setText(r.text || "");
      setSaved(true);
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  };

  const lines = text ? text.split("\n").length : 0;

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Personality & SOUL.md")}</h1>
        <p>{t("Personality, tone, and identity shared across Desktop, TUI, and Telegram")}</p>
      </header>

      <div className="stg-editor-shell">
        <div className="stg-editor-bar">
          <span className="stg-editor-path" dir="ltr">
            <Icon name="file" size={13} />
            {path || "SOUL.md"}
          </span>
          <span className="stg-editor-meta" dir="ltr">
            {lines} lines · {text.length} chars
          </span>
        </div>
        <textarea
          className="stg-soul-editor"
          rows={16}
          dir="auto"
          disabled={loading}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setSaved(false);
          }}
        />
        <div className="stg-editor-foot">
          <button type="button" className="btn ghost sm" disabled={saving || loading} onClick={() => void reset()}>
            {t("Reset default")}
          </button>
          <div className="left">
            {saved && <span className="meta ok-text">{t("Saved")}</span>}
            <button type="button" className="btn primary sm" disabled={saving || loading || !text.trim()} onClick={() => void save()}>
              {saving ? t("Saving…") : t("Save")}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

export function MemorySettingsPage() {
  const t = useT();
  const [items, setItems] = useState<MemoryEntry[] | null>(null);
  const [scope, setScope] = useState<"memory" | "user">("memory");
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);

  const load = () => {
    server
      .memories()
      .then((r) => setItems(r.memories || []))
      .catch(() => setItems([]));
  };

  useEffect(() => {
    load();
  }, []);

  const filtered = useMemo(() => (items || []).filter((m) => m.scope === scope), [items, scope]);
  const agentCount = (items || []).filter((m) => m.scope === "memory").length;
  const userCount = (items || []).filter((m) => m.scope === "user").length;

  const add = async () => {
    const text = draft.trim();
    if (!text) return;
    setSaving(true);
    try {
      await server.addMemory(scope, text);
      setDraft("");
      load();
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  };

  const del = async (entry: MemoryEntry) => {
    try {
      await server.deleteMemory(entry.scope, entry.id);
      load();
    } catch (e) {
      fail(e);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Memory")}</h1>
        <p>{t("Agent memory")} · {t("About you")}</p>
      </header>

      <div className="mem-tabs">
        <button type="button" className={"mem-tab" + (scope === "memory" ? " on" : "")} onClick={() => setScope("memory")}>
          {t("Agent memory")} <span className="ps-badge">{agentCount}</span>
        </button>
        <button type="button" className={"mem-tab" + (scope === "user" ? " on" : "")} onClick={() => setScope("user")}>
          {t("About you")} <span className="ps-badge">{userCount}</span>
        </button>
      </div>

      <div className="mem-add">
        <input
          type="text"
          dir="auto"
          value={draft}
          placeholder={t("Add a memory note…")}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void add();
            }
          }}
        />
        <button type="button" className="btn primary sm" disabled={saving || !draft.trim()} onClick={() => void add()}>
          {t("Add")}
        </button>
      </div>

      <div className="set-list">
        {items === null && <div className="empty-row">{t("Loading…")}</div>}
        {items && !filtered.length && <div className="empty-row">{t("No saved memories yet.")}</div>}
        {filtered.map((m) => (
          <div key={m.scope + ":" + m.id} className="set-row static">
            <span className="grow">
              <span className="mem-text" dir="auto">
                {m.text}
              </span>
            </span>
            <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void del(m)}>
              <Icon name="trash" size={13} />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
