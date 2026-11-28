import asyncio
import base64
import mimetypes
import re
import uuid

from playwright.async_api import Error as PWError
from playwright.async_api import TimeoutError as PWTimeout
from playwright.async_api import async_playwright

REF_RE = re.compile(r"^(\d+):(main|f\d+):(\d+)$")

SENSITIVE_FIELD_RE = re.compile(
    r"(password|passwd|passcode|pwd|one[-_ ]?time|otp|2fa|mfa|totp|cvv|cvc|csc|cc[-_ ]?(number|exp|csc|code)|card[-_ ]?(num|number|code)|credit[-_ ]?card|security[-_ ]?code|pin\b|secret|private[-_ ]?key|seed[-_ ]?phrase|ssn)",
    re.IGNORECASE,
)

# OpenBot-inspired semantic DOM snapshot:
# - Uses TreeWalker to prune hidden/aria-hidden/inert/overlay subtrees early.
# - Tags each interactive element with a revisioned ref (`rev:frameKey:idx`).
# - Extracts ARIA role, accessible name, states (checked/expanded/disabled/selected),
#   and safe value (masked for passwords), up to maxItems (200).
SNAPSHOT_JS = r"""
([rev, frameKey, maxText, maxItems]) => {
  const INTERACTIVE_TAGS = new Set(["A", "BUTTON", "INPUT", "SELECT", "TEXTAREA", "SUMMARY", "OPTION"]);
  const INTERACTIVE_ROLES = new Set([
    "button", "link", "tab", "menuitem", "menuitemcheckbox", "menuitemradio",
    "checkbox", "radio", "switch", "textbox", "searchbox", "combobox", "listbox",
    "option", "slider", "spinbutton", "treeitem"
  ]);

  const clean = (s, lim = 90) => String(s || "").replace(/\s+/g, " ").trim().slice(0, lim);

  const isHiddenStyle = (el) => {
    try {
      const s = getComputedStyle(el);
      return s.display === "none" || s.visibility === "hidden" || parseFloat(s.opacity) === 0;
    } catch {
      return false;
    }
  };

  const isVisibleBox = (el) => {
    try {
      const r = el.getBoundingClientRect();
      return r.width > 0.5 && r.height > 0.5;
    } catch {
      return false;
    }
  };

  const isInteractive = (el) => {
    const tag = el.tagName;
    if (tag === "INPUT" && (el.type || "").toLowerCase() === "hidden") return false;
    if (tag === "A" && !el.hasAttribute("href") && !el.getAttribute("role")) return false;
    if (INTERACTIVE_TAGS.has(tag) && tag !== "OPTION") return true;
    const role = (el.getAttribute("role") || "").toLowerCase().trim();
    if (role && INTERACTIVE_ROLES.has(role)) return true;
    if (el.isContentEditable) return true;
    if (el.hasAttribute("tabindex") && parseInt(el.getAttribute("tabindex"), 10) >= 0) {
      if (el.onclick || el.getAttribute("onclick")) return true;
    }
    return false;
  };

  const inferRole = (el) => {
    const explicit = (el.getAttribute("role") || "").toLowerCase().trim();
    if (explicit) return explicit;
    const tag = el.tagName.toLowerCase();
    if (tag === "a") return "link";
    if (tag === "button" || tag === "summary") return "button";
    if (tag === "select") return "combobox";
    if (tag === "textarea") return "textbox";
    if (tag === "input") {
      const t = (el.type || "text").toLowerCase();
      if (t === "checkbox") return "checkbox";
      if (t === "radio") return "radio";
      if (t === "submit" || t === "button" || t === "reset") return "button";
      if (t === "search") return "searchbox";
      if (t === "range") return "slider";
      return "textbox";
    }
    if (el.isContentEditable) return "textbox";
    return tag;
  };

  const labelFor = (el) => {
    const ariaLabel = el.getAttribute("aria-label");
    if (ariaLabel) return clean(ariaLabel);
    const labelledBy = el.getAttribute("aria-labelledby");
    if (labelledBy) {
      const parts = labelledBy.split(/\s+/).map(id => {
        const refEl = document.getElementById(id);
        return refEl ? refEl.innerText || refEl.textContent || "" : "";
      }).filter(Boolean);
      if (parts.length) return clean(parts.join(" "));
    }
    if (el.id) {
      try {
        const lbl = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
        if (lbl) {
          const t = clean(lbl.innerText || lbl.textContent);
          if (t) return t;
        }
      } catch {}
    }
    const parentLabel = el.closest && el.closest("label");
    if (parentLabel) {
      const t = clean(parentLabel.innerText || parentLabel.textContent);
      if (t) return t;
    }
    const placeholder = el.getAttribute("placeholder");
    const title = el.getAttribute("title");
    const alt = el.getAttribute("alt");
    const text = el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT"
      ? (placeholder || title || el.getAttribute("name") || "")
      : (el.innerText || el.textContent || placeholder || title || alt || "");
    return clean(text);
  };

  const unique = (s) => {
    try { return document.querySelectorAll(s).length === 1; } catch { return false; }
  };

  const fallbackSel = (el) => {
    const tag = el.tagName.toLowerCase();
    if (el.id && unique("#" + CSS.escape(el.id))) return "#" + CSS.escape(el.id);
    for (const a of ["data-testid", "name", "aria-label", "placeholder"]) {
      const v = el.getAttribute(a);
      if (v && unique(`${tag}[${a}=${JSON.stringify(v)}]`)) return `${tag}[${a}=${JSON.stringify(v)}]`;
    }
    const parts = [];
    for (let e = el; e && e.nodeType === 1 && parts.length < 5; e = e.parentElement) {
      if (e.id && unique("#" + CSS.escape(e.id))) { parts.unshift("#" + CSS.escape(e.id)); break; }
      let i = 1;
      for (let s = e.previousElementSibling; s; s = s.previousElementSibling) {
        if (s.tagName === e.tagName) i++;
      }
      parts.unshift(e.tagName.toLowerCase() + ":nth-of-type(" + i + ")");
    }
    return parts.join(" > ");
  };

  // Clear previous snapshot ref markers cleanly.
  try {
    for (const old of document.querySelectorAll("[data-macron-ref]")) {
      old.removeAttribute("data-macron-ref");
    }
  } catch {}

  const items = [];
  if (document.body) {
    const walker = document.createTreeWalker(
      document.body,
      NodeFilter.SHOW_ELEMENT,
      {
        acceptNode(node) {
          if (node.hasAttribute("data-macron-overlay")) return NodeFilter.FILTER_REJECT;
          if (node.getAttribute("aria-hidden") === "true" || node.hasAttribute("inert") || node.hidden) {
            return NodeFilter.FILTER_REJECT;
          }
          if (isHiddenStyle(node)) return NodeFilter.FILTER_REJECT;
          if (isInteractive(node) && isVisibleBox(node)) return NodeFilter.FILTER_ACCEPT;
          return NodeFilter.FILTER_SKIP;
        }
      }
    );

    let idx = 0;
    let node;
    while ((node = walker.nextNode())) {
      const ref = `${rev}:${frameKey}:${idx}`;
      try { node.setAttribute("data-macron-ref", ref); } catch {}
      const tag = node.tagName.toLowerCase();
      const role = inferRole(node);
      const it = {
        ref,
        sel: fallbackSel(node),
        tag,
        role,
        text: labelFor(node),
      };
      if (node.type && tag === "input") it.type = String(node.type).toLowerCase();
      if (node.href) it.href = String(node.href).slice(0, 160);
      if (tag === "select") {
        it.options = [...node.options].slice(0, 14).map(o => clean(o.label || o.textContent, 50));
      }
      const states = [];
      if (node.disabled || node.getAttribute("aria-disabled") === "true") states.push("disabled");
      const checked = node.getAttribute("aria-checked");
      if (node.checked === true || checked === "true") states.push("checked");
      else if (checked === "mixed") states.push("mixed");
      const expanded = node.getAttribute("aria-expanded");
      if (expanded === "true") states.push("expanded");
      else if (expanded === "false") states.push("collapsed");
      if (node.getAttribute("aria-selected") === "true") states.push("selected");
      if (node.required || node.getAttribute("aria-required") === "true") states.push("required");
      if (states.length) it.states = states;

      // Include current non-sensitive value for form fields.
      if (tag === "input" || tag === "textarea" || tag === "select") {
        const itype = (node.type || "").toLowerCase();
        if (itype === "password") {
          if (node.value) it.value = "••••";
        } else if (itype !== "checkbox" && itype !== "radio" && node.value) {
          it.value = clean(node.value, 60);
        }
      }
      items.push(it);
      idx++;
      if (items.length >= maxItems) break;
    }
  }

  const extractText = () => {
    if (!document.body) return "";
    const parts = [];
    const tw = document.createTreeWalker(
      document.body,
      NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT,
      {
        acceptNode(n) {
          if (n.nodeType === 1) {
            const tag = n.tagName;
            if (tag === "SCRIPT" || tag === "STYLE" || tag === "NOSCRIPT" || tag === "TEMPLATE") return NodeFilter.FILTER_REJECT;
            if (n.hasAttribute("data-macron-overlay") || n.getAttribute("aria-hidden") === "true" || n.hasAttribute("inert") || n.hidden) {
              return NodeFilter.FILTER_REJECT;
            }
            if (isHiddenStyle(n)) return NodeFilter.FILTER_REJECT;
            return NodeFilter.FILTER_SKIP;
          }
          return NodeFilter.FILTER_ACCEPT;
        }
      }
    );
    let tn;
    let total = 0;
    while ((tn = tw.nextNode())) {
      const s = (tn.nodeValue || "").replace(/\s+/g, " ").trim();
      if (!s) continue;
      parts.push(s);
      total += s.length + 1;
      if (total > maxText + 4000) break;
    }
    return parts.join("\n").replace(/\n{3,}/g, "\n\n").trim();
  };

  const rawText = extractText();
  return {
    rev,
    title: document.title || "",
    url: location.href || "",
    text: rawText.slice(0, maxText),
    more: Math.max(0, rawText.length - maxText),
    items,
  };
}
"""

# OpenBot-inspired Smart SPA Settle:
# Waits for active loading indicators (aria-busy, progressbar, skeleton/shimmer)
# to clear and for 150ms of DOM mutation quiet, capped at maxMs (2000ms).
SMART_SETTLE_JS = r"""
([quietMs, maxMs]) => new Promise((resolve) => {
  const start = performance.now();
  const hasLoadingUI = () => {
    try {
      const busy = document.querySelectorAll(
        '[aria-busy="true"], [role="progressbar"], [data-loading="true"], [class*="skeleton" i], [class*="shimmer" i]'
      );
      for (const el of busy) {
        if (el.closest("[data-macron-overlay]")) continue;
        const r = el.getBoundingClientRect();
        if (r.width > 4 && r.height > 4) {
          const s = getComputedStyle(el);
          if (s.display !== "none" && s.visibility !== "hidden") return true;
        }
      }
    } catch {}
    return false;
  };

  let done = false;
  let quietTimer = null;
  let pollTimer = null;
  let maxTimer = null;
  let obs = null;

  const finish = (reason) => {
    if (done) return;
    done = true;
    if (quietTimer) clearTimeout(quietTimer);
    if (pollTimer) clearInterval(pollTimer);
    if (maxTimer) clearTimeout(maxTimer);
    if (obs) try { obs.disconnect(); } catch {}
    resolve({ reason, elapsed: Math.round(performance.now() - start) });
  };

  const armQuiet = () => {
    if (quietTimer) clearTimeout(quietTimer);
    quietTimer = setTimeout(() => {
      if (!hasLoadingUI()) finish("dom-quiet");
    }, quietMs);
  };

  maxTimer = setTimeout(() => finish("timeout"), maxMs);

  if (document.body) {
    obs = new MutationObserver((muts) => {
      for (const m of muts) {
        const t = m.target;
        if (t && t.nodeType === 1 && t.closest && t.closest("[data-macron-overlay]")) continue;
        armQuiet();
        break;
      }
    });
    try {
      obs.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ["aria-busy", "class", "style", "hidden", "data-loading"] });
    } catch {}
  }

  pollTimer = setInterval(() => {
    if (!hasLoadingUI() && quietTimer === null) armQuiet();
  }, 120);

  armQuiet();
})
"""

