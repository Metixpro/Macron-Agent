import type {
  BrowserContextMenuInfo,
  BrowserInputPayload,
  BrowserProfileUpsert,
  BrowserProfileView,
  BrowserState,
  BrowserTeachStep,
  ChangesResponse,
  ConnectResult,
  Connection,
  ConnectionDetail,
  DiffResponse,
  ExtensionStatus,
  FileRef,
  Info,
  Listing,
  MemoryEntry,
  Mode,
  MoaConfigUpdate,
  MoaPresetView,
  MoaState,
  Plugin,
  ProviderDetail,
  ProviderTestResult,
  ProviderUpsert,
  RoutingConfig,
  RoutingTestResult,
  RoutingUpdate,
  SearchHit,
  ServerView,
  SkillView,
  Snapshot,
  StateResponse,
  Summary,
  ThreadSummary,
  Tuning,
  UpdateStatus,
} from "./types";

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
    if (base && path.startsWith(base)) {
      const local = await fetch("/api/servers", { headers: { "X-Macron": "1" }, credentials: "same-origin" }).then(
        () => true,
        () => false,
      );
      if (local) throw new ApiError("The connection to the server dropped", "Pick the server again in the machine menu to reconnect.");
    }
    throw new ApiError("The Macron engine on this PC is not answering", "Close Macron and open it again. If it keeps happening, send the end of .macron\\desktop.log in your user folder.");
  }
  const type = res.headers.get("Content-Type") || "";
  const data = type.includes("json") ? await res.json().catch(() => ({})) : { error: (await res.text()).trim() };
  if (!res.ok) throw new ApiError(data.error || `Request failed (${res.status})`, data.hint || "");
  return data as T;
}

async function uploadForm(path: string, file: File): Promise<FileRef> {
  const fd = new FormData();
  fd.append("file", file, file.name || "attachment");
  let res: Response;
  try {
    res = await fetch(path, {
      method: "POST",
      headers: { "X-Macron": "1" },
      credentials: "same-origin",
      body: fd,
    });
  } catch {
    throw new ApiError("Could not upload the file");
  }
  const type = res.headers.get("Content-Type") || "";
  const data = type.includes("json") ? await res.json().catch(() => ({})) : { error: (await res.text()).trim() };
  if (!res.ok) throw new ApiError(data.error || `Upload failed (${res.status})`, data.hint || "");
  return data as FileRef;
}

type InfoResponse = { info: Info; mode: Mode };
type Ok = { ok: boolean };

const p = (id: string) => `${base}/api/projects/${encodeURIComponent(id)}`;
const r = (path: string) => base + path;

export function fileURL(project: string, name: string, download = false): string {
  return `${p(project)}/file?name=${encodeURIComponent(name)}${download ? "&download=1" : ""}`;
}

export function browserShotURL(project: string, updated = 0): string {
  return `${p(project)}/browser/screenshot?v=${updated}`;
}

export function browserStreamURL(project: string, updated = 0): string {
  return `${p(project)}/browser/stream?v=${updated}`;
}

