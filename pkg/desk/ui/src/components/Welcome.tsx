import { useEffect, useMemo, useRef, useState } from "react";
import { errorText, openDialog, toast } from "../actions";
import { server } from "../api";
import { local } from "../format";
import { useMod } from "../hooks";
import { Icon } from "../icons";
import { setState, useStore } from "../store";

type Provider = { id: string; label: string; about: string; url: string; key: boolean; keyAt?: string; keyHint?: string };

const PROVIDERS: Provider[] = [
  { id: "openai", label: "OpenAI", about: "GPT models", url: "https://api.openai.com/v1", key: true, keyAt: "https://platform.openai.com/api-keys" },
  { id: "anthropic", label: "Anthropic", about: "Claude models", url: "https://api.anthropic.com/v1/messages", key: true, keyAt: "https://console.anthropic.com/settings/keys" },
  { id: "gemini", label: "Google Gemini", about: "Gemini models, with a free tier", url: "https://generativelanguage.googleapis.com/v1beta/openai", key: true, keyAt: "https://aistudio.google.com/apikey" },
  { id: "openrouter", label: "OpenRouter", about: "Hundreds of models with one key", url: "https://openrouter.ai/api/v1", key: true, keyAt: "https://openrouter.ai/keys" },
  { id: "deepseek", label: "DeepSeek", about: "Low-cost models", url: "https://api.deepseek.com/v1", key: true, keyAt: "https://platform.deepseek.com/api_keys" },
  { id: "ollama", label: "Ollama", about: "Runs on this PC, free, no key", url: "http://localhost:11434/v1", key: false, keyAt: "https://ollama.com/download", keyHint: "Install Ollama and pull a model first" },
  { id: "custom", label: "Other", about: "Any OpenAI-compatible address", url: "", key: true },
];

type Step = "hello" | "provider" | "model" | "folder" | "done";
const ORDER: Step[] = ["hello", "provider", "model", "folder", "done"];

export function Welcome() {
  const online = useStore((s) => s.online);
  const loaded = useStore((s) => !!s.info.version);
  const hasModel = useStore((s) => !!s.info.model);
  const onServer = useStore((s) => s.machine !== "");
  const [open, setOpen] = useState(false);
  const decided = useRef(false);
  const [step, setStep] = useState<Step>("hello");
  const [provider, setProvider] = useState<Provider>(PROVIDERS[0]);

  useEffect(() => {
    if (decided.current || online !== true || !loaded || onServer) return;
    decided.current = true;
    if (!hasModel && !local.get("welcomed", false)) setOpen(true);
  }, [online, loaded, hasModel, onServer]);

  if (!open) return null;
  const finish = () => {
    local.set("welcomed", true);
    setOpen(false);
  };
  const at = ORDER.indexOf(step);
  return (
    <div className="welcome" role="dialog" aria-label="Set up Macron">
      <div className="welcome-top">
        <div className="steps-dots" aria-hidden>
          {ORDER.slice(1, 4).map((s, i) => (
            <span key={s} className={i + 1 < at ? "done" : i + 1 === at ? "on" : ""} />
          ))}
        </div>
        {step !== "done" && (
          <button type="button" className="btn ghost" onClick={finish}>
            Skip for now
          </button>
        )}
      </div>
      <div className="welcome-body">
        {step === "hello" && <Hello onNext={() => setStep("provider")} onSkip={finish} />}
        {step === "provider" && (
          <ProviderStep
            current={provider.id}
            onPick={(p) => {
              setProvider(p);
              setStep("model");
            }}
          />
        )}
        {step === "model" && <ModelStep key={provider.id} provider={provider} onBack={() => setStep("provider")} onDone={() => setStep("folder")} />}
        {step === "folder" && (
          <FolderStep
            onHome={() => setStep("done")}
            onPick={() => {
              setStep("done");
              window.setTimeout(() => openDialog({ kind: "folder" }), 50);
            }}
          />
        )}
        {step === "done" && <Done onStart={finish} />}
      </div>
    </div>
  );
}

function Hello({ onNext, onSkip }: { onNext: () => void; onSkip: () => void }) {
  return (
    <div className="welcome-card">
      <div className="welcome-mark" aria-hidden>
        <svg viewBox="0 0 256 256" width="44" height="44">
          <rect x="12" y="12" width="232" height="232" rx="54" fill="currentColor" />
          <polyline points="76,180 76,80 128,138 180,80 180,180" fill="none" stroke="var(--bg)" strokeWidth="28" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </div>
      <h1>Welcome to Macron</h1>
      <p className="lead">An agent that works on this computer: it reads and changes your files, runs commands, looks things up and hands you the results.</p>
      <p className="muted">Setup takes about a minute: pick where its model comes from, then where it works. You can change everything later in Settings.</p>
      <div className="welcome-actions">
        <button type="button" className="btn primary big" onClick={onNext} autoFocus>
          Set up Macron
          <Icon name="arrowRight" size={15} />
        </button>
        <button type="button" className="btn ghost" onClick={onSkip}>
          I'll do it later
        </button>
      </div>
    </div>
  );
}

function ProviderStep({ current, onPick }: { current: string; onPick: (p: Provider) => void }) {
  return (
    <div className="welcome-card wide">
      <h2>Where should the model come from?</h2>
      <p className="muted">Macron thinks with a language model from a provider you choose. Your key stays on this computer.</p>
      <div className="provider-grid">
        {PROVIDERS.map((p) => (
          <button key={p.id} type="button" className={"provider" + (p.id === current ? " on" : "")} onClick={() => onPick(p)}>
            <span className="pname">{p.label}</span>
            <span className="pabout">{p.about}</span>
          </button>
        ))}
      </div>
    </div>
  );
}

