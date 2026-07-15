import { createHash, randomUUID } from 'node:crypto';
import { lookup as dnsLookup } from 'node:dns/promises';
import { realpathSync } from 'node:fs';
import { createServer } from 'node:http';
import { isIP } from 'node:net';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  chmod,
  mkdir,
  open,
  readFile,
  rename,
  rm,
} from 'node:fs/promises';

import { redactConsoleText, safeResponseRecord } from './sanitize.mjs';

const PROGRESS_PREFIX = 'TSHOOT_BROWSER_PROGRESS ';
const ALLOWED_ACTIONS = new Set(['goto', 'click', 'fill', 'press', 'select', 'wait_for', 'screenshot']);
const ALLOWED_LOCATORS = new Set(['role', 'label', 'text', 'placeholder', 'test_id', 'css']);
const READ_ONLY_PROD_ACTIONS = new Set(['goto', 'wait_for', 'screenshot']);
export const EVIDENCE_MAX_RECORDS = 1000;
export const EVIDENCE_MAX_BYTES = 1 << 20;
export const EVIDENCE_TRUNCATION_MARKER = Object.freeze({ type: 'truncated', reason: 'record_or_byte_limit' });
const METADATA_HOSTS = new Set([
  'metadata',
  'metadata.google.internal',
  'metadata.goog',
  'instance-data',
  'instance-data.ec2.internal',
  'metadata.azure.internal',
  'metadata.oraclecloud.com',
  'metadata.packet.net',
  'metadata.service.internal',
  'metadata.tencentyun.com',
  'metadata.tencentyun.internal',
]);
const METADATA_ADDRESSES = new Set(['100.100.100.200', 'fd00:ec2::254']);

function ownKeys(value, allowed, label) {
  for (const key of Object.keys(value ?? {})) {
    if (!allowed.has(key)) throw new Error(`${label} field ${key} is not supported`);
  }
}

function requiredString(value, label, maxBytes = 4096) {
  if (typeof value !== 'string' || value.trim() !== value || value.length === 0) {
    throw new Error(`${label} must be a non-empty string`);
  }
  if (Buffer.byteLength(value, 'utf8') > maxBytes) throw new Error(`${label} is too long`);
  return value;
}

export function createBoundedRecordCollector({ maxRecords = EVIDENCE_MAX_RECORDS, maxBytes = EVIDENCE_MAX_BYTES } = {}) {
  const markerBytes = Buffer.byteLength(JSON.stringify(EVIDENCE_TRUNCATION_MARKER), 'utf8');
  if (!Number.isInteger(maxRecords) || maxRecords < 1 || !Number.isInteger(maxBytes) || maxBytes < markerBytes + 2) {
    throw new Error('evidence collector limits are invalid');
  }
  const records = [];
  let serializedBytes = 2;
  let stopped = false;

  const stopWithMarker = () => {
    if (stopped) return;
    const separatorBytes = records.length === 0 ? 0 : 1;
    if (records.length >= maxRecords || serializedBytes + separatorBytes + markerBytes > maxBytes) {
      records.length = 0;
      serializedBytes = 2;
    }
    records.push({ ...EVIDENCE_TRUNCATION_MARKER });
    serializedBytes += (records.length === 1 ? 0 : 1) + markerBytes;
    stopped = true;
  };

  return {
    add(record) {
      if (stopped) return false;
      let encoded;
      try {
        encoded = JSON.stringify(record);
      } catch {
        stopWithMarker();
        return false;
      }
      if (typeof encoded !== 'string') {
        stopWithMarker();
        return false;
      }
      const recordBytes = Buffer.byteLength(encoded, 'utf8');
      const separatorBytes = records.length === 0 ? 0 : 1;
      const markerSeparatorBytes = 1;
      const exceedsRecords = records.length + 2 > maxRecords;
      const exceedsBytes = serializedBytes + separatorBytes + recordBytes + markerSeparatorBytes + markerBytes > maxBytes;
      if (exceedsRecords || exceedsBytes) {
        stopWithMarker();
        return false;
      }
      records.push(record);
      serializedBytes += separatorBytes + recordBytes;
      return true;
    },
    isStopped() {
      return stopped;
    },
    truncate() {
      stopWithMarker();
    },
    snapshot() {
      return records.map((record) => ({ ...record }));
    },
  };
}

function normalizeOrigin(raw) {
  const parsed = new URL(requiredString(raw, 'origin'));
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) {
    throw new Error('origin is not an allowed HTTP(S) origin');
  }
  return parsed.origin;
}

