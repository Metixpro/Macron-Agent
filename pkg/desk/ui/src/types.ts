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
}

export interface Live {
  running: boolean;
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
  platform: string;
  host: string;
  model: string;
  provider: string;
  effort: string;
  approvals: string;
  yolo: boolean;
  contextWindow: number;
  connections: number;
  services: number;
}

export type Mode = "build" | "plan";

export interface StateResponse {
  info: Info;
  mode: Mode;
  projects: Summary[];
  default: string;
  home: string;
}

export interface Snapshot {
  id: string;
  path: string;
  name: string;
  blocks: Block[] | null;
  live: Live | null;
}

export interface ProviderChoice {
  id: number;
  name: string;
  model: string;
  active: boolean;
  enabled: boolean;
}

export interface Connection {
  id: string;
  name: string;
  capability: string;
  ready: boolean;
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
  state: "off" | "connecting" | "ready" | "failed";
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