export const server = {
  browserState: (id: string) => call<BrowserState>("GET", p(id) + "/browser"),
  browserAction: (id: string, body: { action: string; url?: string; text?: string; key?: string; submit?: boolean; x?: number; y?: number; deltaY?: number; index?: number }) =>
    call<BrowserState>("POST", p(id) + "/browser/action", body),
  browserInput: (id: string, body: BrowserInputPayload) =>
    call<Ok>("POST", p(id) + "/browser/input", body),
  browserTakeover: (id: string, active: boolean, reason = "user") =>
    call<BrowserState>("POST", p(id) + "/browser/takeover", { active, reason }),
  browserDialog: (id: string, accept: boolean, promptText = "", dialogId = "") =>
    call<BrowserState>("POST", p(id) + "/browser/dialog", { accept, promptText, dialogId }),
  browserContextMenu: (id: string, body: Record<string, unknown>) =>
    call<BrowserContextMenuInfo & { ok?: boolean }>("POST", p(id) + "/browser/context-menu", body),
  browserResize: (id: string, width: number, height: number) =>
    call<BrowserState>("POST", p(id) + "/browser/resize", { width, height }),
  browserTeach: (id: string, action: "start" | "stop" | "cancel") =>
    call<{ browser: BrowserState; steps: BrowserTeachStep[] }>("POST", p(id) + "/browser/teach", { action }),
  setProjectBrowserProfile: (id: string, profileId: string) =>
    call<{ profile: BrowserProfileView; browser: BrowserState }>("POST", p(id) + "/browser/profile", { profileId }),
  browserProfiles: () => call<{ profiles: BrowserProfileView[] }>("GET", r("/api/browser/profiles")),
  saveBrowserProfile: (body: BrowserProfileUpsert) =>
    call<{ profile: BrowserProfileView; profiles: BrowserProfileView[] }>(
      body.id ? "PUT" : "POST",
      body.id ? r("/api/browser/profiles/") + encodeURIComponent(body.id) : r("/api/browser/profiles"),
      body
    ),
  deleteBrowserProfile: (id: string) =>
    call<{ ok: boolean; profiles: BrowserProfileView[] }>("DELETE", r("/api/browser/profiles/") + encodeURIComponent(id)),
  extensionStatus: () => call<ExtensionStatus>("GET", r("/api/browser/extension")),
  extensionRotateToken: () => call<ExtensionStatus>("POST", r("/api/browser/extension/rotate"), {}),
  reveal: (id: string, name: string, folder: boolean) => call<Ok>("POST", p(id) + "/reveal", { name, folder }),
  state: () => call<StateResponse>("GET", r("/api/state")),
  project: (id: string) => call<Snapshot>("GET", p(id)),
  send: (id: string, text: string, attachments?: FileRef[]) => call<Ok>("POST", p(id) + "/send", attachments?.length ? { text, attachments } : { text }),
  upload: (id: string, file: File) => uploadForm(p(id) + "/upload", file),
  projectFiles: (id: string, q: string) => call<{ files: string[] }>("GET", p(id) + "/files?q=" + encodeURIComponent(q)),
  changes: (id: string) => call<ChangesResponse>("GET", p(id) + "/changes"),
  diff: (id: string, path: string) => call<DiffResponse>("GET", p(id) + "/diff?path=" + encodeURIComponent(path)),
  revert: (id: string, path: string) => call<Ok>("POST", p(id) + "/revert", { path }),
  threads: (id: string) => call<{ active: string; threads: ThreadSummary[] }>("GET", p(id) + "/threads"),
  switchThread: (id: string, tid: string) => call<Snapshot>("POST", p(id) + "/threads/" + encodeURIComponent(tid) + "/switch"),
  deleteThread: (id: string, tid: string) => call<{ active: string; threads: ThreadSummary[]; snapshot: Snapshot }>("DELETE", p(id) + "/threads/" + encodeURIComponent(tid)),
  stop: (id: string) => call<Ok>("POST", p(id) + "/stop"),
  newChat: (id: string) => call<{ ok: boolean; thread?: ThreadSummary; snapshot?: Snapshot }>("POST", p(id) + "/new"),
  compact: (id: string) => call<{ text: string }>("POST", p(id) + "/compact"),
  answer: (id: string, ask: string, choice: string) => call<Ok>("POST", p(id) + "/answer", { id: ask, choice }),
  addProject: (path: string) => call<Summary>("POST", r("/api/projects"), { path }),
  removeProject: (id: string) => call<Ok>("DELETE", p(id)),
  models: () => call<{ current: string; models: string[] | null }>("GET", r("/api/models")),
  setModel: (name: string) => call<InfoResponse>("POST", r("/api/model"), { name }),
  providers: () => call<{ providers: ProviderDetail[] | null; failover?: boolean }>("GET", r("/api/providers")),
  addProvider: (req: ProviderUpsert) => call<{ provider: ProviderDetail; info: InfoResponse }>("POST", r("/api/providers"), req),
  updateProvider: (id: number, req: ProviderUpsert) => call<{ provider: ProviderDetail; info: InfoResponse }>("PUT", r("/api/providers/") + id, req),
  deleteProvider: (id: number) => call<{ ok: boolean; info: InfoResponse }>("DELETE", r("/api/providers/") + id),
  testProvider: (id: number) => call<ProviderTestResult>("POST", r("/api/providers/") + id + "/test", {}),
  providerModels: (id: number) => call<{ models: string[] }>("GET", r("/api/providers/") + id + "/models"),
  setProvider: (id: number) => call<InfoResponse>("POST", r("/api/provider"), { id }),
  setFailover: (on: boolean) => call<{ failover: boolean; info: InfoResponse }>("POST", r("/api/failover"), { on }),
  routing: () => call<RoutingConfig>("GET", r("/api/routing")),
  saveRouting: (req: RoutingUpdate) => call<RoutingConfig>("POST", r("/api/routing"), req),
  testRouting: (text: string) => call<RoutingTestResult>("POST", r("/api/routing/test"), { text }),
  moa: () => call<MoaState>("GET", r("/api/moa")),
  saveMoaPreset: (preset: MoaPresetView) => call<MoaState>("POST", r("/api/moa/presets"), preset),
  deleteMoaPreset: (name: string) => call<MoaState>("DELETE", r("/api/moa/presets/") + encodeURIComponent(name)),
  updateMoaConfig: (req: MoaConfigUpdate) => call<MoaState>("POST", r("/api/moa/config"), req),
  searchSessions: (q: string) => call<{ hits: SearchHit[] }>("GET", r("/api/search?q=") + encodeURIComponent(q)),
  getSoul: () => call<{ text: string; path: string }>("GET", r("/api/soul")),
  saveSoul: (text: string) => call<{ text: string; path: string }>("POST", r("/api/soul"), { text }),
  resetSoul: () => call<{ text: string; path: string }>("POST", r("/api/soul"), { reset: true }),
  runShell: (id: string, command: string) => call<{ output: string }>("POST", p(id) + "/shell", { command }),
  projectStatus: (id: string) => call<{ status: string; info: InfoResponse }>("GET", p(id) + "/status"),
  setMode: (mode: Mode) => call<InfoResponse>("POST", r("/api/mode"), { mode }),
  setApprovals: (mode: string) => call<InfoResponse>("POST", r("/api/approvals"), { mode }),
  setExecutionMode: (mode: string) => call<InfoResponse>("POST", r("/api/execution"), { mode }),
  setYolo: (on: boolean) => call<InfoResponse>("POST", r("/api/yolo"), { on }),
  connections: () => call<{ connections: Connection[] | null }>("GET", r("/api/connections")),
  connectionDetails: () => call<{ connections: ConnectionDetail[] }>("GET", r("/api/connections/details")),
  saveConnections: (values: Record<string, string>) => call<{ connections: ConnectionDetail[]; info: Info }>("POST", r("/api/connections"), { values }),
  testConnection: (service: string, resource?: string) => call<{ ok: boolean; message: string }>("POST", r("/api/connections/test"), { service, resource }),
  memories: () => call<{ memories: MemoryEntry[] }>("GET", r("/api/memories")),
  addMemory: (scope: "memory" | "user" | "site", text: string, host?: string) =>
    call<MemoryEntry>("POST", r("/api/memories"), host ? { scope, host, text } : { scope, text }),
  deleteMemory: (scope: "memory" | "user" | "site", id: number) =>
    call<Ok>("DELETE", r("/api/memories/") + id + "?scope=" + scope),
  skills: () => call<{ skills: SkillView[] }>("GET", r("/api/skills")),
  enableSkill: (name: string, on: boolean) => call<{ ok: boolean; skills: SkillView[] }>("POST", r("/api/skills/") + encodeURIComponent(name) + "/enable", { on }),
  deleteSkill: (name: string) => call<{ ok: boolean; skills: SkillView[] }>("DELETE", r("/api/skills/") + encodeURIComponent(name)),
  checkUpdate: (force = false) => call<UpdateStatus>("GET", "/api/update" + (force ? "?force=1" : "")),
  installUpdate: () => call<UpdateStatus>("POST", "/api/update/install", {}),
  fs: (path: string) => call<Listing>("GET", r("/api/fs?path=") + encodeURIComponent(path)),
  connectModel: (m: { name: string; url: string; key: string; model: string }) => call<InfoResponse>("POST", r("/api/providers/connect"), m),
  listModels: (url: string, key: string) => call<{ models: string[] | null }>("POST", r("/api/providers/models"), { url, key }),
  localProviders: () => call<{ servers: { id: string; name: string; baseUrl: string; models: string[] }[] }>("GET", r("/api/providers/local")),
  diagnostics: () => call<Record<string, unknown>>("GET", r("/api/diagnostics")),
  themes: () => call<{ themes: { id: string; name: string; appearance: "dark" | "light"; builtin?: boolean; swatches: [string, string, string]; colors: Record<string, string> }[]; dir: string }>("GET", r("/api/themes")),
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
  installServer: (id: string, secrets: { password?: string; passphrase?: string; trust?: string } = {}) =>
    call<ConnectResult & { output?: string }>("POST", "/api/servers/" + encodeURIComponent(id) + "/install", secrets),
};
