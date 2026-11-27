import { useCallback, useEffect, useRef, useState } from "react";
import type { ClipboardEvent, CompositionEvent, KeyboardEvent, MouseEvent, WheelEvent } from "react";
import { errorText, loadBrowser, noteBrowserVisit, openBrowserOnFirstVisit, send, stop } from "../actions";
import { browserShotURL, browserStreamURL, server } from "../api";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { getState, setState, useStore } from "../store";
import type { BrowserInputPayload } from "../types";
import { StarBuddy, useStarBuddyMood } from "./StarBuddy";

const DEFAULT_W = 1366;
const DEFAULT_H = 860;
const DEFAULT_URL = "https://www.google.com";

function modMask(e: { altKey?: boolean; ctrlKey?: boolean; metaKey?: boolean; shiftKey?: boolean }): number {
  let m = 0;
  if (e.altKey) m |= 1;
  if (e.ctrlKey) m |= 2;
  if (e.metaKey) m |= 4;
  if (e.shiftKey) m |= 8;
  return m;
}

function mouseButtonName(b: number): "left" | "middle" | "right" {
  if (b === 1) return "middle";
  if (b === 2) return "right";
  return "left";
}

function mapPlaywrightKey(e: KeyboardEvent<HTMLDivElement>): string | null {
  let key = e.key;
  if (key === " ") key = "Space";
  else if (key === "Esc") key = "Escape";

  const special = new Set([
    "Enter", "Backspace", "Delete", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
    "Home", "End", "PageUp", "PageDown", "Space", "F1", "F2", "F3", "F4", "F5", "F6",
    "F7", "F8", "F9", "F10", "F11", "F12", "Insert",
  ]);
  const hasMod = e.ctrlKey || e.altKey || e.metaKey;
  if (!special.has(key) && (!hasMod || key.length !== 1)) {
    return null;
  }
  const mods: string[] = [];
  if (e.ctrlKey) mods.push("Control");
  if (e.metaKey && !e.ctrlKey) mods.push("Control");
  if (e.altKey) mods.push("Alt");
  if (e.shiftKey && (hasMod || special.has(key))) mods.push("Shift");
  if (mods.length) {
    const k = key.length === 1 ? key.toLowerCase() : key;
    return `${mods.join("+")}+${k}`;
  }
  return key;
}

