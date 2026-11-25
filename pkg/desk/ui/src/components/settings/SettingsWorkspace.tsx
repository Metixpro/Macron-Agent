import { useEffect, useMemo, useRef, useState } from "react";
import { closeDialog } from "../../actions";
import { useT } from "../../i18n";
import { Icon } from "../../icons";
import type { IconName } from "../../icons";
import type { SettingsSectionId } from "../../store";
import { ConnectionsSettingsPage, McpSettingsPage, ServersSettingsPage, SkillsSettingsPage } from "./CapabilitiesSettings";
import {
  MemorySettingsPage,
  MoaSettingsPage,
  ParametersSettingsPage,
  ProvidersSettingsPage,
  RoutingSettingsPage,
  SoulSettingsPage,
} from "./IntelligenceSettings";
import { ApprovalsSettingsPage, ExecutionSettingsPage } from "./SecuritySettings";
import { AboutSettingsPage, DiagnosticsSettingsPage, UpdatesSettingsPage } from "./SystemSettings";
import { AppearanceSettingsPage, GeneralSettingsPage, LanguageSettingsPage } from "./WorkspaceSettings";

interface NavItem {
  id: SettingsSectionId;
  group: "Workspace" | "Intelligence" | "Capabilities" | "Security" | "System";
  label: string;
  icon: IconName;
  keywords: string;
}

const NAV_ITEMS: NavItem[] = [
  // Workspace
  { id: "general", group: "Workspace", label: "General", icon: "sliders", keywords: "general keyboard shortcuts workspace عمومی میانبر" },
  { id: "appearance", group: "Workspace", label: "Appearance & Theme", icon: "palette", keywords: "theme dark light palette color density motion starbuddy ظاهر تم رنگ انیمیشن" },
  { id: "language", group: "Workspace", label: "Language & Direction", icon: "globe", keywords: "language persian english rtl ltr font زبان فارسی جهت" },
  // Intelligence
  { id: "providers", group: "Intelligence", label: "Models & Providers", icon: "chip", keywords: "models providers api key openai anthropic gemini deepseek ollama failover مدل پرووایدر کلید" },
  { id: "routing", group: "Intelligence", label: "Smart Routing", icon: "route", keywords: "routing smart light strong classifier مسیریابی هوشمند" },
  { id: "moa", group: "Intelligence", label: "Mixture of Agents", icon: "network", keywords: "moa voting council aggregator advisors ترکیب مدل رأی‌گیری" },
  { id: "parameters", group: "Intelligence", label: "Model Parameters", icon: "gauge", keywords: "effort reasoning context window temperature پارامتر دقت دما کانتکست" },
  { id: "soul", group: "Intelligence", label: "Personality & SOUL.md", icon: "sparkles", keywords: "soul personality prompt tone identity شخصیت هویت" },
  { id: "memory", group: "Intelligence", label: "Memory", icon: "brain", keywords: "memory notes user facts حافظه یادداشت" },
  // Capabilities
  { id: "mcp", group: "Capabilities", label: "Tools & MCP", icon: "plug", keywords: "mcp servers plugins tools ابزار پلاگین" },
  { id: "skills", group: "Capabilities", label: "Skills", icon: "book", keywords: "skills capabilities مهارت" },
  { id: "connections", group: "Capabilities", label: "Connected Services", icon: "link", keywords: "connections telegram github services سرویس اتصال" },
  { id: "servers", group: "Capabilities", label: "SSH & Remote", icon: "server", keywords: "ssh remote vps wsl server سرور ریموت" },
  // Security
  { id: "execution", group: "Security", label: "Execution & Sandbox", icon: "shield", keywords: "execution sandbox bubblewrap windows job isolation سندباکس اجرا" },
  { id: "approvals", group: "Security", label: "Approvals & YOLO", icon: "lock", keywords: "approvals smart manual yolo safety command تأیید امنیت" },
  // System
  { id: "diagnostics", group: "System", label: "Diagnostics", icon: "activity", keywords: "diagnostics status logs export telemetry تشخیصی وضعیت گزارش" },
  { id: "updates", group: "System", label: "Updates", icon: "download", keywords: "updates version github release آپدیت به‌روزرسانی نسخه" },
  { id: "about", group: "System", label: "About Macron", icon: "info", keywords: "about spectordev license version درباره مکرون" },
];

