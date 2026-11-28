import { useEffect, useRef } from "react";
import { useT } from "../i18n";
import { Icon } from "../icons";
import type { BrowserContextMenuInfo } from "../types";

export interface ContextMenuState {
  clientX: number;
  clientY: number;
  info: BrowserContextMenuInfo;
}

export function BrowserContextMenu({
  menu,
  onClose,
  onAction,
}: {
  menu: ContextMenuState;
  onClose: () => void;
  onAction: (action: string, payload?: Record<string, unknown>) => void;
}) {
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onClose();
      }
    };
    window.addEventListener("mousedown", onDown, true);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("mousedown", onDown, true);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [onClose]);

  const left = Math.min(menu.clientX, Math.max(8, window.innerWidth - 220));
  const top = Math.min(menu.clientY, Math.max(8, window.innerHeight - 260));
  const { linkUrl, selectedText, isEditable } = menu.info;

  const act = (action: string, payload?: Record<string, unknown>) => {
    onClose();
    onAction(action, payload);
  };

  return (
    <div ref={ref} className="br-ctx-menu" role="menu" style={{ left, top }}>
      {linkUrl && (
        <>
          <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("open_new_tab", { url: linkUrl })}>
            <Icon name="open" size={13} />
            <span>{t("Open link in new tab")}</span>
          </button>
          <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("copy_text", { text: linkUrl })}>
            <Icon name="copy" size={13} />
            <span>{t("Copy link address")}</span>
          </button>
          <div className="br-ctx-sep" />
        </>
      )}
      {selectedText && (
        <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("copy_text", { text: selectedText })}>
          <Icon name="copy" size={13} />
          <span>{t("Copy")}</span>
        </button>
      )}
      {isEditable && (
        <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("paste")}>
          <Icon name="clipboard" size={13} />
          <span>{t("Paste")}</span>
        </button>
      )}
      {(selectedText || isEditable) && <div className="br-ctx-sep" />}
      <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("back")}>
        <Icon name="arrowLeft" size={13} />
        <span>{t("Back")}</span>
      </button>
      <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("forward")}>
        <Icon name="arrowRight" size={13} />
        <span>{t("Forward")}</span>
      </button>
      <button type="button" className="br-ctx-item" role="menuitem" onClick={() => act("reload")}>
        <Icon name="refresh" size={13} />
        <span>{t("Reload")}</span>
      </button>
    </div>
  );
}
