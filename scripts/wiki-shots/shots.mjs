// Screenshot harness for the Kraken wiki — drives the fake-live stack in the
// locally installed Edge through playwright-core and writes PNGs to ./shots.
// See README.md for the stack, the seeding recipe and the scene per figure.
//
//   node shots.mjs <scene> [--out name] [--w 1440 --h 900] [scene options]
//
// Nothing here reaches a real Panel: the token comes from a file the operator
// wrote after logging into the fake stack, and every address this machine
// owns is rewritten to documentation-range values before the shutter.
import { chromium } from "playwright-core";
import { readFileSync } from "node:fs";
import { networkInterfaces } from "node:os";

const args = process.argv.slice(2);
const scene = args[0];
const opt = (k, d) => { const i = args.indexOf("--" + k); return i >= 0 ? args[i + 1] : d; };
const W = +opt("w", 1440), H = +opt("h", 900);
const BASE = "http://localhost:5173";
const API = "http://127.0.0.1:8080/api/v1";
const TOKEN = readFileSync(process.env.KRAKEN_SHOT_TOKEN_FILE || process.env.TEMP + "/kraken-shot-token.txt", "utf8").trim();

const api = async (method, path, body, headers = {}) => {
  const r = await fetch(API + path, { method, headers: { authorization: "Bearer " + TOKEN, "content-type": "application/json", ...headers }, body: body ? JSON.stringify(body) : undefined });
  const t = await r.text(); let j; try { j = JSON.parse(t); } catch { j = t; }
  return { status: r.status, body: j };
};

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// replace text in every text node + value/placeholder attributes (redaction)
async function stripDrift(page) {
  // A same-version drift chip is a dev artifact: the go-run agent binary never
  // matches the Panel's embedded dist binary by hash. A real version skew stays.
  const keep = !!opt("keepdrift", "");
  await page.evaluate((keep) => {
    document.querySelectorAll(".container-drift").forEach((el) => el.remove());
    document.querySelectorAll(".agent-drift").forEach((el) => {
      const v = [...el.querySelectorAll(".nc-v")].map((b) => b.textContent.trim());
      if (!keep || v.length < 2 || v[0] === v[1]) el.remove();
    });
  }, keep);
}

async function redact(page, map) {
  await stripDrift(page);
  await page.evaluate((map) => {
    const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    const nodes = []; while (walk.nextNode()) nodes.push(walk.currentNode);
    for (const n of nodes) for (const [from, to] of map) if (n.nodeValue.includes(from)) n.nodeValue = n.nodeValue.split(from).join(to);
    for (const el of document.querySelectorAll("input,textarea")) for (const [from, to] of map) if (el.value && el.value.includes(from)) el.value = el.value.split(from).join(to);
  }, map);
}

async function boot({ authed = true, path = "/" } = {}) {
  const browser = await chromium.launch({ channel: "msedge", headless: true });
  const ctx = await browser.newContext({ viewport: { width: W, height: H }, deviceScaleFactor: 2, colorScheme: "dark", reducedMotion: "reduce" });
  const page = await ctx.newPage();
  page.on("pageerror", (e) => console.error("pageerror:", e.message));
  await page.goto(BASE + path);
  if (authed) {
    await page.evaluate((t) => localStorage.setItem("kraken_token", t), TOKEN);
    await page.reload();
  }
  await page.waitForLoadState("networkidle");
  await sleep(600);
  return { browser, page };
}

async function shot(page, name) {
  const out = `shots/${opt("out", name)}.png`;
  await page.screenshot({ path: out, type: "png", fullPage: !!opt("full", "") });
  console.log("wrote", out);
}

// Redaction map: this machine's identity → RFC 5737 documentation addresses.
// Built at runtime from the interfaces present, so nothing real is ever
// written down. Longer literals first so "127.0.0.1:9099" wins over "127.0.0.1".
function hostMap() {
  const map = [
    ["127.0.0.1:9099", "192.0.2.10:9090"],
    ["127.0.0.1:9098", "192.0.2.11:9090"],
    ["localhost:5173", "panel.example.com"],
  ];
  let n = 8;
  for (const addrs of Object.values(networkInterfaces())) {
    for (const a of addrs) if (a.family === "IPv4" && !a.internal) map.push([a.address, `198.51.100.${n++}`]);
  }
  map.push(["127.0.0.1", "192.0.2.10"], ["::1", "192.0.2.10"], ["localhost", "panel.example.com"]);
  return map;
}
const HOST_MAP = hostMap();

