import { useEffect, useState } from "react";
import { setDensity, setLang, setMotion, setTheme } from "../../actions";
import { useHomePath, useMod } from "../../hooks";
import { useT } from "../../i18n";
import type { Lang } from "../../i18n";
import { Icon } from "../../icons";
import { applyPalette, clearPaletteOverrides, getSavedPaletteId, loadPalettes } from "../../palettes";
import { useStore } from "../../store";
import type { Density, MotionPref, Theme } from "../../store";
import type { ThemePalette } from "../../types";
import { StarBuddy } from "../StarBuddy";
import type { StarBuddyMood } from "../StarBuddy";

export function GeneralSettingsPage() {
  const t = useT();
  const mod = useMod();
  const info = useStore((s) => s.info);
  const mode = useStore((s) => s.mode);
  const projects = useStore((s) => s.projects);

  const shortcuts: [string, string[]][] = [
    [t("Send"), ["Enter"]],
    [t("New line"), ["Shift", "Enter"]],
    [t("Stop"), ["Esc"]],
    [t("Command palette"), [mod, "K"]],
    [t("Search chats"), [mod, "Shift", "F"]],
    [t("New conversation"), [mod, "N"]],
    [t("Plan mode"), [mod, "Shift", "P"]],
    [t("Open folder"), [mod, "O"]],
    [t("Collapse sidebar"), [mod, "\\"]],
    [t("Agent Companion"), [mod, "."]],
    [t("Settings"), [mod, ","]],
  ];

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("General")}</h1>
        <p>{t("Overview of your active workspace and keyboard shortcuts.")}</p>
      </header>

      <div className="stg-summary-strip">
        <div className="stg-metric">
          <span className="stg-metric-label">{t("Workspaces")}</span>
          <strong dir="ltr">{projects.length}</strong>
        </div>
        <div className="stg-metric">
          <span className="stg-metric-label">{t("Active model")}</span>
          <strong dir="ltr">{info.model || "—"}</strong>
        </div>
        <div className="stg-metric">
          <span className="stg-metric-label">{t("Execution mode")}</span>
          <strong>{info.sandboxed ? t("Sandbox") : info.executionMode || "best-effort"}</strong>
        </div>
        <div className="stg-metric">
          <span className="stg-metric-label">{t("Mode")}</span>
          <strong>{mode === "plan" ? t("Plan") : t("Build")}</strong>
        </div>
      </div>

      <section className="stg-block">
        <div className="stg-block-head">
          <h2>{t("Keyboard")}</h2>
        </div>
        <dl className="keys stg-keys">
          {shortcuts.map(([label, combo]) => (
            <div key={label}>
              <dt>{label}</dt>
              <dd dir="ltr">
                {combo.map((k) => (
                  <kbd key={k}>{k}</kbd>
                ))}
              </dd>
            </div>
          ))}
        </dl>
      </section>
    </div>
  );
}

const PREVIEW_MOODS: StarBuddyMood[] = ["idle", "typing", "working", "streaming", "asking", "success", "error", "offline", "speaking"];