function parseHTTPURL(raw) {
  const parsed = new URL(requiredString(raw, 'URL'));
  if (!['http:', 'https:'].includes(parsed.protocol)) throw new Error('URL scheme is blocked');
  if (parsed.username || parsed.password) throw new Error('URL userinfo is blocked');
  const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, '').replace(/\.$/, '');
  if (!host || METADATA_HOSTS.has(host)) throw new Error('URL metadata host is blocked');
  return { parsed, host };
}

function validatePolicy(policy) {
  ownKeys(policy, new Set(['allowed_origins', 'private_origins', 'auth_origins', 'is_prod']), 'policy');
  for (const field of ['allowed_origins', 'private_origins', 'auth_origins']) {
    if (!Array.isArray(policy?.[field])) throw new Error(`policy ${field} must be an array`);
    for (const origin of policy[field]) normalizeOrigin(origin);
  }
  if (typeof policy.is_prod !== 'boolean') throw new Error('policy is_prod must be boolean');
}

function validateLocator(locator, label) {
  if (!locator || typeof locator !== 'object' || Array.isArray(locator)) throw new Error(`${label} locator is required`);
  ownKeys(locator, new Set(['kind', 'value', 'name']), `${label} locator`);
  if (!ALLOWED_LOCATORS.has(locator.kind)) throw new Error(`${label} locator kind is not supported`);
  requiredString(locator.value, `${label} locator value`);
  if (locator.name !== undefined) requiredString(locator.name, `${label} locator name`);
}

export function validateWorkerRequest(request) {
  if (!request || typeof request !== 'object' || Array.isArray(request)) throw new Error('worker request must be an object');
  ownKeys(request, new Set(['mode', 'plan', 'policy', 'staging_dir', 'storage_state_path', 'headless']), 'request');

  if (request.mode !== 'execute' && request.mode !== 'login') throw new Error('worker request mode is not supported');
  if (typeof request.headless !== 'boolean') throw new Error('headless must be boolean');
  validatePolicy(request.policy);

  const plan = request.plan;
  if (!plan || typeof plan !== 'object' || Array.isArray(plan)) throw new Error('plan must be an object');
  ownKeys(plan, new Set(['version', 'start_url', 'actions', 'assertions']), 'plan');
  if (plan.version !== 1) throw new Error('plan version must be 1');
  const start = parseHTTPURL(plan.start_url).parsed;
  if (request.mode === 'login') {
    if (request.headless !== false) throw new Error('login browser must be visible');
    if (request.staging_dir !== '') throw new Error('login must not use evidence staging');
    if (!isAbsolute(requiredString(request.storage_state_path, 'storage_state_path'))) throw new Error('storage_state_path must be absolute');
    if (!Array.isArray(plan.actions) || plan.actions.length !== 0) throw new Error('login plan actions are forbidden');
    if (!Array.isArray(plan.assertions) || plan.assertions.length !== 0) throw new Error('login plan assertions are forbidden');
    if (start.pathname !== '/' || start.search || start.hash) throw new Error('login must start at an application origin');
    const applicationOrigins = new Set(request.policy.allowed_origins.map(normalizeOrigin));
    if (!applicationOrigins.has(start.origin)) throw new Error('login must start at a configured application origin');
    return;
  }

  if (!isAbsolute(requiredString(request.staging_dir, 'staging_dir'))) throw new Error('staging_dir must be absolute');
  if (request.storage_state_path !== undefined && !isAbsolute(requiredString(request.storage_state_path, 'storage_state_path'))) {
    throw new Error('storage_state_path must be absolute');
  }
  if (!Array.isArray(plan.actions) || plan.actions.length < 1 || plan.actions.length > 40) throw new Error('plan actions must contain 1 to 40 entries');
  if (!Array.isArray(plan.assertions) || plan.assertions.length < 1) throw new Error('plan assertions are required');

  const ids = new Set();
  for (const action of plan.actions) {
    if (!action || typeof action !== 'object' || Array.isArray(action)) throw new Error('browser action must be an object');
    ownKeys(action, new Set(['id', 'action', 'locator', 'url', 'value', 'key', 'screenshot_after']), 'action');
    requiredString(action.id, 'action id', 256);
    if (ids.has(action.id)) throw new Error('action id is duplicated');
    ids.add(action.id);
    if (!ALLOWED_ACTIONS.has(action.action)) throw new Error(`action ${String(action.action)} is not supported`);
    if (request.policy.is_prod && !READ_ONLY_PROD_ACTIONS.has(action.action)) throw new Error('interaction action is blocked in production');
    if (action.screenshot_after !== undefined && typeof action.screenshot_after !== 'boolean') throw new Error('screenshot_after must be boolean');
    if (action.action === 'screenshot' && action.screenshot_after === true) throw new Error('screenshot_after is forbidden for screenshot action');

    const locatorActions = new Set(['click', 'fill', 'press', 'select', 'wait_for']);
    if (locatorActions.has(action.action)) validateLocator(action.locator, action.id);
    else if (action.locator !== undefined) throw new Error(`${action.action} locator is forbidden`);
    if (action.action === 'goto') parseHTTPURL(action.url);
    else if (action.url !== undefined) throw new Error(`${action.action} URL is forbidden`);
    if (action.action === 'fill' || action.action === 'select') requiredString(action.value, `${action.action} value`);
    else if (action.value !== undefined) throw new Error(`${action.action} value is forbidden`);
    if (action.action === 'press') requiredString(action.key, 'press key', 128);
    else if (action.key !== undefined) throw new Error(`${action.action} key is forbidden`);
  }
  for (const assertion of plan.assertions) {
    if (!assertion || typeof assertion !== 'object' || Array.isArray(assertion)) throw new Error('assertion must be an object');
    ownKeys(assertion, new Set(['kind', 'value']), 'assertion');
    if (assertion.kind !== 'visible_text') throw new Error('assertion kind is not supported');
    requiredString(assertion.value, 'assertion value');
  }
}

