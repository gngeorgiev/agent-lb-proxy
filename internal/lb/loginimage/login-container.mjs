import fs from "node:fs/promises";
import { constants as fsConstants } from "node:fs";
import process from "node:process";
import { spawn } from "node:child_process";
import { chromium } from "playwright-core";

const requestPath = process.env.LOGIN_REQUEST_PATH || "/work/request/request.json";
const outputHome = process.env.LOGIN_OUTPUT_HOME || "/work/output/codex-home";
const chromiumPath = process.env.CHROMIUM_BIN || "/usr/bin/chromium";
const loginTimeoutMs = Number(process.env.LOGIN_TIMEOUT_MS || 8 * 60 * 1000);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function log(line) {
  process.stderr.write(`${line}\n`);
}

async function pathExists(path) {
  try {
    await fs.access(path, fsConstants.F_OK);
    return true;
  } catch {
    return false;
  }
}

async function readRequest() {
  const raw = await fs.readFile(requestPath, "utf8");
  const req = JSON.parse(raw);
  if (!req.username || !req.password) {
    throw new Error("request.json must include username and password");
  }
  return req;
}

function spawnCodexLogin(homeDir) {
  return spawn("codex", ["login"], {
    env: { ...process.env, CODEX_HOME: homeDir },
    stdio: ["ignore", "pipe", "pipe"],
  });
}

function captureLoginURL(child) {
  return new Promise((resolve, reject) => {
    let combined = "";
    let resolved = false;
    const timeout = setTimeout(() => {
      if (!resolved) {
        reject(new Error("timed out waiting for codex login URL"));
      }
    }, 30_000);

    const onChunk = (chunk) => {
      const text = chunk.toString();
      combined += text;
      process.stderr.write(text);
      const match = combined.match(/https:\/\/auth\.openai\.com\/oauth\/authorize\S+/);
      if (match && !resolved) {
        resolved = true;
        clearTimeout(timeout);
        resolve(match[0]);
      }
    };

    child.stdout.on("data", onChunk);
    child.stderr.on("data", onChunk);
    child.once("error", (err) => {
      if (!resolved) {
        clearTimeout(timeout);
        reject(err);
      }
    });
    child.once("exit", (code) => {
      if (!resolved) {
        clearTimeout(timeout);
        reject(new Error(`codex login exited before emitting an auth URL (code=${code ?? "unknown"})`));
      }
    });
  });
}

async function clickFirstVisible(page, selectors) {
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    try {
      if (await locator.isVisible({ timeout: 1500 })) {
        await locator.click({ timeout: 5000 });
        return true;
      }
    } catch {
    }
  }
  return false;
}

async function fillFirstVisible(page, selectors, value) {
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    try {
      if (await locator.isVisible({ timeout: 1500 })) {
        await locator.fill(value, { timeout: 5000 });
        return true;
      }
    } catch {
    }
  }
  return false;
}

async function maybeDismissPrompts(page) {
  const selectors = [
    'button:has-text("Accept")',
    'button:has-text("Agree")',
    'button:has-text("Continue")',
    'button:has-text("Authorize")',
    'button:has-text("Allow")',
    'button:has-text("Open Codex")',
    'button[type="submit"]',
  ];
  for (let i = 0; i < 6; i += 1) {
    const clicked = await clickFirstVisible(page, selectors);
    if (!clicked) {
      return;
    }
    await sleep(1000);
  }
}

async function automateLogin(url, username, password) {
  const browser = await chromium.launch({
    executablePath: chromiumPath,
    headless: true,
    args: [
      "--no-sandbox",
      "--disable-gpu",
      "--disable-dev-shm-usage",
      "--disable-setuid-sandbox",
    ],
  });
  const page = await browser.newPage();
  try {
    await page.goto(url, { waitUntil: "domcontentloaded", timeout: 60_000 });

    await fillFirstVisible(page, [
      'input[type="email"]',
      'input[name="email"]',
      'input[name="username"]',
      'input[autocomplete="username"]',
    ], username);
    await clickFirstVisible(page, [
      'button:has-text("Continue")',
      'button:has-text("Next")',
      'button[type="submit"]',
      'input[type="submit"]',
    ]);

    await clickFirstVisible(page, [
      'button:has-text("Continue with password")',
      'button:has-text("Use password")',
    ]);

    const filledPassword = await fillFirstVisible(page, [
      'input[type="password"]',
      'input[name="password"]',
      'input[autocomplete="current-password"]',
    ], password);
    if (!filledPassword) {
      throw new Error("password input did not appear; interactive verification may be required");
    }

    await clickFirstVisible(page, [
      'button:has-text("Continue")',
      'button:has-text("Sign in")',
      'button:has-text("Log in")',
      'button[type="submit"]',
      'input[type="submit"]',
    ]);

    for (let i = 0; i < 20; i += 1) {
      if (page.url().startsWith("http://localhost:")) {
        return;
      }
      await maybeDismissPrompts(page);
      if (page.url().startsWith("http://localhost:")) {
        return;
      }
      await sleep(1000);
    }
    throw new Error(`login flow did not reach the local callback, current URL=${page.url()}`);
  } finally {
    await browser.close();
  }
}

async function waitForCodex(child) {
  return new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => {
      if (code === 0) {
        resolve();
        return;
      }
      reject(new Error(`codex login exited with status ${code ?? "unknown"}`));
    });
  });
}

async function waitForAuthFile(homeDir) {
  const authPath = `${homeDir}/auth.json`;
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (await pathExists(authPath)) {
      return;
    }
    await sleep(500);
  }
  throw new Error("codex login completed but auth.json was not created");
}

async function main() {
  const timer = setTimeout(() => {
    log("login flow timed out");
    process.exit(1);
  }, loginTimeoutMs);
  try {
    const request = await readRequest();
    await fs.mkdir(outputHome, { recursive: true, mode: 0o700 });
    const child = spawnCodexLogin(outputHome);
    const loginURL = await captureLoginURL(child);
    log(`opening login URL inside Chromium`);
    await automateLogin(loginURL, request.username, request.password);
    await waitForCodex(child);
    await waitForAuthFile(outputHome);
    log(`login completed and auth.json captured under ${outputHome}`);
  } finally {
    clearTimeout(timer);
  }
}

main().catch((err) => {
  log(`login automation failed: ${err.message}`);
  process.exit(1);
});