const GROUPS: NavItem["group"][] = ["Workspace", "Intelligence", "Capabilities", "Security", "System"];

export function SettingsWorkspace({ initialSection = "general" }: { initialSection?: SettingsSectionId }) {
  const t = useT();
  const [section, setSection] = useState<SettingsSectionId>(initialSection);
  const [query, setQuery] = useState("");
  const contentRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setSection(initialSection);
  }, [initialSection]);

  useEffect(() => {
    if (contentRef.current) contentRef.current.scrollTop = 0;
  }, [section]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return NAV_ITEMS;
    return NAV_ITEMS.filter((item) => {
      const hay = `${item.label} ${t(item.label)} ${item.group} ${t(item.group)} ${item.keywords}`.toLowerCase();
      return hay.includes(q);
    });
  }, [query, t]);

  useEffect(() => {
    if (query.trim() && filtered.length > 0 && !filtered.some((x) => x.id === section)) {
      setSection(filtered[0].id);
    }
  }, [query, filtered, section]);

  const renderPage = () => {
    switch (section) {
      case "general":
        return <GeneralSettingsPage />;
      case "appearance":
        return <AppearanceSettingsPage />;
      case "language":
        return <LanguageSettingsPage />;
      case "providers":
        return <ProvidersSettingsPage />;
      case "routing":
        return <RoutingSettingsPage />;
      case "moa":
        return <MoaSettingsPage />;
      case "parameters":
        return <ParametersSettingsPage />;
      case "soul":
        return <SoulSettingsPage />;
      case "memory":
        return <MemorySettingsPage />;
      case "mcp":
        return <McpSettingsPage />;
      case "skills":
        return <SkillsSettingsPage />;
      case "connections":
        return <ConnectionsSettingsPage />;
      case "servers":
        return <ServersSettingsPage />;
      case "execution":
        return <ExecutionSettingsPage />;
      case "approvals":
        return <ApprovalsSettingsPage />;
      case "diagnostics":
        return <DiagnosticsSettingsPage />;
      case "updates":
        return <UpdatesSettingsPage />;
      case "about":
        return <AboutSettingsPage />;
    }
  };

  return (
    <div className="settings-screen" role="dialog" aria-modal="true" aria-label={t("Settings")}>
      {/* Left Content Canvas (explicitly positioned on the left in both LTR and RTL) */}
      <div className="settings-content-pane">
        <div className="settings-topbar">
          <button type="button" className="stg-back-btn" onClick={closeDialog}>
            <Icon name="arrowLeft" size={14} />
            <span>{t("Back to workspace")}</span>
            <kbd>Esc</kbd>
          </button>
        </div>
        <div className="settings-scroll" ref={contentRef}>
          {renderPage()}
        </div>
      </div>

      {/* Right Navigation Rail (positioned on the right side with Quick Search) */}
      <aside className="settings-nav-rail" aria-label={t("Settings")}>
        <div className="stg-rail-head">
          <span className="stg-rail-title">{t("Settings")}</span>
          <button type="button" className="icon-btn sm" title="Close (Esc)" aria-label={t("Close")} onClick={closeDialog}>
            <Icon name="x" size={15} />
          </button>
        </div>

        <div className="stg-rail-search">
          <Icon name="search" size={13} />
          <input
            type="text"
            value={query}
            placeholder={t("Search settings…")}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button type="button" className="icon-btn sm" aria-label={t("Clear")} onClick={() => setQuery("")}>
              <Icon name="x" size={12} />
            </button>
          )}
        </div>

        <nav className="stg-rail-list">
          {filtered.length === 0 && <div className="empty-row">{t("No matching settings")}</div>}
          {GROUPS.map((grp) => {
            const items = filtered.filter((i) => i.group === grp);
            if (!items.length) return null;
            return (
              <div key={grp} className="stg-nav-group">
                <div className="stg-nav-group-label">{t(grp)}</div>
                {items.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    className={"stg-nav-item" + (section === item.id ? " on" : "")}
                    aria-current={section === item.id ? "page" : undefined}
                    onClick={() => setSection(item.id)}
                  >
                    <Icon name={item.icon} size={14} />
                    <span>{t(item.label)}</span>
                  </button>
                ))}
              </div>
            );
          })}
        </nav>
      </aside>
    </div>
  );
}
