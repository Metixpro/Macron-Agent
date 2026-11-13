import type { Connection, Info, Listing, Mode, ProviderChoice, Snapshot, StateResponse, Summary } from "./types";

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

const p = (id: string) => `/api/projects/${encodeURIComponent(id)}`;

export const server = {
  state: () => call<StateResponse>("GET", "/api/state"),
  project: (id: string) => call<Snapshot>("GET", p(id)),
  send: (id: string, text: string) => call<Ok>("POST", p(id) + "/send", { text }),
  stop: (id: string) => call<Ok>("POST", p(id) + "/stop"),
  newChat: (id: string) => call<Ok>("POST", p(id) + "/new"),
  compact: (id: string) => call<{ text: string }>("POST", p(id) + "/compact"),
  answer: (id: string, ask: string, choice: string) => call<Ok>("POST", p(id) + "/answer", { id: ask, choice }),
  addProject: (path: string) => call<Summary>("POST", "/api/projects", { path }),
  removeProject: (id: string) => call<Ok>("DELETE", p(id)),
  models: () => call<{ current: string; models: string[] | null }>("GET", "/api/models"),
  setModel: (name: string) => call<InfoResponse>("POST", "/api/model", { name }),
  providers: () => call<{ providers: ProviderChoice[] | null }>("GET", "/api/providers"),
  setProvider: (id: number) => call<InfoResponse>("POST", "/api/provider", { id }),
  setMode: (mode: Mode) => call<InfoResponse>("POST", "/api/mode", { mode }),
  setApprovals: (mode: string) => call<InfoResponse>("POST", "/api/approvals", { mode }),
  setYolo: (on: boolean) => call<InfoResponse>("POST", "/api/yolo", { on }),
  connections: () => call<{ connections: Connection[] | null }>("GET", "/api/connections"),
  fs: (path: string) => call<Listing>("GET", "/api/fs?path=" + encodeURIComponent(path)),
};
