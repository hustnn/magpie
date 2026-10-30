// Run with Node's test runner and Playwright on the module path; see README.md.
// A routing group's hand-offs (provider.Relay, AgentBaton): a turn on one
// model is handed on, once, to a cheaper one when the group's detector
// says its hard part is done. The editor adds one, its two ends picked
// among the group's models (a model a turn is handed to hands nothing on
// itself), and the detector that tells; Save refuses a hand-off without a
// detector, then posts relays, detector and relayFrom. A model taken out
// takes its hand-off with it. The group's card says it has one; the trace
// tells a turn handed on, held there, begun back on the first model, and
// not yet and why; "Strong starts, cheap finishes" is a template card of
// its own. In every language, Chromium and WebKit, 1100 and 390 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/big", name: "Big", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "a/mini", name: "Mini", providerName: "A", icon: "generic", context: 128000, ready: true },
  { id: "c/det", name: "Det", providerName: "C", icon: "generic", context: 128000, ready: true },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const relayed = { relays: [{ from: "a/big", to: "a/mini" }], detector: "c/det" };
const templates = [{ kind: "relay", group: { id: "strong-then-cheap", name: "Strong starts, cheap finishes", members: ["a/big", "a/mini"], routing: "order", rules: [{ use: "a/mini", compact: true }], ...relayed } }];

const now = Date.now();
// one request of a conversation on the group, its rule step as the
// gateway tells it (gateway.RuleHit, Relayed)
const route = (rule, model) => ({
  id: 7, seq: 7, time: new Date(now - 2000).toISOString(), agent: "claude", model: "coder", provider: "a", done: true, status: 200, ms: 900, tokens: 30000,
  group: { id: "coder", name: "Coder", members: ["a/big", "a/mini"] },
  order: [{ id: "a", provider: "a", name: "A", kind: "key", model, known: true }],
  tries: [{ id: "a", model, start: new Date(now - 2000).toISOString(), done: true, ms: 800, status: 200 }],
  rule,
});
const H = { from: "a/big", to: "a/mini", by: "c/det" };
const cases = [
  ["moved", route({ turn: 2, tokens: 30000, use: "a/mini", more: ["a/big"], relay: { ...H, moved: true, round: 7, rounds: 8 } }, "mini"),
    "Turn {turn} had had {n} tool rounds when {by} said its hard part was done, so it is handed on from {from} to {to}; {from} takes it back only if {to} fails.", { turn: 2, n: 7 }],
  ["held", route({ turn: 2, tokens: 30000, held: true, use: "a/mini", more: ["a/big"], relay: { ...H, held: true, round: 7, rounds: 9 } }, "mini"),
    "Turn {turn} was handed on from {from} to {to} after {n} tool rounds, and stays there; {from} takes it back only if {to} fails.", { turn: 2, n: 7 }],
  ["back", route({ turn: 3, tokens: 30000, use: "a/big", relay: { ...H, back: true, rounds: 0 } }, "big"),
    "The turn before was handed on to {to}, and no rule decides this one, so it begins on {from} again.", {}],
  ["not yet", route({ turn: 3, tokens: 30000, held: true, relay: { ...H, why: "notyet", seen: 6, rounds: 6 } }, "big"),
    "Turn {turn} stays on {from} for now, not handed on to {to}: {reason}.", { turn: 3, reason: ["{by} said its hard part isn't done, after {n} tool rounds", { n: 6 }] }],
];

function serve(lang, posts, { group = {}, routes = [], tpl = false } = {}) {
  let list = [{ id: "coder", name: "Coder", members: ["a/big", "a/mini"], routing: "order", ready: true, memberInfo: info(["a/big", "a/mini"]), ...group }];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list, ...(tpl ? { templates } : {}) });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  let first = true;
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      const rs = first ? routes : [];
      first = false;
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: rs.length, rerouted: 0, errors: 0 }, routes: rs });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      const body = JSON.parse(r.request().postData() || "{}");
      posts.push({ path: url.pathname, body });
      if (url.pathname === "/api/groups/save") list = [{ ...list[0], ...body, ready: true, memberInfo: info(body.members) }];
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// wheel to a control, as the reader would (the page keeps still for any
// other scroll), and click it where it is
async function press(page, loc) {
  const height = page.viewportSize().height;
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 30; i++) {
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}
const pick = async (page, label) => {
  const li = page.locator("#list li", { hasText: label });
  await li.first().waitFor();
  await li.first().click();
};
// the page's own words, in its language
const say = (page, key, vars) => page.evaluate(([k, v]) => t(k, v), [key, vars || {}]);
const saves = (posts) => posts.filter((p) => p.path === "/api/groups/save").map((p) => p.body);