# OpenBot-inspired Reject-Only Cookie Banner Refuser (traverses open shadow roots).
# Never clicks Accept; clicks known Reject / Necessary-Only buttons once per page.
CONSENT_REFUSE_JS = r"""
() => {
  if (window.__macronConsentDone) return { acted: false, reason: "already" };

  const KNOWN_SELECTORS = [
    "#onetrust-reject-all-handler",
    "button.ot-pc-refuse-all-handler",
    "#CybotCookiebotDialogBodyButtonDecline",
    "#didomi-notice-disagree-button",
    "button[mode='secondary'][class*='qc-cmp2']",
    ".truste_popframe .rejectAll",
    "button[data-testid='cookie-reject-all']",
    "button[data-testid='reject-all']",
    "button[aria-label*='Reject all' i]",
    "button[aria-label*='Decline all' i]",
    "button[aria-label*='Necessary only' i]",
  ];

  const REJECT_TEXT_RE = /^(reject( all)?|decline( all)?|deny( all)?|refuse( all)?|necessary only|essential only|only necessary|only essential|continue without accepting|no thanks|رد همه|فقط ضروری|نپذیرفتن)$/i;
  const BANNER_HINT_RE = /(cookie|consent|gdpr|privacy|onetrust|cookiebot|didomi|cmp|کوکی|حریم خصوصی)/i;

  const isVisible = (el) => {
    if (!el || el.disabled) return false;
    try {
      const r = el.getBoundingClientRect();
      if (r.width < 16 || r.height < 14) return false;
      const s = getComputedStyle(el);
      return s.display !== "none" && s.visibility !== "hidden" && parseFloat(s.opacity) > 0.1;
    } catch {
      return false;
    }
  };

  const queryAllDeep = (root, selector, out = []) => {
    try {
      for (const el of root.querySelectorAll(selector)) out.push(el);
      for (const el of root.querySelectorAll("*")) {
        if (el.shadowRoot) queryAllDeep(el.shadowRoot, selector, out);
      }
    } catch {}
    return out;
  };

  // 1. Known CMP reject buttons
  for (const sel of KNOWN_SELECTORS) {
    const matches = queryAllDeep(document, sel);
    for (const btn of matches) {
      if (isVisible(btn)) {
        window.__macronConsentDone = true;
        btn.click();
        return { acted: true, selector: sel, text: (btn.innerText || "").trim().slice(0, 60) };
      }
    }
  }

  // 2. Generic reject button inside a cookie/consent container
  const buttons = queryAllDeep(document, "button, a[role='button'], [role='button']");
  for (const btn of buttons) {
    if (!isVisible(btn)) continue;
    const txt = (btn.innerText || btn.getAttribute("aria-label") || "").replace(/\s+/g, " ").trim();
    if (!txt || txt.length > 42 || !REJECT_TEXT_RE.test(txt)) continue;
    let cur = btn;
    let inBanner = false;
    for (let depth = 0; cur && depth < 8; depth++, cur = cur.parentElement || (cur.getRootNode && cur.getRootNode().host)) {
      const sig = `${cur.id || ""} ${typeof cur.className === "string" ? cur.className : ""} ${cur.getAttribute && cur.getAttribute("aria-label") || ""}`;
      if (BANNER_HINT_RE.test(sig) || (cur.getAttribute && cur.getAttribute("role") === "dialog")) {
        inBanner = true;
        break;
      }
      if (depth >= 2 && BANNER_HINT_RE.test((cur.innerText || "").slice(0, 400))) {
        inBanner = true;
        break;
      }
    }
    if (inBanner) {
      window.__macronConsentDone = true;
      btn.click();
      return { acted: true, text: txt };
    }
  }
  return { acted: false };
}
"""

# Honest Challenge / Interstitial Detector:
# Accurately detects security challenges so Macron can hand off to human Takeover
# immediately instead of wasting agent turns. Never attempts to solve/bypass.
CHALLENGE_DETECT_JS = r"""
() => {
  const title = (document.title || "").trim();
  const bodyText = (document.body && document.body.innerText ? document.body.innerText : "").slice(0, 2500);
  const combined = (title + "\n" + bodyText).toLowerCase();

  const hasIframe = (substr) => {
    for (const fr of document.querySelectorAll("iframe[src]")) {
      const src = (fr.getAttribute("src") || "").toLowerCase();
      if (src.includes(substr)) return true;
    }
    return false;
  };

  // Explicit blocked / access denied
  if (
    /sorry,\s*you have been blocked|access denied|attention required!\s*\|\s*cloudflare|error\s*1020|error\s*1015/i.test(combined) &&
    bodyText.length < 2200
  ) {
    return { status: "blocked", reason: "Access blocked by site security policy" };
  }

  // Active Cloudflare / WAF verification
  if (/just a moment\.\.\.|checking your browser|checking if the site connection is secure|verifying you are human/i.test(combined)) {
    const hasInteractiveChallenge =
      Boolean(document.querySelector("input[name='cf-turnstile-response'], .cf-turnstile, #challenge-stage input[type='checkbox']")) ||
      hasIframe("challenges.cloudflare.com");
    if (hasInteractiveChallenge) {
      return { status: "needs-user", reason: "Cloudflare human verification required" };
    }
    return { status: "checking", reason: "Waiting for site security check to finish" };
  }

  // Interactive CAPTCHA widgets
  if (hasIframe("challenges.cloudflare.com") || document.querySelector(".cf-turnstile, [data-sitekey][class*='turnstile' i]")) {
    return { status: "needs-user", reason: "Cloudflare Turnstile verification required" };
  }
  if (hasIframe("google.com/recaptcha") || hasIframe("recaptcha.net") || document.querySelector(".g-recaptcha, #recaptcha")) {
    // Only flag as blocking challenge if the page is primarily a challenge/gate or checkbox is prominent
    const isGate = /unusual traffic|not a robot|verify you are human|captcha|security check/i.test(combined) || bodyText.length < 900;
    if (isGate) {
      return { status: "needs-user", reason: "reCAPTCHA verification required" };
    }
  }
  if (hasIframe("hcaptcha.com") || document.querySelector(".h-captcha")) {
    const isGate = /human|captcha|verify|security check/i.test(combined) || bodyText.length < 1200;
    if (isGate) {
      return { status: "needs-user", reason: "hCaptcha verification required" };
    }
  }
  if (/press\s*&\s*hold|confirm you are human|are you a robot|security verification|perimeterx|datadome/i.test(combined) && bodyText.length < 1600) {
    return { status: "needs-user", reason: "Human verification challenge required" };
  }

  return { status: "none", reason: "" };
}
"""

CONTEXT_MENU_QUERY_JS = r"""
([x, y]) => {
  const el = document.elementFromPoint(x, y);
  const sel = (window.getSelection && window.getSelection().toString ? window.getSelection().toString() : "").trim();
  let linkUrl = "";
  let srcUrl = "";
  let isEditable = false;
  if (el) {
    const a = el.closest && el.closest("a[href]");
    if (a && a.href) linkUrl = a.href;
    if (el.tagName === "IMG" && el.src) srcUrl = el.src;
    const tag = el.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable) {
      isEditable = !el.readOnly && !el.disabled;
    }
  }
  return {
    linkUrl,
    srcUrl,
    selectedText: sel.slice(0, 2000),
    isEditable,
    pageUrl: location.href || "",
  };
}
"""

CURSOR_PROBE_JS = r"""
([x, y]) => {
  const el = document.elementFromPoint(x, y);
  if (!el) return "default";
  try {
    const c = getComputedStyle(el).cursor || "auto";
    if (c === "auto") {
      if (el.closest && el.closest("a[href], button, [role='button'], summary, select, label")) return "pointer";
      if (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable) return "text";
      return "default";
    }
    return c;
  } catch {
    return "default";
  }
}
"""

TEACH_INSTALL_JS = r"""
() => {
  if (window.__macronTeachInstalled) return;
  window.__macronTeachInstalled = true;

  const SENSITIVE = /(password|passwd|passcode|pwd|one[-_ ]?time|otp|2fa|mfa|totp|cvv|cvc|csc|cc[-_ ]?(number|exp|csc|code)|card[-_ ]?(num|number|code)|credit[-_ ]?card|security[-_ ]?code|\bpin\b|secret|private[-_ ]?key|ssn)/i;

  const isSensitive = (el) => {
    if (!el) return false;
    const t = (el.type || "").toLowerCase();
    if (t === "password") return true;
    const sig = [
      el.getAttribute("autocomplete") || "",
      el.getAttribute("name") || "",
      el.id || "",
      el.getAttribute("aria-label") || "",
      el.getAttribute("placeholder") || "",
    ].join(" ");
    return SENSITIVE.test(sig);
  };

  const labelOf = (el) => {
    if (!el) return "";
    const aria = el.getAttribute("aria-label");
    if (aria) return aria.trim().slice(0, 70);
    if (el.id) {
      try {
        const lbl = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
        if (lbl && lbl.innerText) return lbl.innerText.trim().slice(0, 70);
      } catch {}
    }
    const txt = (el.innerText || el.getAttribute("placeholder") || el.getAttribute("title") || el.getAttribute("name") || "").replace(/\s+/g, " ").trim();
    return txt.slice(0, 70);
  };

  document.addEventListener("click", (e) => {
    if (!window.__macronTeachActive) return;
    const el = e.target && e.target.closest
      ? e.target.closest("a[href], button, input, select, textarea, [role='button'], [role='link'], [role='tab'], [role='menuitem'], summary")
      : e.target;
    if (!el || (el.closest && el.closest("[data-macron-overlay]"))) return;
    const sensitive = isSensitive(el);
    const tag = el.tagName ? el.tagName.toLowerCase() : "element";
    const role = (el.getAttribute && el.getAttribute("role")) || tag;
    const lbl = sensitive ? "[sensitive field — ask user]" : (labelOf(el) || tag);
    if (typeof window.__macronTeachEmit === "function") {
      window.__macronTeachEmit({
        kind: tag === "input" || tag === "textarea" ? "focus_field" : "click",
        role,
        label: lbl,
        sensitive,
        url: location.href,
      });
    }
  }, true);

  document.addEventListener("change", (e) => {
    if (!window.__macronTeachActive) return;
    const el = e.target;
    if (!el || !el.tagName) return;
    const tag = el.tagName.toLowerCase();
    const sensitive = isSensitive(el);
    const lbl = sensitive ? "[sensitive field — ask user]" : (labelOf(el) || tag);
    if (typeof window.__macronTeachEmit === "function") {
      if (tag === "select") {
        const opt = el.options && el.selectedIndex >= 0 ? (el.options[el.selectedIndex].text || "").trim().slice(0, 50) : "";
        window.__macronTeachEmit({ kind: "select", role: "combobox", label: lbl, option: opt, sensitive: false, url: location.href });
      } else if (tag === "input" && (el.type === "checkbox" || el.type === "radio")) {
        window.__macronTeachEmit({ kind: el.checked ? "check" : "uncheck", role: el.type, label: lbl, sensitive: false, url: location.href });
      } else {
        // NEVER record typed text values!
        window.__macronTeachEmit({ kind: "fill", role: "textbox", label: lbl, sensitive, url: location.href });
      }
    }
  }, true);
}
"""

CURSOR_SHOW_JS = r"""
([x, y, clicking]) => {
  if (!document.documentElement) return;
  let root = document.getElementById("__macron_cursor");
  if (!root) {
    root = document.createElement("div");
    root.id = "__macron_cursor";
    root.setAttribute("data-macron-overlay", "1");
    root.setAttribute("aria-hidden", "true");
    root.style.cssText = [
      "position:fixed",
      "left:0",
      "top:0",
      "width:18px",
      "height:18px",
      "margin-left:-9px",
      "margin-top:-9px",
      "border-radius:9999px",
      "border:2px solid #38bdf8",
      "background:rgba(56,189,248,0.24)",
      "box-shadow:0 0 0 3px rgba(56,189,248,0.18),0 2px 10px rgba(0,0,0,0.45)",
      "pointer-events:none",
      "z-index:2147483647",
      "transition:transform 28ms linear,width 90ms ease,height 90ms ease,margin 90ms ease,background 90ms ease"
    ].join(";");
    document.documentElement.appendChild(root);
  }
  root.style.display = "block";
  root.style.transform = `translate3d(${Math.round(x)}px, ${Math.round(y)}px, 0) scale(${clicking ? 1.45 : 1})`;
  if (clicking) {
    root.style.background = "rgba(56,189,248,0.48)";
  } else {
    root.style.background = "rgba(56,189,248,0.24)";
  }
}
"""

CURSOR_HIDE_JS = r"""
() => {
  const root = document.getElementById("__macron_cursor");
  if (root) root.remove();
}
"""

_STORAGE_DIR = CFG.get("storage_dir")
if _STORAGE_DIR:
    os.makedirs(_STORAGE_DIR, exist_ok=True)
    STATE = os.path.join(_STORAGE_DIR, "state.json")
    LAST = os.path.join(_STORAGE_DIR, "url")
    META = os.path.join(_STORAGE_DIR, "meta.json")
