import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import {
  addFolder,
  addServer,
  answer,
  authServer,
  installOnServer,
  effortAbout,
  effortLevels,
  effortName,
  exportConversation,
  setTuning,
  chooseModel,
  closeDialog,
  compact,
  errorText,
  fail,
  initProject,
  newChat,
  openDialog,
  openProject,
  checkUpdate,
  setLang,
  setTheme,
  stop,
  toast,
  toggleChanges,
  toggleMode,
} from "../actions";
import { server } from "../api";
import { shortPath } from "../format";
import { useMod, useProjectName } from "../hooks";
import { Icon } from "../icons";
import type { IconName } from "../icons";
import { getState, setState, useStore } from "../store";
import type { Theme } from "../store";
import type { Ask, Listing, Plugin } from "../types";
import { ProviderEditor } from "./ProviderManager";
import { SessionSearch } from "./SessionSearch";
import { StatusDialog } from "./SettingsExtras";
import { SettingsWorkspace } from "./settings/SettingsWorkspace";
import { StarBuddy } from "./StarBuddy";

function Modal({ kind, dismiss, children }: { kind: string; dismiss: boolean; children: ReactNode }) {
  return (
    <div
      className={"overlay " + kind}
      onMouseDown={(e) => {
        if (dismiss && e.target === e.currentTarget) closeDialog();
      }}
    >
      <div className={"dialog " + kind} role="dialog" aria-modal="true">
        {children}
      </div>
    </div>
  );
}

export function Dialogs() {
  const d = useStore((s) => s.dialog);
  const settingsBg = useStore((s) => s.settingsBg);
  if (!d) return null;
  if (d.kind === "settings") {
    return <SettingsWorkspace initialSection={d.section || "general"} />;
  }

  let modal: ReactNode = null;
  switch (d.kind) {
    case "ask":
      modal = (
        <Modal kind="ask" dismiss={false}>
          <AskDialog ask={d.ask} />
        </Modal>
      );
      break;
    case "palette":
      modal = (
        <Modal kind="palette" dismiss>
          <Palette key={d.mode} mode={d.mode} />
        </Modal>
      );
      break;
    case "search":
      modal = (
        <Modal kind="palette" dismiss>
          <SessionSearch />
        </Modal>
      );
      break;
    case "folder":
      modal = (
        <Modal kind="folder" dismiss>
          <FolderPicker />
        </Modal>
      );
      break;
    case "status":
      modal = (
        <Modal kind="settings" dismiss>
          <StatusDialog onClose={closeDialog} />
        </Modal>
      );
      break;
    case "provider-edit":
      modal = (
        <Modal kind="settings provider-modal" dismiss>
          <ProviderEditor key={d.provider ? String(d.provider.id) : "new"} provider={d.provider} />
        </Modal>
      );
      break;
    case "mcp-add":
      modal = (
        <Modal kind="form" dismiss>
          <McpAdd />
        </Modal>
      );
      break;
    case "model-connect":
      modal = (
        <Modal kind="form" dismiss>
          <ModelConnect />
        </Modal>
      );
      break;
    case "server-add":
      modal = (
        <Modal kind="form" dismiss>
          <ServerAdd />
        </Modal>
      );
      break;
    case "server-auth":
      modal = (
        <Modal kind="form" dismiss>
          <ServerAuth key={d.id + d.need} id={d.id} need={d.need} fingerprint={d.fingerprint} install={d.install} />
        </Modal>
      );
      break;
    case "server-install":
      modal = (
        <Modal kind="form" dismiss={false}>
          <ServerInstall key={d.id + (d.secrets ? "+" : "")} id={d.id} update={d.update} secrets={d.secrets} />
        </Modal>
      );
      break;
  }

  if (settingsBg) {
    return (
      <>
        <SettingsWorkspace initialSection={settingsBg} />
        {modal}
      </>
    );
  }
  return modal;
}

