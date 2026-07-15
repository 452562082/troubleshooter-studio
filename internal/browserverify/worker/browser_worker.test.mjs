import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFileSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { redactConsoleText, sanitizeURL, safeResponseRecord } from './sanitize.mjs';
import {
  assertAllowedURL,
  buildLocator,
  captureSafePNG,
  createBoundedRecordCollector,
  EVIDENCE_MAX_BYTES,
  EVIDENCE_MAX_RECORDS,
  EVIDENCE_TRUNCATION_MARKER,
  hasVisiblePasswordField,
  validateWorkerRequest,
} from './browser_worker.mjs';

test('sanitizeURL removes userinfo and redacts every repeated sensitive query value', () => {
  assert.equal(
    sanitizeURL('https://user:pass@app.test/users?token=first&TOKEN=second&q=%E6%B1%A4%E5%9C%86&apiKey=third'),
    'https://app.test/users?token=%5BREDACTED%5D&TOKEN=%5BREDACTED%5D&q=%E6%B1%A4%E5%9C%86&apiKey=%5BREDACTED%5D',
  );
});

test('sanitizeURL fails closed without echoing an invalid URL', () => {
  const raw = 'not a URL?password=do-not-return';
  const sanitized = sanitizeURL(raw);
  assert.equal(sanitized, '[INVALID_URL]');
  assert.equal(sanitized.includes('do-not-return'), false);
});

test('redactConsoleText redacts whole credential-bearing records', () => {
  for (const message of [
    'Authorization: Bearer top-secret password=hunter2',
    'Cookie: sid=cookie-secret',
    'Proxy-Authorization: Basic dXNlcjpwYXNz',
    'client_secret=actual-value',
    'prefix Bearer abc.def.ghi suffix',
  ]) {
    assert.equal(redactConsoleText(message), '[REDACTED]', message);
  }
  assert.equal(redactConsoleText('render completed for user list'), 'render completed for user list');
});

test('redactConsoleText redacts structured records with quoted credential keys', () => {
  const credentials = [
    ['password', 'PaSsWoRd', 'hunter2'],
    ['Authorization', 'aUtHoRiZaTiOn', 'Basic dXNlcjpwYXNz'],
    ['Cookie', 'cOoKiE', 'sid=cookie-secret'],
  ];
  for (const [key, mixedCaseKey, value] of credentials) {
    for (const message of [
      `{"${key}":"${value}"}`,
      `{ "${mixedCaseKey}" : "${value}" }`,
      `{'${key}' : '${value}'}`,
    ]) {
      assert.equal(redactConsoleText(message), '[REDACTED]', message);
    }
  }
});

test('redactConsoleText processes at most 8 KiB', () => {
  const sanitized = redactConsoleText('汤圆'.repeat(5000));
  assert.ok(Buffer.byteLength(sanitized, 'utf8') <= 8 * 1024);
});

test('safeResponseRecord emits only the eight safe fields', () => {
  const record = safeResponseRecord({
    method: 'get',
    url: 'https://user:pass@app.test/api?code=abc&q=ok',
    status: 200,
    duration_ms: 12.75,
    headers: {
      'Set-Cookie': 'sid=response-secret',
      'Content-Type': 'application/json; charset=utf-8',
      'Content-Length': '321',
      'X-Request-ID': 'req-1',
      'x-TRACE-id': 'trace-1',
      'x-arbitrary-secret': 'must-not-copy',
    },
    body: 'raw-body-secret',
    arbitrary: 'arbitrary-secret',
  });

  assert.deepEqual(Object.keys(record), [
    'method', 'url', 'status', 'duration_ms', 'content_type', 'content_length', 'request_id', 'trace_id',
  ]);
  assert.deepEqual(record, {
    method: 'GET',
    url: 'https://app.test/api?code=%5BREDACTED%5D&q=ok',
    status: 200,
    duration_ms: 12.75,
    content_type: 'application/json; charset=utf-8',
    content_length: 321,
    request_id: 'req-1',
    trace_id: 'trace-1',
  });
  const encoded = JSON.stringify(record);
  for (const secret of ['response-secret', 'must-not-copy', 'raw-body-secret', 'arbitrary-secret', 'abc', 'user:pass']) {
    assert.equal(encoded.includes(secret), false, secret);
  }
});

