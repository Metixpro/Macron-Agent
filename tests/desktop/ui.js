const { chromium } = require(process.env.MACRON_UI_PLAYWRIGHT || "playwright-core");
const fs = require("fs");
const url = process.argv[2];
const shots = process.argv[3];
const modelURL = process.env.MACRON_UI_MODEL_URL || "http://127.0.0.1:18941/v1";
const out = { steps: [], problems: [] };
const note = (s) => { out.steps.push(s); console.log(s); };
const bad = (s) => { out.problems.push(s); console.log("PROBLEM " + s); };

(async () => {
  const channel = process.env.MACRON_UI_BROWSER === undefined ? "msedge" : process.env.MACRON_UI_BROWSER;
  const browser = await chromium.launch(channel ? { channel } : {});
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 820 }, acceptDownloads: true });
  const p = await ctx.newPage();
  p.on("pageerror", (e) => bad("page error: " + e.message));
  p.on("console", (m) => { if (m.type() === "error") note("console: " + m.text()); });
  const shot = (n) => p.screenshot({ path: `${shots}/${n}.png` }).catch(() => {});
  const errorsShown = async () => (await p.locator(".err-dock, .dialog .form-error").allInnerTexts()).join(" | ");
  const turn = async (ms) => {
    const t0 = Date.now();
    await p.waitForSelector(".box.busy", { timeout: 15000 }).catch(() => {});
    while (Date.now() - t0 < ms) {
      if (await p.locator(".dialog.ask").count()) await p.click(".dialog.ask .btn.primary");
      if (!(await p.locator(".box.busy").count())) break;
      await p.waitForTimeout(500);
    }
    return Math.round((Date.now() - t0) / 1000);
  };
  const connectModel = async (where) => {
    await p.locator(".intro-connect-btn").click({ timeout: 20000 });
    const form = p.locator(".dialog form").filter({ has: p.getByRole("heading", { name: "Connect a model" }) });
    await form.getByRole("button", { name: "Other", exact: true }).click();
    await form.getByLabel("Address", { exact: true }).fill(modelURL);
    const key = form.locator("input[type=password]");
    if (await key.count()) await key.fill("x");
    await form.getByPlaceholder("The model's exact name").fill("fake-model");
    await form.locator("button[type=submit]").click();
    await p.locator("button.model").filter({ hasText: "fake-model" }).waitFor({ timeout: 30000 });
    note(where + " model: " + (await p.locator("button.model").innerText().catch(() => "?")).trim());
  };
  const job = async (where) => {
    await p.fill(".box textarea", "smoke test: run every step");
    await p.keyboard.press("Enter");
    await p.waitForSelector(".box.busy", { timeout: 15000 });
    await p.waitForSelector(".live-dock", { timeout: 15000 });
    const rects = await p.evaluate(() => {
      const b = document.querySelector(".box-stage > .starbuddy-peek");
      const box = document.querySelector(".box-stage > .box");
      const d = document.querySelector(".live-dock");
      const r = (el) => {
        if (!el) return null;
        const x = el.getBoundingClientRect();
        return { top: x.top, bottom: x.bottom, left: x.left, right: x.right, height: x.height };
      };
      const dockContentTop = d ? d.getBoundingClientRect().top + (parseFloat(getComputedStyle(d).paddingTop) || 0) : null;
      return { buddy: r(b), box: r(box), dock: r(d), dockContentTop };
    });
    if (!rects.buddy || !rects.box || !rects.dock || rects.dockContentTop === null || rects.buddy.bottom > rects.dockContentTop + 0.5 || rects.buddy.bottom < rects.box.top - 1) {
      bad(`${where} StarBuddy positioning invalid (${JSON.stringify(rects)})`);
    }
    await shot(`${where}-working`);
    await p.click(".machine > button");
    await p.waitForSelector(".machine .menu");
    await p.keyboard.press("Escape");
    if (await p.locator(".machine .menu").count()) bad(where + " Escape did not dismiss machine menu");
    if (!(await p.locator(".box.busy").count())) bad(where + " closing machine menu stopped the job");
    await p.fill(".box textarea", "/");
    await p.waitForSelector(".slash");
    await p.keyboard.press("Escape");
    if (await p.locator(".slash").count()) bad(where + " Escape did not dismiss commands");
    if (!(await p.locator(".box.busy").count())) bad(where + " closing commands stopped the job");
    await p.fill(".box textarea", "");
    const took = await turn(240000);
    const steps = (await p.locator(".feed .tool-row").allInnerTexts()).map((s) => s.replace(/\s+/g, " ").trim());
    const failed = await p.locator(".feed .tool.fail").count();
    const cards = (await p.locator(".file-card").allInnerTexts()).map((s) => s.replace(/\s+/g, " "));
    const errors = await p.locator(".feed .error").allInnerTexts();
    note(`${where} job: ${took}s, ${steps.length} step rows, ${failed} failed rows, cards: ${cards.join(" / ")}`);
    if (await p.locator(".meter-btn").count()) {
      await p.locator(".meter-btn").click();
      await p.waitForSelector(".meter-popover", { timeout: 5000 });
      // The tuck is a state change in the parent, not part of the popover's
      // own render; wait for it instead of snapshotting whatever frame the
      // popover happened to appear in (racy on loaded CI runners).
      await p.waitForFunction(() => document.querySelector(".box-stage > .starbuddy-peek")?.classList.contains("tucked"), null, { timeout: 5000 }).catch(() => {});
      const overlap = await p.evaluate(() => {
        const pop = document.querySelector(".meter-popover");
        const buddy = document.querySelector(".box-stage > .starbuddy-peek");
        const tucked = buddy ? buddy.classList.contains("tucked") : false;
        const pStyle = pop ? getComputedStyle(pop) : null;
        return { visible: Boolean(pop && pStyle && pStyle.display !== "none"), tucked };
      });
      if (!overlap.visible || !overlap.tucked) {
        bad(`${where} meter popover or StarBuddy tuck failed (${JSON.stringify(overlap)})`);
      }
      await p.keyboard.press("Escape");
      await p.locator(".meter-popover").waitFor({ state: "hidden", timeout: 5000 });
    }
    if (errors.length) bad(where + " errors in feed: " + errors.join(" | "));
    if (!cards.length) bad(where + " no file card");
    if (cards.length) {
      try {
        const [dl] = await Promise.all([p.waitForEvent("download", { timeout: 20000 }), p.locator(".file-card").last().locator("button[title='Save a copy']").click()]);
        const body = fs.readFileSync(await dl.path(), "utf8");
        note(`${where} download ${dl.suggestedFilename()} ${body.length} chars`);
        if (!body.includes("return a + b")) bad(where + " download content wrong");
      } catch (e) { bad(where + " download: " + e.message.split("\n")[0]); }
    }
  };

  const verifyTabs = async () => {
    const userLabel = "smoke test: run every step";
    const history = p.locator(".thread-list");
    const tabs = p.getByRole("tablist", { name: "Conversations" });
    await tabs.getByRole("button", { name: "New conversation" }).click();
    const current = tabs.getByRole("tab", { name: "New conversation", selected: true });
    await current.focus();
    await p.keyboard.press("ArrowRight");
    if (!(await tabs.getByRole("tab", { name: userLabel }).evaluate((tab) => document.activeElement === tab))) {
      bad("arrow key navigation did not move focus between conversation tabs");
    }
    await current.locator("..").getByRole("button", { name: "Close tab" }).click();
    await tabs.getByRole("tab", { name: "New conversation" }).waitFor({ state: "hidden" });
    const closed = history.getByRole("button", { name: "New conversation", exact: true });
    await closed.waitFor();
    await tabs.getByRole("tab", { name: userLabel, selected: true }).waitFor();
    await closed.click();
    await tabs.getByRole("tab", { name: "New conversation", selected: true }).waitFor();
    await history.getByRole("button", { name: userLabel, exact: true }).click();
    await tabs.getByRole("tab", { name: userLabel, selected: true }).waitFor();
    note("closing a tab preserves conversation history and it can be reopened");
  };

  const verifyBrowserPreview = async () => {
    let view = { available: true, open: false, hasScreenshot: false };
    const history = [];
    await p.route("**/api/projects/*/browser", (route) => route.fulfill({ json: view }));
    await p.route("**/api/projects/*/browser/action", async (route) => {
      const { action, url } = route.request().postDataJSON();
      history.push({ action, url });
      if (action === "goto") view = { ...view, open: true, url, title: "Google" };
      if (action === "close") view = { available: true, open: false, hasScreenshot: false };
      await route.fulfill({ json: view });
    });
    const companion = p.getByRole("complementary", { name: "Agent Companion" });
    if (!(await companion.isVisible())) await p.getByRole("button", { name: "Open Agent Companion" }).click();
    const browserTab = companion.getByRole("tab", { name: "Browser" });
    await browserTab.click();
    await companion.locator(".br-card").getByRole("button", { name: /^Open/ }).click();
    const preview = p.getByRole("dialog", { name: "Live Browser" });
    await preview.getByRole("button", { name: "Back to chat" }).waitFor();
    await p.keyboard.press("Escape");
    await preview.waitFor({ state: "hidden" });
    await companion.getByRole("button", { name: "Close", exact: true }).click();
    await companion.getByRole("button", { name: "Open Google" }).waitFor();
    await companion.getByRole("tab", { name: "Activity" }).click();
    await browserTab.click();
    await companion.getByRole("button", { name: "Open Google" }).waitFor();
    await p.waitForTimeout(250);
    if (history.length !== 2 || history[0].url !== "https://www.google.com" || history[1].action !== "close") {
      bad("browser preview did not open Google once and remain closed when revisited");
    }
    await p.unroute("**/api/projects/*/browser");
    await p.unroute("**/api/projects/*/browser/action");
    await companion.getByRole("button", { name: "Hide Agent Companion" }).click();
    note("browser preview lifecycle verified with a stub browser service");
  };

  try {
    await p.goto(url);
    await p.waitForSelector(".engine.ok", { timeout: 30000 });
    note("engine ready: " + (await p.locator(".engine").innerText()).trim());
    await shot("01-start");
    const welcome = p.getByRole("dialog", { name: "Set up Macron" });
    await welcome.getByRole("button", { name: "Set up Macron" }).click({ timeout: 15000 });
    await welcome.getByRole("button", { name: /^Other/ }).click();
    await welcome.getByPlaceholder("https://…/v1").fill(modelURL);
    await welcome.locator("input[type=password]").fill("x");
    await welcome.getByRole("button", { name: "fake-model" }).click({ timeout: 20000 });
    await shot("01-setup-model");
    await welcome.locator("button[type=submit]").click();
    await welcome.getByRole("button", { name: /^Start in Home/ }).click({ timeout: 30000 });
    await welcome.getByRole("button", { name: "Start", exact: true }).click();
    await welcome.waitFor({ state: "hidden" });
    note("first-run setup: model " + (await p.locator("button.model").innerText()).trim());
    await p.getByRole("button", { name: "Open folder", exact: true }).click();
    const folderDialog = p.locator(".dialog.folder");
    const folderPath = folderDialog.getByRole("textbox", { name: "Type or paste a path" });
    await folderDialog.locator(".folder-selected[title]:not([title=''])").waitFor();
    await folderPath.fill(process.env.WORKDIR);
    await p.keyboard.press("Enter");
    const folder = process.env.WORKDIR.split(/[\\/]+/).filter(Boolean).pop();
    const open = folderDialog.locator(".folder-open-btn").filter({ hasText: folder });
    await open.waitFor({ timeout: 20000 });
    await open.click();
    await p.locator(".top-ws-chip").filter({ hasText: folder }).waitFor({ timeout: 20000 });
    note("project: " + (await p.locator(".top-ws-chip").innerText()));
    await job("local");
    await shot("02-local-job");
    const planItems = await p.locator(".planside .ps-item").count();
    const planFoot = (await p.locator(".planside .ps-foot").innerText().catch(() => "")).trim();
    note(`plan panel: ${planItems} steps, "${planFoot}"`);
    if (planItems !== 3 || planFoot !== "All steps done") bad("plan panel did not show the finished plan");
    await verifyTabs();
    await verifyBrowserPreview();
    await p.click(".machine > button");
    await p.click(".mrow.add");
    await p.fill('input[placeholder^="root@203.0.113.7"]', `u@127.0.0.1:${process.env.MACRON_UI_SSH_PORT || "2222"}`);
    await p.fill('input[placeholder="e.g. Production VPS"]', "Test VPS");
    await p.click("form .btn.primary");
    note("after add: " + (await errorsShown()));
    await p.waitForSelector("h3:has-text('Is this the right server?')", { timeout: 60000 });
    note("trust dialog: " + (await p.locator("pre.command").innerText()));
    await shot("03-trust");
    await p.click("form .btn.primary");
    await p.waitForSelector("input[type=password]", { timeout: 60000 });
    await shot("04-password");
    await p.fill("input[type=password]", "pw");
    await p.click("form .btn.primary");
    const t0 = Date.now();
    while (Date.now() - t0 < 120000) {
      if (await p.locator(".on-server").count()) break;
      const err = await errorsShown();
      if (/not answering|failed|refused|could not|not installed|did not start|stopped/i.test(err)) { bad("server connect: " + err); break; }
      await p.waitForTimeout(700);
    }
    note(`server connected in ${Math.round((Date.now() - t0) / 1000)}s: ${await p.locator(".top").innerText().catch(() => "")}`.replace(/\s+/g, " "));
    await shot("05-server");
    if (await p.locator(".on-server").count()) {
      await connectModel("server");
      await job("server");
      await shot("06-server-job");
      await p.click(".machine > button");
      await p.click(".menu .mrow:has-text('This PC')");
      await p.locator(".on-server").waitFor({ state: "hidden" });
      await p.locator(".engine.ok").waitFor();
      note("back: " + (await p.locator(".top").innerText()).replace(/\s+/g, " "));
      await p.click(".machine > button");
      await p.click(".menu .mrow:has-text('Test VPS')");
      await p.locator(".on-server").waitFor({ timeout: 20000 });
      note("switched to the server again");
      await shot("07-again");
    } else bad("server job was not run because the SSH workspace did not connect");
  } catch (e) {
    bad("stopped: " + e.message.replace(url, "the desktop engine").split("\n").slice(0, 8).join("\n") + " | visible errors: " + (await errorsShown()));
    await shot("99-fail");
  }
  console.log(JSON.stringify(out, null, 1));
  await browser.close();
  process.exit(out.problems.length ? 1 : 0);
})();
