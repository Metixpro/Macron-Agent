export type BlockKind = "user" | "assistant" | "tool" | "note" | "error" | "plan" | "divider" | "file";

export interface FileRef {
  name: string;
  base: string;
  size: number;
  type?: string;
  caption?: string;
}

export interface Block {
  kind: BlockKind;
  text?: string;
  html?: string;
  name?: string;
  label?: string;
  arg?: string;
  args?: string;
  summary?: string;
  output?: string;
  failed?: boolean;
  file?: FileRef;
  attachments?: FileRef[];
  at: number;
}

export interface PlanItem {
  text: string;
  status: string;
}

export interface LiveTool {
  id: number;
  name: string;
  label: string;
  arg: string;
  progress?: string;
  started: number;
}

export interface LiveSub {
  id: number;
  title: string;
  step?: string;
  steps: number;
  done: boolean;
  failed: boolean;
}

export interface Ask {
  id: string;
  title: string;
  command: string;
  findings?: string[];
  guardian?: string;
}

export interface Usage {
  in: number;
  out: number;
  cost?: number;
  costKnown?: boolean;
}

export interface LiveDraft {
  name: string;
  label: string;
  arg?: string;
  size: number;
}

export interface Live {
  running: boolean;
  draft?: LiveDraft | null;
  started?: number;
  html?: string;
  thought?: string;
  plan?: PlanItem[];
  subs?: LiveSub[];
  tools?: LiveTool[];
  queued?: string[];
  ask?: Ask | null;
  context?: { used: number; window: number };
  usage?: Usage;
}

export interface Summary {
  id: string;
  name: string;
  path: string;
  preview: string;
  updated: number;
  running: boolean;
  asking: boolean;
}

export interface Info {
  version: string;
  creator?: string;
  creatorUrl?: string;
  license?: string;
  platform: string;
  host: string;
  model: string;
  provider: string;
  effort: string;
  approvals: string;
  executionMode?: string;
  sandboxed?: boolean;
  sandboxBackend?: string;
  yolo: boolean;
  failover?: boolean;
  routingActive?: boolean;
  moaActive?: string;
  contextWindow: number;
  connections: number;
  services: number;
}

export interface ThemePalette {
  id: string;
  name: string;
  appearance: "dark" | "light";
  builtin?: boolean;
  swatches: [string, string, string];
  colors: Record<string, string>;
}

export type Mode = "build" | "plan";

export interface StateResponse {
  info: Info;
  mode: Mode;
  projects: Summary[];
  default: string;
  home: string;
}

export interface ThreadSummary {
  id: string;
  title: string;
  preview: string;
  updated: number;
  count: number;
}

export interface Snapshot {
  id: string;
  path: string;
  name: string;
  blocks: Block[] | null;
  live: Live | null;
  thread?: string;
  threads?: ThreadSummary[];
}

export interface ProviderChoice {
  id: number;
  name: string;
  model: string;
  active: boolean;
  enabled: boolean;
}

export interface ProviderDetail extends ProviderChoice {
  baseUrl?: string;
  apiKeySet?: boolean;
  apiKeyMasked?: string;
  api?: string;
  resolvedApi?: string;
  priority?: number;
  vision?: boolean;
  noStream?: boolean;
  contextWindow?: number;
  autoWindow?: number;
  temperature?: number | null;
  tempLimit?: number;
  effort?: string;
  requests?: number;
  failures?: number;
  avgLatencyMs?: number;
  lastOkAt?: string;
  lastError?: string;
  lastErrorAt?: string;
  cooling?: boolean;
}

export interface ProviderUpsert {
  name?: string;
  baseUrl?: string;
  apiKey?: string;
  clearKey?: boolean;
  model?: string;
  api?: string;
  priority?: number;
  enabled?: boolean;
  vision?: boolean;
  noStream?: boolean;
  contextWindow?: number;
  temperature?: number | null;
  clearTemp?: boolean;
  effort?: string;
  activate?: boolean;
  skipTest?: boolean;
}

export interface ProviderTestResult {
  ok: boolean;
  reply?: string;
  latencyMs: number;
  error?: string;
}

export interface Connection {
  id: string;
  name: string;
  capability: string;
  ready: boolean;
}

export interface ConnectionField {
  key: string;
  type: string;
  purpose: string;
  default?: string;
  secret: boolean;
  set: boolean;
}

export interface ConnectionDetail {
  id: string;
  name: string;
  capability: string;
  ready: boolean;
  restart?: boolean;
  fields: ConnectionField[];
}

