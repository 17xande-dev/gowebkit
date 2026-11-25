#!/usr/bin/env node
// First-run acceptance check for a generated app, driven through a real
// headless Chromium over the Chrome DevTools Protocol. Node 22+, no npm
// dependencies. The browser is always started with --headless: this script
// never opens a visible window.
//
// Configuration (environment):
//   E2E_BASE_URL     where the app listens, exactly as BASE_URL says (required)
//   E2E_MAILPIT_URL  mailpit's HTTP address (required: step 6 reads its inbox)
//   E2E_LOGS_CMD     shell command printing the server log, to read the setup
//                    token (default: docker compose logs --no-color server)
//   E2E_SETUP_TOKEN  use this token instead of reading the log
//   E2E_OUT          directory for screenshots and browser logs (default: a new temp directory)
//   E2E_CHROME       browser binary (default: chromium, google-chrome, chrome
//                    from PATH)
//   E2E_NO_SANDBOX   set to 1 where Chromium's sandbox cannot start (containers)
//
// Exit status 0 only when every check passed.

import { execSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Browser, findBrowser } from "./browser.mjs";

const EXPECTED_CSP =
  "default-src 'self'; img-src 'self'; font-src 'self'; style-src 'self'; script-src 'self'; " +
  "form-action 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'";
const SIGN_IN_FAILED = "Sign-in failed. Check your credentials and try again.";

const base = (process.env.E2E_BASE_URL ?? "").replace(/\/+$/, "");
const mailpit = (process.env.E2E_MAILPIT_URL ?? "").replace(/\/+$/, "");
if (!base || !mailpit) {
  console.error("E2E_BASE_URL and E2E_MAILPIT_URL are required");
  process.exit(2);
}
const outDir = process.env.E2E_OUT || mkdtempSync(join(tmpdir(), "e2e-shots-"));
mkdirSync(outDir, { recursive: true });

// Throwaway credentials, generated per run, never printed.
const rand = () => Math.random().toString(36).slice(2, 10);
const adminEmail = `admin-${rand()}@example.com`;
const adminPassword = `Admin-${rand()}-${rand()}`;
const memberEmail = `member-${rand()}@example.com`;
const memberTemp = `Temp-${rand()}-${rand()}`;
const memberPassword = `Member-${rand()}-${rand()}`;

