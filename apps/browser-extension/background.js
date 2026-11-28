const EXT_VERSION = "2.1.0";
const GROUP_TITLE = "Macron";
const GROUP_COLOR = "blue";

let ws = null;
let reconnectTimer = null;
let pingTimer = null;
let macronGroupId = null;

async function getConfig() {
  const data = await chrome.storage.local.get({
    endpoint: "",
    token: "",
    approvedOrigins: [],
    allowDownloads: false,
  });
  return data;
}

async function setConfig(patch) {
  await chrome.storage.local.set(patch);
}

function originOf(rawUrl) {
  try {
    const u = new URL(rawUrl);
    if (u.protocol === "http:" || u.protocol === "https:") return u.origin;
  } catch {}
  return "";
}

async function ensureMacronGroup(tabId) {
  try {
    if (macronGroupId !== null) {
      await chrome.tabs.group({ groupId: macronGroupId, tabIds: [tabId] });
      return macronGroupId;
    }
  } catch {
    macronGroupId = null;
  }
  const existing = await chrome.tabGroups.query({ title: GROUP_TITLE });
  if (existing.length > 0) {
    macronGroupId = existing[0].id;
    await chrome.tabs.group({ groupId: macronGroupId, tabIds: [tabId] });
    return macronGroupId;
  }
  const gid = await chrome.tabs.group({ tabIds: [tabId] });
  await chrome.tabGroups.update(gid, { title: GROUP_TITLE, color: GROUP_COLOR });
  macronGroupId = gid;
  return gid;
}

async function listGroupTabs() {
  const groups = await chrome.tabGroups.query({ title: GROUP_TITLE });
  const groupIds = new Set(groups.map((g) => g.id));
  if (groups.length > 0) macronGroupId = groups[0].id;
  const allTabs = await chrome.tabs.query({});
  return allTabs
    .filter((t) => groupIds.has(t.groupId))
    .map((t) => ({
      id: t.id,
      url: t.url || "",
      title: t.title || "",
      active: Boolean(t.active),
    }));
}

function sendJSON(obj) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(obj));
  }
}

async function pushState() {
  const cfg = await getConfig();
  const tabs = await listGroupTabs();
  sendJSON({
    type: "state",
    browserName: "Chrome",
    extensionVersion: EXT_VERSION,
    approvedOrigins: cfg.approvedOrigins || [],
    tabs,
  });
}

async function checkApproved(url) {
  const origin = originOf(url);
  if (!origin) throw new Error("Only http(s) origins are supported");
  const cfg = await getConfig();
  if (!(cfg.approvedOrigins || []).includes(origin)) {
    throw new Error(`Site ${origin} is not approved in Macron Browser Bridge popup`);
  }
  return origin;
}

async function handleCommand(cmd, params = {}) {
  switch (cmd) {
    case "list_tabs":
      return { tabs: await listGroupTabs() };

    case "open_tab": {
      const url = String(params.url || "https://example.com");
      await checkApproved(url);
      const tab = await chrome.tabs.create({ url, active: true });
      await ensureMacronGroup(tab.id);
      await pushState();
      return { id: tab.id, url };
    }

    case "focus_tab": {
      const tabId = Number(params.tabId);
      await chrome.tabs.update(tabId, { active: true });
      await pushState();
      return { ok: true };
    }

    case "close_tab": {
      const tabId = Number(params.tabId);
      await chrome.tabs.remove(tabId);
      await pushState();
      return { ok: true };
    }

    case "navigate": {
      const tabId = Number(params.tabId);
      const url = String(params.url || "");
      await checkApproved(url);
      await chrome.tabs.update(tabId, { url });
      await pushState();
      return { ok: true, url };
    }

    case "snapshot": {
      const tabs = await listGroupTabs();
      const tab = params.tabId ? tabs.find((t) => t.id === Number(params.tabId)) : tabs.find((t) => t.active) || tabs[0];
      if (!tab) throw new Error("No tab in Macron Tab Group");
      await checkApproved(tab.url);
      const [{ result }] = await chrome.scripting.executeScript({
        target: { tabId: tab.id },
        func: () => {
          const items = [];
          const sels = "a[href],button,input:not([type=hidden]),select,textarea,[role=button],[role=link],[role=tab]";
          for (const el of document.querySelectorAll(sels)) {
            const r = el.getBoundingClientRect();
            if (r.width <= 0 || r.height <= 0) continue;
            const s = getComputedStyle(el);
            if (s.display === "none" || s.visibility === "hidden") continue;
            const text = (el.innerText || el.getAttribute("aria-label") || el.getAttribute("placeholder") || "").replace(/\s+/g, " ").trim().slice(0, 80);
            items.push({ tag: el.tagName.toLowerCase(), role: el.getAttribute("role") || el.tagName.toLowerCase(), text });
            if (items.length >= 120) break;
          }
          return {
            title: document.title,
            url: location.href,
            text: (document.body?.innerText || "").slice(0, 3500),
            items,
          };
        },
      });
      return result;
    }

    default:
      throw new Error(`Unknown extension command: ${cmd}`);
  }
}