export interface MemoryEntry {
  id: number;
  scope: "memory" | "user";
  text: string;
}

export interface SkillView {
  name: string;
  category: string;
  description: string;
  version?: string;
  enabled: boolean;
  uses: number;
  files?: string[];
}

export interface GitChange {
  path: string;
  status: string;
  staged: boolean;
  added: number;
  deleted: number;
}

export interface ChangesResponse {
  git: boolean;
  branch: string;
  files: GitChange[];
}

export interface DiffResponse {
  path: string;
  diff: string;
  truncated?: boolean;
}

export interface UpdateStatus {
  current: string;
  latest?: string;
  tag?: string;
  url?: string;
  notes?: string;
  available: boolean;
  installing?: boolean;
  installed?: boolean;
  step?: string;
  phase?: string;
  downloaded?: number;
  total?: number;
  percent?: number;
  speedBps?: number;
  assetSize?: number;
  error?: string;
  checkedAt?: number;
}

export interface Listing {
  path: string;
  parent: string;
  dirs: string[] | null;
  home: string;
  roots: string[] | null;
  sep: string;
}

export interface Plugin {
  key: string;
  name: string;
  kind: string;
  source: string;
  target: string;
  enabled: boolean;
  status: string;
  error?: string;
  removable: boolean;
  tools: string[];
}

export interface Tuning {
  model: string;
  effort: string;
  effortParam: string;
  effortValue: string;
  effortSupport: "yes" | "no" | "maybe";
  window: number;
  autoWindow: number;
  temperature: number | null;
  tempLimit: number;
}

export interface ServerView {
  id: string;
  name: string;
  kind: "ssh" | "wsl";
  host?: string;
  user?: string;
  port?: number;
  key?: string;
  state: "off" | "connecting" | "installing" | "ready" | "failed";
  error?: string;
  version?: string;
}

export interface ConnectResult {
  server: ServerView;
  need?: "trust" | "password" | "passphrase";
  fingerprint?: string;
  message?: string;
}

export interface ServerEvent {
  type: string;
  project?: string;
  seq?: number;
  [key: string]: unknown;
}

export interface RoutingConfig {
  enabled: boolean;
  active: boolean;
  lightProviderId: number;
  lightProviderName: string;
  strongProviderId: number;
  strongProviderName: string;
  sensitivity: string;
  escalate: boolean;
  kinds: string[];
  allKinds: string[];
}

export interface RoutingUpdate {
  enabled?: boolean;
  strongProviderId?: number;
  sensitivity?: string;
  escalate?: boolean;
  kinds?: string[];
  kindsSet?: boolean;
}

export interface RoutingTestResult {
  strong: boolean;
  kind: string;
  hard: boolean;
  targetProvider: string;
  signals: string[];
}

export interface MoaSlotView {
  provider: string;
  effort?: string;
}

export interface MoaPresetView {
  name: string;
  aggregator: MoaSlotView;
  references: MoaSlotView[];
  fanout: string;
  privacyFilter: string;
  enabled: boolean;
  problems?: string[];
}

export interface MoaState {
  presets: MoaPresetView[];
  active: string;
  defaultPreset: string;
  privacyFilter: string;
}

export interface MoaConfigUpdate {
  active?: string;
  defaultPreset?: string;
  privacyFilter?: string;
}

export interface SearchHit {
  projectId: string;
  projectName: string;
  threadId: string;
  threadTitle: string;
  role: string;
  snippet: string;
  at: number;
}

export interface BrowserTab {
  index: number;
  url: string;
  title: string;
  active: boolean;
}

export interface BrowserViewport {
  width: number;
  height: number;
}

export interface BrowserInputPayload {
  type: "wheel" | "mouse_move" | "mouse_down" | "mouse_up" | "click" | "insert_text" | "press" | "key_down" | "key_up";
  x?: number;
  y?: number;
  deltaX?: number;
  deltaY?: number;
  button?: "left" | "middle" | "right";
  clickCount?: number;
  key?: string;
  text?: string;
  modifiers?: number;
}

export interface BrowserState {
  available: boolean;
  open: boolean;
  live?: boolean;
  agentActive?: boolean;
  takeover?: boolean;
  takeoverReason?: string;
  url?: string;
  title?: string;
  updated?: number;
  hasScreenshot: boolean;
  output?: string;
  tabs?: BrowserTab[];
  viewport?: BrowserViewport;
}
