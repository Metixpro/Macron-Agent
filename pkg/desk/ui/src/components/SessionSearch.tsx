import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { closeDialog, openProject, switchThread } from "../actions";
import { server } from "../api";
import { ago } from "../format";
import { useT } from "../i18n";
import { Icon } from "../icons";
import { getState } from "../store";
import type { SearchHit } from "../types";

function Highlight({ text, query }: { text: string; query: string }) {
  const terms = query
    .trim()
    .split(/\s+/)
    .filter((w) => w.length >= 2)
    .map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  if (!terms.length) return <>{text}</>;
  const re = new RegExp(`(${terms.join("|")})`, "ig");
  const parts = text.split(re);
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <mark key={i} className="srch-mark">
            {part}
          </mark>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}

export function SessionSearch() {
  const t = useT();
  const [q, setQ] = useState("");
  const [hits, setHits] = useState<SearchHit[]>([]);
  const [loading, setLoading] = useState(false);
  const [sel, setSel] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const query = q.trim();
    if (!query) {
      setHits([]);
      setLoading(false);
      setSel(0);
      return;
    }
    let alive = true;
    setLoading(true);
    const timer = window.setTimeout(() => {
      server
        .searchSessions(query)
        .then((r) => {
          if (alive) {
            setHits(r.hits || []);
            setSel(0);
          }
        })
        .catch(() => {
          if (alive) setHits([]);
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    }, 140);
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [q]);

  const jump = async (hit: SearchHit) => {
    closeDialog();
    if (getState().current !== hit.projectId) {
      if (!(await openProject(hit.projectId))) return;
    }
    if (hit.threadId && getState().activeThread !== hit.threadId) {
      await switchThread(hit.threadId);
    }
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" && hits.length) {
      e.preventDefault();
      setSel((i) => (i + 1) % hits.length);
    } else if (e.key === "ArrowUp" && hits.length) {
      e.preventDefault();
      setSel((i) => (i + hits.length - 1) % hits.length);
    } else if (e.key === "Enter" && hits[sel]) {
      e.preventDefault();
      void jump(hits[sel]);
    }
  };

  return (
    <>
      <div className="palette-input">
        <Icon name="search" size={16} />
        <input
          autoFocus
          spellCheck={false}
          placeholder={t("Search all conversations across workspaces…")}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
        />
        {q && (
          <button type="button" className="icon-btn" title={t("Clear")} onClick={() => setQ("")}>
            <Icon name="x" size={14} />
          </button>
        )}
      </div>
      <div className="palette-list srch-list" ref={listRef}>
        {!q.trim() && <div className="empty-row">{t("Type keywords to search every workspace and thread.")}</div>}
        {q.trim() && loading && <div className="empty-row">{t("Searching…")}</div>}
        {q.trim() && !loading && hits.length === 0 && <div className="empty-row">{t("No matching messages found.")}</div>}
        {hits.map((h, i) => (
          <button
            key={`${h.projectId}:${h.threadId}:${h.at}:${i}`}
            type="button"
            className={"srch-hit" + (i === sel ? " sel" : "")}
            onMouseMove={() => i !== sel && setSel(i)}
            onClick={() => void jump(h)}
          >
            <div className="srch-head">
              <span className="srch-proj">{h.projectName}</span>
              <span className="srch-sep">/</span>
              <span className="srch-thread" dir="auto">
                {h.threadTitle === "New conversation" ? t("New conversation") : h.threadTitle}
              </span>
              <span className="pv-pill api">{h.role === "user" ? t("You") : "Macron"}</span>
              {h.at > 0 && <span className="srch-ago">{ago(h.at)}</span>}
            </div>
            <div className="srch-snip" dir="auto">
              <Highlight text={h.snippet} query={q} />
            </div>
          </button>
        ))}
      </div>
      <div className="palette-foot">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> {t("move")}
        </span>
        <span>
          <kbd>Enter</kbd> {t("open conversation")}
        </span>
        <span>
          <kbd>Esc</kbd> {t("close")}
        </span>
      </div>
    </>
  );
}