async function connect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer);
    reconnectTimer = null;
  }
  const cfg = await getConfig();
  if (!cfg.endpoint || !cfg.token) return;

  try {
    if (ws) ws.close();
  } catch {}

  const sep = cfg.endpoint.includes("?") ? "&" : "?";
  const url = `${cfg.endpoint}${sep}ext_token=${encodeURIComponent(cfg.token)}`;
  try {
    ws = new WebSocket(url);
  } catch {
    scheduleReconnect();
    return;
  }

  ws.addEventListener("open", async () => {
    const cur = await getConfig();
    sendJSON({
      type: "hello",
      browserName: "Chrome",
      extensionVersion: EXT_VERSION,
      approvedOrigins: cur.approvedOrigins || [],
      tabs: await listGroupTabs(),
    });
    if (pingTimer) clearInterval(pingTimer);
    pingTimer = setInterval(() => sendJSON({ type: "ping" }), 20000);
  });

  ws.addEventListener("message", async (ev) => {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }
    if (msg.type === "command") {
      try {
        const result = await handleCommand(msg.command, msg.params || {});
        sendJSON({ id: msg.id, type: "reply", ok: true, result });
      } catch (err) {
        sendJSON({ id: msg.id, type: "reply", ok: false, error: err?.message || String(err) });
      }
    }
  });

  ws.addEventListener("close", () => {
    if (pingTimer) clearInterval(pingTimer);
    pingTimer = null;
    ws = null;
    scheduleReconnect();
  });

  ws.addEventListener("error", () => {
    try { ws?.close(); } catch {}
  });
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, 4000);
}

// Download guard for tabs inside Macron Tab Group.
chrome.downloads.onCreated.addListener(async (item) => {
  const cfg = await getConfig();
  if (cfg.allowDownloads) return;
  const tabs = await listGroupTabs();
  if (tabs.some((t) => item.referrer && t.url && item.referrer.startsWith(originOf(t.url)))) {
    try {
      await chrome.downloads.cancel(item.id);
    } catch {}
  }
});

chrome.tabs.onUpdated.addListener(() => { if (ws?.readyState === WebSocket.OPEN) pushState(); });
chrome.tabs.onRemoved.addListener(() => { if (ws?.readyState === WebSocket.OPEN) pushState(); });

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  (async () => {
    if (msg.type === "status") {
      const cfg = await getConfig();
      const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
      const currentOrigin = activeTab?.url ? originOf(activeTab.url) : "";
      sendResponse({
        connected: Boolean(ws && ws.readyState === WebSocket.OPEN),
        endpoint: cfg.endpoint,
        tokenSet: Boolean(cfg.token),
        approvedOrigins: cfg.approvedOrigins || [],
        currentOrigin,
        tabs: await listGroupTabs(),
      });
      return;
    }
    if (msg.type === "connect") {
      await setConfig({ endpoint: msg.endpoint.trim(), token: msg.token.trim() });
      await connect();
      sendResponse({ ok: true });
      return;
    }
    if (msg.type === "disconnect") {
      await setConfig({ token: "" });
      try { ws?.close(); } catch {}
      ws = null;
      sendResponse({ ok: true });
      return;
    }
    if (msg.type === "toggle_origin") {
      const cfg = await getConfig();
      const list = new Set(cfg.approvedOrigins || []);
      if (list.has(msg.origin)) list.delete(msg.origin);
      else if (msg.origin) list.add(msg.origin);
      await setConfig({ approvedOrigins: [...list] });
      await pushState();
      sendResponse({ approvedOrigins: [...list] });
      return;
    }
    if (msg.type === "attach_current_tab") {
      const [activeTab] = await chrome.tabs.query({ active: true, currentWindow: true });
      if (activeTab?.id) {
        const orig = originOf(activeTab.url || "");
        if (orig) {
          const cfg = await getConfig();
          const list = new Set(cfg.approvedOrigins || []);
          list.add(orig);
          await setConfig({ approvedOrigins: [...list] });
        }
        await ensureMacronGroup(activeTab.id);
        await pushState();
      }
      sendResponse({ ok: true });
      return;
    }
  })();
  return true;
});

connect();