function ipv4Number(address) {
  const parts = address.split('.').map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return null;
  return (((parts[0] * 256 + parts[1]) * 256 + parts[2]) * 256 + parts[3]) >>> 0;
}

function classifyAddress(rawAddress) {
  const address = String(rawAddress).toLowerCase().split('%')[0];
  if (METADATA_ADDRESSES.has(address)) return 'metadata';
  const family = isIP(address);
  if (family === 4) {
    const value = ipv4Number(address);
    const first = value >>> 24;
    const second = (value >>> 16) & 0xff;
    if (first === 0 || first >= 224) return 'non-routable';
    if (first === 169 && second === 254) return 'link-local';
    if (first === 10 || first === 127 || (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168)) return 'private';
    return 'public';
  }
  if (family === 6) {
    if (address === '::' || address.startsWith('ff')) return 'non-routable';
    if (address === '::1') return 'private';
    if (/^fe[89ab]/.test(address)) return 'link-local';
    if (/^f[cd]/.test(address)) return 'private';
    if (address.startsWith('::ffff:')) {
      const mapped = address.slice('::ffff:'.length);
      return isIP(mapped) === 4 ? classifyAddress(mapped) : 'private';
    }
    return 'public';
  }
  return 'invalid';
}

export async function assertAllowedURL(raw, policy, lookup = dnsLookup) {
  validatePolicy(policy);
  const { parsed, host } = parseHTTPURL(raw);
  const allowedOrigins = new Set([...policy.allowed_origins, ...policy.auth_origins].map(normalizeOrigin));
  if (!allowedOrigins.has(parsed.origin)) throw new Error('URL origin is not allowed');
  const privateOrigins = new Set(policy.private_origins.map(normalizeOrigin));
  let addresses;
  if (isIP(host)) addresses = [{ address: host, family: isIP(host) }];
  else addresses = await lookup(host, { all: true, verbatim: true });
  if (!Array.isArray(addresses) || addresses.length === 0) throw new Error('URL DNS resolution returned no addresses');
  for (const answer of addresses) {
    const classification = classifyAddress(answer.address);
    if (classification === 'metadata' || classification === 'link-local' || classification === 'non-routable' || classification === 'invalid') {
      throw new Error(`URL ${classification} address is blocked`);
    }
    if (classification === 'private' && !privateOrigins.has(parsed.origin)) {
      throw new Error('URL private address requires exact configured origin');
    }
  }
  return parsed;
}

export function buildLocator(page, locator) {
  validateLocator(locator, 'action');
  switch (locator.kind) {
    case 'role': return page.getByRole(locator.value, locator.name ? { name: locator.name } : {});
    case 'label': return page.getByLabel(locator.value);
    case 'text': return page.getByText(locator.value, { exact: false });
    case 'placeholder': return page.getByPlaceholder(locator.value);
    case 'test_id': return page.getByTestId(locator.value);
    case 'css': return page.locator(`css=${locator.value}`);
    default: throw new Error('action locator kind is not supported');
  }
}

function emitProgress(code, message, actionId = '', current = 0, total = 0) {
  process.stderr.write(`${PROGRESS_PREFIX}${JSON.stringify({ code, message, action_id: actionId, current, total })}\n`);
}

async function syncDirectory(path) {
  const handle = await open(path, 'r');
  try {
    await handle.sync();
  } finally {
    await handle.close();
  }
}

async function atomicWrite(path, content) {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = join(dirname(path), `.${basename(path)}-${randomUUID()}`);
  const handle = await open(temporary, 'wx', 0o600);
  try {
    await handle.writeFile(content);
    await handle.sync();
  } finally {
    await handle.close();
  }
  try {
    await rename(temporary, path);
    await syncDirectory(dirname(path));
  } catch (error) {
    await rm(temporary, { force: true });
    throw error;
  }
}

