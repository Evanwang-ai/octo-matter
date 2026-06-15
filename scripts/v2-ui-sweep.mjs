#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const BASE = process.env.BASE || "http://localhost:28080";
const DEPLOY_DIR = process.env.DEPLOY_DIR || path.resolve(__dirname, "../../octo-deployment/docker");
const OUT_DIR = process.env.OUT_DIR || path.resolve(__dirname, "../../../research/uiux-20260613");
const RUN_LABEL = process.env.RUN_LABEL || String(Date.now());
const KEEP_FIXTURES = process.env.KEEP_FIXTURES === "1";
const LOCK_DIR = process.env.MATTER_ACCEPTANCE_LOCK_DIR || path.join(process.env.TMPDIR || "/tmp", "octo-matter-v2-acceptance.lock");
let lockHeld = false;

function die(message) {
  console.error(message);
  process.exit(1);
}

function releaseAcceptanceLock() {
  if (!lockHeld) return;
  try {
    fs.rmdirSync(LOCK_DIR);
  } catch {
    // Best effort only; stale locks are obvious because the path is printed.
  }
  lockHeld = false;
}

function acquireAcceptanceLock() {
  try {
    fs.mkdirSync(LOCK_DIR);
    lockHeld = true;
  } catch {
    die(`ERROR: another Matter live acceptance run is active (${LOCK_DIR}); run smoke/CLI/UI checks serially.`);
  }
  process.on("exit", releaseAcceptanceLock);
  for (const signal of ["SIGINT", "SIGTERM"]) {
    process.on(signal, () => {
      releaseAcceptanceLock();
      process.exit(signal === "SIGINT" ? 130 : 143);
    });
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

acquireAcceptanceLock();

async function loadPlaywright() {
  try {
    return await import("playwright");
  } catch (directError) {
    const candidates = [
      process.env.PLAYWRIGHT_REQUIRE_FROM,
      path.resolve(__dirname, "../../octo-web/apps/web/package.json"),
      path.resolve(process.cwd(), "package.json")
    ].filter(Boolean);
    for (const from of candidates) {
      if (!fs.existsSync(from)) continue;
      try {
        return createRequire(from)("playwright");
      } catch {
        // Try the next known workspace package root.
      }
    }
    throw new Error(
      "Cannot load Playwright. Install it for this repo, or set PLAYWRIGHT_REQUIRE_FROM=/path/to/package.json. " +
      `Original error: ${directError.message}`
    );
  }
}

function readEnvValue(file, key) {
  const text = fs.readFileSync(file, "utf8");
  return text.match(new RegExp(`^${key}=(.*)$`, "m"))?.[1] || "";
}

async function readJson(res) {
  const text = await res.text();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

async function apiJson(url, opts = {}) {
  const res = await fetch(url, opts);
  const json = await readJson(res);
  if (!res.ok) {
    const snippet = json && json.raw ? json.raw.slice(0, 200) : JSON.stringify(json).slice(0, 200);
    throw new Error(`${opts.method || "GET"} ${url} -> ${res.status} ${snippet}`);
  }
  return json;
}

async function login() {
  const adminPwd = readEnvValue(path.join(DEPLOY_DIR, ".env"), "OCTO_ADMIN_PWD");
  if (!adminPwd) die(`OCTO_ADMIN_PWD missing in ${DEPLOY_DIR}/.env`);
  const got = await apiJson(`${BASE}/api/v1/user/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: "superAdmin", password: adminPwd, flag: 1 })
  });
  const token = got.token;
  if (!token) die("Login response did not include token");
  const spaces = await apiJson(`${BASE}/api/v1/space/my`, { headers: { token } });
  const space = spaces?.[0]?.space_id;
  if (!space) die("No space returned for superAdmin");
  return { token, space };
}

async function ensureProject(api, headers) {
  const projects = await apiJson(`${api}/projects`, { headers });
  const list = Array.isArray(projects?.data) ? projects.data : (Array.isArray(projects) ? projects : []);
  const existing = list.find((p) => p.name === "v2冒烟项目") || list.find((p) => p.name === "v2 UI sweep project");
  if (existing) return existing;
  return await apiJson(`${api}/projects`, {
    method: "POST",
    headers,
    body: JSON.stringify({ name: "v2 UI sweep project", description: "browser UI sweep fixtures" })
  });
}

async function createMatter(api, headers, project, label) {
  return await apiJson(`${api}/matters`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      title: `UI detail state sweep ${label} ${RUN_LABEL}`,
      project_id: project.id,
      leader_uid: "admin",
      brief: `fixture for ${label}`
    })
  });
}

async function setStatus(api, headers, id, status, reason) {
  return await apiJson(`${api}/matters/${id}/status`, {
    method: "PUT",
    headers,
    body: JSON.stringify(reason ? { status, reason } : { status })
  });
}

async function createStateFixtures(api, headers, project, created) {
  const states = [];
  const open = await createMatter(api, headers, project, "open");
  created.push(open.id);
  states.push({ state: "open", id: open.id, title: open.title, reason: "" });

  const inProgress = await createMatter(api, headers, project, "in_progress");
  created.push(inProgress.id);
  await setStatus(api, headers, inProgress.id, "in_progress");
  states.push({ state: "in_progress", id: inProgress.id, title: inProgress.title, reason: "" });

  const review = await createMatter(api, headers, project, "review");
  created.push(review.id);
  await setStatus(api, headers, review.id, "in_progress");
  await setStatus(api, headers, review.id, "review");
  states.push({ state: "review", id: review.id, title: review.title, reason: "" });

  const blocked = await createMatter(api, headers, project, "blocked");
  created.push(blocked.id);
  await setStatus(api, headers, blocked.id, "in_progress");
  await setStatus(api, headers, blocked.id, "blocked", "missing data-source permission");
  states.push({ state: "blocked", id: blocked.id, title: blocked.title, reason: "missing data-source permission" });
  return states;
}

async function cleanup(api, headers, created) {
  const deleted = [];
  for (const id of [...created].reverse()) {
    let last = { id, status: 0, attempts: 0 };
    for (let attempt = 1; attempt <= 5; attempt += 1) {
      try {
        const res = await fetch(`${api}/matters/${id}`, { method: "DELETE", headers });
        last = { id, status: res.status, attempts: attempt };
        if (res.status === 204 || res.status === 404) break;
      } catch (err) {
        last = { id, status: 0, attempts: attempt, error: err.message };
      }
      await sleep(250 * attempt);
    }
    deleted.push(last);
  }
  return deleted;
}

async function verifyDeleted(api, headers, created) {
  const active = [];
  for (const id of created) {
    try {
      const res = await fetch(`${api}/matters/${id}`, { headers });
      if (res.ok) active.push(id);
      else if (res.status !== 404) active.push(`${id}:verify-${res.status}`);
    } catch (err) {
      active.push(`${id}:verify-error:${err.message}`);
    }
  }
  return active;
}

async function sweepViewport(browser, auth, states, viewportSpec) {
  const context = await browser.newContext({
    viewport: viewportSpec.viewport,
    isMobile: !!viewportSpec.isMobile,
    deviceScaleFactor: viewportSpec.deviceScaleFactor || 1
  });
  await context.addInitScript(({ token, space }) => {
    window.localStorage.setItem("token", token);
    window.localStorage.setItem("currentSpaceId", space);
    window.localStorage.setItem("uid", "admin");
    window.localStorage.setItem("name", "superAdmin");
  }, auth);
  const page = await context.newPage();
  const consoleErrors = [];
  const pageErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => pageErrors.push(err.message));

  const results = [];
  for (const state of states) {
    const consoleStart = consoleErrors.length;
    const pageStart = pageErrors.length;
    await page.goto(`${BASE}/matter/ui/#/matter/${state.id}`, { waitUntil: "networkidle" });
    await page.waitForFunction((title) => document.body.innerText.includes(title), state.title, { timeout: 10000 });
    const metrics = await page.evaluate((reason) => {
      const title = document.querySelector("h1")?.textContent?.trim() || "";
      function isActuallyVisible(el) {
        const closedDetails = el.closest("details:not([open])");
        if (closedDetails) {
          const summary = closedDetails.querySelector("summary");
          if (!summary || !summary.contains(el)) return false;
        }
        if (typeof el.checkVisibility === "function" && !el.checkVisibility({ checkVisibilityCSS: true })) return false;
        const rect = el.getBoundingClientRect();
        const style = getComputedStyle(el);
        return !!(rect.width && rect.height) && style.visibility !== "hidden" && style.display !== "none";
      }
      const unnamedButtons = Array.from(document.querySelectorAll("button")).filter((button) => {
        const name = (button.innerText || button.getAttribute("aria-label") || button.getAttribute("title") || "").trim();
        return isActuallyVisible(button) && !name;
      }).map((button) => {
        const rect = button.getBoundingClientRect();
        return {
          html: button.outerHTML.slice(0, 160),
          x: Math.round(rect.x),
          y: Math.round(rect.y),
          w: Math.round(rect.width),
          h: Math.round(rect.height)
        };
      });
      return {
        title,
        hasReason: reason ? document.body.innerText.includes(reason) : true,
        scrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
        overflow: Math.max(0, document.documentElement.scrollWidth - window.innerWidth),
        unnamedButtons
      };
    }, state.reason);
    const screenshot = path.join(OUT_DIR, `matter-detail-${state.state}-${viewportSpec.name}-${RUN_LABEL}.png`);
    await page.screenshot({ path: screenshot, fullPage: true });
    results.push({
      viewport: viewportSpec.name,
      state: state.state,
      id: state.id,
      screenshot,
      consoleErrors: consoleErrors.slice(consoleStart),
      pageErrors: pageErrors.slice(pageStart),
      ...metrics
    });
  }
  await context.close();
  return results;
}

fs.mkdirSync(OUT_DIR, { recursive: true });
const { chromium } = await loadPlaywright();
const auth = await login();
const API = `${BASE}/matter/api/v1`;
const headers = { token: auth.token, "X-Space-Id": auth.space, "Content-Type": "application/json" };
let project = null;
const created = [];
let states = [];
let browser = null;
let results = [];
let cleanupResult = [];
let activeAfterCleanup = [];
let runError = null;
try {
  project = await ensureProject(API, headers);
  states = await createStateFixtures(API, headers, project, created);
  browser = await chromium.launch({ headless: true });
  const viewports = [
    { name: "desktop", viewport: { width: 1440, height: 1000 } },
    { name: "mobile", viewport: { width: 390, height: 844 }, isMobile: true, deviceScaleFactor: 2 }
  ];
  for (const viewport of viewports) {
    results = results.concat(await sweepViewport(browser, auth, states, viewport));
  }
} catch (err) {
  runError = err;
} finally {
  if (browser) await browser.close();
  if (!KEEP_FIXTURES && created.length) {
    cleanupResult = await cleanup(API, headers, created);
    activeAfterCleanup = await verifyDeleted(API, headers, created);
  }
}

const failures = [];
for (const row of results) {
  if (row.consoleErrors.length) failures.push(`${row.viewport}/${row.state}: console errors`);
  if (row.pageErrors.length) failures.push(`${row.viewport}/${row.state}: page errors`);
  if (row.overflow > 0) failures.push(`${row.viewport}/${row.state}: horizontal overflow ${row.overflow}`);
  if (!row.hasReason) failures.push(`${row.viewport}/${row.state}: blocked reason not visible`);
  if (row.unnamedButtons?.length) failures.push(`${row.viewport}/${row.state}: ${row.unnamedButtons.length} visible buttons without names`);
}
if (runError) failures.push(`run error: ${runError.message}`);
if (activeAfterCleanup.length) failures.push(`cleanup left active fixtures: ${activeAfterCleanup.join(",")}`);

console.log(JSON.stringify({
  base: BASE,
  runLabel: RUN_LABEL,
  project: project ? project.id : null,
  created,
  states,
  results,
  cleanup: KEEP_FIXTURES ? "kept by KEEP_FIXTURES=1" : cleanupResult,
  activeAfterCleanup,
  failures
}, null, 2));
console.log(`RESULT: ${results.length} checked, ${failures.length} failed`);
if (failures.length) process.exit(1);