function ServerInstall({ id, update, secrets }: { id: string; update: boolean; secrets?: { password?: string; passphrase?: string; trust?: string } }) {
  const srv = useStore((s) => s.servers.find((x) => x.id === id));
  const name = srv?.name || "the server";
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [started, setStarted] = useState(0);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!busy) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [busy]);
  useEffect(() => {
    if (secrets) run();
  }, []);
  const run = async () => {
    setBusy(true);
    setError("");
    setStarted(Date.now());
    try {
      await installOnServer(id, secrets);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const secs = Math.max(0, Math.round((now - started) / 1000));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!busy) run();
      }}
    >
      <h3>{update ? `Macron on ${name} needs an update` : `Macron is not on ${name} yet`}</h3>
      <div className="sub">
        {update
          ? "The desktop app talks to a newer Macron than the one on that server. Macron can update it for you over this connection."
          : "The desktop app runs your work through Macron on the server itself. Macron can install it there for you over this connection."}
      </div>
      <div className="hint">
        It runs the official installer on the server with no questions: the program, its tools, nothing else. The Telegram bot stays off. This usually takes two to five
        minutes.
      </div>
      {busy && (
        <div className="install-progress">
          <span className="spin" />
          {update ? "Updating" : "Installing"} on {name} · {secs < 60 ? secs + "s" : Math.floor(secs / 60) + "m " + String(secs % 60).padStart(2, "0") + "s"}
        </div>
      )}
      {error && <div className="form-error">{error}</div>}
      <div className="bar">
        <span />
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog} disabled={busy}>
            {error ? "Close" : "Not now"}
          </button>
          <button type="submit" className="btn primary" disabled={busy}>
            {busy ? "Working…" : error ? "Try again" : update ? "Update it there" : "Install it there"}
          </button>
        </div>
      </div>
    </form>
  );
}

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

export function pluginStatus(p: Plugin): string {
  if (!p.enabled) return "Off";
  if (p.error) return p.error;
  if (p.status === "connecting") return "Connecting…";
  if (p.status === "ready") return p.tools.length === 1 ? "1 tool" : `${p.tools.length} tools`;
  return p.status;
}