async function capturePNG(page, stagingDir, name) {
  const finalPath = join(stagingDir, name);
  const temporary = join(stagingDir, `.${name}-${randomUUID()}.png`);
  try {
    await page.screenshot({ path: temporary, fullPage: true, type: 'png' });
    const handle = await open(temporary, 'r');
    try {
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporary, finalPath);
    await syncDirectory(stagingDir);
  } catch (error) {
    await rm(temporary, { force: true });
    throw error;
  }
  return `browser/${name}`;
}

function safeFilePart(value) {
  const result = String(value).replace(/[^A-Za-z0-9_.-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64);
  return result || 'action';
}

function knownAuthOrigin(rawURL, policy) {
  try {
    const origin = new URL(rawURL).origin;
    return new Set(policy.auth_origins.map(normalizeOrigin)).has(origin);
  } catch {
    return false;
  }
}

export async function hasVisiblePasswordField(page) {
  const password = page.locator('input[type="password"]');
  const count = await password.count().catch(() => 0);
  for (let index = 0; index < count; index += 1) {
    if (await password.nth(index).isVisible().catch(() => false)) return true;
  }
  return false;
}

async function loginPageState(page, policy, authFailure) {
  const passwordVisible = await hasVisiblePasswordField(page);
  let knownRoute = false;
  let httpPage = false;
  try {
    const parsed = new URL(page.url());
    httpPage = parsed.protocol === 'http:' || parsed.protocol === 'https:';
    knownRoute = /\/(?:login|sign-in|signin|sso)(?:\/|$)/i.test(parsed.pathname);
  } catch {
    // about:blank before a failed navigation is not a login page.
  }
  return {
    required: passwordVisible || knownRoute || knownAuthOrigin(page.url(), policy) || authFailure,
    passwordVisible,
    httpPage,
  };
}

export async function observeLoginState(pages, policy, previouslyStarted = false, authFailure = false) {
  const states = [];
  for (const page of pages) states.push(await loginPageState(page, policy, false));
  const activeLogin = states.some((state) => state.required);
  const started = previouslyStarted || activeLogin;
  const ready = started
    && !authFailure
    && pages.length > 0
    && states.every((state) => state.httpPage && !state.required);
  return { started, ready };
}

export function createLoginAuthFailureTracker(now = Date.now, quietWindowMs = 1_000) {
  let lastAuthFailureAt = null;
  return {
    observeStatus(status) {
      if (status === 401 || status === 403) lastAuthFailureAt = now();
    },
    active() {
      if (lastAuthFailureAt === null) return false;
      const elapsed = now() - lastAuthFailureAt;
      return elapsed >= 0 && elapsed <= quietWindowMs;
    },
  };
}

export async function captureSafePNG(page, request, name, getAuthFailure, capture = capturePNG) {
  if ((await loginPageState(page, request.policy, getAuthFailure())).required) {
    return { loginRequired: true, path: '' };
  }
  const path = await capture(page, request.staging_dir, name);
  if ((await loginPageState(page, request.policy, getAuthFailure())).required) {
    await rm(join(request.staging_dir, path.replace(/^browser\//, '')), { force: true });
    return { loginRequired: true, path: '' };
  }
  return { loginRequired: false, path };
}

async function accessibilitySummary(page) {
  const result = [];
  const nodes = page.locator('a,button,input,select,textarea,[role]');
  const count = Math.min(await nodes.count().catch(() => 0), 25);
  for (let index = 0; index < count; index += 1) {
    const node = nodes.nth(index);
    const visible = await node.isVisible().catch(() => false);
    if (!visible) continue;
    const role = (await node.getAttribute('role').catch(() => '')) || 'element';
    const name = (await node.getAttribute('aria-label').catch(() => ''))
      || (await node.getAttribute('placeholder').catch(() => ''))
      || (await node.textContent().catch(() => ''))
      || '';
    result.push({
      role: redactConsoleText(role).slice(0, 128),
      name: redactConsoleText(name.trim()).slice(0, 512),
      visible: true,
      disabled: await node.isDisabled().catch(() => false),
    });
  }
  return result;
}

async function executeAction(page, action, request, index, captureScreenshot) {
  switch (action.action) {
    case 'goto':
      await assertAllowedURL(action.url, request.policy);
      await page.goto(action.url, { waitUntil: 'domcontentloaded' });
      return { loginRequired: false, path: '' };
    case 'screenshot':
      return captureScreenshot(`action-${String(index + 1).padStart(2, '0')}-${safeFilePart(action.id)}.png`);
    default: {
      const locator = buildLocator(page, action.locator).first();
      if (action.action === 'click') await locator.click();
      else if (action.action === 'fill') {
        const type = (await locator.getAttribute('type').catch(() => '')).toLowerCase();
        if (type === 'password') throw new Error('password input is not allowed');
        await locator.fill(action.value);
      } else if (action.action === 'press') await locator.press(action.key);
      else if (action.action === 'select') await locator.selectOption(action.value);
      else if (action.action === 'wait_for') await locator.waitFor({ state: 'visible' });
      return { loginRequired: false, path: '' };
    }
  }
}

function responseHeadersPromise(response) {
  const names = ['content-type', 'content-length', 'x-request-id', 'request-id', 'x-correlation-id', 'correlation-id', 'x-amzn-requestid', 'x-trace-id', 'trace-id', 'traceparent'];
  return Promise.all(names.map(async (name) => [name, await response.headerValue(name)]));
}

function checkedEvidenceContent(content, label) {
  if (Buffer.byteLength(content, 'utf8') > EVIDENCE_MAX_BYTES) throw new Error(`${label} evidence exceeds its byte limit`);
  return content;
}

async function writeEvidenceFiles(request, networkCollector, consoleCollector, actions) {
  const network = networkCollector.snapshot();
  const consoleRecords = consoleCollector.snapshot();
  if (network.length > EVIDENCE_MAX_RECORDS || consoleRecords.length > EVIDENCE_MAX_RECORDS || actions.length > 40) {
    throw new Error('browser evidence exceeds its record limit');
  }
  const networkJSON = checkedEvidenceContent(`${JSON.stringify(network)}\n`, 'network');
  const consoleJSONL = consoleRecords.map((record) => JSON.stringify(record)).join('\n');
  const consoleContent = checkedEvidenceContent(consoleJSONL ? `${consoleJSONL}\n` : '', 'console');
  const actionJSON = checkedEvidenceContent(`${JSON.stringify(actions)}\n`, 'browser action');
  await atomicWrite(join(request.staging_dir, 'network.json'), networkJSON);
  await atomicWrite(join(request.staging_dir, 'console.jsonl'), consoleContent);
  await atomicWrite(join(request.staging_dir, 'browser-actions.json'), actionJSON);
  const firstRequest = network.find((record) => record.request_id || record.trace_id) ?? {};
  return [
    { kind: 'network', path: 'browser/network.json', request_id: firstRequest.request_id || '', trace_id: firstRequest.trace_id || '' },
    { kind: 'console', path: 'browser/console.jsonl' },
    { kind: 'browser_actions', path: 'browser/browser-actions.json' },
  ];
}

async function executeWorker(request) {
  validateWorkerRequest(request);
  await mkdir(request.staging_dir, { recursive: true, mode: 0o700 });
  const { chromium } = await import('playwright');
  const browser = await chromium.launch({ headless: request.headless });
  let context;
  const screenshots = [];
  const network = createBoundedRecordCollector();
  const consoleRecords = createBoundedRecordCollector();
  const actions = [];
  const pendingResponses = new Set();
  const requestStarted = new WeakMap();
  let authFailure = false;
  let blockedRequest = false;
  try {
    context = await browser.newContext({
      ...(request.storage_state_path ? { storageState: request.storage_state_path } : {}),
      serviceWorkers: 'block',
    });
    await context.route('**/*', async (route) => {
      try {
        await assertAllowedURL(route.request().url(), request.policy);
        await route.continue();
      } catch {
        blockedRequest = true;
        await route.abort('blockedbyclient');
      }
    });
    const page = await context.newPage();
    await page.routeWebSocket('**/*', (webSocket) => webSocket.close());
    page.setDefaultTimeout(15_000);
    page.setDefaultNavigationTimeout(30_000);
    page.on('dialog', (dialog) => dialog.dismiss().catch(() => {}));
    page.on('download', (download) => download.cancel().catch(() => {}));
    page.on('request', (browserRequest) => requestStarted.set(browserRequest, Date.now()));
    page.on('console', (message) => {
      if (consoleRecords.isStopped()) return;
      consoleRecords.add({ type: String(message.type()).slice(0, 32), text: redactConsoleText(message.text()), timestamp: new Date().toISOString() });
    });
    page.on('response', (response) => {
      if (response.status() === 401 || response.status() === 403) authFailure = true;
      if (network.isStopped()) return;
      if (pendingResponses.size >= EVIDENCE_MAX_RECORDS) {
        network.truncate();
        return;
      }
      const pending = (async () => {
        const browserRequest = response.request();
        const headers = Object.fromEntries((await responseHeadersPromise(response)).filter(([, value]) => value !== null));
        network.add(safeResponseRecord({
          method: browserRequest.method(),
          url: response.url(),
          status: response.status(),
          duration_ms: Math.max(0, Date.now() - (requestStarted.get(browserRequest) ?? Date.now())),
          headers,
        }));
      })().finally(() => pendingResponses.delete(pending));
      pendingResponses.add(pending);
    });

    const captureScreenshot = (name) => captureSafePNG(page, request, name, () => authFailure);
    const finishLogin = async () => {
      for (const screenshot of screenshots) await rm(join(request.staging_dir, screenshot.replace('browser/', '')), { force: true });
      await Promise.allSettled([...pendingResponses]);
      const artifacts = await writeEvidenceFiles(request, network, consoleRecords, actions);
      return {
        status: 'login_required',
        error_code: 'browser_login_required',
        final_url: page.url(),
        title: redactConsoleText(await page.title().catch(() => '')),
        login_origin: new URL(page.url()).origin,
        accessibility_summary: await accessibilitySummary(page),
        artifacts,
      };
    };
    emitProgress('browser_starting', 'Opening validation page', '', 0, request.plan.actions.length);
    try {
      await assertAllowedURL(request.plan.start_url, request.policy);
      await page.goto(request.plan.start_url, { waitUntil: 'domcontentloaded' });
      if (blockedRequest) throw new Error('browser destination was blocked');
      await assertAllowedURL(page.url(), request.policy);
    } catch {
      const login = await loginPageState(page, request.policy, authFailure);
      if (login.required) return finishLogin();
      const captured = await captureScreenshot('failure.png');
      if (captured.loginRequired) return finishLogin();
      const failure = captured.path;
      screenshots.push(failure);
      actions.push({ id: 'start_url', action: 'goto', locator_kind: '', started_at: new Date().toISOString(), duration_ms: 0, result: 'failed', error_code: blockedRequest ? 'browser_destination_blocked' : 'navigation_failed' });
      await Promise.allSettled([...pendingResponses]);
      const artifacts = [...screenshots.map((path) => ({ kind: 'screenshot', path })), ...(await writeEvidenceFiles(request, network, consoleRecords, actions))];
      const finalURL = page.url().startsWith('http:') || page.url().startsWith('https:') ? page.url() : '';
      return {
        status: 'locator_failed',
        error_code: blockedRequest ? 'browser_destination_blocked' : 'navigation_failed',
        error_message: 'browser navigation failed',
        failed_action_id: 'start_url',
        final_url: finalURL,
        title: redactConsoleText(await page.title().catch(() => '')),
        final_screenshot_path: failure,
        accessibility_summary: await accessibilitySummary(page),
        artifacts,
      };
    }
    if ((await loginPageState(page, request.policy, authFailure)).required) return finishLogin();

    for (let index = 0; index < request.plan.actions.length; index += 1) {
      const action = request.plan.actions[index];
      if ((await loginPageState(page, request.policy, authFailure)).required) return finishLogin();
      const started = Date.now();
      emitProgress('browser_action_started', `Executing browser action ${index + 1}/${request.plan.actions.length}`, action.id, index + 1, request.plan.actions.length);
      try {
        const captured = await executeAction(page, action, request, index, captureScreenshot);
        if (captured.loginRequired) {
          actions.push({ id: action.id, action: action.action, locator_kind: action.locator?.kind || '', started_at: new Date(started).toISOString(), duration_ms: Date.now() - started, result: 'login_required', error_code: 'browser_login_required' });
          return finishLogin();
        }
        if (captured.path) screenshots.push(captured.path);
        if (blockedRequest) throw new Error('browser destination was blocked');
        if (page.url().startsWith('http:') || page.url().startsWith('https:')) await assertAllowedURL(page.url(), request.policy);
        const login = await loginPageState(page, request.policy, authFailure);
        if (login.required) {
          actions.push({ id: action.id, action: action.action, locator_kind: action.locator?.kind || '', started_at: new Date(started).toISOString(), duration_ms: Date.now() - started, result: 'login_required', error_code: 'browser_login_required' });
          return finishLogin();
        }
        if (action.screenshot_after) {
          const after = await captureScreenshot(`after-${String(index + 1).padStart(2, '0')}-${safeFilePart(action.id)}.png`);
          if (after.loginRequired) {
            actions.push({ id: action.id, action: action.action, locator_kind: action.locator?.kind || '', started_at: new Date(started).toISOString(), duration_ms: Date.now() - started, result: 'login_required', error_code: 'browser_login_required' });
            return finishLogin();
          }
          screenshots.push(after.path);
        }
        actions.push({ id: action.id, action: action.action, locator_kind: action.locator?.kind || '', started_at: new Date(started).toISOString(), duration_ms: Date.now() - started, result: 'completed', error_code: '' });
        emitProgress('browser_action_completed', `Completed browser action ${index + 1}/${request.plan.actions.length}`, action.id, index + 1, request.plan.actions.length);
      } catch {
        actions.push({ id: action.id, action: action.action, locator_kind: action.locator?.kind || '', started_at: new Date(started).toISOString(), duration_ms: Date.now() - started, result: 'failed', error_code: blockedRequest ? 'browser_destination_blocked' : 'locator_failed' });
        const login = await loginPageState(page, request.policy, authFailure);
        if (login.required) return finishLogin();
        const captured = await captureScreenshot('failure.png');
        if (captured.loginRequired) return finishLogin();
        const failure = captured.path;
        screenshots.push(failure);
        await Promise.allSettled([...pendingResponses]);
        const artifacts = [...screenshots.map((path) => ({ kind: 'screenshot', path })), ...(await writeEvidenceFiles(request, network, consoleRecords, actions))];
        return {
          status: 'locator_failed',
          error_code: blockedRequest ? 'browser_destination_blocked' : 'locator_failed',
          error_message: 'browser action failed',
          failed_action_id: action.id,
          final_url: page.url(),
          title: redactConsoleText(await page.title().catch(() => '')),
          final_screenshot_path: failure,
          accessibility_summary: await accessibilitySummary(page),
          artifacts,
        };
      }
    }

    for (const assertion of request.plan.assertions) {
      try {
        await page.getByText(assertion.value, { exact: false }).first().waitFor({ state: 'visible' });
      } catch {
        const login = await loginPageState(page, request.policy, authFailure);
        if (login.required) return finishLogin();
        const captured = await captureScreenshot('failure.png');
        if (captured.loginRequired) return finishLogin();
        const failure = captured.path;
        screenshots.push(failure);
        await Promise.allSettled([...pendingResponses]);
        const artifacts = [...screenshots.map((path) => ({ kind: 'screenshot', path })), ...(await writeEvidenceFiles(request, network, consoleRecords, actions))];
        return {
          status: 'assertion_failed',
          error_code: 'assertion_failed',
          error_message: 'browser assertion failed',
          final_url: page.url(),
          title: redactConsoleText(await page.title().catch(() => '')),
          final_screenshot_path: failure,
          accessibility_summary: await accessibilitySummary(page),
          artifacts,
        };
      }
    }

    const login = await loginPageState(page, request.policy, authFailure);
    if (login.required) return finishLogin();
    const captured = await captureScreenshot('final.png');
    if (captured.loginRequired) return finishLogin();
    const finalScreenshot = captured.path;
    screenshots.push(finalScreenshot);
    await Promise.allSettled([...pendingResponses]);
    const artifacts = [...screenshots.map((path) => ({ kind: 'screenshot', path })), ...(await writeEvidenceFiles(request, network, consoleRecords, actions))];
    return {
      status: 'completed',
      final_url: page.url(),
      title: redactConsoleText(await page.title().catch(() => '')),
      final_screenshot_path: finalScreenshot,
      accessibility_summary: await accessibilitySummary(page),
      artifacts,
    };
  } finally {
    if (context) await context.close().catch(() => {});
    await browser.close().catch(() => {});
  }
}

async function loginStorageStateInput(path) {
  const content = await readFile(path);
  if (content.length === 0) return {};
  if (content.length > 16 << 20) throw new Error('existing login state exceeds its limit');
  JSON.parse(content.toString('utf8'));
  return { storageState: path };
}

export async function createGuardedLoginContext(browser, storageStateInput) {
  const context = await browser.newContext({
    ...storageStateInput,
    serviceWorkers: 'block',
    acceptDownloads: false,
  });
  const guardedPages = new WeakSet();
  const guardPage = (page) => {
    if (guardedPages.has(page)) return;
    guardedPages.add(page);
    page.setDefaultTimeout(15_000);
    page.setDefaultNavigationTimeout(30_000);
    page.on('dialog', (dialog) => dialog.dismiss().catch(() => {}));
    page.on('download', (download) => download.cancel().catch(() => {}));
  };
  context.on('page', guardPage);
  for (const page of context.pages()) guardPage(page);
  await context.routeWebSocket('**/*', (webSocketRoute) => webSocketRoute.close());
  const page = await context.newPage();
  guardPage(page);
  return { context, page };
}

export async function saveLoginStorageState(context, path) {
  const temporary = join(dirname(path), `.${basename(path)}-${randomUUID()}`);
  try {
    const reserved = await open(temporary, 'wx', 0o600);
    await reserved.close();
    await context.storageState({ path: temporary });
    await chmod(temporary, 0o600);
    const handle = await open(temporary, 'r');
    try {
      await handle.sync();
    } finally {
      await handle.close();
    }
    await rename(temporary, path);
    await syncDirectory(dirname(path));
  } catch (error) {
    await rm(temporary, { force: true });
    throw error;
  }
}

async function loginWorker(request) {
  validateWorkerRequest(request);
  const { chromium } = await import('playwright');
  let browser;
  let context;
  let interrupting = false;
  const closeForInterrupt = () => {
    if (interrupting) return;
    interrupting = true;
    void (async () => {
      if (context) await context.close().catch(() => {});
      if (browser) await browser.close().catch(() => {});
      process.exit(130);
    })();
  };
  process.once('SIGINT', closeForInterrupt);
  process.once('SIGTERM', closeForInterrupt);
  try {
    browser = await chromium.launch({ headless: false });
    const guarded = await createGuardedLoginContext(browser, await loginStorageStateInput(request.storage_state_path));
    context = guarded.context;
    const page = guarded.page;
    let blockedRequest = false;
    const authFailures = createLoginAuthFailureTracker();
    context.on('response', (response) => {
      authFailures.observeStatus(response.status());
    });
    await context.route('**/*', async (route) => {
      try {
        await assertAllowedURL(route.request().url(), request.policy);
        await route.continue();
      } catch {
        blockedRequest = true;
        await route.abort('blockedbyclient');
      }
    });
    emitProgress('browser_login_opened', 'Complete login in the visible validation browser');
    await assertAllowedURL(request.plan.start_url, request.policy);
    await page.goto(request.plan.start_url, { waitUntil: 'domcontentloaded' });
    let loginStarted = false;
    while (true) {
      if (blockedRequest) throw new Error('browser destination was blocked');
      const pages = context.pages();
      for (const currentPage of pages) {
        const currentURL = currentPage.url();
        if (currentURL && currentURL !== 'about:blank') await assertAllowedURL(currentURL, request.policy);
      }
      const observed = await observeLoginState(pages, request.policy, loginStarted, authFailures.active());
      loginStarted = observed.started;
      if (observed.ready) {
        await saveLoginStorageState(context, request.storage_state_path);
        emitProgress('browser_login_completed', 'Browser login session saved');
        return { status: 'completed' };
      }
      await page.waitForTimeout(250);
    }
  } finally {
    process.off('SIGINT', closeForInterrupt);
    process.off('SIGTERM', closeForInterrupt);
    if (context) await context.close().catch(() => {});
    if (browser) await browser.close().catch(() => {});
  }
}

async function probeWorker(outputPath) {
  const { chromium } = await import('playwright');
  const server = createServer((_request, response) => {
    response.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
    response.end('<!doctype html><html><body><main><h1>tshoot browser runtime probe</h1></main></body></html>');
  });
  await new Promise((resolveListen, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolveListen);
  });
  const address = server.address();
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      await page.goto(`http://127.0.0.1:${address.port}/`, { waitUntil: 'domcontentloaded' });
      await page.screenshot({ path: outputPath, type: 'png' });
    } finally {
      await context.close();
    }
  } finally {
    await browser.close();
    await new Promise((resolveClose) => server.close(resolveClose));
  }
  const content = await readFile(outputPath);
  if (content.length <= 8) throw new Error('probe screenshot is empty');
  return { status: 'ready', sha256: createHash('sha256').update(content).digest('hex') };
}

async function readSingleRequest() {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  const lines = input.split(/\r?\n/).filter((line) => line.trim() !== '');
  if (lines.length !== 1) throw new Error('worker expects exactly one JSON request line');
  return JSON.parse(lines[0]);
}

function argument(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : '';
}

async function main() {
  const mode = argument('--mode');
  let result;
  if (mode === 'probe') {
    const output = resolve(requiredString(argument('--output'), 'probe output path'));
    result = await probeWorker(output);
  } else if (mode === 'execute') {
    const request = await readSingleRequest();
    if (request.mode !== mode) throw new Error('worker request mode does not match CLI mode');
    result = await executeWorker(request);
  } else if (mode === 'login') {
    const request = await readSingleRequest();
    if (request.mode !== mode) throw new Error('worker request mode does not match CLI mode');
    result = await loginWorker(request);
  } else {
    throw new Error('worker mode is not supported');
  }
  process.stdout.write(`${JSON.stringify(result)}\n`);
}

const invokedPath = process.argv[1] ? realpathSync(resolve(process.argv[1])) : '';
const modulePath = realpathSync(fileURLToPath(import.meta.url));
if (modulePath === invokedPath) {
  main().catch(() => {
    process.stdout.write(`${JSON.stringify({ status: 'worker_failed', error_code: 'browser_worker_failed', error_message: 'browser worker failed' })}\n`);
    process.exitCode = 1;
  });
}
