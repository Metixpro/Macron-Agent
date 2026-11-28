import { useEffect, useState } from "react";
import { useT } from "../i18n";
import type { BrowserDialog } from "../types";

export function BrowserDialogSheet({
  dialog,
  busy,
  onResolve,
}: {
  dialog: BrowserDialog;
  busy?: boolean;
  onResolve: (accept: boolean, promptText?: string) => void;
}) {
  const t = useT();
  const [val, setVal] = useState(dialog.defaultValue || "");

  useEffect(() => {
    setVal(dialog.defaultValue || "");
  }, [dialog.id, dialog.defaultValue]);

  const isAlert = dialog.type === "alert";
  const isPrompt = dialog.type === "prompt";

  return (
    <div className="br-dlg-backdrop" role="dialog" aria-modal="true" aria-label={t("Page dialog")}>
      <form
        className="br-dlg-sheet"
        onSubmit={(e) => {
          e.preventDefault();
          onResolve(true, isPrompt ? val : "");
        }}
      >
        <div className="br-dlg-badge">{dialog.type.toUpperCase()}</div>
        <div className="br-dlg-msg" dir="auto">
          {dialog.message || t("This page is asking for confirmation.")}
        </div>
        {isPrompt && (
          <input
            type="text"
            className="br-dlg-input"
            autoFocus
            dir="auto"
            value={val}
            onChange={(e) => setVal(e.target.value)}
          />
        )}
        <div className="br-dlg-actions">
          {!isAlert && (
            <button type="button" className="btn ghost sm" disabled={busy} onClick={() => onResolve(false)}>
              {t("Cancel")}
            </button>
          )}
          <button type="submit" className="btn primary sm" disabled={busy} autoFocus={!isPrompt}>
            {t("OK")}
          </button>
        </div>
      </form>
    </div>
  );
}