function ModelStep({ provider, onBack, onDone }: { provider: Provider; onBack: () => void; onDone: () => void }) {
  const [url, setUrl] = useState(provider.url);
  const [key, setKey] = useState("");
  const [model, setModel] = useState("");
  const [models, setModels] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [listError, setListError] = useState("");
  const timer = useRef(0);

  const load = async (u: string, k: string) => {
    if (!u.trim() || (provider.key && !k.trim() && provider.id !== "custom")) return;
    setLoading(true);
    setListError("");
    try {
      const r = await server.listModels(u.trim(), k.trim());
      const list = (r.models || []).slice().sort();
      setModels(list);
      if (!list.length) setListError("The provider listed no models. Type the model's exact name.");
    } catch (e) {
      setModels(null);
      setListError(errorText(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => load(url, key), provider.key ? 700 : 50);
    return () => window.clearTimeout(timer.current);
  }, [url, key]);

  const shown = useMemo(() => (models || []).filter((m) => m.toLowerCase().includes(model.trim().toLowerCase())).slice(0, 300), [models, model]);

  const connect = async () => {
    setBusy(true);
    setError("");
    try {
      const r = await server.connectModel({ name: provider.id, url: url.trim(), key: key.trim(), model: model.trim() });
      setState({ info: r.info });
      toast("Connected: " + model.trim());
      onDone();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form
      className="welcome-card wide"
      onSubmit={(e) => {
        e.preventDefault();
        if (model.trim() && !busy) connect();
      }}
    >
      <button type="button" className="link-btn back" onClick={onBack}>
        <Icon name="right" size={12} className="flip" />
        {provider.label}
      </button>
      <h2>{provider.key ? "Your key and model" : "Pick a model"}</h2>
      {(provider.id === "custom" || provider.id === "ollama") && (
        <label className="field">
          <span>Address</span>
          <input spellCheck={false} dir="ltr" placeholder="https://…/v1" value={url} onChange={(e) => setUrl(e.target.value)} />
        </label>
      )}
      {provider.key && (
        <label className="field">
          <span className="field-head">
            API key
            {provider.keyAt && (
              <a className="link-btn inline" href={provider.keyAt} target="_blank" rel="noopener noreferrer">
                Get a key
              </a>
            )}
          </span>
          <input autoFocus type="password" spellCheck={false} dir="ltr" value={key} onChange={(e) => setKey(e.target.value)} placeholder="Paste it here" />
        </label>
      )}
      {!provider.key && provider.keyHint && (
        <div className="hint">
          {provider.keyHint}:{" "}
          <a href={provider.keyAt} target="_blank" rel="noopener noreferrer">
            ollama.com
          </a>
        </div>
      )}
      <label className="field">
        <span className="field-head">
          Model
          {loading && <span className="muted small">Loading the list…</span>}
        </span>
        <input spellCheck={false} dir="ltr" placeholder={models && models.length ? "Search or type a name" : "The model's exact name"} value={model} onChange={(e) => setModel(e.target.value)} />
      </label>
      {models && shown.length > 0 && (
        <div className="model-list tall">
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
      {listError && !error && <div className="hint">{listError}</div>}
      {error && <div className="form-error">{error}</div>}
      <div className="welcome-actions end">
        <button type="submit" className="btn primary big" disabled={busy || !url.trim() || !model.trim() || (provider.key && provider.id !== "custom" && !key.trim())}>
          {busy ? "Testing the model…" : "Test and connect"}
        </button>
      </div>
    </form>
  );
}

function FolderStep({ onHome, onPick }: { onHome: () => void; onPick: () => void }) {
  return (
    <div className="welcome-card wide">
      <h2>Where should Macron work?</h2>
      <p className="muted">Each folder is a workspace with its own conversation. Macron changes files only inside the folder you are in, unless you ask otherwise.</p>
      <div className="choice-list">
        <button type="button" className="choice" onClick={onPick}>
          <Icon name="folder" size={18} />
          <span className="grow">
            <span className="cname">Open a project folder</span>
            <span className="cabout">Code, documents or anything Macron should work on</span>
          </span>
          <Icon name="right" size={14} />
        </button>
        <button type="button" className="choice" onClick={onHome}>
          <Icon name="home" size={18} />
          <span className="grow">
            <span className="cname">Start in Home</span>
            <span className="cabout">General conversations; open folders later from the sidebar</span>
          </span>
          <Icon name="right" size={14} />
        </button>
      </div>
    </div>
  );
}

function Done({ onStart }: { onStart: () => void }) {
  const mod = useMod();
  const tips: [string, string][] = [
    [mod + " K", "Commands, models, workspaces and settings in one search"],
    [mod + " Shift P", "Plan mode: Macron plans first and changes nothing"],
    ["This PC ▾", "Work on a server over SSH; Macron installs itself there"],
    ["Settings", "Effort, context, Telegram, MCP servers and more"],
  ];
  return (
    <div className="welcome-card">
      <h1>Macron is ready</h1>
      <p className="muted">Ask for anything: build or fix something, research a question, sort out files, or automate a chore.</p>
      <dl className="tips">
        {tips.map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      <div className="welcome-actions">
        <button type="button" className="btn primary big" onClick={onStart} autoFocus>
          Start
        </button>
      </div>
    </div>
  );
}