export function AppearanceSettingsPage() {
  const t = useT();
  const themesDirHint = useHomePath(".macron/themes/*.json");
  const theme = useStore((s) => s.theme);
  const density = useStore((s) => s.density);
  const motion = useStore((s) => s.motion);

  const [palettes, setPalettes] = useState<ThemePalette[]>([]);
  const [activePalette, setActivePalette] = useState<string>(() => getSavedPaletteId());
  const [previewMood, setPreviewMood] = useState<StarBuddyMood>("idle");
  const [voiceSim, setVoiceSim] = useState<number>(0.55);

  useEffect(() => {
    void loadPalettes().then((r) => setPalettes(r.themes || []));
  }, []);

  const pickThemeMode = (next: Theme) => {
    clearPaletteOverrides();
    applyPalette(null);
    setActivePalette("");
    setTheme(next);
  };

  const pickPalette = (p: ThemePalette) => {
    setTheme(p.appearance);
    applyPalette(p);
    setActivePalette(p.id);
  };

  const moodLabels: Record<StarBuddyMood, string> = {
    idle: t("Idle"),
    typing: t("Typing"),
    working: t("Working"),
    streaming: t("Streaming"),
    asking: t("Approval"),
    success: t("Success"),
    error: t("Error"),
    offline: t("Offline"),
    speaking: t("Speaking"),
  };

  const densitySpec =
    density === "compact"
      ? "13px · 11px gap · 820px col"
      : density === "spacious"
        ? "15px · 30px gap · 680px col"
        : "14px · 20px gap · 740px col";

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Appearance & Theme")}</h1>
        <p>{t("Customize theme, color palette, layout density, motion, and StarBuddy.")}</p>
      </header>

      {/* Interactive StarBuddy Live Preview Studio */}
      <section className="stg-buddy-studio">
        <div className="stg-buddy-stage">
          <StarBuddy
            variant="stage"
            size="xl"
            mood={previewMood}
            interactive
            trackPointer
            audioLevel={previewMood === "speaking" ? voiceSim : 0}
            title={`StarBuddy · ${moodLabels[previewMood]}`}
          />
          <div className="stg-buddy-info">
            <div className="stg-buddy-title">{t("StarBuddy live preview")}</div>
            <p className="stg-buddy-desc">
              {t("Interact with StarBuddy — move your cursor to test eye tracking, click for spring reaction, or preview every behavioral state.")}
            </p>
            <div className="stg-mood-pills" role="radiogroup" aria-label="StarBuddy moods">
              {PREVIEW_MOODS.map((m) => (
                <button
                  key={m}
                  type="button"
                  role="radio"
                  aria-checked={previewMood === m}
                  className={"stg-pill" + (previewMood === m ? " on" : "")}
                  onClick={() => setPreviewMood(m)}
                >
                  {moodLabels[m]}
                </button>
              ))}
            </div>
            {previewMood === "speaking" && (
              <label className="stg-audio-slider">
                <span>{t("Audio amplitude")}</span>
                <input
                  type="range"
                  min={0.05}
                  max={1}
                  step={0.05}
                  value={voiceSim}
                  onChange={(e) => setVoiceSim(Number(e.target.value))}
                />
              </label>
            )}
          </div>
        </div>
      </section>

      {/* Theme Mode Selector */}
      <section className="stg-block">
        <div className="stg-row-setting">
          <div>
            <div className="stg-row-title">{t("Theme mode")}</div>
            <div className="stg-row-help">{t("System")} · {t("Dark")} · {t("Light")}</div>
          </div>
          <div className="choice" role="radiogroup">
            {(
              [
                ["system", t("System"), "monitor"],
                ["dark", t("Dark"), "moon"],
                ["light", t("Light"), "sun"],
              ] as const
            ).map(([v, lbl, ico]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={theme === v && !activePalette}
                className={theme === v && !activePalette ? "on" : ""}
                onClick={() => pickThemeMode(v)}
              >
                <Icon name={ico} size={13} />
                <span>{lbl}</span>
              </button>
            ))}
          </div>
        </div>
      </section>

      {/* Live Visual Palette Cards */}
      {palettes.length > 0 && (
        <section className="stg-block">
          <div className="stg-block-head">
            <h2>{t("Color palettes")}</h2>
            <span className="stg-mono-hint" dir="ltr">
              {themesDirHint}
            </span>
          </div>
          <div className="stg-palette-grid" role="radiogroup" aria-label={t("Color palettes")}>
            {palettes.map((p) => {
              const selected = activePalette === p.id;
              return (
                <button
                  key={p.id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  className={"stg-pal-card" + (selected ? " on" : "")}
                  onClick={() => pickPalette(p)}
                >
                  <div
                    className="stg-pal-preview"
                    style={{ background: p.colors.frame || p.swatches[0] }}
                    aria-hidden="true"
                  >
                    <div className="pal-side" style={{ background: p.colors.sidebar || p.swatches[0] }}>
                      <span className="pal-dot" style={{ background: p.colors.accent || p.swatches[2] }} />
                      <span className="pal-line" />
                      <span className="pal-line short" />
                    </div>
                    <div className="pal-canvas" style={{ background: p.colors.canvas || p.swatches[1] }}>
                      <span className="pal-bubble" style={{ background: p.colors.surface || p.swatches[1] }} />
                      <span className="pal-pill" style={{ background: p.colors.accent || p.swatches[2] }} />
                    </div>
                  </div>
                  <div className="stg-pal-foot">
                    <span className="stg-pal-name">{p.name}</span>
                    <span className="stg-pal-mode">{p.appearance === "light" ? t("Light") : t("Dark")}</span>
                  </div>
                </button>
              );
            })}
          </div>
        </section>
      )}

      {/* Density & Motion */}
      <section className="stg-block">
        <div className="stg-row-setting">
          <div>
            <div className="stg-row-title">{t("Interface density")}</div>
            <div className="stg-row-help">{t("Adjust spacing, typography scale, and reading measure across the entire app.")}</div>
          </div>
          <div className="choice" role="radiogroup">
            {(
              [
                ["compact", t("Compact")],
                ["comfortable", t("Comfortable")],
                ["spacious", t("Spacious")],
              ] as [Density, string][]
            ).map(([v, lbl]) => (
              <button key={v} type="button" role="radio" aria-checked={density === v} className={density === v ? "on" : ""} onClick={() => setDensity(v)}>
                {lbl}
              </button>
            ))}
          </div>
        </div>

        {/* Live Density Specimen */}
        <div className="stg-density-preview" aria-live="polite">
          <div className="dp-head">
            <span>{t("Live layout preview")}</span>
            <span className="dp-tag" dir="ltr">
              {densitySpec}
            </span>
          </div>
          <div className="dp-user">{t("Inspect the build pipeline and fix any flaky tests.")}</div>
          <div className="dp-tool" dir="ltr">
            <span className="dp-ok">
              <Icon name="check" size={12} />
            </span>
            <span>run_command</span>
            <code>go test ./...</code>
          </div>
          <div className="dp-reply">{t("All 42 tests pass. Spacing and typography update instantly across chat, sidebar, and settings.")}</div>
        </div>

        <div className="stg-row-setting">
          <div>
            <div className="stg-row-title">{t("Motion & animations")}</div>
            <div className="stg-row-help">{t("Full motion")} · {t("Reduced motion")}</div>
          </div>
          <div className="choice" role="radiogroup">
            {(
              [
                ["full", t("Full motion")],
                ["reduced", t("Reduced motion")],
              ] as [MotionPref, string][]
            ).map(([v, lbl]) => (
              <button key={v} type="button" role="radio" aria-checked={motion === v} className={motion === v ? "on" : ""} onClick={() => setMotion(v)}>
                {lbl}
              </button>
            ))}
          </div>
        </div>
      </section>
    </div>
  );
}

