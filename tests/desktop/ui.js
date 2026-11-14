const { chromium } = require(process.env.MACRON_UI_PLAYWRIGHT || "playwright-core");
const fs = require("fs");
const url = process.argv[2];
const shots = process.argv[3];
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
  const toasts = async () => (await p.locator(".toast").allInnerTexts().catch(() => [])).join(" | ");
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
    await p.waitForSelector(".setup-card .btn.primary", { timeout: 20000 });
    await p.click(".setup-card .btn.primary");
    await p.click(".preset:has-text('Other')");
    await p.fill('input[placeholder="https://…/v1"]', "http://127.0.0.1:18941/v1");
    const key = p.locator("form input[type=password]").first();
    if (await key.count()) await key.fill("x");
    await p.fill('input[placeholder="The model\'s exact name"]', "fake-model");
    await p.click("form .btn.primary");
    for (let i = 0; i < 60 && (await p.locator("form .btn.primary").count()); i++) await p.waitForTimeout(500);
    const err = await p.locator("form .form-error").allInnerTexts();
    if (err.length) bad(where + " model connect: " + err.join(" "));
    note(where + " model: " + (await p.locator("button.model").innerText().catch(() => "?")).trim());
  };
  const job = async (where) => {
    await p.fill(".box textarea", "smoke test: run every step");
    await p.keyboard.press("Enter");
    const took = await turn(240000);
    const steps = (await p.locator(".feed .tool-row").allInnerTexts()).map((s) => s.replace(/\s+/g, " ").trim());
    const failed = await p.locator(".feed .tool.fail").count();
    const cards = (await p.locator(".file-card").allInnerTexts()).map((s) => s.replace(/\s+/g, " "));
    const errors = await p.locator(".feed .error").allInnerTexts();
    note(`${where} job: ${took}s, ${steps.length} step rows, ${failed} failed rows, cards: ${cards.join(" / ")}`);
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
  try {
    await p.goto(url);
    await p.waitForSelector(".engine.ok", { timeout: 30000 });
    note("engine ready: " + (await p.locator(".engine").innerText()).trim());
    await shot("01-start");
    await p.waitForSelector(".welcome", { timeout: 15000 });
    await p.click(".welcome .btn.primary");
    await p.click(".provider:has-text('Other')");
    await p.fill('.welcome input[placeholder="https://…/v1"]', "http://127.0.0.1:18941/v1");
    await p.fill(".welcome input[type=password]", "x");
    await p.waitForSelector(".welcome .model-list .entry", { timeout: 20000 });
    await p.click(".welcome .model-list .entry:has-text('fake-model')");
    await shot("01-setup-model");
    await p.click(".welcome button[type=submit]");
    await p.waitForSelector(".choice-list", { timeout: 30000 });
    await p.click(".choice:has-text('Start in Home')");
    await p.waitForSelector(".welcome .tips");
    await p.click(".welcome .btn.primary");
    await p.waitForTimeout(500);
    if (await p.locator(".welcome").count()) bad("the first-run setup did not close");
    note("first-run setup: model " + (await p.locator("button.model").innerText()).trim());
    await p.keyboard.press("Control+o");
    await p.waitForSelector(".path-input input");
    await p.fill(".path-input input", process.env.WORKDIR);
    await p.keyboard.press("Enter");
    const folder = process.env.WORKDIR.split(/[\\/]+/).filter(Boolean).pop();
    const open = p.locator(`button.btn.primary:has-text("${folder}")`);
    await open.waitFor({ timeout: 20000 });
    await open.click();
    await p.waitForSelector(`.top h1:has-text("${folder}")`, { timeout: 20000 });
    note("project: " + (await p.locator(".top h1").innerText().catch(() => "?")));
    await job("local");
    await shot("02-local-job");
    const planItems = await p.locator(".planside .ps-item").count();
    const planFoot = (await p.locator(".planside .ps-foot").innerText().catch(() => "")).trim();
    note(`plan panel: ${planItems} steps, "${planFoot}"`);
    if (planItems !== 3 || planFoot !== "All steps done") bad("plan panel did not show the finished plan");
    await p.click(".machine > button");
    await p.click(".mrow.add");
    await p.fill('input[placeholder^="root@203.0.113.7"]', "u@127.0.0.1:2222");
    await p.fill('input[placeholder="e.g. Production VPS"]', "Test VPS");
    await p.click("form .btn.primary");
    note("after add: " + (await toasts()));
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
      const st = await p.locator(".machine").innerText().catch(() => "");
      const err = (await toasts()) + " " + (await p.locator("form .form-error").allInnerTexts().catch(() => [])).join(" ");
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
      await p.waitForTimeout(2000);
      note("back: " + (await p.locator(".top").innerText()).replace(/\s+/g, " "));
      await p.click(".machine > button");
      await p.click(".menu .mrow:has-text('Test VPS')");
      await p.waitForTimeout(4000);
      note("switched to the server again: on-server=" + (await p.locator(".on-server").count()) + " " + (await toasts()));
      await shot("07-again");
    }
  } catch (e) {
    bad("stopped: " + e.message.split("\n")[0] + " | toasts: " + (await toasts()));
    await shot("99-fail");
  }
  console.log(JSON.stringify(out, null, 1));
  await browser.close();
  process.exit(out.problems.length ? 1 : 0);
})();
