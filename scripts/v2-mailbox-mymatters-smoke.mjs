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
const REQUIRE_MAILBOX_FIXTURE = process.env.REQUIRE_MAILBOX_FIXTURE === "1";
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
    // Best effort only.
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
        // Try next candidate.
      }
    }
    throw new Error(
      "Cannot load Playwright. Install it for this repo, or set PLAYWRIGHT_REQUIRE_FROM=/path/to/package.json. " +
      `Original error: ${directError.message}`
    );
  }
}

function readEnvValue(file, key) {
  if (!fs.existsSync(file)) return "";
  const text = fs.readFileSync(file, "utf8");
  return text.match(new RegExp(`^${key}=(.*)$`, "m"))?.[1] || "";
}

function envValue(key) {
  return process.env[key] || readEnvValue(path.join(DEPLOY_DIR, ".env"), key);
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
    const body = json && json.raw ? json.raw.slice(0, 240) : JSON.stringify(json).slice(0, 240);
    const err = new Error(`${opts.method || "GET"} ${url} -> ${res.status} ${body}`);
    err.status = res.status;
    err.body = json;
    throw err;
  }
  return json;
}

function listData(json) {
  if (Array.isArray(json)) return json;
  if (Array.isArray(json?.data)) return json.data;
  if (Array.isArray(json?.items)) return json.items;
  return [];
}

async function login() {
  const adminPwd = envValue("OCTO_ADMIN_PWD");
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
  return { token, space, uid: got.uid || got.user?.uid || got.data?.uid || "admin", name: got.name || "superAdmin" };
}

async function ensureProject(api, headers) {
  const projects = await apiJson(`${api}/projects`, { headers });
  const list = listData(projects);
  const existing = list.find((p) => p.name === "mailbox/my matters smoke") || list.find((p) => p.name === "v2冒烟项目");
  if (existing) return existing;
  return await apiJson(`${api}/projects`, {
    method: "POST",
    headers,
    body: JSON.stringify({ name: "mailbox/my matters smoke", description: "Mailbox and My Matters live UI smoke fixtures" })
  });
}

async function createMatter(api, headers, project) {
  return await apiJson(`${api}/matters`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      title: `Mailbox My Matters smoke ${RUN_LABEL}`,
      project_id: project.id,
      leader_uid: "admin",
      brief: "temporary fixture for My Matters list/board smoke"
    })
  });
}

async function deleteMatter(api, headers, id) {
  const res = await fetch(`${api}/matters/${id}`, { method: "DELETE", headers });
  if (res.status === 204 || res.status === 404) return { id, status: res.status };
  const json = await readJson(res);
  return { id, status: res.status, body: json };
}

async function findMailboxLetter(api, mailboxHeaders, title) {
  const json = await apiJson(`${api}/mailbox/letters?source_type=system&limit=50`, { headers: mailboxHeaders });
  return listData(json).find((row) => row.title === title) || null;
}

async function deleteMailboxLetter(api, mailboxHeaders, id) {
  const res = await fetch(`${api}/mailbox/letters/${encodeURIComponent(id)}`, { method: "DELETE", headers: mailboxHeaders });
  if (res.status === 204 || res.status === 404) return { id, status: res.status };
  const json = await readJson(res);
  return { id, status: res.status, body: json };
}

async function maybeCreateSystemLetter(api, mailboxHeaders, userID) {
  const internalToken = process.env.NOTIFY_INTERNAL_TOKEN || envValue("NOTIFY_INTERNAL_TOKEN") || envValue("OCTO_NOTIFY_INTERNAL_TOKEN");
  if (!internalToken) {
    return { status: "skipped", reason: "NOTIFY_INTERNAL_TOKEN/OCTO_NOTIFY_INTERNAL_TOKEN is not configured" };
  }
  const title = `Mailbox system smoke ${RUN_LABEL}`;
  const body = `<p>Mailbox live smoke ${RUN_LABEL}</p>`;
  try {
    await apiJson(`${api}/internal/mailbox/system-letter`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Internal-Token": internalToken },
      body: JSON.stringify({ user_ids: [userID], template_id: `smoke-${RUN_LABEL}`, title, body_html: body })
    });
    const letter = await findMailboxLetter(api, mailboxHeaders, title);
    return { status: "created", id: letter?.id || "", title };
  } catch (err) {
    if (REQUIRE_MAILBOX_FIXTURE) throw err;
    return { status: "skipped", reason: err.message };
  }
}

async function newAuthedContext(browser, auth, viewportSpec) {
  const context = await browser.newContext({
    viewport: viewportSpec.viewport,
    isMobile: !!viewportSpec.isMobile,
    deviceScaleFactor: viewportSpec.deviceScaleFactor || 1
  });
  await context.addInitScript(({ token, space, uid, name }) => {
    try {
      window.localStorage.setItem("token", token);
      window.localStorage.setItem("currentSpaceId", space);
      window.localStorage.setItem("uid", uid);
      window.localStorage.setItem("name", name);
    } catch {
      // The Mailbox detail uses a sandboxed srcdoc iframe without same-origin;
      // Playwright init scripts also run there, and localStorage is unavailable.
    }
  }, auth);
  return context;
}

