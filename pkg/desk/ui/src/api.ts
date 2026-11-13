import type { ConnectResult, Connection, Info, Listing, Mode, Plugin, ProviderChoice, ServerView, Snapshot, StateResponse, Summary, Tuning } from "./types";

let base = "";

export function setBase(b: string) {
  base = b;
}

export function apiBase(): string {
  return base;
}

export class ApiError extends Error {
  hint: string;
  constructor(message: string, hint = "") {
    super(message);
    this.hint = hint;
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method, headers: { "X-Macron": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    init.headers = { "X-Macron": "1", "Content-Type": "application/json" };
    init.body = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(path, init);
  } catch {
    throw new ApiError("The Macron engine is not answering", "It may have stopped. Start Macron again.");
  }
  const type = res.headers.get("Content-Type") || "";
  const data = type.includes("json") ? await res.json().catch(() => ({})) : { error: (await res.text()).trim() };
  if (!res.ok) throw new ApiError(data.error || `Request failed (${res.status})`, data.hint || "");
  return data as T;
}

type InfoResponse = { info: Info; mode: Mode };
type Ok = { ok: boolean };

const p = (id: string) => `${base}/api/projects/${encodeURIComponent(id)}`;
const r = (path: string) => base + path;

export function fileURL(project: string, name: string, download = false): string {
  return `${p(project)}/file?name=${encodeURIComponent(name)}${download ? "&download=1" : ""}`;
}

export const server = {
  reveal: (id: string, name: string, folder: boolean) => call<Ok>("POST", p(id) + "/reveal", { name, folder }),
  state: () => call<StateResponse>("GET", r("/api/state")),
  project: (id: string) => call<Snapshot>("GET", p(id)),
  send: (id: string, text: string) => call<Ok>("POST", p(id) + "/send", { text }),
  stop: (id: string) => call<Ok>("POST", p(id) + "/stop"),
  newChat: (id: string) => call<Ok>("POST", p(id) + "/new"),
  compact: (id: string) => call<{ text: string }>("POST", p(id) + "/compact"),
  answer: (id: string, ask: string, choice: string) => call<Ok>("POST", p(id) + "/answer", { id: ask, choice }),
  addProject: (path: string) => call<Summary>("POST", r("/api/projects"), { path }),
  removeProject: (id: string) => call<Ok>("DELETE", p(id)),
  models: () => call<{ current: string; models: string[] | null }>("GET", r("/api/models")),
  setModel: (name: string) => call<InfoResponse>("POST", r("/api/model"), { name }),
  providers: () => call<{ providers: ProviderChoice[] | null }>("GET", r("/api/providers")),
  setProvider: (id: number) => call<InfoResponse>("POST", r("/api/provider"), { id }),
  setMode: (mode: Mode) => call<InfoResponse>("POST", r("/api/mode"), { mode }),
  setApprovals: (mode: string) => call<InfoResponse>("POST", r("/api/approvals"), { mode }),
  setYolo: (on: boolean) => call<InfoResponse>("POST", r("/api/yolo"), { on }),
  connections: () => call<{ connections: Connection[] | null }>("GET", r("/api/connections")),
  fs: (path: string) => call<Listing>("GET", r("/api/fs?path=") + encodeURIComponent(path)),
  connectModel: (m: { name: string; url: string; key: string; model: string }) => call<InfoResponse>("POST", r("/api/providers/connect"), m),
  listModels: (url: string, key: string) => call<{ models: string[] | null }>("POST", r("/api/providers/models"), { url, key }),
  plugins: () => call<{ plugins: Plugin[] }>("GET", r("/api/plugins")),
  addPlugin: (p: { name: string; target: string; args: string[]; env: Record<string, string> }) => call<Plugin>("POST", r("/api/plugins"), p),
  enablePlugin: (key: string, on: boolean) => call<Plugin>("POST", r("/api/plugins/") + encodeURIComponent(key) + "/enable", { on }),
  removePlugin: (key: string) => call<Ok>("DELETE", r("/api/plugins/") + encodeURIComponent(key)),
  tuning: () => call<Tuning>("GET", r("/api/tuning")),
  setTuning: (field: "effort" | "window" | "temperature", value: string | number | null) => call<Tuning>("POST", r("/api/tuning"), { field, value }),
  servers: () => call<{ servers: ServerView[] | null; wsl: boolean }>("GET", "/api/servers"),
  addServer: (spec: Partial<ServerView>) => call<ServerView>("POST", "/api/servers", spec),
  removeServer: (id: string) => call<Ok>("DELETE", "/api/servers/" + encodeURIComponent(id)),
  connectServer: (id: string, secrets: { password?: string; passphrase?: string; trust?: string }) =>
    call<ConnectResult>("POST", "/api/servers/" + encodeURIComponent(id) + "/connect", secrets),
  disconnectServer: (id: string) => call<Ok>("POST", "/api/servers/" + encodeURIComponent(id) + "/disconnect"),
};