async function open(engine, width, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 1400 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  // WebKit's notice when a layout outruns its observers, not a fault
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 390]) {
      test(`${engine} ${lang} ${width}: a hand-off is added in the editor, its detector chosen`, async (t) => {
        const { page, errors } = await open(engine, width, t);
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=routing");
        const group = page.locator(".rt-group").first();
        await group.waitFor();
        await page.waitForTimeout(300);
        await press(page, group.locator("button", { hasText: await say(page, "Edit") }));
        const ed = page.locator(".rt-gedit");
        await ed.waitFor();
        assert(await ed.evaluate((e, s) => e.innerText.includes(s), await say(page, "Hand-offs")), "the editor has a Hand-offs section");
        assert.equal(await ed.locator(".rt-relay-det").isVisible(), false, "no detector before a hand-off");

        // a hand-off: the first model to the last, a detector to choose
        await press(page, ed.locator(".rt-relays .rt-gadd"));
        const row = ed.locator(".rt-relay");
        await row.first().waitFor();
        const ends = () => row.first().locator(".rt-model").evaluateAll((bs) => bs.map((b) => b.textContent.trim().split(" · ")[0]));
        assert.deepEqual(await ends(), ["Big", "Mini"]);
        assert(await row.first().evaluate((r, s) => r.innerText.includes(s), await say(page, "→ when ready →")));
        const det = ed.locator(".rt-relay-det");
        assert(await det.isVisible(), "the detector shows with a hand-off");
        assert.equal((await det.locator("button.rt-detector").textContent()).trim(), await say(page, "choose a model"));

        // its end is one of the group's: Mini can't be handed to itself,
        // so it is offered Big only
        await press(page, row.first().locator(".rt-model").nth(1));
        const offered = await page.locator("#list li").evaluateAll((ls) => ls.map((l) => l.textContent));
        assert(offered.some((s) => s.includes("Mini")) && !offered.some((s) => s.includes("Big")), `the end offers ${offered}`);
        await page.keyboard.press("Escape");

        // no detector: refused, said in the status line
        const saveBtn = ed.locator("button.primary", { hasText: await say(page, "Save") });
        await press(page, saveBtn);
        const refusal = await say(page, "Choose the model that tells when a turn is ready to hand on");
        await page.waitForFunction((m) => document.querySelector("#status").textContent.includes(m), refusal);
        assert.deepEqual(saves(posts), [], "saved without a detector");

        // a detector and its rounds, then saved
        await press(page, det.locator("button.rt-detector"));
        await pick(page, "Det");
        assert.match(await det.locator("button.rt-detector").textContent(), /Det/);
        const rounds = det.locator("input.rt-relay-rounds");
        assert.equal(await rounds.inputValue(), "6");
        await rounds.fill("4");
        await rounds.dispatchEvent("change");
        await press(page, saveBtn);
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const [saved] = saves(posts);
        assert.deepEqual(saved.relays, [{ from: "a/big", to: "a/mini" }]);
        assert.equal(saved.detector, "c/det");
        assert.equal(saved.relayFrom, 4);

        // the card says it hands off
        const tag = page.locator(".rt-group .rt-relaytag");
        await tag.waitFor();
        assert.equal((await tag.textContent()).trim(), await say(page, "1 hand-off"));
        assert((await tag.getAttribute("title")).includes(await say(page, "Ready told by {model}", { model: "c/det" })));

        // a model taken out takes its hand-off with it
        await press(page, page.locator(".rt-group").first().locator("button", { hasText: await say(page, "Edit") }));
        await ed.waitFor();
        await ed.locator(".rt-relay").first().waitFor();
        const mini = ed.locator(".fbrow", { hasText: "Mini" }).first();
        await press(page, mini.locator("button.text", { hasText: await say(page, "Remove") }));
        assert.equal(await ed.locator(".rt-relay").count(), 0, "the hand-off outlived its model");
        await press(page, ed.locator("button.primary", { hasText: await say(page, "Save") }));
        await page.waitForFunction(() => !document.querySelector(".rt-gedit"));
        const second = saves(posts)[1];
        assert.deepEqual(second.relays, []);
        assert.equal(second.detector, "");

        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
        if (lang !== "en") {
          const keys = ["Hand-offs", "Add a hand-off", "Ready told by", "Tool rounds before it is first asked", "→ when ready →", "1 hand-off", "Choose the model that tells when a turn is ready to hand on", "Strong starts, cheap finishes"];
          assert.deepEqual(await page.evaluate(([l, ks]) => ks.filter((k) => !I18N[l][k]), [lang, keys]), [], `every string has its ${lang}`);
        }
        assert.deepEqual(errors, []);
      });
    }

    test(`${engine} ${lang}: the trace tells each step of a hand-off`, async (t) => {
      for (const [name, rt, key, vars] of cases) {
        const { page, errors } = await open(engine, 1100, t);
        await page.route("**/*", serve(lang, [], { group: relayed, routes: [rt] }));
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-group").first().waitFor();
        const v = { from: "a/big", to: "a/mini", by: "c/det", ...vars };
        if (Array.isArray(v.reason)) v.reason = await say(page, v.reason[0], { ...v, ...v.reason[1] });
        const story = await say(page, key, v);
        await page.waitForFunction((s) => document.documentElement.innerText.includes(s), story, { timeout: 5000 })
          .catch(() => assert.fail(`${name}: the trace doesn't say ${story}`));
        assert.deepEqual(errors, [], name);
      }
    });

    test(`${engine} ${lang}: Strong starts, cheap finishes is a template`, async (t) => {
      const { page, errors } = await open(engine, 420, t);
      await page.route("**/*", serve(lang, [], { tpl: true }));
      await page.goto("http://magpie.test/?view=routing");
      const btn = page.locator(".rt-gtplbtn");
      await btn.waitFor();
      await page.waitForTimeout(300);
      await page.mouse.move(1, 1);
      await btn.click();
      const name = await say(page, "Strong starts, cheap finishes");
      await page.locator(".row-menu").getByRole("menuitem", { name }).click();
      const ed = page.locator(".rt-gedit");
      await ed.waitFor();
      assert.equal(await ed.locator("input").first().inputValue(), name);
      // its hand-off and detector are in the editor, as in the template
      await ed.locator(".rt-relay").first().waitFor();
      assert.deepEqual(await ed.locator(".rt-relay .rt-model").evaluateAll((bs) => bs.map((b) => b.textContent.trim().split(" · ")[0])), ["Big", "Mini"]);
      assert.match(await ed.locator(".rt-relay-det button.rt-detector").textContent(), /Det/);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      assert.deepEqual(errors, []);
    });
  }
}
