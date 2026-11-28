import { useEffect, useState } from "react";
import { errorText, fail } from "../../actions";
import { server } from "../../api";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import type { BrowserProfileView, BrowserProxyConfig, ExtensionStatus } from "../../types";

const PROFILE_COLORS = ["#60a5fa", "#a78bfa", "#34d399", "#f59e0b", "#f472b6", "#22d3ee"];

export function BrowserSettingsPage() {
  const t = useT();
  const [profiles, setProfiles] = useState<BrowserProfileView[] | null>(null);
  const [ext, setExt] = useState<ExtensionStatus | null>(null);
  const [copied, setCopied] = useState<string>("");

  // Inline profile editor state
  const [editing, setEditing] = useState<BrowserProfileView | "new" | null>(null);
  const [name, setName] = useState("");
  const [color, setColor] = useState(PROFILE_COLORS[0]);
  const [useProxy, setUseProxy] = useState(false);
  const [scheme, setScheme] = useState<"http" | "https" | "socks5">("socks5");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("1080");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [bypass, setBypass] = useState("localhost,127.0.0.1");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = () => {
    server
      .browserProfiles()
      .then((r) => setProfiles(r.profiles || []))
      .catch(() => setProfiles([]));
    server
      .extensionStatus()
      .then((r) => setExt(r))
      .catch(() => setExt(null));
  };

  useEffect(() => {
    load();
  }, []);

  const openEditor = (p: BrowserProfileView | "new") => {
    setError("");
    setEditing(p);
    if (p === "new") {
      setName("");
      setColor(PROFILE_COLORS[(profiles?.length || 0) % PROFILE_COLORS.length]);
      setUseProxy(false);
      setScheme("socks5");
      setHost("");
      setPort("1080");
      setUsername("");
      setPassword("");
      setBypass("localhost,127.0.0.1");
    } else {
      setName(p.name);
      setColor(p.color || PROFILE_COLORS[0]);
      setUseProxy(Boolean(p.hasProxy && p.proxy));
      setScheme(p.proxy?.scheme || "socks5");
      setHost(p.proxy?.host || "");
      setPort(p.proxy?.port ? String(p.proxy.port) : "1080");
      setUsername(p.proxy?.username || "");
      setPassword("");
      setBypass(p.proxy?.bypass || "localhost,127.0.0.1");
    }
  };

  const closeEditor = () => {
    setEditing(null);
    setError("");
  };

  const saveProfile = async () => {
    setError("");
    let proxy: BrowserProxyConfig | null = null;
    if (useProxy) {
      const pNum = parseInt(port, 10);
      if (!host.trim()) {
        setError(t("Proxy host is required."));
        return;
      }
      if (!Number.isFinite(pNum) || pNum < 1 || pNum > 65535) {
        setError(t("Proxy port must be between 1 and 65535."));
        return;
      }
      proxy = {
        scheme,
        host: host.trim(),
        port: pNum,
        username: username.trim() || undefined,
        password: password || undefined,
        bypass: bypass.trim() || undefined,
      };
    }
    setBusy(true);
    try {
      const res = await server.saveBrowserProfile({
        id: editing && editing !== "new" ? editing.id : undefined,
        name: name.trim(),
        color,
        proxy,
        keepExistingPassword: editing !== "new" && !password,
      });
      setProfiles(res.profiles || []);
      closeEditor();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const removeProfile = async (id: string) => {
    try {
      const res = await server.deleteBrowserProfile(id);
      setProfiles(res.profiles || []);
    } catch (e) {
      fail(e);
    }
  };

  const copyText = (key: string, value: string) => {
    navigator.clipboard?.writeText(value).catch(() => undefined);
    setCopied(key);
    setTimeout(() => setCopied((cur) => (cur === key ? "" : cur)), 1800);
  };

  const rotateToken = async () => {
    try {
      const next = await server.extensionRotateToken();
      setExt(next);
    } catch (e) {
      fail(e);
    }
  };

  return (
    <div className="stg-page">
      <header className="stg-head stg-head-row">
        <div>
          <h1>{t("Browser & Profiles")}</h1>
          <p>{t("Manage isolated browser profiles, per-profile proxies, and the Chrome Extension bridge.")}</p>
        </div>
        {!editing && (
          <button type="button" className="btn primary sm" onClick={() => openEditor("new")}>
            <Icon name="plus" size={14} />
            <span>{t("New profile")}</span>
          </button>
        )}
      </header>

      {editing && (
        <form
          className="stg-inline-studio"
          onSubmit={(e) => {
            e.preventDefault();
            void saveProfile();
          }}
        >
          <div className="stg-inline-head">
            <h3>{editing === "new" ? t("New browser profile") : `${t("Edit profile")}: ${editing.name}`}</h3>
            <button type="button" className="icon-btn sm" onClick={closeEditor} aria-label={t("Close")}>
              <Icon name="x" size={14} />
            </button>
          </div>
          <div className="stg-inline-body">
            <div className="pv-grid2">
              <label className="field">
                <span>{t("Profile name")}</span>
                <input
                  autoFocus
                  disabled={editing !== "new" && Boolean(editing.builtIn)}
                  placeholder="e.g. Work, Personal, Research"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </label>
              <div className="field">
                <span>{t("Accent color")}</span>
                <div className="br-color-row">
                  {PROFILE_COLORS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      className={"br-color-dot" + (color === c ? " on" : "")}
                      style={{ background: c }}
                      aria-label={c}
                      onClick={() => setColor(c)}
                    />
                  ))}
                </div>
              </div>
            </div>

            <div className="set-row static" style={{ marginTop: 8 }}>
              <span className="grow">
                <span className="name">{t("Use dedicated proxy")}</span>
                <span className="meta">{t("Routes this profile through HTTP/SOCKS5 and disables non-proxied WebRTC UDP.")}</span>
              </span>
              <button
                type="button"
                role="switch"
                aria-checked={useProxy}
                className={"switch" + (useProxy ? " on" : "")}
                onClick={() => setUseProxy((v) => !v)}
              >
                <span />
              </button>
            </div>

            {useProxy && (
              <div className="br-proxy-grid">
                <label className="field">
                  <span>{t("Scheme")}</span>
                  <select value={scheme} onChange={(e) => setScheme(e.target.value as "http" | "https" | "socks5")}>
                    <option value="socks5">SOCKS5</option>
                    <option value="http">HTTP</option>
                    <option value="https">HTTPS</option>
                  </select>
                </label>
                <label className="field">
                  <span>{t("Host")}</span>
                  <input dir="ltr" spellCheck={false} placeholder="127.0.0.1 or proxy.example.com" value={host} onChange={(e) => setHost(e.target.value)} />
                </label>
                <label className="field">
                  <span>{t("Port")}</span>
                  <input dir="ltr" spellCheck={false} placeholder="1080" value={port} onChange={(e) => setPort(e.target.value)} />
                </label>
                <label className="field">
                  <span>{t("Username (optional)")}</span>
                  <input dir="ltr" spellCheck={false} value={username} onChange={(e) => setUsername(e.target.value)} />
                </label>
                <label className="field">
                  <span>{t("Password (optional)")}</span>
                  <input
                    type="password"
                    dir="ltr"
                    placeholder={editing !== "new" && editing.proxyPasswordSet ? "•••• (unchanged)" : ""}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </label>
                <label className="field">
                  <span>{t("Bypass hosts")}</span>
                  <input dir="ltr" spellCheck={false} value={bypass} onChange={(e) => setBypass(e.target.value)} />
                </label>
              </div>
            )}

            {error && <div className="form-error">{error}</div>}
          </div>
          <div className="stg-inline-foot">
            <button type="button" className="btn ghost sm" onClick={closeEditor}>
              {t("Cancel")}
            </button>
            <button type="submit" className="btn primary sm" disabled={busy || !name.trim()}>
              {busy ? t("Saving…") : t("Save profile")}
            </button>
          </div>
        </form>
      )}

      <section className="stg-sec">
        <div className="stg-sec-title">{t("Browser profiles")}</div>
        <div className="set-list">
          {profiles === null && <div className="empty-row">{t("Loading…")}</div>}
          {(profiles || []).map((p) => (
            <div key={p.id} className="set-row static">
              <span className="br-prof-swatch" style={{ background: p.color }} aria-hidden="true" />
              <span className="grow">
                <span className="name">
                  {p.name}
                  {p.builtIn && <span className="ps-badge" style={{ marginInlineStart: 6 }}>{p.scope}</span>}
                </span>
                <span className="meta" dir="ltr">
                  {p.hasProxy && p.proxy
                    ? `${p.proxy.scheme}://${p.proxy.host}:${p.proxy.port}${p.proxy.username ? " (auth)" : ""}`
                    : t("Direct connection · Isolated cookies & storage")}
                </span>
              </span>
              <button type="button" className="btn ghost sm" onClick={() => openEditor(p)}>
                {t("Configure")}
              </button>
              {!p.builtIn && (
                <button type="button" className="icon-btn sm" title={t("Remove")} onClick={() => void removeProfile(p.id)}>
                  <Icon name="trash" size={13} />
                </button>
              )}
            </div>
          ))}
        </div>
      </section>

      <section className="stg-sec" style={{ marginTop: 22 }}>
        <div className="stg-sec-title">{t("Chrome Extension Bridge (Manifest V3)")}</div>
        <div className="stg-card">
          <div className="set-row static" style={{ borderBottom: "1px solid var(--border)", paddingBottom: 10, marginBottom: 10 }}>
            <span className={"state" + (ext?.connected ? " ok" : "")} />
            <span className="grow">
              <span className="name">
                {ext?.connected
                  ? `${t("Connected to")} ${ext.browserName || "Chrome"} (v${ext.extensionVersion || "2.1.0"})`
                  : t("Not connected")}
              </span>
              <span className="meta">
                {ext?.connected
                  ? `${(ext.tabs || []).length} ${t("tabs in Macron group")} · ${(ext.approvedOrigins || []).length} ${t("approved sites")}`
                  : t("Load apps/browser-extension in chrome://extensions and paste the endpoint & pairing token below.")}
              </span>
            </span>
            <button type="button" className="btn ghost sm" onClick={() => void rotateToken()}>
              {t("Rotate token")}
            </button>
          </div>

          {ext && (
            <div className="pv-grid2">
              <div className="field">
                <span>{t("WebSocket Endpoint")}</span>
                <div className="br-copy-row">
                  <input readOnly dir="ltr" value={ext.endpoint} />
                  <button type="button" className="btn ghost sm" onClick={() => copyText("ep", ext.endpoint)}>
                    {copied === "ep" ? t("Copied") : t("Copy")}
                  </button>
                </div>
              </div>
              <div className="field">
                <span>{t("Pairing Token")}</span>
                <div className="br-copy-row">
                  <input readOnly dir="ltr" type="password" value={ext.pairingToken} />
                  <button type="button" className="btn ghost sm" onClick={() => copyText("tok", ext.pairingToken)}>
                    {copied === "tok" ? t("Copied") : t("Copy")}
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