test('safeResponseRecord protects overlong and credential-bearing IDs', () => {
  const record = safeResponseRecord({
    method: 'POST\nAuthorization: secret',
    url: 'invalid URL?token=url-secret',
    status: 200.5,
    duration_ms: -1,
    content_type: 'text/plain\r\nSet-Cookie: leaked',
    content_length: -5,
    headers: {
      'x-request-id': 'Authorization: Bearer request-secret',
      'x-trace-id': 'x'.repeat(129),
    },
  });

  assert.equal(record.method, '');
  assert.equal(record.url, '[INVALID_URL]');
  assert.equal(record.status, 0);
  assert.equal(record.duration_ms, 0);
  assert.equal(record.content_type, '');
  assert.equal(record.content_length, 0);
  assert.equal(record.request_id, '[REDACTED]');
  assert.equal(record.trace_id, '[REDACTED]');
  assert.equal(JSON.stringify(record).includes('request-secret'), false);
  assert.equal(JSON.stringify(record).includes('url-secret'), false);
});

const baseRequest = () => ({
  mode: 'execute',
  plan: {
    version: 1,
    start_url: 'https://app.test/users',
    actions: [{ id: 'shot', action: 'screenshot' }],
    assertions: [{ kind: 'visible_text', value: 'Users' }],
  },
  policy: {
    allowed_origins: ['https://app.test'],
    private_origins: [],
    auth_origins: ['https://login.test'],
    is_prod: false,
  },
  staging_dir: '/opaque/browser',
  headless: true,
});

test('browser worker loads and validates offline without importing Playwright', () => {
  assert.doesNotThrow(() => validateWorkerRequest(baseRequest()));
});

test('browser worker accepts exactly seven actions and six locator kinds', () => {
  const actions = [
    { id: 'goto', action: 'goto', url: 'https://app.test/next' },
    { id: 'click', action: 'click', locator: { kind: 'role', value: 'button', name: 'Search' } },
    { id: 'fill', action: 'fill', locator: { kind: 'label', value: 'Keyword' }, value: 'soup' },
    { id: 'press', action: 'press', locator: { kind: 'text', value: 'Search' }, key: 'Enter' },
    { id: 'select', action: 'select', locator: { kind: 'placeholder', value: 'Status' }, value: 'open' },
    { id: 'wait', action: 'wait_for', locator: { kind: 'test_id', value: 'results' } },
    { id: 'shot', action: 'screenshot' },
    { id: 'css', action: 'wait_for', locator: { kind: 'css', value: '.rendered' } },
  ];
  const request = baseRequest();
  request.plan.actions = actions;
  assert.doesNotThrow(() => validateWorkerRequest(request));

  for (const action of ['evaluate', 'upload', 'shell', 'xpath']) {
    const invalid = baseRequest();
    invalid.plan.actions = [{ id: 'bad', action }];
    assert.throws(() => validateWorkerRequest(invalid), /not supported/);
  }
  for (const kind of ['xpath', 'javascript', 'file']) {
    const invalid = baseRequest();
    invalid.plan.actions = [{ id: 'bad', action: 'click', locator: { kind, value: '//button' } }];
    assert.throws(() => validateWorkerRequest(invalid), /locator/);
  }
});

test('browser worker rejects production interaction before browser launch', () => {
  for (const action of ['click', 'fill', 'press', 'select']) {
    const request = baseRequest();
    request.policy.is_prod = true;
    request.plan.actions = [{
      id: 'write',
      action,
      locator: { kind: 'text', value: 'Submit' },
      ...(action === 'fill' || action === 'select' ? { value: 'x' } : {}),
      ...(action === 'press' ? { key: 'Enter' } : {}),
    }];
    assert.throws(() => validateWorkerRequest(request), /production/);
  }
});