function McpAdd() {
  const [name, setName] = useState("");
  const [target, setTarget] = useState("");
  const [vars, setVars] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const remote = /^https?:\/\//i.test(target.trim());

  const submit = async () => {
    setError("");
    const env: Record<string, string> = {};
    for (const line of vars.split("\n")) {
      const t = line.trim();
      if (!t) continue;
      const i = remote && t.includes(":") && !t.includes("=") ? t.indexOf(":") : t.indexOf("=");
      if (i <= 0) {
        setError(`“${t}” is not KEY=value`);
        return;
      }
      env[t.slice(0, i).trim()] = t.slice(i + 1).trim();
    }
    const parts = remote ? [target.trim()] : splitArgs(target.trim());
    setBusy(true);
    try {
      const p = await server.addPlugin({ name: name.trim(), target: parts[0], args: parts.slice(1), env });
      if (p.error || (p.enabled && p.status !== "ready")) {
        setError(`${p.name} was added, but it is not ready: ${pluginStatus(p)}`);
        return;
      }
      setState({ dialog: null });
      toast(`${p.name}: ${pluginStatus(p)}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <h3>Add an MCP server</h3>
      <div className="sub">Its tools reach Macron here, in the terminal and in Telegram. Local servers run on this machine; remote ones are reached by their address.</div>
      <label className="field">
        <span>Name</span>
        <input autoFocus spellCheck={false} placeholder="e.g. github" value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label className="field">
        <span>Command or address</span>
        <input spellCheck={false} dir="ltr" placeholder="npx -y @modelcontextprotocol/server-github  or  https://example.com/mcp" value={target} onChange={(e) => setTarget(e.target.value)} />
      </label>
      <label className="field">
        <span>{remote ? "Headers (one per line)" : "Environment variables (one per line)"}</span>
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
      <div className="bar">
        <span />
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            {error && !busy ? "Close" : "Cancel"}
          </button>
          <button type="submit" className="btn primary" disabled={busy || !name.trim() || !target.trim()}>
            {busy ? "Connecting…" : "Add and connect"}
          </button>
        </div>
      </div>
    </form>
  );
}

const PRESETS = [
  { id: "openai", label: "OpenAI", url: "https://api.openai.com/v1", key: true },
  { id: "anthropic", label: "Anthropic", url: "https://api.anthropic.com/v1/messages", key: true },
  { id: "openrouter", label: "OpenRouter", url: "https://openrouter.ai/api/v1", key: true },
  { id: "gemini", label: "Gemini", url: "https://generativelanguage.googleapis.com/v1beta/openai", key: true },
  { id: "deepseek", label: "DeepSeek", url: "https://api.deepseek.com/v1", key: true },
  { id: "ollama", label: "Ollama", url: "http://localhost:11434/v1", key: false },
  { id: "custom", label: "Other", url: "", key: true },
] as const;

function ModelConnect() {
  const [preset, setPreset] = useState<(typeof PRESETS)[number]["id"]>("openai");
  const p = PRESETS.find((x) => x.id === preset) || PRESETS[0];
  const [url, setUrl] = useState<string>(p.url);
  const [key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const pick = (id: (typeof PRESETS)[number]["id"]) => {
    const next = PRESETS.find((x) => x.id === id) || PRESETS[0];
    setPreset(id);
    setUrl(next.url);
    setModels(null);
    setError("");
  };

  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const r = await server.listModels(url, key);
      setModels((r.models || []).slice().sort());
      if (!(r.models || []).length) setError("The provider did not list any models; type the model's name.");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await server.connectModel({ name: preset === "custom" ? "custom" : preset, url: url.trim(), key: key.trim(), model: model.trim() });
      setState({ info: r.info, dialog: null });
      toast("Connected: " + model.trim());
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const shown = (models || []).filter((m) => m.toLowerCase().includes(model.trim().toLowerCase())).slice(0, 200);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <h3>Connect a model</h3>
      <div className="sub">Macron thinks with a language model from a provider you choose. Your key stays on this machine.</div>
      <div className="presets">
        {PRESETS.map((x) => (
          <button key={x.id} type="button" className={"preset" + (x.id === preset ? " on" : "")} onClick={() => pick(x.id)}>
            {x.label}
          </button>
        ))}
      </div>
      <label className="field">
        <span>Address</span>
        <input spellCheck={false} dir="ltr" placeholder="https://…/v1" value={url} onChange={(e) => setUrl(e.target.value)} />
      </label>
      {p.key && (
        <label className="field">
          <span>API key</span>
          <input autoFocus type="password" spellCheck={false} dir="ltr" value={key} onChange={(e) => setKey(e.target.value)} />
        </label>
      )}
      <label className="field">
        <span className="field-head">
          Model
          <button type="button" className="link-btn inline" disabled={loading || !url.trim()} onClick={load}>
            {loading ? "Loading…" : models ? "Reload the list" : "Show the provider's models"}
          </button>
        </span>
        <input spellCheck={false} dir="ltr" placeholder="The model's exact name" value={model} onChange={(e) => setModel(e.target.value)} />
      </label>
      {models && shown.length > 0 && (
        <div className="model-list">
          {shown.map((m) => (
            <button key={m} type="button" className={"entry" + (m === model ? " sel" : "")} onClick={() => setModel(m)}>
              <span className="label">{m}</span>
              {m === model && (
                <span className="end">
                  <Icon name="check" size={14} />
                </span>
              )}
            </button>
          ))}
        </div>
      )}
      {error && <div className="form-error">{error}</div>}
      <div className="bar">
        <span />
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || !url.trim() || !model.trim()}>
            {busy ? "Testing…" : "Test and connect"}
          </button>
        </div>
      </div>
    </form>
  );
}

const addressRe = /^(?:([^@\s]+)@)?(\[[^\]]+\]|[^:@\s]+)(?::(\d{1,5}))?$/;

function ServerAdd() {
  const wsl = useStore((s) => s.wsl);
  const [kind, setKind] = useState<"ssh" | "wsl">("ssh");
  const [address, setAddress] = useState("");
  const [name, setName] = useState("");
  const [key, setKey] = useState("");
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setError("");
    let host = address.trim();
    let user = "";
    let port = 0;
    if (kind === "ssh") {
      const m = addressRe.exec(host);
      if (!m) {
        setError("Write it like root@203.0.113.7 or me@example.com:2222");
        return;
      }
      user = m[1] || "";
      host = m[2].replace(/^\[|\]$/g, "");
      port = m[3] ? Number(m[3]) : 0;
    }
    setBusy(true);
    try {
      await addServer({ kind, name: name.trim(), host, user, port, key: key.trim() });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <h3>Add a server</h3>
      <div className="sub">
        Macron has to be installed there. The app starts it over SSH and talks to it through the encrypted connection; no port is opened to the internet.
      </div>
      {wsl && (
        <div className="field">
          <Choice
            value={kind}
            options={[
              ["ssh", "Server over SSH"],
              ["wsl", "WSL on this PC"],
            ]}
            onPick={setKind}
          />
        </div>
      )}
      {kind === "ssh" ? (
        <label className="field">
          <span>Address</span>
          <input autoFocus spellCheck={false} dir="ltr" placeholder="root@203.0.113.7 or me@example.com:2222" value={address} onChange={(e) => setAddress(e.target.value)} />
        </label>
      ) : (
        <label className="field">
          <span>Linux distribution</span>
          <input autoFocus spellCheck={false} dir="ltr" placeholder="The default one" value={address} onChange={(e) => setAddress(e.target.value)} />
        </label>
      )}
      <label className="field">
        <span>Name</span>
        <input spellCheck={false} dir="auto" placeholder={kind === "ssh" ? "e.g. Production VPS" : "WSL"} value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      {kind === "ssh" &&
        (more ? (
          <label className="field">
            <span>Private key file</span>
            <input spellCheck={false} dir="ltr" placeholder="Default: your SSH agent, then ~/.ssh/id_ed25519, id_ecdsa, id_rsa" value={key} onChange={(e) => setKey(e.target.value)} />
          </label>
        ) : (
          <button type="button" className="link-btn" onClick={() => setMore(true)}>
            Use a specific key file
          </button>
        ))}
      {error && <div className="form-error">{error}</div>}
      <div className="bar">
        <span />
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={busy || (kind === "ssh" && !address.trim())}>
            {busy ? "Connecting…" : "Add and connect"}
          </button>
        </div>
      </div>
    </form>
  );
}

function ServerAuth({ id, need, fingerprint, install }: { id: string; need: "trust" | "password" | "passphrase"; fingerprint?: string; install?: boolean }) {
  const srv = useStore((s) => s.servers.find((x) => x.id === id));
  const [value, setValue] = useState("");
  const where = srv ? (srv.user ? srv.user + "@" : "") + (srv.host || "") : "the server";
  const submit = () => {
    if (need === "trust") return authServer(id, { trust: fingerprint }, install);
    if (!value) return;
    return authServer(id, need === "password" ? { password: value } : { passphrase: value }, install);
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      {need === "trust" ? (
        <>
          <h3>Is this the right server?</h3>
          <div className="sub">
            This is the first connection to <b dir="ltr">{srv?.host}</b>. Its key fingerprint is:
          </div>
          <pre className="command">{fingerprint}</pre>
          <div className="hint">
            Compare it with what <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> prints on the server, or with your hosting panel. Macron remembers it in
            ~/.ssh/known_hosts and warns you if it ever changes.
          </div>
        </>
      ) : (
        <>
          <h3>{need === "password" ? "Password for " + where : "Unlock your SSH key"}</h3>
          <div className="sub">
            {need === "password"
              ? "Used for this session only and never saved. An SSH key spares you this step."
              : "Your private key is protected with a passphrase. It stays in memory for this session only."}
          </div>
          <label className="field">
            <span>{need === "password" ? "Password" : "Passphrase"}</span>
            <input autoFocus type="password" value={value} onChange={(e) => setValue(e.target.value)} />
          </label>
        </>
      )}
      <div className="bar">
        <span />
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            Cancel
          </button>
          <button type="submit" className="btn primary" disabled={need !== "trust" && !value}>
            {need === "trust" ? "Trust and connect" : "Connect"}
          </button>
        </div>
      </div>
    </form>
  );
}

function AskDialog({ ask }: { ask: Ask }) {
  const name = useProjectName();
  const opened = useRef(Date.now());
  useEffect(() => {
    opened.current = Date.now();
    (document.activeElement as HTMLElement | null)?.blur();
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        answer("deny");
      } else if (e.key === "Enter" && !(document.activeElement instanceof HTMLButtonElement)) {
        e.preventDefault();
        if (Date.now() - opened.current > 700) answer("once");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ask.id]);
  return (
    <>
      <div className="ask-head">
        <StarBuddy variant="stage" size="md" mood="asking" interactive />
        <div>
          <h3>{ask.title || "Allow this?"}</h3>
          <div className="sub">Macron wants to run this in {name}.</div>
        </div>
      </div>
      <pre className="command">{ask.command}</pre>
      {(ask.findings || []).map((f) => (
        <div key={f} className="finding">
          {f}
        </div>
      ))}
      {ask.guardian && <div className="guardian">{ask.guardian}</div>}
      <div className="bar">
        <div className="left">
          <button type="button" className="btn primary" onClick={() => answer("once")}>
            Allow once
          </button>
          <button type="button" className="btn" onClick={() => answer("session")}>
            Allow this session
          </button>
          <button type="button" className="btn" onClick={() => answer("always")}>
            Always allow
          </button>
        </div>
        <button type="button" className="btn danger" onClick={() => answer("deny")}>
          Deny
        </button>
      </div>
      <div className="foot-keys">
        <kbd>Enter</kbd> allow once <kbd>Esc</kbd> deny
      </div>
    </>
  );
}

type Entry = { id: string; group: string; label: string; hint?: string; icon: IconName; on?: boolean; keys?: string[]; run: () => void };

function useList(count: number) {
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (sel >= count) setSel(Math.max(0, count - 1));
  }, [count, sel]);
  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(".sel");
    el?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const move = (e: KeyboardEvent) => {
    if (!count) return false;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setSel((i) => (i + (e.key === "ArrowDown" ? 1 : count - 1)) % count);
      return true;
    }
    return false;
  };
  return { sel, setSel, listRef, move };
}

let modelCache: { key: string; models: string[] } | null = null;

function Palette({ mode }: { mode: "root" | "models" }) {
  const projects = useStore((s) => s.projects);
  const current = useStore((s) => s.current);
  const defaultId = useStore((s) => s.defaultId);
  const home = useStore((s) => s.home);
  const appMode = useStore((s) => s.mode);
  const theme = useStore((s) => s.theme);
  const running = useStore((s) => s.live.running);
  const info = useStore((s) => s.info);
  const tuning = useStore((s) => s.tuning);
  const mod = useMod();
  const [q, setQ] = useState("");
  const cacheKey = info.provider + "|" + info.model;
  const [models, setModels] = useState<string[] | null>(() => (modelCache && modelCache.key === cacheKey ? modelCache.models : null));
  const [active, setActive] = useState(info.model);
  const [error, setError] = useState("");

  useEffect(() => {
    if (mode !== "models") return;
    let live = true;
    server
      .models()
      .then((r) => {
        const list = r.models || [];
        modelCache = { key: cacheKey, models: list };
        if (!live) return;
        setModels(list);
        if (r.current) setActive(r.current);
      })
      .catch((e) => {
        if (!live) return;
        setModels((m) => m || []);
        setError(errorText(e));
      });
    return () => {
      live = false;
    };
  }, [mode, cacheKey]);

  const entries = useMemo<Entry[]>(() => {
    if (mode === "models") {
      const all = models || [];
      const ordered = all.includes(active) ? [active, ...all.filter((m) => m !== active)] : all;
      const list = ordered.map<Entry>((m) => ({ id: "m:" + m, group: "Models", label: m, icon: "chip", on: m === active, run: () => chooseModel(m) }));
      const typed = q.trim();
      if (typed && !(models || []).includes(typed)) list.push({ id: "custom", group: "Models", label: `Use “${typed}”`, icon: "chip", run: () => chooseModel(typed) });
      return list;
    }
    const themes: [Theme, string, IconName][] = [
      ["system", "Match the system", "monitor"],
      ["dark", "Dark", "moon"],
      ["light", "Light", "sun"],
    ];
    const list: Entry[] = [
      { id: "new", group: "Actions", label: "New conversation", icon: "edit", keys: [mod, "N"], run: () => newChat() },
      { id: "search", group: "Actions", label: "Search conversations…", hint: "Across all workspaces & threads", icon: "search", keys: [mod, "Shift", "F"], run: () => openDialog({ kind: "search" }) },
      appMode === "plan"
        ? { id: "mode", group: "Actions", label: "Switch to Build mode", hint: "Let Macron make changes", icon: "code", keys: [mod, "Shift", "P"], run: () => toggleMode() }
        : { id: "mode", group: "Actions", label: "Switch to Plan mode", hint: "Plan first, change nothing", icon: "map", keys: [mod, "Shift", "P"], run: () => toggleMode() },
      info.model
        ? { id: "model", group: "Actions", label: "Change model…", hint: info.model, icon: "chip", run: () => openDialog({ kind: "palette", mode: "models" }) }
        : { id: "model", group: "Actions", label: "Connect a model…", icon: "chip", run: () => openDialog({ kind: "model-connect" }) },
      { id: "connect", group: "Actions", label: "Add or customize provider…", icon: "plus", run: () => openDialog({ kind: "provider-edit" }) },
      { id: "folder", group: "Actions", label: "Open folder…", icon: "folder", keys: [mod, "O"], run: () => openDialog({ kind: "folder" }) },
      { id: "changes", group: "Actions", label: "Show git changes & diff", icon: "gitBranch", run: () => toggleChanges(true) },
      { id: "status", group: "Actions", label: "System & session status", hint: "Model, sandbox, SpectorDev", icon: "info", run: () => openDialog({ kind: "status" }) },
      { id: "export", group: "Actions", label: "Export conversation as Markdown", icon: "download", run: () => exportConversation() },
      { id: "init", group: "Actions", label: "Initialize MACRON.md", hint: "Generate project instructions", icon: "file", run: () => void initProject() },
      { id: "mcp", group: "Actions", label: "Add an MCP server…", icon: "plug", run: () => openDialog({ kind: "mcp-add" }) },
      { id: "compact", group: "Actions", label: "Compact the conversation", hint: "Summarise to free room", icon: "layers", run: () => compact() },
      { id: "update", group: "Actions", label: "Check for updates (GitHub)", icon: "download", run: () => void checkUpdate(true) },
      { id: "lang", group: "Actions", label: getState().lang === "fa" ? "Switch language to English" : "تغییر زبان به فارسی (Persian)", icon: "globe", run: () => setLang(getState().lang === "fa" ? "en" : "fa") },
      { id: "settings", group: "Actions", label: "Settings", icon: "sliders", keys: [mod, ","], run: () => openDialog({ kind: "settings" }) },
    ];
    if (running) list.unshift({ id: "stop", group: "Actions", label: "Stop the current job", icon: "square", keys: ["Esc"], run: () => stop() });
    for (const [t, label, icon] of themes) list.push({ id: "t:" + t, group: "Theme", label, icon, on: theme === t, run: () => setTheme(t) });
    if (tuning && tuning.model) {
      for (const l of effortLevels) {
        list.push({ id: "e:" + (l || "auto"), group: "Effort", label: effortName(l), hint: effortAbout[l], icon: "gauge", on: tuning.effort === l, run: () => setTuning("effort", l || "auto", "Effort: " + effortName(l)) });
      }
    }
    for (const p of projects) {
      list.push({
        id: "p:" + p.id,
        group: "Workspaces",
        label: p.id === defaultId ? "Home" : p.name,
        hint: shortPath(p.path, home),
        icon: p.id === defaultId ? "home" : "folder",
        on: p.id === current,
        run: () => openProject(p.id),
      });
    }
    return list;
  }, [mode, models, active, q, mod, appMode, info.model, running, theme, projects, defaultId, current, home, tuning]);

  const shown = useMemo(() => {
    const f = q.trim().toLowerCase();
    if (!f || mode === "models") return mode === "models" && f ? entries.filter((e) => e.id === "custom" || e.label.toLowerCase().includes(f)) : entries;
    return entries.filter((e) => (e.label + " " + (e.hint || "") + " " + e.group).toLowerCase().includes(f));
  }, [entries, q, mode]);

  const { sel, setSel, listRef, move } = useList(shown.length);

  const pick = (e: Entry) => {
    const opensDialog = e.id === "model" || e.id === "folder" || e.id === "settings" || e.id === "connect" || e.id === "mcp" || e.id === "status" || e.id === "search";
    if (!opensDialog) closeDialog();
    e.run();
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (move(e)) return;
    if (e.key === "Enter" && shown[sel]) {
      e.preventDefault();
      pick(shown[sel]);
    } else if (e.key === "Backspace" && !q && mode === "models") {
      openDialog({ kind: "palette", mode: "root" });
    }
  };

  let lastGroup = "";
  return (
    <>
      <div className="palette-input">
        <Icon name="search" size={15} />
        <input
          autoFocus
          spellCheck={false}
          value={q}
          placeholder={mode === "models" ? "Search models, or type any model name" : "Type a command or a workspace"}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={onKey}
        />
        {mode === "models" && info.provider && <span className="scope">{info.provider}</span>}
      </div>
      <div className="palette-list" ref={listRef}>
        {mode === "models" && models === null && <div className="empty-row">Loading models…</div>}
        {error && <div className="empty-row">{error}</div>}
        {models !== null && !shown.length && !error && <div className="empty-row">Nothing matches</div>}
        {shown.map((e, i) => {
          const head = e.group !== lastGroup ? e.group : "";
          lastGroup = e.group;
          return (
            <div key={e.id}>
              {head && mode !== "models" && <div className="group">{head}</div>}
              <button type="button" className={"entry" + (i === sel ? " sel" : "")} onMouseMove={() => i !== sel && setSel(i)} onClick={() => pick(e)}>
                <Icon name={e.icon} size={15} />
                <span className="label">{e.label}</span>
                {e.hint && <span className="hint">{e.hint}</span>}
                <span className="end">
                  {e.on && <Icon name="check" size={14} className="tick" />}
                  {e.keys && e.keys.map((k) => <kbd key={k}>{k}</kbd>)}
                </span>
              </button>
            </div>
          );
        })}
      </div>
      <div className="palette-foot">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> move
        </span>
        <span>
          <kbd>Enter</kbd> choose
        </span>
        <span>
          <kbd>Esc</kbd> close
        </span>
      </div>
    </>
  );
}

function crumbs(path: string, sep: string): { label: string; path: string }[] {
  const out: { label: string; path: string }[] = [];
  const parts = path.split(/[\\/]+/).filter(Boolean);
  if (sep === "/") {
    out.push({ label: "/", path: "/" });
    let acc = "";
    for (const p of parts) {
      acc += "/" + p;
      out.push({ label: p, path: acc });
    }
    return out;
  }
  let acc = "";
  parts.forEach((p, i) => {
    acc = i === 0 ? p + "\\" : acc.replace(/\\$/, "") + "\\" + p;
    out.push({ label: p, path: acc });
  });
  return out;
}

function FolderPicker() {
  const [listing, setListing] = useState<Listing | null>(null);
  const [input, setInput] = useState("");
  const [error, setError] = useState("");
  const dirs = listing?.dirs || [];
  const { sel, setSel, listRef, move } = useList(dirs.length);
  const [moved, setMoved] = useState(false);
  const pathRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const el = pathRef.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [listing]);

  const latest = useRef(0);
  const typed = useRef(false);
  const go = async (path: string) => {
    const ticket = ++latest.current;
    try {
      const r = await server.fs(path);
      if (ticket !== latest.current) return;
      setListing(r);
      if (path !== "" || !typed.current) {
        typed.current = false;
        setInput(r.path);
      }
      setSel(0);
      setMoved(false);
      setError("");
    } catch (e) {
      if (ticket === latest.current) setError(errorText(e));
    }
  };

  useEffect(() => {
    go("");
  }, []);

  const join = (name: string) => (listing ? listing.path.replace(/[\\/]+$/, "") + listing.sep + name : name);

  const choose = async (path: string) => {
    closeDialog();
    try {
      await addFolder(path);
    } catch (e) {
      fail(e);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (move(e)) {
      setMoved(true);
      return;
    }
    if (e.key !== "Enter") return;
    e.preventDefault();
    if (!listing) {
      if (input.trim()) void go(input.trim());
      return;
    }
    if (e.ctrlKey || e.metaKey) return choose(listing.path);
    if (input.trim() && input.trim() !== listing.path) return void go(input.trim());
    if (moved && dirs[sel]) return void go(join(dirs[sel]));
    choose(listing.path);
  };

  const name = listing ? listing.path.split(/[\\/]+/).filter(Boolean).pop() || listing.path : "";
  return (
    <>
      <h3>Open a folder</h3>
      <div className="sub">Macron works inside the folder you choose. Each folder keeps its own conversation.</div>
      <div className="path-input">
        <Icon name="folder" size={15} />
        <input
          ref={pathRef}
          autoFocus
          spellCheck={false}
          dir="ltr"
          value={input}
          onChange={(e) => {
            typed.current = true;
            setInput(e.target.value);
          }}
          onKeyDown={onKey}
          placeholder="Type or paste a path"
        />
      </div>
      {listing && (
        <div className="crumbs">
          {crumbs(listing.path, listing.sep).map((c, i, all) => (
            <span key={c.path}>
              <button type="button" className={i === all.length - 1 ? "here" : ""} onClick={() => go(c.path)}>
                {c.label}
              </button>
              {i < all.length - 1 && c.label !== "/" && <span className="slash-sep">/</span>}
            </span>
          ))}
        </div>
      )}
      <div className="folder-list" ref={listRef}>
        {error && <div className="empty-row">{error}</div>}
        {listing && !dirs.length && !error && <div className="empty-row">No folders inside</div>}
        {dirs.map((d, i) => (
          <button
            key={d}
            type="button"
            className={"entry" + (moved && i === sel ? " sel" : "")}
            onClick={() => go(join(d))}
            onDoubleClick={() => choose(join(d))}
          >
            <Icon name="folder" size={15} />
            <span className="label">{d}</span>
            <span className="end">
              <Icon name="right" size={12} />
            </span>
          </button>
        ))}
      </div>
      <div className="bar">
        <div className="left">
          {listing?.home && (
            <button type="button" className="btn ghost" onClick={() => go(listing.home)}>
              <Icon name="home" size={14} />
              Home
            </button>
          )}
          {listing?.parent && (
            <button type="button" className="btn ghost" onClick={() => go(listing.parent)}>
              <Icon name="arrowUp" size={14} />
              Up
            </button>
          )}
          {(listing?.roots || [])
            .filter((r) => r !== "/" && r !== listing?.path)
            .map((r) => (
              <button key={r} type="button" className="btn ghost" onClick={() => go(r)}>
                <Icon name="drive" size={14} />
                {r}
              </button>
            ))}
        </div>
        <div className="left">
          <button type="button" className="btn" onClick={closeDialog}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={!listing} onClick={() => listing && choose(listing.path)}>
            Open {name ? `“${name}”` : ""}
          </button>
        </div>
      </div>
    </>
  );
}

function Choice<T extends string>({ value, options, onPick }: { value: T; options: [T, string][]; onPick: (v: T) => void }) {
  return (
    <div className="choice" role="radiogroup">
      {options.map(([v, label]) => (
        <button key={v} type="button" role="radio" aria-checked={v === value} className={v === value ? "on" : ""} onClick={() => v !== value && onPick(v)}>
          {label}
        </button>
      ))}
    </div>
  );
}

