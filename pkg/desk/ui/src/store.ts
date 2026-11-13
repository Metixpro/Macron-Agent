import { useSyncExternalStore } from "react";
import { local } from "./format";
import type { Ask, Block, Info, Live, Mode, ServerView, Summary } from "./types";

export type Theme = "system" | "dark" | "light";

export type Dialog =
  | { kind: "ask"; ask: Ask }
  | { kind: "palette"; mode: "root" | "models" }
  | { kind: "folder" }
  | { kind: "settings" }
  | { kind: "server-add" }
  | { kind: "server-auth"; id: string; need: "trust" | "password" | "passphrase"; fingerprint?: string; message?: string }
  | null;

export interface State {
  online: boolean | null;
  machine: string;
  servers: ServerView[];
  wsl: boolean;
  info: Info;
  mode: Mode;
  projects: Summary[];
  defaultId: string;
  home: string;
  current: string;
  blocks: Block[];
  fresh: number;
  live: Live;
  lastTook: number;
  dialog: Dialog;
  toast: { text: string; n: number } | null;
  theme: Theme;
  sideOpen: boolean;
  unseen: boolean;
}

const emptyInfo: Info = {
  version: "",
  platform: "",
  host: "",
  model: "",
  provider: "",
  effort: "",
  approvals: "smart",
  yolo: false,
  contextWindow: 0,
  connections: 0,
  services: 0,
};

let state: State = {
  online: null,
  machine: "",
  servers: [],
  wsl: false,
  info: emptyInfo,
  mode: "build",
  projects: [],
  defaultId: "",
  home: "",
  current: "",
  blocks: [],
  fresh: 0,
  live: { running: false },
  lastTook: 0,
  dialog: null,
  toast: null,
  theme: local.get<Theme>("theme", "system"),
  sideOpen: false,
  unseen: false,
};

const listeners = new Set<() => void>();

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export function getState(): State {
  return state;
}

export function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const next = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...next };
  listeners.forEach((fn) => fn());
}

export function patchLive(fn: (l: Live) => Partial<Live>) {
  setState((s) => ({ live: { ...s.live, ...fn(s.live) } }));
}

export function useStore<T>(select: (s: State) => T): T {
  return useSyncExternalStore(subscribe, () => select(state));
}
