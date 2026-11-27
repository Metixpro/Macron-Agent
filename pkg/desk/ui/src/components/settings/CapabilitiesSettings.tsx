import { useEffect, useMemo, useState } from "react";
import { connectServer, disconnectServer, errorText, fail, loadServers, removeServer } from "../../actions";
import { server } from "../../api";
import { useHomePath } from "../../hooks";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { setState, useStore } from "../../store";
import type { ConnectionDetail, Plugin, SkillView } from "../../types";

function splitArgs(s: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote = "";
  let has = false;
  for (const ch of s) {
    if (quote) {
      if (ch === quote) quote = "";
      else cur += ch;
    } else if (ch === '"' || ch === "'") {
      quote = ch;
      has = true;
    } else if (/\s/.test(ch)) {
      if (has || cur) out.push(cur);
      cur = "";
      has = false;
    } else cur += ch;
  }
  if (has || cur) out.push(cur);
  return out;
}

function pluginStatus(p: Plugin): string {
  if (!p.enabled) return "Off";
  if (p.error) return p.error;
  if (p.status === "connecting") return "Connecting…";
  if (p.status === "ready") return p.tools.length === 1 ? "1 tool" : `${p.tools.length} tools`;
  return p.status;
}

export function McpSettingsPage() {
  const t = useT();
  const [plugins, setPlugins] = useState<Plugin[] | null>(null);
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [target, setTarget] = useState("");
  const [vars, setVars] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const remote = /^https?:\/\//i.test(target.trim());

  const load = () =>
    server
      .plugins()
      .then((r) => setPlugins(r.plugins || []))
      .catch(() => setPlugins([]));

  useEffect(() => {
    load();
  }, []);

  useEffect(() => {
    if (!adding) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeForm();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [adding]);

  const closeForm = () => {
    setAdding(false);
    setName("");
    setTarget("");
    setVars("");
    setError("");
  };

  const submitAdd = async () => {
    setError("");
    const env: Record<string, string> = {};
    for (const line of vars.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const i = remote && trimmed.includes(":") && !trimmed.includes("=") ? trimmed.indexOf(":") : trimmed.indexOf("=");
      if (i <= 0) {
        setError(`“${trimmed}” is not KEY=value`);
        return;
      }
      env[trimmed.slice(0, i).trim()] = trimmed.slice(i + 1).trim();
    }
    const parts = remote ? [target.trim()] : splitArgs(target.trim());
    if (!parts.length || !parts[0]) {
      setError(t("Command or address is required."));
      return;
    }
    setBusy(true);
    try {
      const p = await server.addPlugin({ name: name.trim(), target: parts[0], args: parts.slice(1), env });
      load();
      if (p.error || (p.enabled && p.status !== "ready")) {
        setError(`${p.name} was added, but it is not ready: ${pluginStatus(p)}`);
        return;
      }
      closeForm();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const toggle = async (p: Plugin) => {
    setPlugins((list) => (list || []).map((x) => (x.key === p.key ? { ...x, enabled: !p.enabled, status: p.enabled ? "off" : "connecting" } : x)));
    try {
      await server.enablePlugin(p.key, !p.enabled);
    } catch (e) {
      fail(e);
    }
    load();
  };

  const drop = async (p: Plugin) => {
    try {
      await server.removePlugin(p.key);
    } catch (e) {
      fail(e);
    }
    load();
  };

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("Tools & MCP")}</h1>
          <p>{t("Connect local or remote MCP tool servers to extend Macron's capabilities.")}</p>
        </div>
        {!adding && (
          <button type="button" className="btn primary sm" onClick={() => setAdding(true)}>
            <Icon name="plus" size={14} />
            <span>{t("Add an MCP server…")}</span>
          </button>
        )}
      </header>

      {adding && (
        <form
          className="stg-inline-studio"
          onSubmit={(e) => {
            e.preventDefault();
            void submitAdd();
          }}
        >
          <div className="stg-inline-head">
            <h3>{t("Add an MCP server")}</h3>
            <button type="button" className="icon-btn sm" title="Close (Esc)" aria-label={t("Close")} onClick={closeForm}>
              <Icon name="x" size={14} />
            </button>
          </div>
          <div className="stg-inline-body">
            <p className="stg-inline-sub">
              {t("Its tools reach Macron here, in the terminal and in Telegram. Local servers run on this machine; remote ones are reached by their address.")}
            </p>
            <div className="pv-grid2">
              <label className="field">
                <span>{t("Name")}</span>
                <input autoFocus spellCheck={false} placeholder="e.g. github" value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label className="field">
                <span>{t("Command or address")}</span>
                <input
                  spellCheck={false}
                  dir="ltr"
                  placeholder="npx -y @modelcontextprotocol/server-github  or  https://example.com/mcp"
                  value={target}
                  onChange={(e) => setTarget(e.target.value)}
                />
              </label>
            </div>
            <label className="field">
              <span>{remote ? t("Headers (one per line)") : t("Environment variables (one per line)")}</span>
              <textarea
                spellCheck={false}
                dir="ltr"
                rows={3}
                placeholder={remote ? "Authorization: Bearer …" : "GITHUB_PERSONAL_ACCESS_TOKEN=…"}
                value={vars}
                onChange={(e) => setVars(e.target.value)}
              />
            </label>
            {error && <div className="form-error">{error}</div>}
          </div>
          <div className="stg-inline-foot">
            <button type="button" className="btn ghost sm" onClick={closeForm}>
              {t("Cancel")}
            </button>
            <button type="submit" className="btn primary sm" disabled={busy || !name.trim() || !target.trim()}>
              {busy ? t("Connecting…") : t("Add and connect")}
            </button>
          </div>
        </form>
      )}

      <div className="set-list">
        {plugins === null && <div className="empty-row">{t("Loading…")}</div>}
        {plugins && !plugins.length && (
          <div className="empty-row">{t("None yet. MCP servers give Macron more tools: GitHub, databases, design files and more.")}</div>
        )}
        {(plugins || []).map((p) => (
          <div key={p.key} className="set-row static">
            <span className={"state" + (p.enabled && p.status === "ready" && !p.error ? " ok" : p.enabled && p.error ? " bad" : "")} />
            <span className="grow">
              <span className="name">{p.name}</span>
              <span className={"meta" + (p.error ? " bad-text" : "")} title={p.target} dir="ltr">
                {(p.kind === "mcp" ? "MCP" : "Plugin") + " · " + pluginStatus(p)}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={p.enabled}
              title={p.enabled ? t("Disable") : t("Enable")}
              className={"switch" + (p.enabled ? " on" : "")}
              onClick={() => void toggle(p)}
            >
              <span />
            </button>
            {p.removable && (
              <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void drop(p)}>
                <Icon name="trash" size={13} />
              </button>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}

export function SkillsSettingsPage() {
  const t = useT();
  const [skills, setSkills] = useState<SkillView[] | null>(null);
  const [q, setQ] = useState("");
  const [cat, setCat] = useState("");

  const load = () => {
    server
      .skills()
      .then((r) => setSkills(r.skills || []))
      .catch(() => setSkills([]));
  };

  useEffect(() => {
    load();
  }, []);

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const s of skills || []) if (s.category) set.add(s.category);
    return Array.from(set).sort();
  }, [skills]);

  const shown = useMemo(() => {
    const f = q.trim().toLowerCase();
    return (skills || []).filter((s) => {
      if (cat && s.category !== cat) return false;
      if (!f) return true;
      return `${s.name} ${s.category} ${s.description}`.toLowerCase().includes(f);
    });
  }, [skills, q, cat]);

  const toggle = async (sk: SkillView) => {
    setSkills((list) => (list || []).map((x) => (x.name === sk.name ? { ...x, enabled: !sk.enabled } : x)));
    try {
      const res = await server.enableSkill(sk.name, !sk.enabled);
      setSkills(res.skills || []);
    } catch (e) {
      fail(e);
      load();
    }
  };

  const remove = async (sk: SkillView) => {
    try {
      const res = await server.deleteSkill(sk.name);
      setSkills(res.skills || []);
    } catch (e) {
      fail(e);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>
          {t("Skills")} {skills ? <span className="stg-head-count" dir="ltr">{skills.filter((s) => s.enabled).length}/{skills.length}</span> : null}
        </h1>
        <p>{t("Manage reusable capabilities and domain instructions for Macron.")}</p>
      </header>

      <div className="set-search">
        <Icon name="search" size={13} />
        <input type="text" value={q} placeholder={t("Filter skills…")} onChange={(e) => setQ(e.target.value)} />
      </div>

      {categories.length > 1 && (
        <div className="stg-chip-row">
          <button type="button" className={"pv-qpill" + (!cat ? " on" : "")} onClick={() => setCat("")}>
            {t("All categories")}
          </button>
          {categories.map((c) => (
            <button key={c} type="button" className={"pv-qpill" + (cat === c ? " on" : "")} onClick={() => setCat(c)}>
              {c}
            </button>
          ))}
        </div>
      )}

      <div className="set-list">
        {skills === null && <div className="empty-row">{t("Loading…")}</div>}
        {skills && !shown.length && <div className="empty-row">{t("No skills loaded.")}</div>}
        {shown.map((sk) => (
          <div key={sk.name} className="set-row static">
            <span className={"state" + (sk.enabled ? " ok" : "")} />
            <span className="grow">
              <span className="name" dir="ltr">
                {sk.name} <span className="tag">{sk.category}</span>
              </span>
              <span className="meta" dir="auto">
                {sk.description}
              </span>
            </span>
            <button
              type="button"
              role="switch"
              aria-checked={sk.enabled}
              className={"switch" + (sk.enabled ? " on" : "")}
              onClick={() => void toggle(sk)}
            >
              <span />
            </button>
            <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void remove(sk)}>
              <Icon name="trash" size={13} />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}

export function ConnectionsSettingsPage() {
  const t = useT();
  const [list, setList] = useState<ConnectionDetail[] | null>(null);
  const [openId, setOpenId] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [testingId, setTestingId] = useState("");
  const [testResult, setTestResult] = useState<Record<string, { ok: boolean; message: string }>>({});

  useEffect(() => {
    server
      .connectionDetails()
      .then((r) => setList(r.connections || []))
      .catch(() => setList([]));
  }, []);

  useEffect(() => {
    if (!openId) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        setOpenId("");
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [openId]);

  const toggleOpen = (id: string) => {
    setOpenId((prev) => (prev === id ? "" : id));
    setValues({});
  };

  const runTest = async (id: string) => {
    setTestingId(id);
    try {
      const res = await server.testConnection(id);
      setTestResult((prev) => ({ ...prev, [id]: { ok: !!res.ok, message: res.message || "" } }));
    } catch (e) {
      setTestResult((prev) => ({ ...prev, [id]: { ok: false, message: e instanceof Error ? e.message : String(e) } }));
    } finally {
      setTestingId("");
    }
  };

  const save = async (c: ConnectionDetail) => {
    const patch: Record<string, string> = {};
    for (const f of c.fields) {
      if (Object.prototype.hasOwnProperty.call(values, f.key)) patch[f.key] = values[f.key];
    }
    if (!Object.keys(patch).length) {
      setOpenId("");
      return;
    }
    setSaving(true);
    try {
      const res = await server.saveConnections(patch);
      setList(res.connections || []);
      if (res.info) setState({ info: res.info });
      setTestResult((prev) => {
        const next = { ...prev };
        delete next[c.id];
        return next;
      });
      setOpenId("");
      setValues({});
    } catch (e) {
      fail(e);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Connected Services")}</h1>
        <p>{t("Configure credentials for Telegram, GitHub, and external integrations.")}</p>
      </header>

      <div className="set-list">
        {list === null && <div className="empty-row">{t("Loading…")}</div>}
        {(list || []).map((c) => {
          const isOpen = openId === c.id;
          const tr = testResult[c.id];
          const isTesting = testingId === c.id;
          return (
            <div key={c.id} className="conn-wrap">
              <div className="set-row static">
                <span className={"state" + (tr ? (tr.ok ? " ok" : " bad") : c.ready ? " ok" : "")} />
                <span className="grow">
                  <span className="name">{c.name}</span>
                  {c.capability && <span className="meta">{c.capability}</span>}
                  {tr && (
                    <span className={"meta " + (tr.ok ? "ok-text" : "bad-text")} dir="auto">
                      {tr.ok ? "✓ " : "✗ "}
                      {tr.message}
                    </span>
                  )}
                </span>
                <span className="meta">{c.ready ? t("Connected") : t("Not set up")}</span>
                <button type="button" className="btn ghost sm" disabled={isTesting} onClick={() => void runTest(c.id)}>
                  {isTesting ? t("Testing…") : t("Test")}
                </button>
                <button type="button" className="btn ghost sm" onClick={() => toggleOpen(c.id)}>
                  {isOpen ? t("Close") : t("Configure")}
                </button>
              </div>
              {isOpen && (
                <div className="conn-form">
                  {c.fields.map((f) => (
                    <label key={f.key} className="conn-field">
                      <span className="conn-key" dir="ltr">
                        {f.key} {f.set && <em className="set-ok">✓</em>}
                      </span>
                      <input
                        type={f.secret ? "password" : "text"}
                        dir="ltr"
                        placeholder={f.set ? "•••••••• (leave blank to keep, or enter new value)" : f.default || f.purpose}
                        value={values[f.key] ?? ""}
                        onChange={(e) => setValues((prev) => ({ ...prev, [f.key]: e.target.value }))}
                      />
                      {f.purpose && <span className="conn-hint">{f.purpose}</span>}
                    </label>
                  ))}
                  <div className="conn-actions">
                    <button type="button" className="btn ghost sm" onClick={() => setOpenId("")}>
                      {t("Cancel")}
                    </button>
                    <button type="button" className="btn ghost sm" disabled={isTesting} onClick={() => void runTest(c.id)}>
                      {isTesting ? t("Testing…") : t("Test")}
                    </button>
                    <button type="button" className="btn primary sm" disabled={saving} onClick={() => void save(c)}>
                      {saving ? t("Saving…") : t("Save")}
                    </button>
                  </div>
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

const addressRe = /^(?:([^@\s]+)@)?(\[[^\]]+\]|[^:@\s]+)(?::(\d{1,5}))?$/;

export function ServersSettingsPage() {
  const t = useT();
  const servers = useStore((s) => s.servers);
  const wsl = useStore((s) => s.wsl);
  const sshKeyDefault = useHomePath(".ssh/id_ed25519");

  const [adding, setAdding] = useState(false);
  const [kind, setKind] = useState<"ssh" | "wsl">("ssh");
  const [address, setAddress] = useState("");
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    void loadServers();
  }, []);

  useEffect(() => {
    if (!adding) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        closeForm();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [adding]);

  const closeForm = () => {
    setAdding(false);
    setKind("ssh");
    setAddress("");
    setName("");
    setKey("");
    setMore(false);
    setError("");
  };

  const submitAdd = async (andConnect: boolean) => {
    setError("");
    let host = address.trim();
    let user = "";
    let port = 0;
    if (kind === "ssh") {
      const m = addressRe.exec(host);
      if (!m) {
        setError(t("Write it like root@203.0.113.7 or me@example.com:2222"));
        return;
      }
      user = m[1] || "";
      host = m[2].replace(/^\[|\]$/g, "");
      port = m[3] ? Number(m[3]) : 0;
    }
    setBusy(true);
    try {
      const created = await server.addServer({ kind, name: name.trim(), host, user, port, key: key.trim() });
      await loadServers();
      closeForm();
      if (andConnect && created?.id) {
        void connectServer(created.id);
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("SSH & Remote")}</h1>
          <p>{t("Work seamlessly on remote Linux servers over SSH or inside WSL.")}</p>
        </div>
        {!adding && (
          <button type="button" className="btn primary sm" onClick={() => setAdding(true)}>
            <Icon name="plus" size={14} />
            <span>{t("Add a server…")}</span>
          </button>
        )}
      </header>

      {adding && (
        <form
          className="stg-inline-studio"
          onSubmit={(e) => {
            e.preventDefault();
            void submitAdd(false);
          }}
        >
          <div className="stg-inline-head">
            <h3>{t("Add a server")}</h3>
            <button type="button" className="icon-btn sm" title="Close (Esc)" aria-label={t("Close")} onClick={closeForm}>
              <Icon name="x" size={14} />
            </button>
          </div>
          <div className="stg-inline-body">
            <p className="stg-inline-sub">
              {t("Macron runs over an encrypted SSH tunnel or directly inside WSL — no port is exposed to the internet.")}
            </p>
            {wsl && (
              <div className="field">
                <span>{t("Connection type")}</span>
                <div className="choice" role="radiogroup">
                  {(
                    [
                      ["ssh", t("Server over SSH")],
                      ["wsl", t("WSL on this PC")],
                    ] as const
                  ).map(([val, lbl]) => (
                    <button key={val} type="button" role="radio" aria-checked={kind === val} className={kind === val ? "on" : ""} onClick={() => setKind(val)}>
                      {lbl}
                    </button>
                  ))}
                </div>
              </div>
            )}
            <div className="pv-grid2">
              {kind === "ssh" ? (
                <label className="field">
                  <span>{t("Address")}</span>
                  <input
                    autoFocus
                    spellCheck={false}
                    dir="ltr"
                    placeholder="root@203.0.113.7 or me@example.com:2222"
                    value={address}
                    onChange={(e) => setAddress(e.target.value)}
                  />
                </label>
              ) : (
                <label className="field">
                  <span>{t("Linux distribution")}</span>
                  <input autoFocus spellCheck={false} dir="ltr" placeholder="Ubuntu (or leave blank for default)" value={address} onChange={(e) => setAddress(e.target.value)} />
                </label>
              )}
              <label className="field">
                <span>{t("Name")}</span>
                <input spellCheck={false} dir="auto" placeholder={kind === "ssh" ? "e.g. Production VPS" : "WSL"} value={name} onChange={(e) => setName(e.target.value)} />
              </label>
            </div>
            {kind === "ssh" &&
              (more ? (
                <label className="field">
                  <span>{t("Private key file")}</span>
                  <input
                    spellCheck={false}
                    dir="ltr"
                    placeholder={`Default: SSH agent, then ${sshKeyDefault}, id_ecdsa, id_rsa`}
                    value={key}
                    onChange={(e) => setKey(e.target.value)}
                  />
                </label>
              ) : (
                <button type="button" className="link-btn" onClick={() => setMore(true)}>
                  {t("Use a specific key file")}
                </button>
              ))}
            {error && <div className="form-error">{error}</div>}
          </div>
          <div className="stg-inline-foot">
            <button type="button" className="btn ghost sm" onClick={closeForm}>
              {t("Cancel")}
            </button>
            <div className="left">
              <button type="submit" className="btn ghost sm" disabled={busy || (kind === "ssh" && !address.trim())}>
                {busy ? t("Saving…") : t("Save")}
              </button>
              <button
                type="button"
                className="btn primary sm"
                disabled={busy || (kind === "ssh" && !address.trim())}
                onClick={() => void submitAdd(true)}
              >
                {busy ? t("Connecting…") : t("Add and connect")}
              </button>
            </div>
          </div>
        </form>
      )}

      <div className="set-list">
        {!servers.length && <div className="empty-row">{t("No servers yet. Add one to let Macron work on a VPS or in WSL.")}</div>}
        {servers.map((s) => (
          <div key={s.id} className="set-row static">
            <span className={"state" + (s.state === "ready" ? " ok" : s.state === "failed" ? " bad" : s.state === "connecting" || s.state === "installing" ? " warn" : "")} />
            <span className="grow">
              <span className="name">{s.name}</span>
              <span className={"meta" + (s.state === "failed" ? " bad-text" : "")} dir="ltr">
                {s.state === "failed" && s.error
                  ? s.error
                  : s.state === "connecting"
                    ? t("Connecting…")
                    : s.kind === "wsl"
                      ? "WSL" + (s.host ? " · " + s.host : "")
                      : (s.user ? s.user + "@" : "") + s.host + (s.port && s.port !== 22 ? ":" + s.port : "")}
                {s.version ? " · Macron " + s.version : ""}
              </span>
            </span>
            {s.state === "ready" ? (
              <button type="button" className="btn ghost sm" onClick={() => void disconnectServer(s.id)}>
                {t("Disconnect")}
              </button>
            ) : (
              <button type="button" className="btn ghost sm" disabled={s.state === "connecting" || s.state === "installing"} onClick={() => void connectServer(s.id)}>
                {s.state === "connecting" ? t("Connecting…") : t("Connect")}
              </button>
            )}
            <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void removeServer(s.id)}>
              <Icon name="trash" size={13} />
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