else:
    STATE = inside(".browser/state.json")
    LAST = inside(".browser/url")
    META = inside(".browser/meta.json")
    os.makedirs(os.path.dirname(STATE), exist_ok=True)

PREVIEW = inside(".browser/preview.png")
os.makedirs(os.path.dirname(PREVIEW), exist_ok=True)

_OUT_LOCK = threading.Lock()


def emit(obj):
    line = (json.dumps(obj, ensure_ascii=False) + "\n").encode("utf-8", errors="replace")
    with _OUT_LOCK:
        try:
            sys.stdout.buffer.write(line)
            sys.stdout.buffer.flush()
        except AttributeError:
            sys.stdout.write(line.decode("utf-8", errors="replace"))
            sys.stdout.flush()


def target(u):
    u = (u or "").strip()
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:(?!\d)", u):
        sp = urlsplit(u)
        if sp.scheme.lower() not in ("http", "https"):
            raise ValueError("only http(s) pages or workspace files can be opened")
        if is_metadata_host(sp.hostname):
            raise ValueError("access to metadata host is blocked")
        return u
    path = inside(u)
    if path and os.path.isfile(path):
        return local_url(u)
    cand = "https://" + u
    if is_metadata_host(urlsplit(cand).hostname):
        raise ValueError("access to metadata host is blocked")
    return cand


def name_for(base, ext):
    base = re.sub(r"[^\w.\-() ]+", "_", base or "").strip(" ._") or "page"
    if not base.lower().endswith(ext):
        base += ext
    return base


def status_note(resp):
    if resp is None or resp.status < 400:
        return ""
    if resp.status == 403 and resp.headers.get("x-macron-proxy"):
        return " (refused: the browser may only reach public websites on web ports)"
    return f" (HTTP {resp.status})"


async def async_apply_hygiene(ctx, locale="en-US"):
    """Honest browser identity & stability hygiene (no anti-bot spoofing)."""
    lang = (locale or "en-US").split(",")[0].strip() or "en-US"
    base = lang.split("-")[0]
    langs = [lang] if lang == base else [lang, base]
    await ctx.add_init_script(f"""
(() => {{
  try {{
    window.print = () => {{}};
  }} catch (e) {{}}
  try {{
    Object.defineProperty(navigator, 'languages', {{ get: () => {json.dumps(langs)} }});
  }} catch (e) {{}}
  try {{
    document.addEventListener('copy', () => {{
      setTimeout(() => {{
        try {{
          const s = (window.getSelection && window.getSelection().toString ? window.getSelection().toString() : '').trim();
          if (s && typeof window.__macronClipboardEmit === 'function') {{
            window.__macronClipboardEmit(s.slice(0, 16000));
          }}
        }} catch (e) {{}}
      }}, 10);
    }}, true);
  }} catch (e) {{}}
}})();
""")


async def async_fulfill(route, path, allow_cors=False):
    ext = os.path.splitext(path)[1].lower()
    ctype = TYPES.get(ext) or mimetypes.guess_type(path)[0] or "application/octet-stream"
    with open(path, "rb") as f:
        body = f.read()
    headers = {"Content-Type": ctype, "Cache-Control": "no-store"}
    if allow_cors:
        headers["Access-Control-Allow-Origin"] = "*"
    await route.fulfill(status=200, body=body, headers=headers)


async def async_serve_local(route):
    req = route.request
    headers = {k.lower(): v for k, v in (req.headers or {}).items()}
    sfs = (headers.get("sec-fetch-site") or "").strip().lower()
    origin = (headers.get("origin") or "").strip().lower()
    if sfs == "cross-site" or (origin and origin != f"https://{LOCAL_HOST}"):
        return await route.fulfill(status=403, body="forbidden")
    try:
        page_url = req.frame.page.url
    except Exception:
        page_url = ""
    if page_url and req.resource_type != "document":
        pu = urlsplit(page_url)
        if pu.scheme in ("http", "https") and (pu.hostname or "").lower() != LOCAL_HOST:
            return await route.fulfill(status=403, body="forbidden")
    path = unquote(urlsplit(req.url).path)
    name = os.path.basename(path)
    target_path = LIBS.get(name) if path.startswith("/lib/") else None
    target_path = target_path or inside(path)
    if not target_path or not os.path.isfile(target_path):
        return await route.fulfill(status=404, body="not found")
    await async_fulfill(route, target_path, allow_cors=False)


async def async_browser_route(route):
    u = urlsplit(route.request.url)
    if u.hostname == LOCAL_HOST:
        return await async_serve_local(route)
    if u.hostname and is_metadata_host(u.hostname):
        return await route.abort("blockedbyclient")
    await route.continue_()