test('assertAllowedURL re-resolves every navigation and request', async () => {
  const policy = baseRequest().policy;
  let calls = 0;
  const lookup = async () => {
    calls += 1;
    return calls === 1
      ? [{ address: '203.0.113.10', family: 4 }]
      : [{ address: '127.0.0.1', family: 4 }];
  };
  await assertAllowedURL('https://app.test/users', policy, lookup);
  await assert.rejects(assertAllowedURL('https://app.test/api', policy, lookup), /private/);
  assert.equal(calls, 2);
});

test('assertAllowedURL rejects schemes, origins, metadata, and private addresses', async () => {
  const policy = baseRequest().policy;
  const publicLookup = async () => [{ address: '203.0.113.10', family: 4 }];
  for (const raw of [
    'file:///etc/passwd',
    'data:text/plain,secret',
    'javascript:alert(1)',
    'https://evil.test/users',
    'https://user:pass@app.test/users',
  ]) {
    await assert.rejects(assertAllowedURL(raw, policy, publicLookup));
  }

  const metadataPolicy = { ...policy, allowed_origins: ['http://169.254.169.254'] };
  await assert.rejects(
    assertAllowedURL('http://169.254.169.254/latest/meta-data', metadataPolicy, async () => [{ address: '169.254.169.254', family: 4 }]),
    /link-local|metadata/,
  );
  await assert.rejects(
    assertAllowedURL('https://app.test/users', policy, async () => [{ address: '10.0.0.8', family: 4 }]),
    /private/,
  );
});

test('private destinations require exact configured origin', async () => {
  const lookup = async () => [{ address: '10.0.0.8', family: 4 }];
  const allowed = baseRequest().policy;
  allowed.allowed_origins = ['https://app.internal:8443'];
  allowed.private_origins = ['https://app.internal:8443'];
  await assertAllowedURL('https://app.internal:8443/users', allowed, lookup);
  await assert.rejects(assertAllowedURL('https://app.internal/users', allowed, lookup), /origin/);
});

test('buildLocator maps only the six declared locator types', () => {
  const calls = [];
  const page = {
    getByRole: (...args) => calls.push(['role', ...args]),
    getByLabel: (...args) => calls.push(['label', ...args]),
    getByText: (...args) => calls.push(['text', ...args]),
    getByPlaceholder: (...args) => calls.push(['placeholder', ...args]),
    getByTestId: (...args) => calls.push(['test_id', ...args]),
    locator: (...args) => calls.push(['css', ...args]),
  };
  for (const locator of [
    { kind: 'role', value: 'button', name: 'Search' },
    { kind: 'label', value: 'Keyword' },
    { kind: 'text', value: 'Results' },
    { kind: 'placeholder', value: 'Search' },
    { kind: 'test_id', value: 'results' },
    { kind: 'css', value: '.results' },
  ]) {
    buildLocator(page, locator);
  }
  assert.deepEqual(calls.map(([kind]) => kind), ['role', 'label', 'text', 'placeholder', 'test_id', 'css']);
  assert.throws(() => buildLocator(page, { kind: 'xpath', value: '//button' }), /locator/);
});

test('login detection checks every password field, including a visible field after a hidden one', async () => {
  const visibility = [false, true];
  const page = {
    locator: (selector) => {
      assert.equal(selector, 'input[type="password"]');
      return {
        count: async () => visibility.length,
        nth: (index) => ({ isVisible: async () => visibility[index] }),
      };
    },
  };
  assert.equal(await hasVisiblePasswordField(page), true);
});

