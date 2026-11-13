import type { Summary } from "./types";

export function tokens(n: number | undefined): string {
  if (!n) return "0";
  if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

export function took(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

export function ago(ms: number): string {
  if (!ms) return "";
  const s = (Date.now() - ms) / 1000;
  if (s < 60) return "now";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  if (s < 86400 * 7) return Math.floor(s / 86400) + "d";
  return new Date(ms).toLocaleDateString([], { month: "short", day: "numeric" });
}

export function clock(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function shortPath(path: string, home: string): string {
  if (!path) return "";
  if (home && (path === home || path.startsWith(home + "/") || path.startsWith(home + "\\"))) return "~" + path.slice(home.length);
  return path;
}

export function projectName(p: Summary | undefined, defaultId: string): string {
  if (!p) return "Macron";
  return p.id === defaultId ? "Home" : p.name;
}

export function modKey(platform: string): string {
  return platform === "darwin" ? "⌘" : "Ctrl";
}

const RTL = /[֐-ࣿיִ-﷿ﹰ-ﻼ]/;
const STRONG = /[A-Za-zÀ-ɏͰ-ϿЀ-ӿ֐-ࣿיִ-﷿ﹰ-ﻼ]/;

export function textDir(text: string | null | undefined): "rtl" | "ltr" {
  const m = STRONG.exec(text || "");
  return m && RTL.test(m[0]) ? "rtl" : "ltr";
}

export function bidi(root: HTMLElement) {
  root.querySelectorAll("table").forEach((t) => {
    if (t.parentElement?.classList.contains("table")) return;
    const wrap = document.createElement("div");
    wrap.className = "table";
    t.replaceWith(wrap);
    wrap.append(t);
  });
  root.setAttribute("dir", textDir(root.textContent));
  root
    .querySelectorAll<HTMLElement>("p, li, h1, h2, h3, h4, h5, h6, blockquote, ul, ol, .table, td, th")
    .forEach((n) => n.setAttribute("dir", textDir(n.textContent)));
}

export function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
  const t = document.createElement("textarea");
  t.value = text;
  t.style.position = "fixed";
  t.style.opacity = "0";
  document.body.append(t);
  t.select();
  document.execCommand("copy");
  t.remove();
  return Promise.resolve();
}

export function prettyArgs(raw: string): string {
  try {
    const v = JSON.parse(raw);
    if (v && typeof v === "object") {
      const keys = Object.keys(v);
      if (keys.length === 1 && typeof v[keys[0]] === "string") return v[keys[0]];
      return JSON.stringify(v, null, 2);
    }
  } catch {
    return raw;
  }
  return raw;
}

export const local = {
  get<T>(key: string, fallback: T): T {
    try {
      const v = localStorage.getItem("macron." + key);
      return v === null ? fallback : (JSON.parse(v) as T);
    } catch {
      return fallback;
    }
  },
  set(key: string, value: unknown) {
    try {
      localStorage.setItem("macron." + key, JSON.stringify(value));
    } catch {
      return;
    }
  },
};