class Daemon:
    def __init__(self, pw, browser):
        self.pw = pw
        self.browser = browser
        self.ctx = None
        self.active_page = None
        self.attached_page = None
        self.cdp = None
        self.locale = CFG.get("locale") or "en-US"
        self.mobile = bool(CFG.get("mobile"))
        self.vw = 390 if self.mobile else 1366
        self.vh = 844 if self.mobile else 860
        self.timeout_ms = int(CFG.get("timeout_ms") or 15000)
        self.nav_timeout_ms = int(CFG.get("nav_timeout_ms") or max(self.timeout_ms, 45000))
        self.mouse_pos = [self.vw / 2.0, self.vh / 2.0]

        self.profile_id = CFG.get("profile_id") or "workspace"
        self.profile_name = CFG.get("profile_name") or "Workspace"
        self.profile_color = CFG.get("profile_color") or "#60a5fa"

        self.paused = False
        self.takeover_reason = ""
        self.agent_active = False
        self.resume_event = asyncio.Event()
        self.resume_event.set()
        self.tool_task = None
        self.tool_lock = asyncio.Lock()

        # Revisioned semantic snapshot state
        self.snapshot_rev = 0
        self.ref_map = {}  # ref -> {"frame": Frame, "sel": str, "role": str, "text": str}

        # Interactive JS dialog queue (alert / confirm / prompt)
        self.active_dialog = None  # {"id", "type", "message", "defaultValue", "obj", "future"}
        self.errors = []
        self.dialogs = []
        self.watched_pages = set()

        # Challenge state
        self.challenge = {"status": "none", "reason": ""}

        # Live CSS cursor tracking
        self.current_cursor = "default"
        self.last_cursor_probe = 0.0

        # Teach a Task recorder
        self.teaching = False
        self.teach_started_at = 0
        self.teach_steps = []
        self.teach_last_url = ""

        # Tab lifecycle: freeze after 2m idle, sleep after 15m when >3 tabs
        self.tab_last_used = {}  # id(page) -> monotonic timestamp
        self.frozen_tabs = set()  # id(page)
        self.crashed_tabs = set()  # id(page)
        self.sleeping_tabs = []  # list of {"url": str, "title": str, "slept_at": float}
        self.lifecycle_task = None

        # 33ms FramePacer
        self.last_frame_at = 0.0
        self.pending_frame = None
        self.flush_task = None
        self.last_preview_at = 0.0
        self.last_meta_key = ("", "", 0)
        self.should_exit = False
        self.state_timer = None
        self.frame_watchdog = None

    async def create_context(self, reset_state=False):
        if reset_state:
            for f in (STATE, LAST):
                if f and os.path.exists(f):
                    try:
                        os.remove(f)
                    except OSError:
                        pass
        if self.cdp is not None:
            try:
                await self.cdp.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await self.cdp.detach()
            except Exception:
                pass
            self.cdp = None
        if self.ctx is not None:
            try:
                await self.ctx.close()
            except Exception:
                pass
            self.ctx = None
        self.watched_pages.clear()
        self.tab_last_used.clear()
        self.frozen_tabs.clear()
        self.crashed_tabs.clear()
        self.sleeping_tabs.clear()
        self.ref_map.clear()

        lang = (self.locale or "en-US").split(",")[0].strip() or "en-US"
        base_lang = lang.split("-")[0]
        accept_lang = lang if lang == base_lang else f"{lang},{base_lang};q=0.9"

        opts = {
            "viewport": {"width": self.vw, "height": self.vh},
            "locale": self.locale,
            "extra_http_headers": {"Accept-Language": accept_lang},
            "accept_downloads": True,
            "user_agent": DESKTOP_UA,
        }
        if self.mobile:
            dev = dict(self.pw.devices["iPhone 13"])
            dev.pop("default_browser_type", None)
            opts.update(dev)
        vp = opts.get("viewport") or {"width": 1366, "height": 860}
        self.vw = int(vp.get("width") or 1366)
        self.vh = int(vp.get("height") or 860)
        self.mouse_pos = [self.vw / 2.0, self.vh / 2.0]

        if CFG.get("proxy"):
            opts["proxy"] = CFG["proxy"]
        if not reset_state and STATE and os.path.exists(STATE):
            opts["storage_state"] = STATE

        self.ctx = await self.browser.new_context(**opts)
        await async_apply_hygiene(self.ctx, self.locale)
        await self.ctx.add_init_script(TEACH_INSTALL_JS)
        try:
            await self.ctx.expose_binding("__macronClipboardEmit", lambda _src, txt: self._on_page_clipboard(txt))
            await self.ctx.expose_binding("__macronTeachEmit", lambda _src, ev: self._on_teach_event(ev))
        except Exception:
            pass
        self.ctx.set_default_timeout(self.timeout_ms)
        await self.ctx.route("**/*", async_browser_route)
        self.ctx.on("page", self._on_new_page)

        page = await self.ctx.new_page()
        self._watch_page(page)
        self.active_page = page
        self._touch_tab(page)
        await self.attach_screencast(page)

        if self.lifecycle_task is None or self.lifecycle_task.done():
            self.lifecycle_task = asyncio.create_task(self._tab_lifecycle_loop())
        return page

    def _touch_tab(self, page):
        if page is not None:
            self.tab_last_used[id(page)] = time.monotonic()

    def _on_page_clipboard(self, txt):
        s = str(txt or "").strip()
        if s:
            emit({"event": "clipboard", "text": s[:16000]})

    def _on_teach_event(self, ev):
        if not self.teaching or not isinstance(ev, dict):
            return
        if len(self.teach_steps) >= 80:
            return
        kind = str(ev.get("kind") or "click")
        role = str(ev.get("role") or "element")
        label = str(ev.get("label") or "").strip()
        sensitive = bool(ev.get("sensitive"))
        url = str(ev.get("url") or "")

        if kind == "focus_field":
            # Avoid noisy click-before-fill duplicate; only keep if sensitive so user sees placeholder.
            if not sensitive:
                return
            desc = "Pause for user input on [sensitive field — ask user]"
        elif kind == "fill":
            if sensitive:
                desc = "Pause for user input on [sensitive field — ask user]"
            else:
                desc = f'Fill field "{label}" (value provided at runtime)'
        elif kind == "select":
            opt = str(ev.get("option") or "")
            desc = f'Select "{opt}" in "{label}"'
        elif kind in ("check", "uncheck"):
            desc = f'{kind.capitalize()} {role} "{label}"'
        else:
            desc = f'Click {role} "{label}"'

        if self.teach_steps and self.teach_steps[-1].get("description") == desc:
            return
        step = {
            "index": len(self.teach_steps) + 1,
            "kind": kind,
            "description": desc,
            "url": url,
            "sensitive": sensitive,
            "at": int(time.time() * 1000),
        }
        self.teach_steps.append(step)
        self._schedule_state_emit(0.05)

    def _watch_page(self, page):
        pid = id(page)
        if pid in self.watched_pages:
            return
        self.watched_pages.add(pid)
        self._touch_tab(page)
        page.on("pageerror", lambda e: self.errors.append(str(e).splitlines()[0][:200]))
        page.on("dialog", lambda d: asyncio.create_task(self._on_dialog(page, d)))
        page.on("framenavigated", lambda fr: self._on_frame_navigated(page, fr))
        page.on("domcontentloaded", lambda: asyncio.create_task(self._on_dom_ready(page)))
        page.on("load", lambda: asyncio.create_task(self.on_page_changed(page, screenshot=False)))
        page.on("crash", lambda: asyncio.create_task(self._on_page_crashed(page)))
        page.on("close", lambda: asyncio.create_task(self._on_page_closed(page)))

    async def _on_dialog(self, page, d):
        """Surface page JS dialogs to UI/agent instead of blindly auto-accepting."""
        dlg_id = uuid.uuid4().hex[:10]
        dlg_type = str(d.type or "alert")
        msg = str(d.message or "")[:600]
        default_val = str(getattr(d, "default_value", "") or "")[:200]
        loop = asyncio.get_running_loop()
        fut = loop.create_future()

        # Replace any stale unawaited dialog cleanly.
        if self.active_dialog and not self.active_dialog["future"].done():
            self.active_dialog["future"].set_result({"accept": False, "prompt_text": ""})

        entry = {
            "id": dlg_id,
            "type": dlg_type,
            "message": msg,
            "defaultValue": default_val,
            "obj": d,
            "future": fut,
        }
        self.active_dialog = entry
        emit({
            "event": "dialog",
            "dialog": {"id": dlg_id, "type": dlg_type, "message": msg, "defaultValue": default_val},
        })
        await self.emit_state()

        # Wait for UI user or agent handle_dialog; auto-resolve only on timeout
        # while agent is running without Takeover so nothing ever deadlocks.
        decision = None
        try:
            timeout = 120.0 if (self.paused or not self.agent_active) else 12.0
            decision = await asyncio.wait_for(fut, timeout=timeout)
        except asyncio.TimeoutError:
            decision = {"accept": dlg_type == "alert", "prompt_text": default_val, "auto": True}
        except asyncio.CancelledError:
            decision = {"accept": False, "prompt_text": "", "auto": True}

        accepted = bool(decision.get("accept"))
        prompt_text = str(decision.get("prompt_text") or "")
        note = "accepted" if accepted else "dismissed"
        if decision.get("auto"):
            note += " (timeout)"
        self.dialogs.append(f"{dlg_type} ({note}): {msg[:200]}")

        try:
            if accepted:
                if dlg_type == "prompt":
                    await d.accept(prompt_text)
                else:
                    await d.accept()
            else:
                await d.dismiss()
        except Exception:
            pass

        if self.active_dialog is entry:
            self.active_dialog = None
            emit({"event": "dialog", "dialog": None})
            await self.emit_state()

    async def resolve_dialog(self, accept=True, prompt_text="", dialog_id=""):
        entry = self.active_dialog
        if entry is None:
            return {"ok": False, "error": "no open browser dialog"}
        if dialog_id and entry["id"] != dialog_id:
            return {"ok": False, "error": "dialog already closed"}
        if not entry["future"].done():
            entry["future"].set_result({"accept": bool(accept), "prompt_text": str(prompt_text or "")})
        await asyncio.sleep(0.05)
        return {"ok": True, "state": await self.state_payload()}

    async def _on_dom_ready(self, page):
        if page is None or page.is_closed():
            return
        if self.teaching:
            try:
                await page.evaluate("() => { window.__macronTeachActive = true; }")
            except Exception:
                pass
        # Background Reject-only cookie banner refuser (up to 15s, stops on Takeover).
        asyncio.create_task(self._refuse_consent_bg(page))

    async def _refuse_consent_bg(self, page):
        if self.paused or page is None or page.is_closed():
            return
        url = page.url or ""
        if not url or url.startswith("about:"):
            return
        for delay in (0.35, 1.0, 2.5, 4.5):
            await asyncio.sleep(delay)
            if self.paused or page.is_closed() or (page.url or "") != url:
                return
            try:
                res = await page.evaluate(CONSENT_REFUSE_JS)
                if isinstance(res, dict) and res.get("acted"):
                    await self.push_snapshot_frame()
                    return
            except Exception:
                return

    async def _on_page_crashed(self, page):
        pid = id(page)
        self.crashed_tabs.add(pid)
        crashed_url = ""
        try:
            crashed_url = page.url or ""
        except Exception:
            pass
        await self.emit_state()
        # Attempt clean automatic recovery if it's the active tab.
        if page is self.active_page and self.ctx is not None:
            try:
                fresh = await self.ctx.new_page()
                self._watch_page(fresh)
                self.active_page = fresh
                await self.attach_screencast(fresh)
                if crashed_url and crashed_url != "about:blank":
                    await fresh.goto(crashed_url, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                try:
                    await page.close()
                except Exception:
                    pass
                self.crashed_tabs.discard(pid)
                await self.push_snapshot_frame()
                await self.emit_state()
            except Exception:
                pass

    def _on_new_page(self, page):
        self._watch_page(page)
        asyncio.create_task(self._handle_new_tab(page))

    async def _handle_new_tab(self, page):
        if not self.agent_active or self.paused:
            self.active_page = page
            self._touch_tab(page)
            try:
                await page.bring_to_front()
            except Exception:
                pass
            await self.attach_screencast(page)
        await self.emit_state()

    def _on_frame_navigated(self, page, frame):
        if page is self.active_page and frame is page.main_frame:
            if self.teaching:
                u = frame.url or ""
                if u and u != "about:blank" and u != self.teach_last_url and len(self.teach_steps) < 80:
                    self.teach_last_url = u
                    self.teach_steps.append({
                        "index": len(self.teach_steps) + 1,
                        "kind": "navigate",
                        "description": f"Navigate to {u}",
                        "url": u,
                        "sensitive": False,
                        "at": int(time.time() * 1000),
                    })
            self._schedule_state_emit(0.08)

    async def _on_page_closed(self, page):
        pid = id(page)
        self.watched_pages.discard(pid)
        self.tab_last_used.pop(pid, None)
        self.frozen_tabs.discard(pid)
        self.crashed_tabs.discard(pid)
        if page is self.active_page and self.ctx is not None:
            remaining = [p for p in self.ctx.pages if not p.is_closed()]
            if remaining:
                self.active_page = remaining[-1]
                self._touch_tab(self.active_page)
                await self._unfreeze_tab(self.active_page)
                await self.attach_screencast(self.active_page)
                await self.push_snapshot_frame()
            else:
                self.active_page = None
        await self.emit_state()

    async def _freeze_tab(self, page):
        pid = id(page)
        if pid in self.frozen_tabs or page is self.active_page or page.is_closed():
            return
        try:
            cdp = await self.ctx.new_cdp_session(page)
            try:
                await cdp.send("Page.setWebLifecycleState", {"state": "frozen"})
                self.frozen_tabs.add(pid)
            finally:
                await cdp.detach()
        except Exception:
            pass

    async def _unfreeze_tab(self, page):
        pid = id(page)
        if pid not in self.frozen_tabs or page.is_closed():
            return
        try:
            cdp = await self.ctx.new_cdp_session(page)
            try:
                await cdp.send("Page.setWebLifecycleState", {"state": "active"})
            finally:
                await cdp.detach()
        except Exception:
            pass
        self.frozen_tabs.discard(pid)

    async def _tab_lifecycle_loop(self):
        """Freeze idle background tabs after 2m; sleep excess idle tabs (>3) after 15m."""
        try:
            while not self.should_exit:
                await asyncio.sleep(30)
                if self.ctx is None or self.agent_active:
                    continue
                now = time.monotonic()
                pages = [p for p in self.ctx.pages if not p.is_closed()]
                changed = False
                for p in list(pages):
                    if p is self.active_page:
                        self._touch_tab(p)
                        continue
                    pid = id(p)
                    idle = now - self.tab_last_used.get(pid, now)
                    if len(pages) > 3 and idle > 900:
                        try:
                            purl = p.url or ""
                            ptitle = await p.title()
                        except Exception:
                            purl, ptitle = "", ""
                        if purl and purl != "about:blank":
                            self.sleeping_tabs.append({"url": purl, "title": ptitle or purl, "slept_at": now})
                        try:
                            await p.close()
                            pages = [x for x in pages if x is not p]
                            changed = True
                        except Exception:
                            pass
                    elif idle > 120 and pid not in self.frozen_tabs:
                        await self._freeze_tab(p)
                        changed = True
                if changed:
                    await self.emit_state()
        except asyncio.CancelledError:
            pass

    async def on_page_changed(self, page, screenshot=False):
        if page is self.active_page:
            await self.persist_meta_and_preview(save_shot=screenshot)
            await self.emit_state()
            await self.push_snapshot_frame()

    def _schedule_state_emit(self, delay=0.12):
        if self.state_timer is not None and not self.state_timer.done():
            return

        async def _job():
            await asyncio.sleep(delay)
            await self.persist_meta_and_preview(save_shot=False)
            await self.emit_state()

        self.state_timer = asyncio.create_task(_job())

    async def _safe_ack(self, cdp, session_id):
        try:
            await cdp.send("Page.screencastFrameAck", {"sessionId": session_id})
        except Exception:
            pass

    async def attach_screencast(self, page):
        if page is None or page.is_closed() or self.ctx is None:
            return
        if page is self.attached_page and self.cdp is not None:
            return
        if self.flush_task is not None and not self.flush_task.done():
            self.flush_task.cancel()
        self.pending_frame = None

        old = self.cdp
        if old is not None:
            try:
                await old.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await old.detach()
            except Exception:
                pass
            self.cdp = None
        self.attached_page = None

        try:
            my_cdp = await self.ctx.new_cdp_session(page)
        except Exception:
            return
        self.cdp = my_cdp
        self.attached_page = page

        # 33ms FramePacer (~30fps max, trailing-edge flush)
        def on_frame(ev):
            asyncio.create_task(self._safe_ack(my_cdp, ev.get("sessionId")))
            if my_cdp is not self.cdp:
                return
            data = ev.get("data")
            if not data:
                return
            now = time.monotonic()
            if now - self.last_frame_at >= 0.033:
                self.last_frame_at = now
                self.pending_frame = None
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
            else:
                self.pending_frame = data
                if self.flush_task is None or self.flush_task.done():
                    self.flush_task = asyncio.create_task(self._flush_trailing_frame(my_cdp))

        my_cdp.on("Page.screencastFrame", on_frame)
        try:
            await my_cdp.send(
                "Page.startScreencast",
                {
                    "format": "jpeg",
                    "quality": 78,
                    "maxWidth": self.vw,
                    "maxHeight": self.vh,
                    "everyNthFrame": 1,
                },
            )
        except Exception:
            pass

    async def _flush_trailing_frame(self, my_cdp):
        try:
            await asyncio.sleep(0.033)
            if my_cdp is self.cdp and self.pending_frame:
                data = self.pending_frame
                self.pending_frame = None
                self.last_frame_at = time.monotonic()
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
        except asyncio.CancelledError:
            pass

    async def push_snapshot_frame(self):
        if self.cdp is None or self.active_page is None or self.active_page.is_closed():
            return
        if self.active_page.url == "about:blank":
            return
        try:
            res = await self.cdp.send("Page.captureScreenshot", {"format": "jpeg", "quality": 78})
            data = res.get("data")
            if data:
                self.last_frame_at = time.monotonic()
                self.pending_frame = None
                emit({"event": "frame", "jpeg": data, "w": self.vw, "h": self.vh})
        except Exception:
            pass

    async def persist_meta_and_preview(self, save_shot=False):
        page = self.active_page
        if page is None or page.is_closed() or page.url == "about:blank":
            return
        try:
            url = page.url or ""
            title = await page.title()
        except Exception:
            return
        updated = int(time.time() * 1000)
        key = (url, title, len(self.ctx.pages) if self.ctx else 0)
        if key != self.last_meta_key:
            self.last_meta_key = key
            if META:
                try:
                    with open(META, "w", encoding="utf-8") as mf:
                        json.dump({"url": url, "title": title, "updated": updated}, mf, ensure_ascii=False)
                except OSError:
                    pass
            if LAST:
                try:
                    with open(LAST, "w", encoding="utf-8") as lf:
                        lf.write(url)
                except OSError:
                    pass
        now = time.monotonic()
        if save_shot and PREVIEW and (now - self.last_preview_at >= 0.6):
            self.last_preview_at = now
            try:
                await page.screenshot(path=PREVIEW, full_page=False)
            except Exception:
                pass

    async def state_payload(self):
        tabs = []
        open_flag = False
        url = ""
        title = ""
        live_count = 0
        if self.ctx is not None:
            for idx, p in enumerate(self.ctx.pages):
                if p.is_closed():
                    continue
                try:
                    p_url = p.url or ""
                    p_title = await p.title()
                except Exception:
                    p_url, p_title = "", ""
                is_active = p is self.active_page
                pid = id(p)
                tabs.append({
                    "index": idx,
                    "url": p_url,
                    "title": p_title,
                    "active": is_active,
                    "frozen": pid in self.frozen_tabs,
                    "crashed": pid in self.crashed_tabs,
                    "sleeping": False,
                })
                live_count = idx + 1
                if p_url and p_url != "about:blank":
                    open_flag = True
                if is_active:
                    url = p_url
                    title = p_title
        for s_idx, st in enumerate(self.sleeping_tabs):
            tabs.append({
                "index": live_count + s_idx,
                "url": st["url"],
                "title": st["title"],
                "active": False,
                "frozen": False,
                "crashed": False,
                "sleeping": True,
            })
            open_flag = True
        if not url and tabs:
            url = tabs[0]["url"]
            title = tabs[0]["title"]
        if url and url != "about:blank":
            open_flag = True

        dlg_view = None
        if self.active_dialog is not None:
            dlg_view = {
                "id": self.active_dialog["id"],
                "type": self.active_dialog["type"],
                "message": self.active_dialog["message"],
                "defaultValue": self.active_dialog["defaultValue"],
            }

        return {
            "open": open_flag,
            "url": "" if url == "about:blank" else url,
            "title": title,
            "updated": int(time.time() * 1000),
            "paused": self.paused,
            "takeover_reason": self.takeover_reason,
            "agent_active": self.agent_active,
            "cursor": self.current_cursor,
            "challenge": self.challenge if self.challenge.get("status") != "none" else None,
            "dialog": dlg_view,
            "teaching": self.teaching,
            "teach_started_at": self.teach_started_at,
            "teach_steps": self.teach_steps,
            "profile": {
                "id": self.profile_id,
                "name": self.profile_name,
                "color": self.profile_color,
            },
            "viewport": {"width": self.vw, "height": self.vh},
            "tabs": tabs,
        }

    async def emit_state(self):
        st = await self.state_payload()
        emit({"event": "state", "state": st})
        return st

    async def save_storage_state(self):
        if self.ctx is None or not STATE:
            return
        try:
            await self.ctx.storage_state(path=STATE)
        except Exception:
            pass

    async def show_cursor(self, page, x, y, clicking=False):
        if page is None or page.is_closed():
            return
        try:
            await page.evaluate(CURSOR_SHOW_JS, [x, y, bool(clicking)])
        except Exception:
            pass

    async def hide_cursor(self, page):
        if page is None or page.is_closed():
            return
        try:
            await page.evaluate(CURSOR_HIDE_JS)
        except Exception:
            pass

    async def smart_settle(self, page, until="dom-quiet", max_ms=2000, pause=0):
        """OpenBot-style SPA settle: checks skeleton/aria-busy + 150ms MutationObserver quiet."""
        if page is None or page.is_closed():
            return
        mode = (until or "dom-quiet").lower()
        if mode == "none":
            if pause:
                await page.wait_for_timeout(min(pause, 400))
            return
        try:
            if pause:
                await page.wait_for_timeout(min(pause, 400))
            await page.wait_for_load_state("domcontentloaded", timeout=max(1200, min(max_ms, 5000)))
            if mode == "load":
                await page.wait_for_load_state("load", timeout=max(1500, min(max_ms, 6000)))
                return
            await page.evaluate(SMART_SETTLE_JS, [150, max(400, min(int(max_ms), 4000))])
        except PWError:
            pass

    async def detect_challenge(self, page):
        """Honest challenge detection -> triggers Takeover when human verification is needed."""
        if page is None or page.is_closed() or not page.url or page.url.startswith("about:"):
            self.challenge = {"status": "none", "reason": ""}
            return self.challenge
        try:
            res = await page.evaluate(CHALLENGE_DETECT_JS)
        except Exception:
            res = {"status": "none", "reason": ""}
        if not isinstance(res, dict):
            res = {"status": "none", "reason": ""}
        status = str(res.get("status") or "none")
        reason = str(res.get("reason") or "")

        # If Cloudflare/WAF is running a brief automatic check, wait once up to 4s.
        if status == "checking":
            for _ in range(4):
                await asyncio.sleep(1.0)
                if page.is_closed():
                    break
                try:
                    res = await page.evaluate(CHALLENGE_DETECT_JS)
                    status = str((res or {}).get("status") or "none")
                    reason = str((res or {}).get("reason") or "")
                except Exception:
                    status = "none"
                if status != "checking":
                    break

        self.challenge = {"status": status, "reason": reason}
        if status == "needs-user":
            self.takeover_reason = reason or "Human verification required"
            emit({"event": "challenge", "challenge": self.challenge})
        return self.challenge

    async def smooth_move(self, page, tx, ty, steps=10):
        sx, sy = self.mouse_pos
        steps = max(2, min(int(steps or 10), 32))
        for i in range(1, steps + 1):
            await self.wait_if_paused()
            t = i / steps
            ease = t * t * (3 - 2 * t)
            nx = sx + (tx - sx) * ease
            ny = sy + (ty - sy) * ease
            await self.show_cursor(page, nx, ny, clicking=False)
            await page.mouse.move(nx, ny)
        self.mouse_pos[0], self.mouse_pos[1] = float(tx), float(ty)

    async def wait_if_paused(self):
        if not self.resume_event.is_set():
            await self.hide_cursor(self.active_page)
            await self.resume_event.wait()

    async def close_session(self):
        clear_live_preview(PREVIEW, META, LAST)
        if self.active_dialog and not self.active_dialog["future"].done():
            self.active_dialog["future"].set_result({"accept": False, "prompt_text": "", "auto": True})
        if self.lifecycle_task is not None and not self.lifecycle_task.done():
            self.lifecycle_task.cancel()
        if self.cdp is not None:
            try:
                await self.cdp.send("Page.stopScreencast")
            except Exception:
                pass
            try:
                await self.cdp.detach()
            except Exception:
                pass
            self.cdp = None
        self.attached_page = None
        if self.ctx is not None:
            try:
                await self.ctx.close()
            except Exception:
                pass
            self.ctx = None
        self.active_page = None
        self.sleeping_tabs.clear()
        await self.emit_state()
        self.should_exit = True

    def _schedule_input_frame(self):
        prev_at = self.last_frame_at
        if self.frame_watchdog is not None and not self.frame_watchdog.done():
            self.frame_watchdog.cancel()

        async def _check():
            try:
                await asyncio.sleep(0.065)
                if self.last_frame_at == prev_at:
                    await self.push_snapshot_frame()
            except asyncio.CancelledError:
                pass

        self.frame_watchdog = asyncio.create_task(_check())

    async def _probe_cursor(self, page, x, y):
        now = time.monotonic()
        if now - self.last_cursor_probe < 0.065:
            return
        self.last_cursor_probe = now
        try:
            cur = await page.evaluate(CURSOR_PROBE_JS, [x, y])
            cur = str(cur or "default")
            if cur != self.current_cursor:
                self.current_cursor = cur
                emit({"event": "cursor", "cursor": cur})
        except Exception:
            pass

    async def handle_input(self, msg):
        page = self.active_page
        cdp = self.cdp
        if page is None or page.is_closed() or cdp is None:
            return {"ok": False, "error": "no active browser tab"}
        self._touch_tab(page)
        await self.hide_cursor(page)
        kind = (msg.get("type") or "").lower()
        x = float(msg.get("x") or self.mouse_pos[0])
        y = float(msg.get("y") or self.mouse_pos[1])
        x = max(0.0, min(float(self.vw - 1), x))
        y = max(0.0, min(float(self.vh - 1), y))
        button = (msg.get("button") or "left").lower()
        if button not in ("left", "middle", "right"):
            button = "left"
        click_count = max(1, min(int(msg.get("clickCount") or 1), 3))
        modifiers = int(msg.get("modifiers") or 0)
        buttons_mask = 1 if button == "left" else 2 if button == "right" else 4

        try:
            if kind == "wheel":
                dx = float(msg.get("deltaX") or 0.0)
                dy = float(msg.get("deltaY") or 0.0)
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mouseWheel", "x": x, "y": y, "deltaX": dx, "deltaY": dy, "modifiers": modifiers},
                )
            elif kind == "mouse_move":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mouseMoved", "x": x, "y": y, "modifiers": modifiers},
                )
                asyncio.create_task(self._probe_cursor(page, x, y))
            elif kind == "mouse_down":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mousePressed", "x": x, "y": y, "button": button, "buttons": buttons_mask, "clickCount": click_count, "modifiers": modifiers},
                )
            elif kind == "mouse_up":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mouseReleased", "x": x, "y": y, "button": button, "buttons": 0, "clickCount": click_count, "modifiers": modifiers},
                )
            elif kind == "click":
                self.mouse_pos[0], self.mouse_pos[1] = x, y
                await cdp.send("Input.dispatchMouseEvent", {"type": "mouseMoved", "x": x, "y": y, "modifiers": modifiers})
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mousePressed", "x": x, "y": y, "button": button, "buttons": buttons_mask, "clickCount": click_count, "modifiers": modifiers},
                )
                await cdp.send(
                    "Input.dispatchMouseEvent",
                    {"type": "mouseReleased", "x": x, "y": y, "button": button, "buttons": 0, "clickCount": click_count, "modifiers": modifiers},
                )
            elif kind == "insert_text":
                txt = str(msg.get("text") or "")
                if txt:
                    await cdp.send("Input.insertText", {"text": txt})
            elif kind == "press":
                key = str(msg.get("key") or "Enter")
                await page.keyboard.press(key)
            elif kind == "key_down":
                key = str(msg.get("key") or "")
                if key:
                    await page.keyboard.down(key)
            elif kind == "key_up":
                key = str(msg.get("key") or "")
                if key:
                    await page.keyboard.up(key)
            else:
                return {"ok": False, "error": f"unknown input type {kind!r}"}
        except Exception as e:
            return {"ok": False, "error": str(e).splitlines()[0][:200]}

        self._schedule_input_frame()
        if kind != "mouse_move":
            self._schedule_state_emit(0.15)
        return {"ok": True}

    async def handle_resize(self, msg):
        page = self.active_page
        w = max(320, min(int(msg.get("width") or self.vw), 2560))
        h = max(240, min(int(msg.get("height") or self.vh), 1600))
        if abs(w - self.vw) < 8 and abs(h - self.vh) < 8:
            return {"ok": True, "state": await self.state_payload()}
        self.vw, self.vh = w, h
        if page is not None and not page.is_closed():
            try:
                await page.set_viewport_size({"width": w, "height": h})
                if self.cdp is not None:
                    try:
                        await self.cdp.send("Page.stopScreencast")
                    except Exception:
                        pass
                    await self.cdp.send(
                        "Page.startScreencast",
                        {"format": "jpeg", "quality": 78, "maxWidth": w, "maxHeight": h, "everyNthFrame": 1},
                    )
                await self.push_snapshot_frame()
            except Exception as e:
                return {"ok": False, "error": str(e).splitlines()[0][:200]}
        st = await self.emit_state()
        return {"ok": True, "state": st}

    async def handle_context_menu(self, msg):
        page = self.active_page
        if page is None or page.is_closed():
            return {"ok": False, "error": "no active browser tab"}
        op = (msg.get("action") or "query").lower()
        x = float(msg.get("x") or self.mouse_pos[0])
        y = float(msg.get("y") or self.mouse_pos[1])

        if op == "query":
            try:
                info = await page.evaluate(CONTEXT_MENU_QUERY_JS, [x, y])
                return {"ok": True, "result": info}
            except Exception as e:
                return {"ok": False, "error": str(e).splitlines()[0][:200]}

        if op == "open_new_tab":
            u = str(msg.get("url") or "").strip()
            if not u:
                return {"ok": False, "error": "link url is required"}
            return await self.handle_nav({"action": "new_tab", "url": u})
        if op == "paste":
            txt = str(msg.get("text") or "")
            if txt and self.cdp is not None:
                await self.cdp.send("Input.insertText", {"text": txt})
                self._schedule_input_frame()
            return {"ok": True}
        if op in ("back", "forward", "reload"):
            return await self.handle_nav({"action": op})
        return {"ok": True}

    async def handle_teach(self, msg):
        action = (msg.get("action") or "").lower()
        page = self.active_page
        if action == "start":
            self.teaching = True
            self.teach_started_at = int(time.time() * 1000)
            self.teach_steps = []
            self.teach_last_url = page.url if (page and not page.is_closed()) else ""
            if self.teach_last_url and self.teach_last_url != "about:blank":
                self.teach_steps.append({
                    "index": 1,
                    "kind": "navigate",
                    "description": f"Start on {self.teach_last_url}",
                    "url": self.teach_last_url,
                    "sensitive": False,
                    "at": self.teach_started_at,
                })
            if self.ctx is not None:
                for p in self.ctx.pages:
                    if not p.is_closed():
                        try:
                            await p.evaluate("() => { window.__macronTeachActive = true; }")
                        except Exception:
                            pass
            st = await self.emit_state()
            return {"ok": True, "result": {"teaching": True, "steps": self.teach_steps}, "state": st}

        if action in ("stop", "cancel"):
            steps = list(self.teach_steps) if action == "stop" else []
            self.teaching = False
            self.teach_started_at = 0
            self.teach_steps = []
            self.teach_last_url = ""
            if self.ctx is not None:
                for p in self.ctx.pages:
                    if not p.is_closed():
                        try:
                            await p.evaluate("() => { window.__macronTeachActive = false; }")
                        except Exception:
                            pass
            st = await self.emit_state()
            return {"ok": True, "result": {"teaching": False, "steps": steps}, "state": st}

        return {"ok": False, "error": f"unknown teach action {action!r}"}

    async def handle_nav(self, msg):
        action = (msg.get("action") or "").lower()
        if action == "close":
            await self.save_storage_state()
            await self.close_session()
            return {"ok": True, "closed": True, "state": await self.state_payload()}

        if self.ctx is None:
            await self.create_context(reset_state=False)
        page = self.active_page
        if page is None or page.is_closed():
            page = await self.ctx.new_page()
            self._watch_page(page)
            self.active_page = page
            await self.attach_screencast(page)

        try:
            if action in ("goto", "open"):
                dest = target(msg.get("url"))
                await page.goto(dest, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                await self.smart_settle(page, until="dom-quiet", max_ms=2000, pause=100)
                await self.detect_challenge(page)
            elif action == "new_tab":
                dest = target(msg.get("url") or "https://www.google.com")
                new_p = await self.ctx.new_page()
                self._watch_page(new_p)
                self.active_page = new_p
                self._touch_tab(new_p)
                await self.attach_screencast(new_p)
                await new_p.goto(dest, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                await self.smart_settle(new_p, until="dom-quiet", max_ms=2000, pause=100)
                await self.detect_challenge(new_p)
            elif action == "switch_tab":
                idx = int(msg.get("index") if msg.get("index") is not None else 0)
                pages = [p for p in self.ctx.pages if not p.is_closed()]
                if 0 <= idx < len(pages):
                    self.active_page = pages[idx]
                    self._touch_tab(self.active_page)
                    await self._unfreeze_tab(self.active_page)
                    await self.active_page.bring_to_front()
                    await self.attach_screencast(self.active_page)
                elif idx >= len(pages) and (idx - len(pages)) < len(self.sleeping_tabs):
                    # Wake sleeping tab!
                    st_tab = self.sleeping_tabs.pop(idx - len(pages))
                    woke = await self.ctx.new_page()
                    self._watch_page(woke)
                    self.active_page = woke
                    self._touch_tab(woke)
                    await self.attach_screencast(woke)
                    await woke.goto(st_tab["url"], wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                    await self.smart_settle(woke, until="dom-quiet", max_ms=2000, pause=80)
                else:
                    raise ValueError(f"tab index {idx} out of range")
            elif action == "close_tab":
                pages = [p for p in self.ctx.pages if not p.is_closed()]
                idx = msg.get("index")
                if idx is not None and int(idx) >= len(pages):
                    s_idx = int(idx) - len(pages)
                    if 0 <= s_idx < len(self.sleeping_tabs):
                        self.sleeping_tabs.pop(s_idx)
                        st = await self.emit_state()
                        return {"ok": True, "state": st}
                    raise ValueError(f"tab index {idx} out of range")
                if len(pages) <= 1 and not self.sleeping_tabs:
                    await self.save_storage_state()
                    await self.close_session()
                    return {"ok": True, "closed": True, "state": await self.state_payload()}
                if idx is not None:
                    i = int(idx)
                    if i < 0 or i >= len(pages):
                        raise ValueError(f"tab index {i} out of range")
                    target_p = pages[i]
                else:
                    target_p = page
                closing_active = target_p is self.active_page
                await target_p.close()
                remaining = [p for p in self.ctx.pages if not p.is_closed()]
                if not remaining:
                    if self.sleeping_tabs:
                        st_tab = self.sleeping_tabs.pop(0)
                        woke = await self.ctx.new_page()
                        self._watch_page(woke)
                        self.active_page = woke
                        await self.attach_screencast(woke)
                        await woke.goto(st_tab["url"], wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                    else:
                        await self.save_storage_state()
                        await self.close_session()
                        return {"ok": True, "closed": True, "state": await self.state_payload()}
                elif closing_active:
                    self.active_page = remaining[-1]
                    await self._unfreeze_tab(self.active_page)
                    await self.active_page.bring_to_front()
                    await self.attach_screencast(self.active_page)
            elif action == "back":
                await page.go_back(timeout=self.nav_timeout_ms)
                await self.smart_settle(page, until="dom-quiet", max_ms=1800, pause=80)
            elif action == "forward":
                await page.go_forward(timeout=self.nav_timeout_ms)
                await self.smart_settle(page, until="dom-quiet", max_ms=1800, pause=80)
            elif action == "reload":
                await page.reload(timeout=self.nav_timeout_ms)
                await self.smart_settle(page, until="dom-quiet", max_ms=2000, pause=100)
                await self.detect_challenge(page)
            else:
                return {"ok": False, "error": f"unknown nav action {action!r}"}
        except (PWError, ValueError) as e:
            return {"ok": False, "error": str(e).splitlines()[0][:300], "state": await self.emit_state()}

        await self.persist_meta_and_preview(save_shot=True)
        await self.save_storage_state()
        await self.push_snapshot_frame()
        st = await self.emit_state()
        return {"ok": True, "state": st}

    async def collect_snapshot(self, page, max_text=3500, max_items=200):
        self.snapshot_rev += 1
        rev = self.snapshot_rev
        self.ref_map.clear()

        snap = await page.evaluate(SNAPSHOT_JS, [rev, "main", max_text, max_items])
        items = list(snap.get("items") or [])
        for it in items:
            if it.get("ref"):
                self.ref_map[it["ref"]] = {"frame": page.main_frame, "sel": it.get("sel", ""), "role": it.get("role", ""), "text": it.get("text", "")}

        if len(items) < max_items:
            for idx, f in enumerate(page.frames[1:], start=1):
                if idx > 8 or not f.url or f.url.startswith("about:"):
                    continue
                fkey = f"f{idx}"
                try:
                    sub = await f.evaluate(SNAPSHOT_JS, [rev, fkey, 600, max_items - len(items)])
                except PWError:
                    continue
                for it in sub.get("items") or []:
                    it["frame"] = idx
                    if it.get("ref"):
                        self.ref_map[it["ref"]] = {"frame": f, "sel": it.get("sel", ""), "role": it.get("role", ""), "text": it.get("text", "")}
                    items.append(it)
                    if len(items) >= max_items:
                        break
                if len(items) >= max_items:
                    break

        snap["rev"] = rev
        snap["items"] = items[:max_items]
        snap["challenge"] = await self.detect_challenge(page)
        if self.active_dialog is not None:
            snap["dialog"] = {
                "id": self.active_dialog["id"],
                "type": self.active_dialog["type"],
                "message": self.active_dialog["message"],
                "defaultValue": self.active_dialog["defaultValue"],
            }
        return snap

    async def _resolve_scope(self, page, a):
        fr = a.get("frame")
        if fr is None or fr == "":
            return page
        frames = page.frames
        if isinstance(fr, int) or (isinstance(fr, str) and fr.isdigit()):
            idx = int(fr)
            if 0 <= idx < len(frames):
                return frames[idx]
            raise ValueError(f"frame index {idx} out of range (0..{len(frames)-1})")
        needle = str(fr).lower()
        for f in frames[1:]:
            if needle in (f.url or "").lower() or needle in (f.name or "").lower():
                return f
        raise ValueError(f"frame {fr!r} not found")

    async def _resolve_target(self, page, scope, sel):
        """Resolve a revisioned ref (`rev:frame:idx`) or Playwright selector."""
        if not sel:
            return None, scope
        m = REF_RE.match(sel.strip())
        if m:
            rev = int(m.group(1))
            if rev != self.snapshot_rev:
                raise ValueError(f"stale ref {sel!r} (current snapshot rev is {self.snapshot_rev}); take a fresh snapshot first")
            entry = self.ref_map.get(sel.strip())
            if not entry:
                raise ValueError(f"unknown ref {sel!r} in snapshot rev {self.snapshot_rev}")
            target_scope = entry["frame"] or scope
            ref_loc = target_scope.locator(f'[data-macron-ref="{sel.strip()}"]').first
            try:
                if await ref_loc.count() > 0:
                    return ref_loc, target_scope
            except Exception:
                pass
            if entry.get("sel"):
                return target_scope.locator(entry["sel"]).first, target_scope
            raise ValueError(f"element for ref {sel!r} is no longer attached to the DOM")
        return scope.locator(sel).first, scope

    async def _box_center(self, loc):
        box = await loc.bounding_box(timeout=self.timeout_ms)
        if not box:
            raise ValueError("element has no visible bounding box")
        return box["x"] + box["width"] / 2.0, box["y"] + box["height"] / 2.0

    async def _check_sensitive_element(self, loc):
        info = await loc.evaluate("""el => ({
          tag: (el.tagName || '').toLowerCase(),
          type: (el.type || '').toLowerCase(),
          autocomplete: el.getAttribute('autocomplete') || '',
          name: el.getAttribute('name') || '',
          id: el.id || '',
          ariaLabel: el.getAttribute('aria-label') || '',
          placeholder: el.getAttribute('placeholder') || ''
        })""")
        if info.get("type") == "password":
            return True, "password input"
        sig = " ".join([
            str(info.get("autocomplete") or ""),
            str(info.get("name") or ""),
            str(info.get("id") or ""),
            str(info.get("ariaLabel") or ""),
            str(info.get("placeholder") or ""),
        ])
        if SENSITIVE_FIELD_RE.search(sig):
            return True, sig.strip()[:60]
        return False, ""

    async def _act_fill_fields(self, page, scope, a):
        fields = a.get("fields") or []
        if not isinstance(fields, list) or len(fields) == 0:
            raise ValueError("fill_fields requires a non-empty fields array (1..16)")
        if len(fields) > 16:
            raise ValueError("fill_fields accepts at most 16 fields per call")

        # Phase 1: Preflight resolve & sensitive-field guard BEFORE touching any input.
        resolved = []
        for idx, item in enumerate(fields):
            if not isinstance(item, dict):
                raise ValueError(f"fill_fields[{idx}] must be an object")
            sel = str(item.get("selector") or item.get("target") or "").strip()
            if not sel:
                raise ValueError(f"fill_fields[{idx}] is missing selector/target")
            val = str(item.get("value") if item.get("value") is not None else item.get("text", ""))
            mode = str(item.get("mode") or "replace").lower()
            loc, _ = await self._resolve_target(page, scope, sel)
            is_sens, why = await self._check_sensitive_element(loc)
            if is_sens:
                raise ValueError(
                    f"fill_fields refused sensitive field {sel!r} ({why}); ask the user to enter sensitive credentials via request_takeover"
                )
            resolved.append((sel, loc, val, mode))

        # Phase 2: Fill every field and confirm final values.
        confirmed = []
        for sel, loc, val, mode in resolved:
            await self.wait_if_paused()
            await loc.scroll_into_view_if_needed(timeout=self.timeout_ms)
            if mode == "append":
                await loc.click(timeout=self.timeout_ms)
                await loc.press_sequentially(val, delay=8, timeout=self.timeout_ms)
            else:
                await loc.fill(val, timeout=self.timeout_ms)
            try:
                actual = await loc.input_value(timeout=3000)
            except Exception:
                actual = val
            confirmed.append(f"{sel}={actual[:40]!r}")

        await self.smart_settle(page, until=a.get("until") or "dom-quiet", max_ms=1500, pause=80)
        return "filled " + ", ".join(confirmed)

    async def _act_step(self, page, a, files):
        kind = (a.get("action") or "").lower()
        scope = await self._resolve_scope(page, a)
        sel = str(a.get("selector") or "").strip()
        until = str(a.get("until") or "dom-quiet")

        if kind == "fill_fields":
            return await self._act_fill_fields(page, scope, a)

        if kind == "handle_dialog":
            accept = bool(a.get("accept", True))
            prompt_text = str(a.get("prompt_text") or a.get("text") or "")
            res = await self.resolve_dialog(accept=accept, prompt_text=prompt_text)
            if not res.get("ok"):
                raise ValueError(res.get("error") or "no open dialog")
            return "accepted dialog" if accept else "dismissed dialog"

        if kind == "request_takeover":
            reason = str(a.get("reason") or "User action required in browser").strip()
            self.takeover_reason = reason
            emit({"event": "takeover_request", "reason": reason})
            return f"requested user takeover: {reason}"

        loc, resolved_scope = await self._resolve_target(page, scope, sel)

        def need():
            if loc is None:
                raise ValueError("this action needs a selector or ref")
            return loc

        if kind == "goto":
            resp = await page.goto(target(a.get("url")), wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
            await self.smart_settle(page, until=until, max_ms=2200, pause=100)
            await self.push_snapshot_frame()
            return page.url + status_note(resp)

        if kind == "new_tab":
            dest = target(a.get("url") or "https://www.google.com")
            new_p = await self.ctx.new_page()
            self._watch_page(new_p)
            self.active_page = new_p
            self._touch_tab(new_p)
            await self.attach_screencast(new_p)
            await new_p.goto(dest, wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
            await self.smart_settle(new_p, until=until, max_ms=2000, pause=100)
            await self.push_snapshot_frame()
            return f"__switch__:{len(self.ctx.pages) - 1}"

        if kind == "click":
            el = need()
            try:
                await el.scroll_into_view_if_needed(timeout=self.timeout_ms)
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 8)
                await self.show_cursor(page, cx, cy, clicking=True)
            except Exception:
                pass
            await el.click(timeout=self.timeout_ms, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
            await self.smart_settle(page, until=until, max_ms=2000, pause=150)
            await self.hide_cursor(page)
            return ""

        if kind == "click_at":
            tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
            await self.smooth_move(page, tx, ty, 8)
            await self.show_cursor(page, tx, ty, clicking=True)
            await page.mouse.click(tx, ty, button=a.get("button") or "left", click_count=int(a.get("count") or 1))
            await self.smart_settle(page, until=until, max_ms=2000, pause=150)
            await self.hide_cursor(page)
            return ""

        if kind == "move_at":
            tx, ty = float(a.get("x") or 0), float(a.get("y") or 0)
            await self.smooth_move(page, tx, ty, int(a.get("steps") or 10))
            await page.wait_for_timeout(150)
            return ""

        if kind in ("drag", "drag_at"):
            if loc is not None and a.get("to_selector"):
                sx, sy = await self._box_center(need())
                to_loc, _ = await self._resolve_target(page, resolved_scope, str(a["to_selector"]))
                tx, ty = await self._box_center(to_loc)
            elif loc is not None and (a.get("to_x") is not None or a.get("to_y") is not None):
                sx, sy = await self._box_center(need())
                tx = float(a.get("to_x") if a.get("to_x") is not None else sx)
                ty = float(a.get("to_y") if a.get("to_y") is not None else sy)
            else:
                sx, sy = float(a.get("x") or 0), float(a.get("y") or 0)
                tx, ty = float(a.get("to_x") or sx), float(a.get("to_y") or sy)
            await self.smooth_move(page, sx, sy, 8)
            await page.mouse.down()
            await self.smooth_move(page, tx, ty, int(a.get("steps") or 16))
            await page.mouse.up()
            await self.smart_settle(page, until=until, max_ms=1800, pause=150)
            await self.hide_cursor(page)
            return ""

        if kind == "hold":
            if loc is not None:
                tx, ty = await self._box_center(need())
            else:
                tx, ty = float(a.get("x") or self.mouse_pos[0]), float(a.get("y") or self.mouse_pos[1])
            await self.smooth_move(page, tx, ty, 8)
            await self.show_cursor(page, tx, ty, clicking=True)
            await page.mouse.down()
            await page.wait_for_timeout(max(100, min(int(a.get("ms") or 1500), 15000)))
            await page.mouse.up()
            await self.smart_settle(page, until=until, max_ms=1800, pause=150)
            await self.hide_cursor(page)
            return ""

        if kind == "tabs":
            out_tabs = []
            for idx, p in enumerate(self.ctx.pages):
                if p.is_closed():
                    continue
                try:
                    out_tabs.append({"i": idx, "url": p.url, "title": await p.title(), "frozen": id(p) in self.frozen_tabs})
                except Exception:
                    out_tabs.append({"i": idx, "url": "", "title": ""})
            return json.dumps(out_tabs, ensure_ascii=False)

        if kind == "switch_tab":
            idx = a.get("index")
            if idx is not None:
                i = int(idx)
                if 0 <= i < len(self.ctx.pages):
                    return f"__switch__:{i}"
                raise ValueError(f"tab index {i} out of range")
            needle = str(a.get("url") or a.get("title") or "").lower()
            for i, p in enumerate(self.ctx.pages):
                try:
                    ptitle = await p.title()
                except Exception:
                    ptitle = ""
                if needle and (needle in (p.url or "").lower() or needle in (ptitle or "").lower()):
                    return f"__switch__:{i}"
            raise ValueError("matching tab not found")

        if kind == "close_tab":
            if len([p for p in self.ctx.pages if not p.is_closed()]) > 1:
                await page.close()
                return "__tab_closed__"
            return "__closed__"

        if kind == "close":
            return "__closed__"

        if kind == "fill":
            el = need()
            txt = str(a.get("text", ""))
            mode = str(a.get("mode") or "replace").lower()
            try:
                await el.scroll_into_view_if_needed(timeout=self.timeout_ms)
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 5)
            except Exception:
                pass
            if mode == "append":
                await el.click(timeout=self.timeout_ms)
                await el.press_sequentially(txt, delay=10, timeout=self.timeout_ms)
            elif len(txt) <= 120:
                try:
                    tag_info = await el.evaluate("el => ({tag: el.tagName.toLowerCase(), type: (el.type||'').toLowerCase(), editable: el.isContentEditable})")
                    tname = tag_info.get("tag")
                    itype = tag_info.get("type")
                    if (tname == "textarea" or (tname == "input" and itype in ("", "text", "search", "email", "url", "tel", "password", "number")) or tag_info.get("editable")):
                        await el.fill("", timeout=self.timeout_ms)
                        await el.press_sequentially(txt, delay=10, timeout=self.timeout_ms)
                    else:
                        await el.fill(txt, timeout=self.timeout_ms)
                except Exception:
                    await el.fill(txt, timeout=self.timeout_ms)
            else:
                await el.fill(txt, timeout=self.timeout_ms)
            if a.get("submit"):
                await el.press("Enter", timeout=self.timeout_ms)
                await self.smart_settle(page, until=until, max_ms=2000, pause=120)
            return ""

        if kind == "type":
            delay = max(0, min(int(a.get("delay_ms") or 14), 200))
            txt = str(a.get("text", ""))
            if loc is not None:
                try:
                    await loc.scroll_into_view_if_needed(timeout=self.timeout_ms)
                    cx, cy = await self._box_center(loc)
                    await self.smooth_move(page, cx, cy, 5)
                except Exception:
                    pass
                if a.get("clear") or str(a.get("mode") or "").lower() == "replace":
                    await loc.fill("", timeout=self.timeout_ms)
                await loc.press_sequentially(txt, delay=delay, timeout=self.timeout_ms)
            else:
                await page.keyboard.type(txt, delay=delay)
            if a.get("submit"):
                await page.keyboard.press("Enter")
                await self.smart_settle(page, until=until, max_ms=2000, pause=120)
            return ""

        if kind == "press":
            key = a.get("key") or "Enter"
            if loc is not None:
                await loc.press(key, timeout=self.timeout_ms)
            else:
                await page.keyboard.press(key)
            await self.smart_settle(page, until=until, max_ms=2000, pause=140)
            return ""

        if kind == "select":
            el = need()
            v = str(a.get("value") or a.get("text") or "")
            try:
                got = await el.select_option(value=v, timeout=self.timeout_ms)
            except PWError:
                got = await el.select_option(label=v, timeout=self.timeout_ms)
            return ", ".join(got)

        if kind in ("check", "uncheck"):
            el = need()
            await getattr(el, kind)(timeout=self.timeout_ms)
            return ""

        if kind == "hover":
            el = need()
            try:
                cx, cy = await self._box_center(el)
                await self.smooth_move(page, cx, cy, 6)
            except Exception:
                pass
            await el.hover(timeout=self.timeout_ms)
            return ""

        if kind == "scroll":
            if loc is not None:
                await loc.scroll_into_view_if_needed(timeout=self.timeout_ms)
            elif a.get("to") == "bottom":
                await resolved_scope.evaluate("() => window.scrollTo({top: document.body.scrollHeight, behavior: 'smooth'})")
            elif a.get("to") == "top":
                await resolved_scope.evaluate("() => window.scrollTo({top: 0, behavior: 'smooth'})")
            else:
                await page.mouse.wheel(0, int(a.get("y") or 800))
            await page.wait_for_timeout(300)
            return ""

        if kind == "wait":
            wait_ms = min(int(a.get("timeout_ms") or a.get("ms") or 15000), 30000)
            if sel:
                await loc.wait_for(state="visible", timeout=wait_ms)
            elif a.get("text"):
                await resolved_scope.get_by_text(str(a["text"])).first.wait_for(state="visible", timeout=wait_ms)
            else:
                ms = min(int(a.get("ms") or 1200), 15000)
                await page.wait_for_timeout(ms)
            return ""

        if kind == "screenshot":
            name = name_for(a.get("name") or "screenshot", ".png")
            path = inside(name)
            await self.hide_cursor(page)
            if loc is not None:
                await loc.screenshot(path=path, timeout=self.timeout_ms)
            else:
                await page.screenshot(path=path, full_page=bool(a.get("full_page")))
            files.append(name)
            return name

        if kind == "pdf":
            name = name_for(a.get("name") or "page", ".pdf")
            await self.hide_cursor(page)
            await page.emulate_media(media="screen")
            await page.pdf(path=inside(name), print_background=True, format=a.get("format") or "A4")
            files.append(name)
            return name

        if kind == "extract":
            need()
            n = min(int(a.get("limit") or 30), 100)
            every = resolved_scope.locator(sel) if not REF_RE.match(sel) else loc
            if a.get("attribute"):
                vals = (await every.evaluate_all("(els, a) => els.map(e => e.getAttribute(a))", a["attribute"]))[:n]
            else:
                vals = (await every.all_inner_texts())[:n]
            vals = [re.sub(r"\s+", " ", str(v or "")).strip()[:600] for v in vals]
            return json.dumps(vals, ensure_ascii=False)

        if kind == "eval":
            val = await resolved_scope.evaluate(str(a.get("script") or ""))
            return json.dumps(val, ensure_ascii=False, default=str)[:8000]

        if kind in ("back", "forward", "reload"):
            fn = "go_back" if kind == "back" else "go_forward" if kind == "forward" else "reload"
            await getattr(page, fn)(timeout=30000)
            await self.smart_settle(page, until=until, max_ms=2000, pause=100)
            await self.push_snapshot_frame()
            return page.url

        if kind == "upload":
            paths = [inside(f) for f in (a.get("files") or [])]
            if not paths or not all(p and os.path.isfile(p) for p in paths):
                raise ValueError("files must be names of files in the workspace")
            await need().set_input_files(paths, timeout=self.timeout_ms)
            return ""

        if kind == "download":
            async with page.expect_download(timeout=90000) as d_info:
                await need().click(timeout=self.timeout_ms)
            dl = await d_info.value
            name = name_for(a.get("name") or dl.suggested_filename, os.path.splitext(dl.suggested_filename)[1] or ".bin")
            await dl.save_as(inside(name))
            files.append(name)
            return name

        raise ValueError(f"unknown action {kind!r}")

    async def run_script_action(self, act):
        if self.ctx is None:
            await self.create_context(reset_state=False)
        page = self.active_page
        if page is None or page.is_closed():
            page = await self.ctx.new_page()
            self._watch_page(page)
            self.active_page = page
            await self.attach_screencast(page)
        files = []
        detail = await self._act_step(page, act, files)
        if isinstance(detail, str) and detail.startswith("__switch__:"):
            idx = int(detail.split(":", 1)[1])
            self.active_page = self.ctx.pages[idx]
            await self._unfreeze_tab(self.active_page)
            await self.active_page.bring_to_front()
            await self.attach_screencast(self.active_page)
            detail = self.active_page.url
        await self.persist_meta_and_preview(save_shot=False)
        await self.push_snapshot_frame()
        await self.emit_state()
        return {"ok": True, "detail": detail, "files": files, "url": self.active_page.url if self.active_page else ""}

    async def run_script_eval(self, expr, max_bytes=1048576):
        page = self.active_page
        if page is None or page.is_closed():
            raise ValueError("no active browser page")
        val = await page.evaluate(str(expr or ""))
        encoded = json.dumps(val, ensure_ascii=False, default=str)
        if len(encoded.encode("utf-8")) > max_bytes:
            raise ValueError(f"evaluate result exceeded {max_bytes} bytes")
        return {"value": val}

    async def run_tool(self, cfg):
        async with self.tool_lock:
            self.agent_active = True
            self.errors = []
            self.dialogs = []
            files = []
            log = []
            snapshot = {}
            closed = False
            until = str(cfg.get("until") or "dom-quiet")
            await self.emit_state()
            try:
                await self.wait_if_paused()
                new_session = bool(cfg.get("new_session"))
                want_mobile = bool(cfg.get("mobile"))
                req_locale = cfg.get("locale") or self.locale
                if cfg.get("timeout_ms"):
                    self.timeout_ms = int(cfg["timeout_ms"])
                if cfg.get("nav_timeout_ms"):
                    self.nav_timeout_ms = int(cfg["nav_timeout_ms"])

                if self.ctx is None or new_session or want_mobile != self.mobile or req_locale != self.locale:
                    self.mobile = want_mobile
                    self.locale = req_locale
                    await self.create_context(reset_state=new_session)
                else:
                    self.ctx.set_default_timeout(self.timeout_ms)

                if self.active_page is None or self.active_page.is_closed():
                    if self.ctx.pages:
                        self.active_page = self.ctx.pages[-1]
                        await self._unfreeze_tab(self.active_page)
                        await self.attach_screencast(self.active_page)
                    else:
                        p = await self.ctx.new_page()
                        self._watch_page(p)
                        self.active_page = p
                        await self.attach_screencast(p)

                page = self.active_page
                self._touch_tab(page)
                actions = cfg.get("actions") or []
                closing_only = bool(actions) and all((a.get("action") or "").lower() == "close" for a in actions if isinstance(a, dict))
                url = (cfg.get("url") or "").strip()
                if not url and not closing_only and (not page.url or page.url == "about:blank") and LAST and os.path.exists(LAST):
                    with open(LAST, encoding="utf-8") as lf:
                        url = lf.read().strip()

                if url:
                    try:
                        resp = await page.goto(target(url), wait_until="domcontentloaded", timeout=self.nav_timeout_ms)
                        await self.smart_settle(page, until=until, max_ms=2200, pause=100)
                        await self.persist_meta_and_preview(save_shot=True)
                        await self.push_snapshot_frame()
                        await self.emit_state()
                        if note := status_note(resp):
                            log.append({"step": 0, "what": "open " + url, "ok": False, "detail": note.strip(" ()")})
                    except (PWError, ValueError) as e:
                        log.append({"step": 0, "what": "open " + url, "ok": False, "detail": str(e).splitlines()[0][:300]})

                for i, a in enumerate(actions):
                    await self.wait_if_paused()
                    if not isinstance(a, dict):
                        continue
                    what = (a.get("action") or "?") + (" " + str(a["selector"]) if a.get("selector") else "") + (" " + str(a["url"]) if a.get("url") else "")
                    try:
                        try:
                            detail = await self._act_step(page, a, files)
                        except PWError as e:
                            if "context was destroyed" not in str(e) and "navigat" not in str(e):
                                raise
                            await self.smart_settle(page, until=until, max_ms=3000, pause=200)
                            await self.wait_if_paused()
                            detail = await self._act_step(page, a, files)

                        if detail == "__closed__":
                            closed = True
                            await self.save_storage_state()
                            await self.close_session()
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": "closed"})
                            break
                        if detail == "__tab_closed__":
                            remaining = [p for p in self.ctx.pages if not p.is_closed()]
                            if remaining:
                                self.active_page = remaining[-1]
                                page = self.active_page
                                await self._unfreeze_tab(page)
                                await self.attach_screencast(page)
                                await self.smart_settle(page, until=until, max_ms=1500)
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                            await self.persist_meta_and_preview(save_shot=True)
                            await self.emit_state()
                            continue
                        if isinstance(detail, str) and detail.startswith("__switch__:"):
                            idx = int(detail.split(":", 1)[1])
                            self.active_page = self.ctx.pages[idx]
                            page = self.active_page
                            await self._unfreeze_tab(page)
                            await page.bring_to_front()
                            await self.attach_screencast(page)
                            await self.smart_settle(page, until=until, max_ms=1500)
                            await self.push_snapshot_frame()
                            await self.emit_state()
                            log.append({"step": i + 1, "what": what, "ok": True, "detail": page.url})
                            continue

                        log.append({"step": i + 1, "what": what, "ok": True, "detail": detail})
                        if (a.get("action") or "").lower() in ("goto", "click", "click_at", "drag", "drag_at", "hold", "press", "scroll", "back", "forward", "reload", "switch_tab", "fill", "fill_fields", "type"):
                            await self.persist_meta_and_preview(save_shot=False)
                            await self.emit_state()
                    except PWTimeout:
                        log.append({"step": i + 1, "what": what, "ok": False, "detail": "timed out: the element was not found or not usable"})
                        break
                    except (PWError, ValueError) as e:
                        log.append({"step": i + 1, "what": what, "ok": False, "detail": str(e).splitlines()[0][:300]})
                        break

                    if self.ctx.pages and self.ctx.pages[-1] is not page and not self.ctx.pages[-1].is_closed() and (a.get("action") or "").lower() not in ("tabs", "switch_tab"):
                        self.active_page = self.ctx.pages[-1]
                        page = self.active_page
                        await self._unfreeze_tab(page)
                        await self.attach_screencast(page)
                        await self.smart_settle(page, until=until, max_ms=1500)
                        await self.emit_state()

                await self.wait_if_paused()
                await self.hide_cursor(page)

                if not closed and page and not page.is_closed() and page.url != "about:blank":
                    snapshot = await self.collect_snapshot(page, int(cfg.get("max_text") or 3500), 200)
                    close_after = bool(cfg.get("close_after"))
                    if not close_after:
                        await self.persist_meta_and_preview(save_shot=True)
                    if cfg.get("screenshot", False):
                        shot = name_for(cfg.get("shot") or "screenshot", ".png")
                        shot_path = inside(shot)
                        if shot_path:
                            await page.screenshot(path=shot_path, full_page=bool(cfg.get("full_page")))
                            if shot not in files:
                                files.append(shot)
                    await self.save_storage_state()
                    await self.push_snapshot_frame()
                    if close_after:
                        await self.close_session()
                elif not closed:
                    await self.save_storage_state()
            except asyncio.CancelledError:
                await self.hide_cursor(self.active_page)
                log.append({"step": len(log), "what": "cancelled", "ok": False, "detail": "cancelled"})
                raise
            finally:
                await self.hide_cursor(self.active_page)
                self.agent_active = False
                if not self.should_exit:
                    await self.emit_state()

            return {
                "page": snapshot,
                "log": log,
                "files": files,
                "errors": self.errors[:6],
                "dialogs": self.dialogs[:5],
                "challenge": self.challenge if self.challenge.get("status") != "none" else None,
            }


async def launch_async(pw):
    args = [
        "--no-sandbox", "--disable-dev-shm-usage", "--no-first-run", "--mute-audio", "--hide-scrollbars",
        "--disable-background-networking", "--disable-component-update",
        "--disable-sync", "--disable-breakpad", "--disable-features=Translate,MediaRouter,OptimizationHints",
        "--js-flags=--max-old-space-size=1024", "--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist",
        "--font-render-hinting=none", "--force-color-profile=srgb",
    ]
    if CFG.get("webrtc_disable_non_proxied_udp"):
        args.append("--force-webrtc-ip-handling-policy=disable_non_proxied_udp")
    if CFG.get("offline"):
        args += ["--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>"]
    return await pw.chromium.launch(executable_path=CFG["chrome"], headless=True, args=args, chromium_sandbox=False, timeout=60000)


async def run_daemon():
    loop = asyncio.get_running_loop()
    cmd_queue = asyncio.Queue()

    def stdin_reader():
        try:
            while True:
                line = sys.stdin.readline()
                if not line:
                    loop.call_soon_threadsafe(cmd_queue.put_nowait, None)
                    break
                s = line.strip()
                if not s:
                    continue
                try:
                    msg = json.loads(s)
                except Exception:
                    continue
                loop.call_soon_threadsafe(cmd_queue.put_nowait, msg)
        except Exception:
            try:
                loop.call_soon_threadsafe(cmd_queue.put_nowait, None)
            except Exception:
                pass

    threading.Thread(target=stdin_reader, daemon=True).start()

    async with async_playwright() as pw:
        browser = await launch_async(pw)
        daemon = Daemon(pw, browser)
        await daemon.create_context(reset_state=bool(CFG.get("new_session")))
        if CFG.get("url"):
            try:
                await daemon.active_page.goto(target(CFG["url"]), wait_until="domcontentloaded", timeout=daemon.nav_timeout_ms)
                await daemon.smart_settle(daemon.active_page, until="dom-quiet", max_ms=2000, pause=100)
                await daemon.detect_challenge(daemon.active_page)
                await daemon.persist_meta_and_preview(save_shot=True)
                await daemon.push_snapshot_frame()
            except Exception:
                pass
        ready_state = await daemon.state_payload()
        emit({"event": "ready", "state": ready_state})

        async def dispatch_tool(msg_id, cfg):
            try:
                res = await daemon.run_tool(cfg)
                emit({"id": msg_id, "ok": True, "result": res, "state": await daemon.state_payload(), "closed": daemon.should_exit})
            except asyncio.CancelledError:
                emit({"id": msg_id, "ok": False, "error": "cancelled", "state": await daemon.state_payload()})
            except Exception as e:
                emit({"id": msg_id, "ok": False, "error": str(e).splitlines()[0][:300], "state": await daemon.state_payload()})
            finally:
                daemon.tool_task = None
                if daemon.should_exit:
                    await cmd_queue.put(None)

        try:
            while True:
                msg = await cmd_queue.get()
                if msg is None:
                    break
                msg_id = msg.get("id")
                op = (msg.get("op") or "").lower()

                if op == "pause":
                    daemon.paused = bool(msg.get("paused"))
                    daemon.takeover_reason = str(msg.get("reason") or "") if daemon.paused else ""
                    if daemon.paused:
                        daemon.resume_event.clear()
                        await daemon.hide_cursor(daemon.active_page)
                    else:
                        daemon.resume_event.set()
                    st = await daemon.emit_state()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": st})
                    continue

                if op == "cancel_tool":
                    if daemon.tool_task is not None and not daemon.tool_task.done():
                        daemon.paused = False
                        daemon.resume_event.set()
                        daemon.tool_task.cancel()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": await daemon.state_payload()})
                    continue

                if op == "input":
                    res = await daemon.handle_input(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "resize":
                    res = await daemon.handle_resize(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "handle_dialog":
                    res = await daemon.resolve_dialog(
                        accept=bool(msg.get("accept", True)),
                        prompt_text=str(msg.get("prompt_text") or ""),
                        dialog_id=str(msg.get("dialog_id") or ""),
                    )
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "context_menu":
                    res = await daemon.handle_context_menu(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "teach":
                    res = await daemon.handle_teach(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    continue

                if op == "script_action":
                    try:
                        res = await daemon.run_script_action(msg.get("action") or {})
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": True, "result": res, "state": await daemon.state_payload()})
                    except Exception as e:
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": False, "error": str(e).splitlines()[0][:300]})
                    continue

                if op == "script_snapshot":
                    try:
                        if daemon.active_page is None or daemon.active_page.is_closed():
                            raise ValueError("no active browser page")
                        snap = await daemon.collect_snapshot(daemon.active_page, 3500, 200)
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": True, "result": snap, "state": await daemon.state_payload()})
                    except Exception as e:
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": False, "error": str(e).splitlines()[0][:300]})
                    continue

                if op == "script_eval":
                    try:
                        res = await daemon.run_script_eval(msg.get("script") or "", int(msg.get("max_bytes") or 1048576))
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": True, "result": res})
                    except Exception as e:
                        if msg_id is not None:
                            emit({"id": msg_id, "ok": False, "error": str(e).splitlines()[0][:300]})
                    continue

                if op == "snapshot_frame":
                    await daemon.push_snapshot_frame()
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True})
                    continue

                if op == "status":
                    if msg_id is not None:
                        emit({"id": msg_id, "ok": True, "state": await daemon.state_payload()})
                    continue

                if op == "nav":
                    res = await daemon.handle_nav(msg)
                    if msg_id is not None:
                        res["id"] = msg_id
                        emit(res)
                    if daemon.should_exit:
                        break
                    continue

                if op == "tool":
                    cfg = msg.get("cfg") or {}
                    daemon.tool_task = asyncio.create_task(dispatch_tool(msg_id, cfg))
                    continue

                if msg_id is not None:
                    emit({"id": msg_id, "ok": False, "error": f"unknown op {op!r}"})
        finally:
            if daemon.tool_task is not None and not daemon.tool_task.done():
                daemon.tool_task.cancel()
            if daemon.lifecycle_task is not None and not daemon.lifecycle_task.done():
                daemon.lifecycle_task.cancel()
            await daemon.save_storage_state()
            try:
                await browser.close()
            except Exception:
                pass


asyncio.run(run_daemon())
