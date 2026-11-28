import { useEffect, useState } from "react";
import { projectName } from "./format";
import { getState, useStore } from "./store";

function isWindowsPlatform(platform: string, home: string): boolean {
  if (platform === "windows") return true;
  if (platform && platform !== "windows") return false;
  if (/^[A-Za-z]:(?:[\\/]|$)|^\\\\/.test(home)) return true;
  if (typeof navigator !== "undefined" && /Win/i.test(navigator.platform || navigator.userAgent)) return true;
  return false;
}

// A path under the user's home folder, written the way this platform writes it.
export function homePath(rel: string): string {
  const s = getState();
  return isWindowsPlatform(s.info.platform, s.home) ? "%USERPROFILE%\\" + rel.replace(/\//g, "\\") : "~/" + rel;
}

export function useHomePath(rel: string): string {
  const platform = useStore((s) => s.info.platform);
  const home = useStore((s) => s.home);
  return isWindowsPlatform(platform, home) ? "%USERPROFILE%\\" + rel.replace(/\//g, "\\") : "~/" + rel;
}

export function useNow(active: boolean, every = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const t = window.setInterval(() => setNow(Date.now()), every);
    return () => window.clearInterval(t);
  }, [active, every]);
  return now;
}

export function useProjectName(): string {
  return useStore((s) => projectName(s.projects.find((p) => p.id === s.current), s.defaultId));
}

export function useMod(): string {
  return useStore((s) => (s.info.platform === "darwin" ? "⌘" : "Ctrl"));
}
