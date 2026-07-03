#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const DEFAULT_OUT = "tshoot-browser-artifacts";

function parseArgs(argv) {
  const args = { out: DEFAULT_OUT, plan: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--plan") {
      args.plan = true;
      continue;
    }
    if (arg === "--url") {
      args.url = argv[i + 1];
      i += 1;
      continue;
    }
    if (arg === "--out") {
      args.out = argv[i + 1];
      i += 1;
      continue;
    }
  }
  return args;
}

function artifactPaths(outDir) {
  const root = path.resolve(outDir || DEFAULT_OUT);
  return {
    har: path.join(root, "network.har"),
    console: path.join(root, "console.jsonl"),
    screenshot: path.join(root, "screenshot.png"),
    trace: path.join(root, "trace.zip"),
  };
}

function printJSON(payload) {
  process.stdout.write(`${JSON.stringify(payload, null, 2)}\n`);
}

function errorPayload(code, message, args, extra = {}) {
  return {
    mode: "error",
    code,
    error: message,
    url: args.url || "",
    artifacts: artifactPaths(args.out),
    ...extra,
  };
}

async function importPlaywright() {
  try {
    const module = await import("playwright");
    if (module.chromium) {
      return module;
    }
    if (module.default?.chromium) {
      return module.default;
    }
    return module;
  } catch (err) {
    if (
      err?.code === "ERR_MODULE_NOT_FOUND" ||
      err?.code === "MODULE_NOT_FOUND" ||
      String(err?.message || "").includes("Cannot find package 'playwright'")
    ) {
      return null;
    }
    throw err;
  }
}

function consoleRecord(type, message, extra = {}) {
  return JSON.stringify({
    ts: new Date().toISOString(),
    type,
    message,
    ...extra,
  });
}

async function collectWithPlaywright(playwright, args, artifacts) {
  await fs.promises.mkdir(path.dirname(artifacts.har), { recursive: true });
  const consoleStream = fs.createWriteStream(artifacts.console, { flags: "w" });
  let browser;
  let context;
  let page;
  let navigationError = null;

  try {
    browser = await playwright.chromium.launch();
    context = await browser.newContext({
      recordHar: {
        path: artifacts.har,
        content: "embed",
        mode: "full",
      },
    });
    await context.tracing.start({
      screenshots: true,
      snapshots: true,
      sources: true,
    });

    page = await context.newPage();
    page.on("console", (msg) => {
      consoleStream.write(
        `${consoleRecord(msg.type(), msg.text(), {
          location: msg.location(),
        })}\n`,
      );
    });
    page.on("pageerror", (err) => {
      consoleStream.write(`${consoleRecord("pageerror", err.message)}\n`);
    });

    try {
      await page.goto(args.url, { waitUntil: "networkidle", timeout: 45_000 });
    } catch (err) {
      navigationError = err;
      consoleStream.write(`${consoleRecord("navigation_error", err.message)}\n`);
    }

    await page.screenshot({ path: artifacts.screenshot, fullPage: true });
    await context.tracing.stop({ path: artifacts.trace });
    await context.close();
    context = null;
    await browser.close();
    browser = null;
  } finally {
    if (context) {
      try {
        await context.tracing.stop({ path: artifacts.trace });
      } catch {
        // Trace may already be stopped or unavailable after a launch failure.
      }
      try {
        await context.close();
      } catch {
        // Closing is best-effort so JSON output remains available.
      }
    }
    if (browser) {
      try {
        await browser.close();
      } catch {
        // Closing is best-effort so JSON output remains available.
      }
    }
    await new Promise((resolve) => consoleStream.end(resolve));
  }

  return {
    mode: "collect",
    url: args.url,
    artifacts,
    navigation_error: navigationError ? navigationError.message : "",
  };
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const artifacts = artifactPaths(args.out);

  if (!args.url) {
    printJSON(errorPayload(2, "missing required --url", args));
    return 2;
  }

  if (args.plan) {
    printJSON({
      mode: "plan",
      url: args.url,
      artifacts,
    });
    return 0;
  }

  let playwright;
  try {
    playwright = await importPlaywright();
  } catch (err) {
    printJSON(errorPayload(4, err.message || String(err), args));
    return 4;
  }
  if (!playwright) {
    printJSON(
      errorPayload(3, "optional dependency playwright is not installed", args, {
        install_hint: "Install Playwright in the generated workspace, for example: npm install playwright && npx playwright install chromium",
      }),
    );
    return 3;
  }

  try {
    printJSON(await collectWithPlaywright(playwright, args, artifacts));
    return 0;
  } catch (err) {
    printJSON(errorPayload(4, err.message || String(err), args));
    return 4;
  }
}

const currentFile = fileURLToPath(import.meta.url);
if (process.argv[1] === currentFile) {
  process.exitCode = await main();
}