export function LanguageSettingsPage() {
  const t = useT();
  const lang = useStore((s) => s.lang);

  const options: { id: Lang; title: string; subtitle: string; dir: "ltr" | "rtl"; sample: string }[] = [
    {
      id: "en",
      title: "English",
      subtitle: "Left-to-Right (LTR) · Geist Variable",
      dir: "ltr",
      sample: "Macron reads your workspace, writes clean code, and runs tools with precision.",
    },
    {
      id: "fa",
      title: "فارسی (Persian)",
      subtitle: "راست‌به‌چپ (RTL) · Estedad Variable",
      dir: "rtl",
      sample: "مکرون فضای کاری شما را می‌خواند، کد دقیق می‌نویسد و ابزارها را هوشمندانه اجرا می‌کند.",
    },
  ];

  return (
    <div className="stg-page">
      <header className="stg-head">
        <h1>{t("Language & Direction")}</h1>
        <p>{t("Macron adapts layout direction, bidirectional Markdown prose, and Estedad + Geist variable typography automatically.")}</p>
      </header>

      <div className="stg-lang-grid" role="radiogroup" aria-label={t("Language")}>
        {options.map((opt) => (
          <button
            key={opt.id}
            type="button"
            role="radio"
            aria-checked={lang === opt.id}
            className={"stg-lang-card" + (lang === opt.id ? " on" : "")}
            onClick={() => setLang(opt.id)}
          >
            <div className="stg-lang-top">
              <span className="stg-lang-title">{opt.title}</span>
              <span className="stg-lang-check">{lang === opt.id && <Icon name="check" size={14} />}</span>
            </div>
            <div className="stg-lang-sub">{opt.subtitle}</div>
            <div className="stg-lang-sample" dir={opt.dir}>
              {opt.sample}
            </div>
          </button>
        ))}
      </div>

      <section className="stg-block">
        <div className="stg-block-head">
          <h2>{t("Direction & Typography")}</h2>
        </div>
        <div className="stg-type-specimen">
          <div className="spec-row">
            <span className="spec-tag">Geist Variable</span>
            <span className="spec-sample" dir="ltr">
              The quick brown fox jumps over 0123456789
            </span>
          </div>
          <div className="spec-row">
            <span className="spec-tag">Estedad Variable</span>
            <span className="spec-sample" dir="rtl">
              طراحی دقیق و خوانا برای متون ترکیبی فارسی و English در محیط دسکتاپ
            </span>
          </div>
          <div className="spec-row">
            <span className="spec-tag">JetBrains Mono</span>
            <span className="spec-sample mono" dir="ltr">
              {"const macron = await agent.run({ mode: \"build\", effort: \"high\" });"}
            </span>
          </div>
        </div>
      </section>
    </div>
  );
}