test('safe screenshot deletes the PNG when a password field appears during capture', async () => {
  const temporary = mkdtempSync(join(tmpdir(), 'tshoot-safe-screenshot-'));
  const output = join(temporary, 'transient.png');
  const visibility = [false, false];
  const page = {
    url: () => 'https://app.test/users',
    locator: () => ({
      count: async () => visibility.length,
      nth: (index) => ({ isVisible: async () => visibility[index] }),
    }),
  };
  const request = baseRequest();
  request.staging_dir = temporary;
  try {
    const captured = await captureSafePNG(page, request, 'transient.png', false, async () => {
      writeFileSync(output, Buffer.from('png bytes'));
      visibility[1] = true;
      return 'browser/transient.png';
    });
    assert.deepEqual(captured, { loginRequired: true, path: '' });
    assert.equal(existsSync(output), false);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
});

test('console and response collectors stop at fixed record and byte limits', () => {
  for (const makeRecord of [
    (index) => ({ type: 'log', text: `safe console ${index}`, timestamp: '2026-07-15T00:00:00.000Z' }),
    (index) => safeResponseRecord({ method: 'GET', url: `https://app.test/api/${index}`, status: 200, duration_ms: 1, headers: {} }),
  ]) {
    const collector = createBoundedRecordCollector();
    for (let index = 0; index < EVIDENCE_MAX_RECORDS + 500; index += 1) collector.add(makeRecord(index));
    collector.add({ text: 'must-never-be-appended-after-truncation' });
    const records = collector.snapshot();
    assert.ok(records.length <= EVIDENCE_MAX_RECORDS);
    assert.ok(Buffer.byteLength(JSON.stringify(records), 'utf8') <= EVIDENCE_MAX_BYTES);
    assert.deepEqual(records.at(-1), EVIDENCE_TRUNCATION_MARKER);
    assert.equal(JSON.stringify(records).includes('must-never-be-appended-after-truncation'), false);
  }
});

test('bounded evidence collector reserves space for its safe marker at the byte limit', () => {
  const collector = createBoundedRecordCollector({ maxRecords: 10, maxBytes: 256 });
  for (let index = 0; index < 20; index += 1) collector.add({ text: 'x'.repeat(80), index });
  const records = collector.snapshot();
  assert.deepEqual(records.at(-1), EVIDENCE_TRUNCATION_MARKER);
  assert.ok(Buffer.byteLength(JSON.stringify(records), 'utf8') <= 256);
});

test('worker source has no arbitrary script, upload, HAR, trace, body, or raw-header escape hatch', () => {
  const workerPath = fileURLToPath(new URL('./browser_worker.mjs', import.meta.url));
  const source = readFileSync(workerPath, 'utf8');
  for (const forbidden of [
    '.evaluate(',
    'setInputFiles',
    'addScriptTag',
    'recordHar',
    'tracing.start',
    'postData(',
    'allHeaders(',
    'request.headers(',
  ]) {
    assert.equal(source.includes(forbidden), false, forbidden);
  }
  assert.equal((source.match(/import\('playwright'\)/g) ?? []).length, 2);
  assert.equal(source.includes("from 'playwright'"), false);
  assert.equal(source.includes("context.route('**/*'"), true);
});

test('unsupported CLI mode emits exactly one final JSON object and no progress on stdout', () => {
  const workerPath = fileURLToPath(new URL('./browser_worker.mjs', import.meta.url));
  const run = spawnSync(process.execPath, [workerPath, '--mode', 'login'], { encoding: 'utf8' });
  assert.notEqual(run.status, 0);
  const lines = run.stdout.trim().split(/\r?\n/);
  assert.equal(lines.length, 1);
  assert.deepEqual(JSON.parse(lines[0]), {
    status: 'worker_failed',
    error_code: 'browser_worker_failed',
    error_message: 'browser worker failed',
  });
  assert.equal(run.stderr, '');
});

test('CLI entrypoint survives a lexical temp path whose canonical path differs', () => {
  const sourceWorker = fileURLToPath(new URL('./browser_worker.mjs', import.meta.url));
  const sourceSanitizer = fileURLToPath(new URL('./sanitize.mjs', import.meta.url));
  const temporary = mkdtempSync(join(tmpdir(), 'tshoot-browser-worker-'));
  try {
    const workerPath = join(temporary, 'browser_worker.mjs');
    copyFileSync(sourceWorker, workerPath);
    copyFileSync(sourceSanitizer, join(temporary, 'sanitize.mjs'));
    const run = spawnSync(process.execPath, [workerPath, '--mode', 'unsupported'], { encoding: 'utf8' });
    assert.notEqual(run.status, 0);
    assert.equal(run.stdout.trim().split(/\r?\n/).length, 1);
    assert.equal(JSON.parse(run.stdout).error_code, 'browser_worker_failed');
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
});
