import { useEffect, useRef } from "react";
import { applyTheme, closeDialog, composer, newChat, openDialog, stop, toggleMode } from "./actions";
import { Composer } from "./components/Composer";
import { Dialogs } from "./components/Dialogs";
import { Welcome } from "./components/Welcome";
import { PlanPanel, usePanelPlan } from "./components/PlanPanel";
import { Feed } from "./components/Feed";
import { Header } from "./components/Header";
import { LiveCard } from "./components/Live";
import { Sidebar } from "./components/Sidebar";
import { connect } from "./events";
import { useProjectName } from "./hooks";
import { getState, setState, useStore } from "./store";

function useShortcuts() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const s = getState();
      if (s.dialog?.kind === "ask") return;
      const mod = e.ctrlKey || e.metaKey;
      const k = e.key.toLowerCase();
      if (e.key === "Escape") {
        if (s.dialog) {
          e.preventDefault();
          closeDialog();
        } else if (s.sideOpen) setState({ sideOpen: false });
        else if (s.live.running) {
          e.preventDefault();
          stop();
        }
        return;
      }
      if (mod && k === "k") {
        e.preventDefault();
        if (s.dialog?.kind === "palette") closeDialog();
        else openDialog({ kind: "palette", mode: "root" });
        return;
      }
      if (s.dialog) return;
      if (mod && e.shiftKey && k === "p") {
        e.preventDefault();
        toggleMode();
      } else if (mod && !e.shiftKey && k === "n") {
        e.preventDefault();
        newChat();
      } else if (mod && k === "o") {
        e.preventDefault();
        openDialog({ kind: "folder" });
      } else if (mod && e.key === ",") {
        e.preventDefault();
        openDialog({ kind: "settings" });
      } else if (!mod && !e.altKey && e.key.length === 1 && (!document.activeElement || document.activeElement === document.body)) {
        composer.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}

declare global {
  interface Window {
    macronOpen?: (url: string) => Promise<void>;
    macronTitle?: (title: string) => Promise<void>;
  }
}

function useNative() {
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      const a = (e.target as Element | null)?.closest?.("a[href]");
      const href = a?.getAttribute("href") || "";
      if (!a || !window.macronOpen || !/^(https?:|mailto:)/i.test(href)) return;
      e.preventDefault();
      window.macronOpen(href).catch(() => undefined);
    };
    document.addEventListener("click", onClick, true);
    return () => document.removeEventListener("click", onClick, true);
  }, []);
}

function useTitle() {
  const name = useProjectName();
  const unseen = useStore((s) => s.unseen);
  const remote = useStore((s) => s.servers.find((x) => x.id === s.machine)?.name || "");
  useEffect(() => {
    const parts = [name && name !== "Home" ? name : "", remote, "Macron"].filter(Boolean);
    document.title = (unseen ? "● " : "") + parts.join(" · ");
    window.macronTitle?.(document.title).catch(() => undefined);
  }, [name, unseen, remote]);
  useEffect(() => {
    const seen = () => setState({ unseen: false });
    window.addEventListener("focus", seen);
    return () => window.removeEventListener("focus", seen);
  }, []);
}

function Toast() {
  const t = useStore((s) => s.toast);
  const last = useRef("");
  if (t) last.current = t.text;
  return (
    <div className={"toast" + (t ? " on" : "")} role="status" aria-live="polite">
      {t ? t.text : last.current}
    </div>
  );
}

export function App() {
  const theme = useStore((s) => s.theme);
  const sideOpen = useStore((s) => s.sideOpen);
  const planOpen = useStore((s) => s.planOpen);
  const withPlan = usePanelPlan().length > 0 && planOpen;

  useEffect(() => {
    applyTheme(theme);
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const follow = () => applyTheme(getState().theme);
    mq.addEventListener("change", follow);
    return () => mq.removeEventListener("change", follow);
  }, [theme]);

  useEffect(() => connect(), []);
  useShortcuts();
  useTitle();
  useNative();

  return (
    <div className={"app" + (sideOpen ? " side-open" : "") + (withPlan ? " with-plan" : "")}>
      <Sidebar />
      <div className="scrim" onClick={() => setState({ sideOpen: false })} />
      <main className="main">
        <Header />
        <Feed />
        <LiveCard />
        <Composer />
      </main>
      <PlanPanel />
      <Dialogs />
      <Welcome />
      <Toast />
    </div>
  );
}
