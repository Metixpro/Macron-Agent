import { useEffect, useState } from "react";
import { projectName } from "./format";
import { useStore } from "./store";

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