const scenes = {
  async login({ page }) {
    await page.fill('input[autocomplete="username"]', "admin");
    await sleep(200);
    await shot(page, "login");
  },
  async nodeAdd({ page }) {
    await page.click("#addNodeBtn");
    await sleep(900);
    await page.check('input[name="nconn"][value="tunnel"]', { force: true });
    await page.click("text=generate enrollment token");
    await page.waitForFunction(() => document.querySelector("#tokenVal")?.textContent?.trim().length > 3);
    await sleep(700);
    await redact(page, [...HOST_MAP, ["http://localhost:5173", "https://panel.example.com"], ["localhost:5173", "panel.example.com"]]);
    await shot(page, "node-add");
  },
  async fleet({ page }) {
    await redact(page, HOST_MAP);
    await shot(page, "fleet");
  },
  async nodeCfg({ page }) {
    await page.click('button[aria-label="Open node settings"]');
    await sleep(900);
    await redact(page, HOST_MAP);
    await shot(page, "node-settings");
  },
  async deploy({ page }) {
    const nodeName = opt("node", "reef-01");
    const idx = await page.evaluate((n) => [...document.querySelectorAll('button[aria-label="Create a new server"]')].findIndex((b) => { let el = b; for (let i = 0; i < 6 && el; i++) { if (el.textContent.toLowerCase().includes(n)) return true; el = el.parentElement; } return false; }), nodeName);
    console.log("node band index", idx);
    await page.locator('button[aria-label="Create a new server"]').nth(Math.max(0, idx)).click();
    await sleep(900);
    const gameVal = await page.evaluate((g) => [...document.querySelectorAll("#nsGame option")].find((o) => o.textContent.toLowerCase().includes(g.toLowerCase()))?.value, opt("game", "Valheim"));
    console.log("game option", gameVal);
    await page.selectOption("#nsGame", gameVal, { force: true });
    await sleep(300);
    const name = opt("name", "");
    if (name) await page.fill('input[aria-label="Server name"]', name);
    if (opt("bepinex", "")) await page.check('label.tgl:has-text("bepinex") input', { force: true });
    await sleep(400);
    await redact(page, HOST_MAP);
    await shot(page, "deploy");
  },
  async depth({ page }) {
    // open the drill-in for the named server, then a station tab
    const server = opt("server", "Midgard Weekend");
    await page.click(`button.srv[aria-label^="${server}"]`);
    await sleep(1200);
    const tab = opt("tab", "");
    if (tab) { await page.check(`section.console input[value="${tab}"]`, { force: true }).catch(async () => { await page.click(`section.console label:has-text("${tab}")`); }); await sleep(500); }
    const cmd = opt("cmd", "");
    if (cmd) { await page.fill('input[aria-label="Console command"]', cmd); await page.press('input[aria-label="Console command"]', "Enter"); await sleep(1500); }
    const hover = opt("hover", "");
    if (hover) { await page.hover(`.p-files :text("${hover}")`); await sleep(300); }
    if (opt("sftp", "")) {
      await page.click('button[aria-label="SFTP connection details"]');
      await sleep(700);
      await page.click('button.mini-act:has-text("generate"), button.mini-act:has-text("rotate")');
      await page.waitForSelector(".sftp-reveal.on", { timeout: 10000 });
      await sleep(500);
      // the fake runtime has no SFTP listener, so the node advertises no port;
      // fill in what a real node shows (port 2022) so the card is not half-empty
      await page.evaluate(() => {
        const card = document.querySelector(".sftp-card"); if (!card) return;
        const user = card.querySelector(".kv:nth-of-type(3) b")?.textContent?.trim();
        const hostEl = card.querySelector(".kv:nth-of-type(1) b"), portEl = card.querySelector(".kv:nth-of-type(2) b");
        const host = "192.0.2.10"; hostEl.textContent = host; portEl.textContent = "2022";
        card.querySelector(".cfg-ro").textContent = `sftp://${user}@${host}:2022`;
        const chip = document.querySelector('button[aria-label="SFTP connection details"] .stn-chip-v'); if (chip) chip.textContent = ":2022";
      });
    }
    const section = opt("section", "");
    await redact(page, HOST_MAP);
    if (section) {
      const el = page.locator(`section.side-block[aria-label="${section}"]`);
      await el.scrollIntoViewIfNeeded();
      if (section === "Backups") { await page.click('button[aria-label^="Show details for the nightly"]'); await sleep(400); }
      await sleep(300);
      const bb = await el.boundingBox(); const pad = 14;
      await page.screenshot({ path: `shots/${opt("out", "section")}.png`, clip: { x: bb.x - pad, y: bb.y - pad, width: bb.width + pad * 2, height: bb.height + pad * 2 } });
      console.log("wrote section", opt("out", "section"));
      return;
    }
    await shot(page, "depth");
  },
  async audit({ page }) {
    await page.click("text=full audit log");
    await sleep(900);
    if (opt("q", "")) { await page.fill('input[placeholder^="filter by actor"]', opt("q", "")); await sleep(400); }
    if (opt("filter", "")) { await page.check(`label.audit-chip.ch-${opt("filter", "")} input`, { force: true }); await sleep(400); }
    await redact(page, HOST_MAP);
    await shot(page, "audit");
  },
  async specs({ page }) {
    await page.click("#prefsBtn");
    await sleep(800);
    await page.click("text=manage specs");
    await sleep(1200);
    await redact(page, HOST_MAP);
    await shot(page, "specs");
  },
  async home({ page }) {
    await redact(page, HOST_MAP);
    await shot(page, "home");
  },
};

if (!scenes[scene]) { console.error("scenes:", Object.keys(scenes).join(", ")); process.exit(2); }
const authed = scene !== "login";
const { browser, page } = await boot({ authed });
try { await scenes[scene]({ page }); } finally { await browser.close(); }