const results = [];
function check(name, ok, detail = "") {
  results.push({ name, ok });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${!ok && detail ? ` (${detail})` : ""}`);
  return ok;
}

// ---- browser ---------------------------------------------------------------

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let browser;
for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(sig, () => { browser?.closeOnExit(); process.exit(130); });
}
process.on("exit", () => browser?.closeOnExit());

class Page {
  constructor(ws) {
    this.ws = ws;
    this.id = 0;
    this.pending = new Map();
    this.handlers = new Map();
    ws.addEventListener("message", (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id !== undefined) {
        const p = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        if (p) msg.error ? p.reject(new Error(`${p.method}: ${msg.error.message}`)) : p.resolve(msg.result);
      } else {
        for (const h of this.handlers.get(msg.method) ?? []) h(msg.params);
      }
    });
  }
  static async open(target) {
    const ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.addEventListener("open", res); ws.addEventListener("error", rej); });
    return new Page(ws);
  }
  send(method, params = {}) {
    const id = ++this.id;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.ws.send(JSON.stringify({ id, method, params }));
    });
  }
  on(method, fn) {
    if (!this.handlers.has(method)) this.handlers.set(method, []);
    this.handlers.get(method).push(fn);
  }
  once(method) {
    return new Promise((resolve) => {
      const list = this.handlers.get(method) ?? [];
      const fn = (p) => { list.splice(list.indexOf(fn), 1); resolve(p); };
      list.push(fn);
      this.handlers.set(method, list);
    });
  }
  async eval(expression) {
    const r = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (r.exceptionDetails) throw new Error(`eval failed: ${r.exceptionDetails.text} ${expression}`);
    return r.result.value;
  }
}

// ---- helpers over a page ---------------------------------------------------

const consoleProblems = [];
const cspViolations = [];
const visited = [];
// Statuses this script provokes on purpose (a refused page is a document load
// the browser reports as a console error). Each expectation is used up once, so
// an asset that fails to load, or a refusal nobody asked for, still counts.
const expected = [];
const expectStatus = (status, path) => expected.push(`${status} ${path}`);
let page;
let shot = 0;

async function setViewport(width, height, mobile) {
  await page.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile });
}

// Runs in the page. Horizontal overflow alone can be satisfied by squeezing text
// until it is one letter per line, so this looks at the text itself: a table
// cell with text must be at least 3em wide; a short text (60 characters or
// fewer) must not take more than three lines; a plain word (letters and digits, so not an address that may break at a
// hyphen) must not be broken across lines. It returns one description per offender.
const readableText = `(() => {
  const problems = [];
  const describe = (el) => el.tagName.toLowerCase() + (el.className ? "." + String(el.className).trim().replace(/\\s+/g, ".") : "") + " " + JSON.stringify(el.textContent.trim().slice(0, 30));
  for (const el of document.body.querySelectorAll("*")) {
    if (["SCRIPT", "STYLE", "OPTION"].includes(el.tagName)) continue;
    const cs = getComputedStyle(el);
    if (cs.display === "none" || cs.visibility === "hidden") continue;
    const font = parseFloat(cs.fontSize);
    if ((el.tagName === "TH" || el.tagName === "TD") && el.textContent.trim() !== "") {
      const w = el.getBoundingClientRect().width;
      if (w < 3 * font) problems.push("cell narrower than 3em (" + Math.round(w) + "px): " + describe(el));
    }
    const nodes = [...el.childNodes].filter((n) => n.nodeType === 3 && n.textContent.trim() !== "");
    if (nodes.length === 0) continue;
    const text = nodes.map((n) => n.textContent).join(" ").trim();
    const range = document.createRange();
    range.setStartBefore(nodes[0]);
    range.setEndAfter(nodes[nodes.length - 1]);
    const rects = [...range.getClientRects()].filter((r) => r.width > 0 && r.height > 0);
    if (rects.length === 0) continue;
    const lineHeight = Number.isNaN(parseFloat(cs.lineHeight)) ? 1.2 * font : parseFloat(cs.lineHeight);
    const top = Math.min(...rects.map((r) => r.top)), bottom = Math.max(...rects.map((r) => r.bottom));
    const lines = Math.round((bottom - top) / lineHeight);
    if (text.length <= 60 && lines > 3) problems.push(lines + " lines for a short text: " + describe(el));
    else if (/^[\\p{L}\\p{N}]+$/u.test(text) && lines > 1) problems.push("one word broken over " + lines + " lines: " + describe(el));
  }
  return problems;
})()`;

// inspect records one visited page: a desktop and a 390x844 screenshot, the
// narrow viewport's horizontal overflow, and text that is not readable at
// either width.
async function inspect(label) {
  const path = new URL(await page.eval("location.href")).pathname;
  const name = `${String(++shot).padStart(2, "0")}-${label}`;
  await setViewport(1280, 800, false);
  const wideText = await page.eval(readableText);
  await writeShot(`${name}-desktop.png`);
  await setViewport(390, 844, true);
  const m = await page.eval("({scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth})");
  const phoneText = await page.eval(readableText);
  await writeShot(`${name}-phone.png`);
  await setViewport(1280, 800, false);
  const unreadable = [...wideText.map((p) => `1280: ${p}`), ...phoneText.map((p) => `390: ${p}`)];
  visited.push({ label, path, scroll: m.scroll, client: m.client, unreadable });
}

async function writeShot(file) {
  const { data } = await page.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(join(outDir, file), Buffer.from(data, "base64"));
}

async function waitLoad(action) {
  const loaded = page.once("Page.loadEventFired");
  await action();
  await Promise.race([loaded, sleep(15000).then(() => { throw new Error("page load timed out"); })]);
  // Late console/log events arrive just after load.
  await sleep(150);
}

const goto = (path) => waitLoad(() => page.send("Page.navigate", { url: base + path }));
const where = async () => {
  const u = new URL(await page.eval("location.href"));
  return u.pathname;
};
const text = () => page.eval("document.body.innerText");
const q = JSON.stringify;

async function fill(formSelector, fields) {
  for (const [name, value] of Object.entries(fields)) {
    const ok = await page.eval(`(() => {
      const el = document.querySelector(${q(`${formSelector} [name=${name}]`)});
      if (!el) return false;
      el.value = ${q(value)};
      el.dispatchEvent(new Event("input", {bubbles: true}));
      return true;
    })()`);
    if (!ok) throw new Error(`no field ${name} in ${formSelector}`);
  }
}

// submit clicks a form's submit button (so the browser's own validation and
// CSP form-action rules apply) and waits for the resulting navigation.
async function submit(formSelector) {
  await waitLoad(() => page.eval(`(() => {
    const b = document.querySelector(${q(`${formSelector} button[type=submit]`)});
    if (!b) throw new Error("no submit button in " + ${q(formSelector)});
    b.click();
  })()`));
}

async function signOut() {
  await submit('form[action="/logout"]');
}

async function signIn(email, password) {
  await goto("/login");
  await fill('form[action="/login"]', { email, password });
  await submit('form[action="/login"]');
}

function logsCommand() {
  return process.env.E2E_LOGS_CMD || "docker compose logs --no-color server";
}

function readSetupToken() {
  if (process.env.E2E_SETUP_TOKEN) return process.env.E2E_SETUP_TOKEN;
  const out = execSync(logsCommand(), { encoding: "utf8", maxBuffer: 16 << 20, stdio: ["ignore", "pipe", "pipe"] });
  const found = [...out.matchAll(/"setup_token":"([^"]+)"/g)];
  if (found.length === 0) throw new Error("no setup_token in the server log");
  return found[found.length - 1][1];
}

// ---- the run ---------------------------------------------------------------

async function run() {
  // 1. Health and headers, over plain HTTP: bare cookie names.
  const health = await fetch(`${base}/healthz`);
  check("1 healthz is 200 ok", health.status === 200 && (await health.text()).trim() === "ok", `status ${health.status}`);
  for (const path of ["/", "/login"]) {
    const res = await fetch(base + path, { redirect: "manual" });
    check(`1 GET ${path} carries the exact CSP`, res.headers.get("content-security-policy") === EXPECTED_CSP,
      String(res.headers.get("content-security-policy")));
    const cookies = res.headers.getSetCookie();
    check(`1 GET ${path} sets no __Host- cookie`, !cookies.some((c) => c.startsWith("__Host-")), cookies.map((c) => c.split("=")[0]).join(","));
  }

  browser = new Browser({ binary: findBrowser(), outDir, noSandbox: process.env.E2E_NO_SANDBOX === "1" });
  const target = await browser.start();
  page = await Page.open(target);
  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Log.enable");
  await page.send("Network.enable");
  // A binding is callable from the page whatever its CSP, unlike an inline script.
  await page.send("Runtime.addBinding", { name: "reportCSP" });
  await page.send("Page.addScriptToEvaluateOnNewDocument", {
    source: `document.addEventListener("securitypolicyviolation", (e) =>
      window.reportCSP(JSON.stringify({directive: e.violatedDirective, blocked: e.blockedURI, page: location.pathname})));`,
  });
  page.on("Runtime.bindingCalled", (p) => { if (p.name === "reportCSP") cspViolations.push(p.payload); });
  page.on("Runtime.consoleAPICalled", (p) => {
    if (p.type === "error") consoleProblems.push(`console.error: ${p.args.map((a) => a.value ?? a.description).join(" ")}`);
  });
  page.on("Runtime.exceptionThrown", (p) => consoleProblems.push(`exception: ${p.exceptionDetails.text} ${p.exceptionDetails.exception?.description ?? ""}`));
  page.on("Log.entryAdded", (p) => {
    const e = p.entry;
    if (/content security policy/i.test(e.text) || e.source === "security") cspViolations.push(`${e.source}: ${e.text}`);
    if (e.level !== "error") return;
    const status = /status of (\d+)/.exec(e.text)?.[1];
    const key = status && e.url ? `${status} ${new URL(e.url).pathname}` : "";
    const i = expected.indexOf(key);
    if (e.source === "network" && i >= 0) expected.splice(i, 1);
    else consoleProblems.push(`log ${e.source}: ${e.text} ${e.url ?? ""}`);
  });

  // 2. First account through the setup token.
  const token = readSetupToken();
  await goto("/setup");
  await inspect("setup");
  await fill('form[action="/setup"]', { token, name: "First Admin", email: adminEmail, password: adminPassword });
  await submit('form[action="/setup"]');
  check("2 setup lands on the signed-in home", (await where()) === "/" && /Signed in as First Admin/.test(await text()), await where());
  const cookies = (await page.send("Network.getCookies", { urls: [base] })).cookies;
  const session = cookies.find((c) => c.name === "session");
  check("2 a session cookie exists (bare name, HttpOnly)", !!session && session.httpOnly && !cookies.some((c) => c.name.startsWith("__Host-")),
    cookies.map((c) => c.name).join(","));
  await inspect("home-admin");
  expectStatus(404, "/setup");
  await goto("/setup");
  check("2 /setup is gone once an account exists", /not found/i.test(await text()), await text());

  // 3. Sign out, a wrong password, the right one.
  await goto("/");
  await signOut();
  check("3 sign-out returns to /login", (await where()) === "/login", await where());
  await inspect("login");
  expectStatus(422, "/login");
  await signIn(adminEmail, adminPassword + "x");
  const wrong = await text();
  check("3 a wrong password shows the one generic message", wrong.includes(SIGN_IN_FAILED));
  await inspect("login-failed");
  expectStatus(422, "/login");
  await signIn("nobody-" + rand() + "@example.com", adminPassword);
  check("3 an unknown email shows the same message", (await text()).includes(SIGN_IN_FAILED));
  await signIn(adminEmail, adminPassword);
  check("3 the right password signs in", (await where()) === "/" && /Signed in as First Admin/.test(await text()), await where());

  // 4. Create a member; the member is forced to change the temporary password.
  await goto("/admin/users");
  await inspect("users");
  await fill('form[action="/admin/users"]', { name: "Test Member", email: memberEmail, role: "member", password: memberTemp });
  await submit('form[action="/admin/users"]');
  check("4 creating a member shows the notice", /The account was created/.test(await text()), await text());
  await inspect("users-created");
  await signOut();
  await signIn(memberEmail, memberTemp);
  check("4 the member is forced to /account/password", (await where()) === "/account/password", await where());
  await inspect("password-forced");
  await goto("/");
  check("4 another page still redirects to /account/password", (await where()) === "/account/password", await where());
  await fill('form[action="/account/password"]', { current: memberTemp, new: memberPassword, confirm: memberPassword });
  await submit('form[action="/account/password"]');
  check("4 changing the password lands on the home with its notice", (await where()) === "/" && /Your password was changed/.test(await text()), await where());
  await inspect("home-member");
  await goto("/account/password");
  check("4 pages open now (password page, no forced notice)", (await where()) === "/account/password" && !/You must choose a new password/.test(await text()));
  await inspect("password");
  expectStatus(403, "/admin/users");
  await goto("/admin/users");
  check("4 a member is refused the admin area", /Not allowed/.test(await text()), await text());
  await inspect("forbidden");
  await signOut();

  // 5. The last admin cannot disable itself.
  await signIn(adminEmail, adminPassword);
  await goto("/admin/users");
  const row = await page.eval(`(() => {
    for (const tr of document.querySelectorAll("tbody tr")) {
      if (tr.innerText.includes(${q(adminEmail)})) return tr.querySelector('form[action$="/disabled"]').getAttribute("action");
    }
    return null;
  })()`);
  if (!row) throw new Error("admin row not found on the Users page");
  await submit(`form[action=${q(row)}]`);
  check("5 disabling the last admin is refused with the notice", /at least one enabled admin must remain/.test(await text()), await text());
  await inspect("last-admin");

  // 6. The test message reaches the mail sink.
  await goto("/admin/users");
  await submit('form[action="/admin/mail-test"]');
  check("6 the test message notice is shown", /A test message was sent/.test(await text()), await text());
  let delivered = false;
  for (let i = 0; i < 30 && !delivered; i++) {
    const res = await fetch(`${mailpit}/api/v1/messages`);
    const body = await res.json();
    delivered = (body.messages ?? []).some((m) => (m.To ?? []).some((t) => t.Address === adminEmail));
    if (!delivered) await sleep(200);
  }
  check("6 the message is in mailpit, addressed to the admin", delivered);
  await inspect("mail-sent");

  // 7. Throughout.
  check("7 zero console errors", consoleProblems.length === 0 && expected.length === 0,
    [...new Set(consoleProblems), ...expected.map((e) => `expected but not seen: ${e}`)].join(" | "));
  check("7 zero CSP violations", cspViolations.length === 0, [...new Set(cspViolations)].join(" | "));
  const wide = visited.filter((v) => v.scroll > v.client);
  check(`7 no horizontal overflow at 390x844 on ${visited.length} visited pages`, wide.length === 0 && visited.length >= 10,
    wide.map((v) => `${v.label} ${v.path}: scrollWidth ${v.scroll} > ${v.client}`).join(" | ") || `only ${visited.length} pages`);
  const unreadable = visited.filter((v) => v.unreadable.length > 0);
  check(`7 text is readable at 1280 and 390 wide on ${visited.length} visited pages`, unreadable.length === 0,
    unreadable.map((v) => `${v.label} ${v.path}: ${v.unreadable.slice(0, 3).join("; ")}`).join(" | "));
}

let fatal;
try {
  await run();
} catch (err) {
  fatal = err;
  check("run completed", false, String(err?.message ?? err));
} finally {
  if (page) {
    try { page.ws.close(); } catch {}
  }
  await browser?.close();
}
// Console findings are still worth printing when a step threw.
if (fatal) {
  for (const p of consoleProblems) console.log(`  console: ${p}`);
  for (const v of cspViolations) console.log(`  csp: ${v}`);
}
const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} checks passed; screenshots and browser logs in ${outDir}`);
process.exit(failed.length === 0 ? 0 : 1);
