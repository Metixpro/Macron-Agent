import { useEffect, useMemo, useState } from "react";
import { connectServer, disconnectServer, fail, openDialog, removeServer } from "../../actions";
import { server } from "../../api";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import { setState, useStore } from "../../store";
import type { ConnectionDetail, Plugin, SkillView } from "../../types";

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

  const load = () =>
    server
      .plugins()
      .then((r) => setPlugins(r.plugins || []))
      .catch(() => setPlugins([]));

  useEffect(() => {
    load();
  }, []);

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
          <p>{t("MCP servers and plugins")}</p>
        </div>
        <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "mcp-add" })}>
          <Icon name="plus" size={14} />
          <span>{t("Add an MCP server…")}</span>
        </button>
      </header>

      <div className="set-list">
        {plugins === null && <div className="empty-row">{t("Loading…")}</div>}
        {plugins && !plugins.length && (
          <div className="empty-row">None yet. MCP servers give Macron more tools: GitHub, databases, design files and more.</div>
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
              title={p.enabled ? "Turn off" : "Turn on"}
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
        <p>{t("Filter skills…")}</p>
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
        <p>{t("Connections")}</p>
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

export function ServersSettingsPage() {
  const t = useT();
  const servers = useStore((s) => s.servers);

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("SSH & Remote")}</h1>
          <p>{t("Servers")}</p>
        </div>
        <button type="button" className="btn primary sm" onClick={() => openDialog({ kind: "server-add" })}>
          <Icon name="plus" size={14} />
          <span>{t("Add a server…")}</span>
        </button>
      </header>

      <div className="set-list">
        {!servers.length && <div className="empty-row">No servers yet. Add one to let Macron work on a VPS or in WSL.</div>}
        {servers.map((s) => (
          <div key={s.id} className="set-row static">
            <span className={"state" + (s.state === "ready" ? " ok" : s.state === "failed" ? " bad" : "")} />
            <span className="grow">
              <span className="name">{s.name}</span>
              <span className="meta" dir="ltr">
                {s.state === "failed" && s.error ? s.error : s.kind === "wsl" ? "WSL" + (s.host ? " · " + s.host : "") : (s.user ? s.user + "@" : "") + s.host}
                {s.version ? " · Macron " + s.version : ""}
              </span>
            </span>
            {s.state === "ready" ? (
              <button type="button" className="btn ghost sm" onClick={() => void disconnectServer(s.id)}>
                {t("Disconnect")}
              </button>
            ) : (
              <button type="button" className="btn ghost sm" disabled={s.state === "connecting" || s.state === "installing"} onClick={() => void connectServer(s.id)}>
                {t("Connect")}
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
