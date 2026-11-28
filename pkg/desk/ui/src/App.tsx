import { useEffect, useRef, useState } from "react";
import type { CSSProperties, DragEvent } from "react";
import {
  applyDensity,
  applyMotion,
  applyTheme,
  closeDialog,
  composer,
  newChat,
  openDialog,
  openSettings,
  stop,
  toggleCompanion,
  toggleMode,
  toggleSidebarCollapsed,
} from "./actions";
import { BrowserDock } from "./components/BrowserDock";
import { Composer } from "./components/Composer";
import { Dialogs } from "./components/Dialogs";
import { Feed } from "./components/Feed";
import { Header } from "./components/Header";
import { PlanPanel, useRightPanelVisible } from "./components/PlanPanel";
import { Sidebar } from "./components/Sidebar";
import { Welcome } from "./components/Welcome";
import { connect } from "./events";
import { useProjectName } from "./hooks";
import { applyLang, useT } from "./i18n";
import { Icon } from "./icons";
import { restoreSavedPalette } from "./palettes";
import { getState, setState, useStore } from "./store";

function useShortcuts() {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing || e.keyCode === 229) return;
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
      if (mod && e.shiftKey && k === "f") {
        e.preventDefault();
        if (s.dialog?.kind === "search") closeDialog();
        else openDialog({ kind: "search" });
        return;
      }
      if (mod && e.key === ",") {
        e.preventDefault();
        if (s.dialog?.kind === "settings") closeDialog();
        else openSettings("general");
        return;
      }
      if (s.dialog) return;
      if (mod && e.key === "\\") {
        e.preventDefault();
        toggleSidebarCollapsed();
      } else if (mod && e.key === ".") {
        e.preventDefault();
        toggleCompanion();
      } else if (mod && e.shiftKey && k === "p") {
        e.preventDefault();
        toggleMode();
      } else if (mod && !e.shiftKey && k === "n") {
        e.preventDefault();
        newChat();
      } else if (mod && k === "o") {
        e.preventDefault();
        openDialog({ kind: "folder" });
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

export function App() {
  const t = useT();
  const theme = useStore((s) => s.theme);
  const density = useStore((s) => s.density);
  const motion = useStore((s) => s.motion);
  const lang = useStore((s) => s.lang);
  const sideOpen = useStore((s) => s.sideOpen);
  const sideCollapsed = useStore((s) => s.sideCollapsed);
  const sideWidth = useStore((s) => s.sideWidth);
  const withRight = useRightPanelVisible();
  const [dragging, setDragging] = useState(false);
  const dragDepth = useRef(0);

  useEffect(() => {
    applyTheme(theme);
    const mq = matchMedia("(prefers-color-scheme: dark)");
    const follow = () => applyTheme(getState().theme);
    mq.addEventListener("change", follow);
    return () => mq.removeEventListener("change", follow);
  }, [theme]);

  useEffect(() => {
    applyDensity(density);
  }, [density]);

  useEffect(() => {
    applyMotion(motion);
  }, [motion]);

  useEffect(() => {
    applyLang(lang);
  }, [lang]);

  useEffect(() => {
    void restoreSavedPalette();
  }, []);

  useEffect(() => connect(), []);
  useShortcuts();
  useTitle();
  useNative();

  const onDragEnter = (e: DragEvent<HTMLElement>) => {
    if (!e.dataTransfer?.types?.includes("Files")) return;
    e.preventDefault();
    dragDepth.current++;
    setDragging(true);
  };

  const onDragLeave = (e: DragEvent<HTMLElement>) => {
    if (!e.dataTransfer?.types?.includes("Files")) return;
    e.preventDefault();
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };

  const onDragOver = (e: DragEvent<HTMLElement>) => {
    if (!e.dataTransfer?.types?.includes("Files")) return;
    e.preventDefault();
  };

  const onDrop = (e: DragEvent<HTMLElement>) => {
    e.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    const files = Array.from(e.dataTransfer?.files || []);
    if (files.length) composer.attachFiles(files);
  };

  const shellStyle = { "--side-w": `${sideWidth}px` } as CSSProperties;
  const appClass = [
    "app",
    sideOpen ? "side-open" : "",
    sideCollapsed ? "side-collapsed" : "",
    withRight ? "with-plan" : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={appClass} style={shellStyle}>
      <Sidebar />
      <div className="scrim" onClick={() => setState({ sideOpen: false })} />
      <main className="main" onDragEnter={onDragEnter} onDragLeave={onDragLeave} onDragOver={onDragOver} onDrop={onDrop}>
        <Header />
        <div className="feed-stage">
          <Feed />
        </div>
        <Composer />
        {dragging && (
          <div className="drop-veil">
            <div className="drop-box">
              <Icon name="paperclip" size={24} />
              <span>{t("Drop files or images here to attach")}</span>
            </div>
          </div>
        )}
      </main>
      <PlanPanel />
      <BrowserDock />
      <Dialogs />
      <Welcome />
    </div>
  );
}
