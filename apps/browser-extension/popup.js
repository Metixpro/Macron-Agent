const statusPill = document.getElementById("status-pill");
const statusTxt = document.getElementById("status-txt");
const endpointInput = document.getElementById("endpoint");
const tokenInput = document.getElementById("token");
const connectBtn = document.getElementById("connect-btn");
const disconnectBtn = document.getElementById("disconnect-btn");
const siteCard = document.getElementById("site-card");
const originEl = document.getElementById("current-origin");
const approveBtn = document.getElementById("approve-btn");
const attachBtn = document.getElementById("attach-btn");

let currentOrigin = "";

async function refresh() {
  const st = await chrome.runtime.sendMessage({ type: "status" });
  statusPill.classList.toggle("on", Boolean(st.connected));
  statusTxt.textContent = st.connected ? "Connected" : "Disconnected";
  if (st.endpoint && !endpointInput.value) endpointInput.value = st.endpoint;
  currentOrigin = st.currentOrigin || "";
  if (currentOrigin) {
    siteCard.style.display = "block";
    originEl.textContent = currentOrigin;
    const approved = (st.approvedOrigins || []).includes(currentOrigin);
    approveBtn.textContent = approved ? "Revoke" : "Approve";
    approveBtn.classList.toggle("primary", !approved);
  } else {
    siteCard.style.display = "none";
  }
}

connectBtn.addEventListener("click", async () => {
  await chrome.runtime.sendMessage({
    type: "connect",
    endpoint: endpointInput.value,
    token: tokenInput.value,
  });
  setTimeout(refresh, 400);
});

disconnectBtn.addEventListener("click", async () => {
  tokenInput.value = "";
  await chrome.runtime.sendMessage({ type: "disconnect" });
  refresh();
});

approveBtn.addEventListener("click", async () => {
  if (!currentOrigin) return;
  await chrome.runtime.sendMessage({ type: "toggle_origin", origin: currentOrigin });
  refresh();
});

attachBtn.addEventListener("click", async () => {
  await chrome.runtime.sendMessage({ type: "attach_current_tab" });
  refresh();
});

refresh();