async function checkRoute(browser, auth, viewportSpec, route) {
  const context = await newAuthedContext(browser, auth, viewportSpec);
  const page = await context.newPage();
  const consoleErrors = [];
  const pageErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => pageErrors.push(err.message));

  await page.goto(`${BASE}/matter/ui/${route.hash}`, { waitUntil: "networkidle" });
  if (route.expectedText) {
    await page.waitForFunction((text) => document.body.innerText.includes(text), route.expectedText, { timeout: 15000 });
  }
  if (route.expectedHash) {
    await page.waitForFunction((hash) => location.hash === hash, route.expectedHash, { timeout: 5000 });
  }
  const metrics = await page.evaluate(() => ({
    hash: location.hash,
    title: document.title,
    text: document.body.innerText.slice(0, 2000),
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth
  }));
  const screenshot = path.join(OUT_DIR, `mailbox-mymatters-${route.name}-${viewportSpec.name}-${RUN_LABEL}.png`);
  await page.screenshot({ path: screenshot, fullPage: true });
  await context.close();
  return {
    name: route.name,
    viewport: viewportSpec.name,
    screenshot,
    consoleErrors,
    pageErrors,
    overflow: Math.max(0, metrics.scrollWidth - metrics.innerWidth),
    hash: metrics.hash,
    hasExpectedText: route.expectedText ? metrics.text.includes(route.expectedText) : true,
    hasForbiddenText: route.forbiddenText ? metrics.text.includes(route.forbiddenText) : false
  };
}

acquireAcceptanceLock();
fs.mkdirSync(OUT_DIR, { recursive: true });

const auth = await login();
const api = `${BASE}/matter/api/v1`;
const headers = { token: auth.token, "X-Space-Id": auth.space, "Content-Type": "application/json" };
const mailboxHeaders = { token: auth.token, "Content-Type": "application/json" };
let project = null;
let matter = null;
let mailboxFixture = null;
let browser = null;
let results = [];
let cleanup = [];
let runError = null;

try {
  project = await ensureProject(api, headers);
  matter = await createMatter(api, headers, project);
  mailboxFixture = await maybeCreateSystemLetter(api, mailboxHeaders, auth.uid);

  const { chromium } = await loadPlaywright();
  browser = await chromium.launch({ headless: true });
  const viewports = [
    { name: "desktop", viewport: { width: 1440, height: 1000 } },
    { name: "mobile", viewport: { width: 390, height: 844 }, isMobile: true, deviceScaleFactor: 2 }
  ];
  const routes = [
    { name: "matters-list", hash: "#/matters", expectedText: matter.title, forbiddenText: "读取失败" },
    { name: "matters-board", hash: "#/matters/board", expectedText: matter.title, forbiddenText: "读取失败" },
    { name: "mailbox", hash: "#/mailbox", expectedText: mailboxFixture?.status === "created" ? mailboxFixture.title : "Mailbox", forbiddenText: "读取失败" },
    { name: "legacy-board", hash: "#/board", expectedHash: "#/matters/board", expectedText: matter.title, forbiddenText: "读取失败" },
    { name: "legacy-review", hash: "#/review-me", expectedHash: "#/matters", expectedText: "My Matters", forbiddenText: "读取失败" },
    { name: "legacy-archived", hash: "#/archived", expectedHash: "#/matters", expectedText: "My Matters", forbiddenText: "读取失败" }
  ];
  for (const viewport of viewports) {
    for (const route of routes) {
      results.push(await checkRoute(browser, auth, viewport, route));
    }
  }
} catch (err) {
  runError = err;
} finally {
  if (browser) await browser.close();
  if (!KEEP_FIXTURES && matter?.id) {
    cleanup.push(await deleteMatter(api, headers, matter.id));
  }
  if (!KEEP_FIXTURES && mailboxFixture?.id) {
    cleanup.push(await deleteMailboxLetter(api, mailboxHeaders, mailboxFixture.id));
  }
}

const failures = [];
for (const row of results) {
  if (row.consoleErrors.length) failures.push(`${row.viewport}/${row.name}: console errors`);
  if (row.pageErrors.length) failures.push(`${row.viewport}/${row.name}: page errors`);
  if (row.overflow > 0) failures.push(`${row.viewport}/${row.name}: horizontal overflow ${row.overflow}`);
  if (!row.hasExpectedText) failures.push(`${row.viewport}/${row.name}: expected text missing`);
  if (row.hasForbiddenText) failures.push(`${row.viewport}/${row.name}: error state visible`);
}
if (runError) failures.push(`run error: ${runError.message}`);
if (REQUIRE_MAILBOX_FIXTURE && mailboxFixture?.status !== "created") failures.push("mailbox system-letter fixture was required but not created");
for (const row of cleanup) {
  if (row.status !== 204 && row.status !== 404) failures.push(`cleanup failed for ${row.id}: ${row.status}`);
}

console.log(JSON.stringify({
  base: BASE,
  runLabel: RUN_LABEL,
  project: project ? project.id : null,
  matter: matter ? { id: matter.id, title: matter.title } : null,
  mailboxFixture,
  results,
  cleanup: KEEP_FIXTURES ? "kept by KEEP_FIXTURES=1" : cleanup,
  failures
}, null, 2));
console.log(`RESULT: ${results.length} checked, ${failures.length} failed`);
if (failures.length) process.exit(1);