export function BrowserBody() {
  const t = useT();
  const current = useStore((s) => s.current);
  const machine = useStore((s) => s.machine);
  const view = useStore((s) => s.browserView);
  const running = useStore((s) => s.live.running);
  const hasModel = useStore((s) => Boolean(s.info.model));
  const mood = useStarBuddyMood();

  const [addr, setAddr] = useState("");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [takeoverBusy, setTakeoverBusy] = useState(false);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState(false);
  const [pillHidden, setPillHidden] = useState(false);
  const [sending, setSending] = useState(false);
  const [streamNonce, setStreamNonce] = useState(1);
  const [pulseTakeover, setPulseTakeover] = useState(false);

  const imgRef = useRef<HTMLImageElement>(null);
  const canvasRef = useRef<HTMLDivElement | null>(null);
  const [canvasEl, setCanvasEl] = useState<HTMLDivElement | null>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const inputQueueRef = useRef<Promise<void>>(Promise.resolve());
  const draggingRef = useRef(false);
  const lastMoveAtRef = useRef(0);
  const wheelRef = useRef<{ dx: number; dy: number; x: number; y: number; raf: number }>({
    dx: 0,
    dy: 0,
    x: DEFAULT_W / 2,
    y: DEFAULT_H / 2,
    raf: 0,
  });

  const dialogOpen = expanded && Boolean(view?.open);
  const isLive = Boolean(view?.live && view?.open);
  const agentActive = Boolean(view?.agentActive);
  const takeover = Boolean(view?.takeover);
  const canInteract = !agentActive || takeover;
  const vpW = view?.viewport?.width || DEFAULT_W;
  const vpH = view?.viewport?.height || DEFAULT_H;

  useEffect(() => {
    if (current) void loadBrowser(current);
    setStreamNonce((n) => n + 1);
  }, [current, machine]);

  const wasLiveRef = useRef(false);
  useEffect(() => {
    if (isLive && !wasLiveRef.current) {
      setStreamNonce((n) => n + 1);
    }
    wasLiveRef.current = isLive;
  }, [isLive]);

  useEffect(() => {
    if (view?.url) setAddr(view.url);
    else if (view && !view.open) setAddr("");
  }, [view?.url, view?.open]);

  useEffect(() => {
    if (!current || !view?.available || view.open || running || busy || !openBrowserOnFirstVisit(current)) return;
    setAddr(DEFAULT_URL);
    void run("goto", { url: DEFAULT_URL });
  }, [current, view, running, busy]);

  useEffect(() => {
    if (!dialogOpen) {
      if (!busy) setExpanded(false);
      return;
    }
    const opener = document.activeElement;
    const modal = modalRef.current;
    modal?.querySelector<HTMLElement>(".br-back-chat")?.focus();
    return () => {
      if (opener instanceof HTMLElement && opener.isConnected && !getState().dialog) {
        opener.focus();
      }
    };
  }, [dialogOpen]);

  const queueInput = useCallback(
    (ev: BrowserInputPayload) => {
      if (!current) return;
      inputQueueRef.current = inputQueueRef.current
        .catch(() => undefined)
        .then(async () => {
          await server.browserInput(current, ev);
        })
        .catch(() => undefined);
    },
    [current],
  );

  const nudgeTakeover = useCallback(() => {
    setPulseTakeover(true);
    window.setTimeout(() => setPulseTakeover(false), 650);
  }, []);

  const toViewport = useCallback(
    (clientX: number, clientY: number): { x: number; y: number } => {
      const el = imgRef.current;
      if (!el) return { x: Math.round(vpW / 2), y: Math.round(vpH / 2) };
      const rect = el.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return { x: Math.round(vpW / 2), y: Math.round(vpH / 2) };
      const x = Math.max(0, Math.min(vpW - 1, Math.round(((clientX - rect.left) / rect.width) * vpW)));
      const y = Math.max(0, Math.min(vpH - 1, Math.round(((clientY - rect.top) / rect.height) * vpH)));
      return { x, y };
    },
    [vpW, vpH],
  );

  useEffect(() => {
    const canvas = canvasEl;
    if (!canvas || !dialogOpen || !isLive) return;
    const onNativeWheel = (e: globalThis.WheelEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (!canInteract) {
        nudgeTakeover();
        return;
      }
      const scale = e.deltaMode === 1 ? 40 : e.deltaMode === 2 ? 600 : 1;
      const pt = toViewport(e.clientX, e.clientY);
      const st = wheelRef.current;
      st.dx += e.deltaX * scale;
      st.dy += e.deltaY * scale;
      st.x = pt.x;
      st.y = pt.y;
      if (!st.raf) {
        st.raf = window.requestAnimationFrame(() => {
          const dx = Math.round(st.dx);
          const dy = Math.round(st.dy);
          const { x, y } = st;
          st.dx = 0;
          st.dy = 0;
          st.raf = 0;
          if (dx !== 0 || dy !== 0) {
            queueInput({ type: "wheel", x, y, deltaX: dx, deltaY: dy, modifiers: modMask(e) });
          }
        });
      }
    };
    canvas.addEventListener("wheel", onNativeWheel, { passive: false });
    return () => {
      canvas.removeEventListener("wheel", onNativeWheel);
      if (wheelRef.current.raf) {
        window.cancelAnimationFrame(wheelRef.current.raf);
        wheelRef.current.raf = 0;
      }
    };
  }, [canvasEl, dialogOpen, isLive, canInteract, toViewport, queueInput, nudgeTakeover]);

  useEffect(() => {
    const onWindowMouseUp = (e: globalThis.MouseEvent) => {
      if (!draggingRef.current) return;
      draggingRef.current = false;
      const pt = toViewport(e.clientX, e.clientY);
      queueInput({
        type: "mouse_up",
        x: pt.x,
        y: pt.y,
        button: mouseButtonName(e.button),
        clickCount: 1,
        modifiers: modMask(e),
      });
    };
    window.addEventListener("mouseup", onWindowMouseUp);
    return () => window.removeEventListener("mouseup", onWindowMouseUp);
  }, [toViewport, queueInput]);

  const onDialogKey = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      setExpanded(false);
      return;
    }
    if (event.key !== "Tab" || event.target === canvasRef.current) return;
    const focusable = modalRef.current?.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled)");
    if (!focusable?.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey ? document.activeElement === first : document.activeElement === last) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  };

  const run = async (action: string, extra?: { url?: string; x?: number; y?: number; deltaY?: number; index?: number }) => {
    if (!current || busy) return;
    noteBrowserVisit(current);
    setBusy(true);
    setError("");
    try {
      const next = await server.browserAction(current, { action, ...extra });
      if (getState().machine === machine && getState().current === current) {
        setState({ browserView: next });
        if (action === "close" || !next.open) setExpanded(false);
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  const toggleTakeover = async (active: boolean, openModal = false) => {
    if (!current || takeoverBusy) return;
    noteBrowserVisit(current);
    if (openModal) setExpanded(true);
    setTakeoverBusy(true);
    setError("");
    try {
      const next = await server.browserTakeover(current, active, "user");
      if (getState().machine === machine && getState().current === current) {
        setState({ browserView: next });
      }
      if (active) {
        window.requestAnimationFrame(() => canvasRef.current?.focus());
      }
    } catch (e) {
      setError(errorText(e));
    } finally {
      setTakeoverBusy(false);
    }
  };

  const submitUrl = (raw = addr) => {
    const u = raw.trim();
    if (!u) return;
    const normalized = /^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(u) ? u : `https://${u}`;
    setAddr(normalized);
    void run("goto", { url: normalized });
  };

  const onLegacyClick = (e: MouseEvent<HTMLImageElement>) => {
    if (isLive || !imgRef.current || busy) return;
    const { x, y } = toViewport(e.clientX, e.clientY);
    void run("click", { x, y });
  };

  const onLegacyWheel = (e: WheelEvent<HTMLImageElement>) => {
    if (isLive || busy || Math.abs(e.deltaY) < 20) return;
    void run("scroll", { deltaY: e.deltaY > 0 ? 520 : -520 });
  };

  const onCanvasMouseDown = (e: MouseEvent<HTMLImageElement>) => {
    if (!isLive || e.button > 2) return;
    e.preventDefault();
    canvasRef.current?.focus();
    if (!canInteract) {
      nudgeTakeover();
      return;
    }
    draggingRef.current = true;
    const { x, y } = toViewport(e.clientX, e.clientY);
    queueInput({
      type: "mouse_down",
      x,
      y,
      button: mouseButtonName(e.button),
      clickCount: Math.max(1, e.detail || 1),
      modifiers: modMask(e),
    });
  };

  const onCanvasMouseMove = (e: MouseEvent<HTMLImageElement>) => {
    if (!isLive || !canInteract) return;
    const now = performance.now();
    const minInterval = draggingRef.current ? 24 : 55;
    if (now - lastMoveAtRef.current < minInterval) return;
    lastMoveAtRef.current = now;
    const { x, y } = toViewport(e.clientX, e.clientY);
    queueInput({ type: "mouse_move", x, y, modifiers: modMask(e) });
  };

  const onCanvasMouseUp = (e: MouseEvent<HTMLImageElement>) => {
    if (!isLive || !draggingRef.current) return;
    draggingRef.current = false;
    const { x, y } = toViewport(e.clientX, e.clientY);
    queueInput({
      type: "mouse_up",
      x,
      y,
      button: mouseButtonName(e.button),
      clickCount: Math.max(1, e.detail || 1),
      modifiers: modMask(e),
    });
  };

  const onCanvasKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!isLive || e.key === "Escape") return;
    if (!canInteract) {
      nudgeTakeover();
      return;
    }
    if (e.nativeEvent.isComposing) return;
    // Allow Ctrl+V / Cmd+V to trigger onPaste so clipboard text forwards cleanly
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "v") return;

    if (e.key.length === 1 && !e.ctrlKey && !e.altKey && !e.metaKey) {
      e.preventDefault();
      e.stopPropagation();
      queueInput({ type: "insert_text", text: e.key });
      return;
    }
    const mapped = mapPlaywrightKey(e);
    if (mapped) {
      e.preventDefault();
      e.stopPropagation();
      queueInput({ type: "press", key: mapped });
    }
  };

  const onCanvasPaste = (e: ClipboardEvent<HTMLDivElement>) => {
    if (!isLive || !canInteract) return;
    const txt = e.clipboardData?.getData("text");
    if (txt) {
      e.preventDefault();
      e.stopPropagation();
      queueInput({ type: "insert_text", text: txt });
    }
  };

  const onCanvasCompositionEnd = (e: CompositionEvent<HTMLDivElement>) => {
    if (!isLive || !canInteract) return;
    if (e.data) {
      queueInput({ type: "insert_text", text: e.data });
    }
  };

  const submitAgentPrompt = async () => {
    const text = prompt.trim();
    if (!text || running || !hasModel || sending) return;
    setSending(true);
    setError("");
    if (await send(text)) setPrompt((value) => (value.trim() === text ? "" : value));
    else setError(getState().errorAlert || t("Could not send message"));
    setSending(false);
  };

  const shotSrc = current && view?.hasScreenshot ? browserShotURL(current, view.updated || 0) : "";
  const streamSrc = current && isLive ? browserStreamURL(current, streamNonce) : "";
  const cardImgSrc = !dialogOpen ? streamSrc || shotSrc : shotSrc;
  const modalImgSrc = streamSrc || shotSrc;

  const tabs = view?.tabs?.length
    ? view.tabs
    : view?.open
      ? [{ index: 0, url: view.url || "", title: view.title || view.url || t("Browser"), active: true }]
      : [];

  return (
    <div className="br-panel">
      <form
        className="br-bar"
        dir="ltr"
        onSubmit={(e) => {
          e.preventDefault();
          submitUrl();
        }}
      >
        <input
          type="text"
          value={addr}
          disabled={busy || (view !== null && !view.available)}
          placeholder={DEFAULT_URL}
          aria-label={t("Browser address")}
          spellCheck={false}
          onChange={(e) => setAddr(e.target.value)}
        />
        <button type="submit" className="br-go" disabled={busy || !addr.trim()} title={t("Open web address")} aria-label={t("Open web address")}>
          {busy ? <span className="spin" /> : <Icon name="arrowRight" size={13} />}
        </button>
        {view?.open && (
          <button type="button" className="br-ico-btn" disabled={busy} title={t("Refresh")} onClick={() => void run("reload")}>
            <Icon name="refresh" size={13} />
          </button>
        )}
      </form>

      {error && !dialogOpen && (
        <div className="br-err" role="alert" dir="auto">
          {error}
        </div>
      )}

      {view && !view.available && (
        <div className="comp-empty">
          <div className="comp-empty-title">{t("Browser runtime unavailable")}</div>
          <p>{t("Install Playwright & Chromium so Macron can browse and stream live pages here.")}</p>
        </div>
      )}

      {(!view || view.available) && !view?.open && (
        busy ? (
          <div className="br-card br-loading-card">
            <div className="br-shot-wrap br-loading-shot">
              <div className="br-skeleton" />
              <div className="br-loading-badge">
                <span className="wave-dots" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
                <span className="shimmer-text">{t("Opening…")}</span>
              </div>
            </div>
            <div className="br-meta">
              <div className="br-skel-line w60" />
            </div>
          </div>
        ) : (
          <div className="br-empty">
            <div className="br-empty-ico">
              <Icon name="globe" size={20} />
            </div>
            <div className="comp-empty-title">{t("Live Browser")}</div>
            <p>{t("Watch Macron browse in real time, or open Google to start.")}</p>
            <button type="button" className="btn sm br-open-google" disabled={busy} onClick={() => submitUrl(DEFAULT_URL)}>
              <Icon name="globe" size={13} />
              <span>{t("Open Google")}</span>
            </button>
          </div>
        )
      )}

      {view?.open && (
        <div className="br-card">
          {(agentActive || takeover) && (
            <div className={"br-card-status " + (takeover ? "user" : "agent")}>
              <span className="br-status-label">
                <span className="br-tab-dot busy" aria-hidden="true" />
                <span>{takeover ? t("You have control") : t("Macron is browsing…")}</span>
              </span>
              {takeover ? (
                <button type="button" className="br-takeover-mini primary" disabled={takeoverBusy} onClick={() => void toggleTakeover(false)}>
                  {t("Hand back")}
                </button>
              ) : (
                <button type="button" className="br-takeover-mini" disabled={takeoverBusy} onClick={() => void toggleTakeover(true, true)}>
                  {t("Take over")}
                </button>
              )}
            </div>
          )}
          <div className="br-shot-wrap" onClick={() => setExpanded(true)}>
            {cardImgSrc ? (
              <img
                src={cardImgSrc}
                alt={view.title || view.url || t("Browser preview")}
                className="br-shot"
                draggable={false}
              />
            ) : (
              <div className="br-shot-placeholder">
                <Icon name="globe" size={22} />
              </div>
            )}
            <button
              type="button"
              className="br-close"
              disabled={busy}
              title={t("Close")}
              aria-label={t("Close")}
              onClick={(e) => {
                e.stopPropagation();
                void run("close");
              }}
            >
              <Icon name="x" size={12} />
            </button>
            <button
              type="button"
              className="br-open-pill"
              aria-label={t("Open browser preview")}
              onClick={(e) => {
                e.stopPropagation();
                setExpanded(true);
              }}
            >
              <Icon name="open" size={12} />
              <span>{t("Open")}</span>
            </button>
          </div>
          <div className="br-card-title" dir="auto" title={view.url || view.title || ""}>
            {view.title || view.url || t("Browser")}
          </div>
        </div>
      )}

      {expanded && view?.open && (
        <div ref={modalRef} className="overlay br-overlay br-full" role="dialog" aria-modal="true" aria-label={t("Live Browser")} onKeyDown={onDialogKey}>
          <div className="br-shell">
            <div className="br-tab-strip" dir="ltr">
              <div className="br-tabs-left">
                {tabs.map((tab) => (
                  <div key={tab.index} className={"br-chrome-tab" + (tab.active ? " on" : "")} title={tab.url || ""}>
                    <button
                      type="button"
                      className="br-tab-hit"
                      disabled={busy}
                      onClick={() => {
                        if (!tab.active) void run("switch_tab", { index: tab.index });
                      }}
                    >
                      <span className={"br-tab-dot" + (tab.active && (busy || agentActive) ? " busy" : "")} aria-hidden="true" />
                      <span className="br-tab-label" dir="auto">
                        {tab.title || tab.url || t("Browser")}
                      </span>
                    </button>
                    <button
                      type="button"
                      className="br-tab-x"
                      disabled={busy}
                      title={tabs.length > 1 ? t("Close tab") : t("Close")}
                      aria-label={tabs.length > 1 ? t("Close tab") : t("Close")}
                      onClick={(e) => {
                        e.stopPropagation();
                        if (tabs.length > 1) void run("close_tab", { index: tab.index });
                        else void run("close");
                      }}
                    >
                      <Icon name="x" size={11} />
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  className="br-tab-add"
                  disabled={busy}
                  title={t("New tab")}
                  aria-label={t("New tab")}
                  onClick={() => void run(isLive ? "new_tab" : "goto", { url: DEFAULT_URL })}
                >
                  <Icon name="plus" size={14} />
                </button>
              </div>
              <button type="button" className="br-back-chat" onClick={() => setExpanded(false)}>
                <Icon name="arrowLeft" size={13} />
                <span>{t("Back to chat")}</span>
              </button>
            </div>

            <div className="br-nav-strip" dir="ltr">
              <div className="br-modal-nav">
                <button type="button" className="icon-btn sm" disabled={busy} title={t("Previous page")} onClick={() => void run("back")}>
                  <Icon name="arrowLeft" size={14} />
                </button>
                <button type="button" className="icon-btn sm" disabled={busy} title={t("Next page")} onClick={() => void run("forward")}>
                  <Icon name="arrowRight" size={14} />
                </button>
                <button type="button" className="icon-btn sm" disabled={busy} title={t("Refresh")} onClick={() => void run("reload")}>
                  <Icon name="refresh" size={14} />
                </button>
              </div>
              <form
                className="br-nav-addr"
                onSubmit={(e) => {
                  e.preventDefault();
                  submitUrl();
                }}
              >
                <Icon name="globe" size={13} className="br-addr-ico" />
                <input type="text" value={addr} disabled={busy} placeholder={DEFAULT_URL} aria-label={t("Browser address")} spellCheck={false} onChange={(e) => setAddr(e.target.value)} />
                {isLive && <span className="br-live-badge">LIVE</span>}
              </form>
            </div>

            {isLive && (agentActive || takeover) && (
              <div className={"br-takeover-bar " + (takeover ? "user" : "agent") + (pulseTakeover ? " pulse" : "")} dir="auto">
                <span className="br-takeover-msg">
                  <span className="br-tab-dot busy" aria-hidden="true" />
                  <span>
                    {takeover
                      ? t("You have control — scroll, click, or type directly")
                      : t("Macron is browsing this page")}
                  </span>
                </span>
                {takeover ? (
                  <button type="button" className="btn sm primary" disabled={takeoverBusy} onClick={() => void toggleTakeover(false)}>
                    {t("Hand back to Macron")}
                  </button>
                ) : (
                  <button type="button" className="btn sm" disabled={takeoverBusy} onClick={() => void toggleTakeover(true)}>
                    {t("Take over")}
                  </button>
                )}
              </div>
            )}

            {error && <div className="br-err" role="alert" dir="auto">{error}</div>}

            <div className="br-modal-stage br-full-stage">
              {(busy || agentActive) && <div className="br-progress" aria-hidden="true" />}
              {modalImgSrc ? (
                <div
                  ref={(el) => {
                    canvasRef.current = el;
                    setCanvasEl(el);
                  }}
                  tabIndex={0}
                  className={"br-canvas" + (isLive && canInteract ? " interactive" : "") + (isLive && !canInteract ? " locked" : "")}
                  aria-label={t("Live Browser")}
                  onKeyDown={onCanvasKeyDown}
                  onPaste={onCanvasPaste}
                  onCompositionEnd={onCanvasCompositionEnd}
                >
                  <img
                    ref={imgRef}
                    src={modalImgSrc}
                    alt={view.title || view.url || t("Browser preview")}
                    className={"br-modal-img" + (busy ? " busy" : "")}
                    draggable={false}
                    onMouseDown={isLive ? onCanvasMouseDown : undefined}
                    onMouseMove={isLive ? onCanvasMouseMove : undefined}
                    onMouseUp={isLive ? onCanvasMouseUp : undefined}
                    onClick={!isLive ? onLegacyClick : undefined}
                    onWheel={!isLive ? onLegacyWheel : undefined}
                    onContextMenu={isLive ? (e) => e.preventDefault() : undefined}
                  />
                </div>
              ) : (
                <div className="empty-row">{t("Loading…")}</div>
              )}

              {pillHidden ? (
                <button
                  type="button"
                  className="br-agent-fab"
                  title={t("Ask Macron to act on this page…")}
                  aria-label={t("Ask Macron to act on this page…")}
                  onClick={() => setPillHidden(false)}
                >
                  <StarBuddy variant="stage" size="xs" mood={mood} />
                </button>
              ) : (
                <form
                  className="br-agent-pill"
                  onSubmit={(e) => {
                    e.preventDefault();
                    void submitAgentPrompt();
                  }}
                >
                  <span className="br-agent-buddy" aria-hidden="true">
                    <StarBuddy variant="stage" size="xs" mood={mood} />
                  </span>
                  <input
                    type="text"
                    value={prompt}
                    disabled={!hasModel}
                    dir="auto"
                    aria-label={t("Ask Macron to act on this page…")}
                    placeholder={hasModel ? t("Ask Macron to act on this page…") : t("Connect a model")}
                    onChange={(e) => setPrompt(e.target.value)}
                  />
                  <button
                    type="button"
                    className="br-agent-hide"
                    title={t("Hide")}
                    aria-label={t("Hide")}
                    onClick={() => setPillHidden(true)}
                  >
                    <Icon name="down" size={13} />
                  </button>
                  {running ? (
                    <button type="button" className="br-agent-send stop" title={t("Stop")} aria-label={t("Stop")} onClick={() => void stop()}>
                      <Icon name="square" size={11} />
                    </button>
                  ) : (
                    <button
                      type="submit"
                      className="br-agent-send"
                      disabled={!prompt.trim() || !hasModel || sending}
                      title={t("Send")}
                      aria-label={t("Send")}
                    >
                      <Icon name="arrowUp" size={14} />
                    </button>
                  )}
                </form>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
