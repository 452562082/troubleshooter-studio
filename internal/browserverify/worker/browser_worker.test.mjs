import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { copyFileSync, existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { createServer as createHTTPServer, request as httpRequest } from 'node:http';
import { connect as connectTCP, createServer as createTCPServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { connect as connectTLS } from 'node:tls';
import { fileURLToPath } from 'node:url';

import { redactConsoleText, sanitizeURL, safeResponseRecord } from './sanitize.mjs';
import {
  assertAllowedURL,
  buildLocator,
  capturePNG,
  captureSafePNG,
  chromiumLaunchOptions,
  createGuardedLoginContext,
  createSupervisedBrowserContext,
  createArtifactBudget,
  createLoginAuthFailureTracker,
  createLoginNavigationTracker,
  createBoundedRecordCollector,
  EVIDENCE_MAX_BYTES,
  EVIDENCE_MAX_RECORDS,
  EVIDENCE_TRUNCATION_MARKER,
  hasVisiblePasswordField,
  observeLoginState,
  dialPinnedTarget,
  launchPinnedBrowser,
  resolvePinnedTarget,
  saveLoginStorageState,
  startPinnedProxy,
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
    application_origins: ['https://app.test'],
    start_origins: ['https://app.test'],
    private_origins: [],
    auth_origins: ['https://login.test'],
    is_prod: false,
  },
  staging_dir: '/opaque/browser',
  headless: true,
});

const baseLoginRequest = () => ({
  mode: 'login',
  plan: {
    version: 1,
    start_url: 'https://app.test/oauth/start?state=opaque',
    actions: [],
    assertions: [],
  },
  policy: {
    allowed_origins: ['https://app.test'],
    application_origins: ['https://app.test'],
    start_origins: ['https://app.test'],
    private_origins: [],
    auth_origins: ['https://login.test'],
    is_prod: false,
  },
  staging_dir: '',
  storage_state_path: '/tmp/7f83b1657ff1fc53b92dc18148a1d65dfa13514b9c01d44f9205940f8c80f54f.json',
  headless: false,
});

test('browser worker loads and validates offline without importing Playwright', () => {
  assert.doesNotThrow(() => validateWorkerRequest(baseRequest()));
});

test('login worker requires visible mode, one absolute state path, and an original application URL', () => {
  assert.doesNotThrow(() => validateWorkerRequest(baseLoginRequest()));

  for (const mutate of [
    (request) => { request.headless = true; },
    (request) => { request.storage_state_path = 'relative/state.json'; },
    (request) => { request.staging_dir = '/evidence/browser'; },
    (request) => { request.plan.actions = [{ id: 'shot', action: 'screenshot' }]; },
    (request) => { request.plan.assertions = [{ kind: 'visible_text', value: 'secret' }]; },
    (request) => { request.plan.start_url = 'https://login.test'; },
  ]) {
    const invalid = baseLoginRequest();
    mutate(invalid);
    assert.throws(() => validateWorkerRequest(invalid));
  }
});

test('worker forbids API and identity-provider origins from owning execute or login starts', () => {
  for (const origin of ['https://api.test', 'https://login.test']) {
    const execute = baseRequest();
    execute.policy.allowed_origins.push('https://api.test', 'https://login.test');
    execute.plan.start_url = `${origin}/start`;
    assert.throws(() => validateWorkerRequest(execute), /start origin/);

    const login = baseLoginRequest();
    login.policy.allowed_origins.push('https://api.test', 'https://login.test');
    login.plan.start_url = `${origin}/start`;
    assert.throws(() => validateWorkerRequest(login), /application origin/);
  }
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

function proxyAuthorization(proxy) {
  const options = proxy.playwrightProxy();
  return `Basic ${Buffer.from(`${options.username}:${options.password}`, 'utf8').toString('base64')}`;
}

function optionalProxyAuthorizationLine(proxy) {
  return typeof proxy.playwrightProxy === 'function' ? `Proxy-Authorization: ${proxyAuthorization(proxy)}\r\n` : '';
}

test('pinned proxy requires per-launch Basic credentials before parsing or network activity', async () => {
  let lookups = 0;
  let dials = 0;
  const policy = { allowed_origins: ['http://app.test'], application_origins: ['http://app.test'], start_origins: ['http://app.test'], private_origins: [], auth_origins: [], is_prod: false };
  const proxyDependencies = {
    lookup: async () => { lookups += 1; return [{ address: '203.0.113.10', family: 4 }]; },
    dial: () => { dials += 1; throw new Error('unauthenticated proxy request reached dial'); },
  };
  const proxy = await startPinnedProxy(policy, proxyDependencies);
  const otherProxy = await startPinnedProxy(policy, proxyDependencies);
  try {
    const launchProxy = proxy.playwrightProxy();
    const otherLaunchProxy = otherProxy.playwrightProxy();
    assert.ok(launchProxy.username.length >= 20);
    assert.ok(launchProxy.password.length >= 32);
    assert.notEqual(launchProxy.username, otherLaunchProxy.username);
    assert.notEqual(launchProxy.password, otherLaunchProxy.password);
    assert.equal(JSON.stringify(proxy).includes(launchProxy.username), false);
    assert.equal(JSON.stringify(proxy).includes(launchProxy.password), false);

    const httpResponse = await new Promise((resolveResponse, reject) => {
      const request = httpRequest({ host: '127.0.0.1', port: proxy.port, path: 'http://invalid.test/blocked' }, (response) => {
        response.resume();
        response.on('end', () => resolveResponse({ status: response.statusCode, authenticate: response.headers['proxy-authenticate'] }));
      });
      request.once('error', reject);
      request.end();
    });
    assert.deepEqual(httpResponse, { status: 407, authenticate: 'Basic realm="tshoot-browser-proxy"' });

    for (const rawRequest of [
      'CONNECT invalid.test:443 HTTP/1.1\r\nHost: invalid.test:443\r\n\r\n',
      'GET ws://invalid.test/socket HTTP/1.1\r\nHost: invalid.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n',
    ]) {
      const client = connectTCP(proxy.port, '127.0.0.1');
      await new Promise((resolveConnect, reject) => {
        client.once('connect', resolveConnect);
        client.once('error', reject);
      });
      client.write(rawRequest);
      const response = await readSocketUntil(client, (content) => content.includes('\r\n\r\n'));
      assert.match(response.toString('latin1'), /^HTTP\/1\.1 407/);
      assert.match(response.toString('latin1'), /Proxy-Authenticate: Basic realm="tshoot-browser-proxy"/i);
      client.destroy();
    }
    assert.equal(lookups, 0);
    assert.equal(dials, 0);
  } finally {
    await Promise.all([proxy.close(), otherProxy.close()]);
  }
});

test('pinned proxy close aborts delayed DNS and forbids every post-close dial', async () => {
  let releaseLookup;
  let announceLookup;
  const lookupStarted = new Promise((resolveStarted) => { announceLookup = resolveStarted; });
  const delayedLookup = new Promise((resolveLookup) => { releaseLookup = resolveLookup; });
  let dials = 0;
  const origin = 'http://app.test';
  const proxy = await startPinnedProxy({ allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [], auth_origins: [], is_prod: false }, {
    lookup: async () => { announceLookup(); return delayedLookup; },
    dial: () => { dials += 1; throw new Error('dial happened after proxy close'); },
    resolveTimeoutMs: 5_000,
  });
  const client = connectTCP(proxy.port, '127.0.0.1');
  client.once('error', () => {});
  await new Promise((resolveConnect) => client.once('connect', resolveConnect));
  const clientClosed = new Promise((resolveClose) => client.once('close', resolveClose));
  client.write(`GET ${origin}/delayed HTTP/1.1\r\nHost: app.test\r\n${optionalProxyAuthorizationLine(proxy)}\r\n`);
  await lookupStarted;
  await proxy.close();
  releaseLookup([{ address: '203.0.113.10', family: 4 }]);
  await new Promise((resolveTurn) => setImmediate(resolveTurn));
  await clientClosed;
  assert.equal(dials, 0);
  assert.equal(client.destroyed, true);
});

test('pinned proxy bounds DNS resolution time and never dials after timeout', async () => {
  let dials = 0;
  const origin = 'http://app.test';
  const policy = { allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [], auth_origins: [], is_prod: false };
  const proxy = await startPinnedProxy(policy, {
    lookup: async () => new Promise(() => {}),
    dial: () => { dials += 1; throw new Error('dial happened after DNS timeout'); },
    resolveTimeoutMs: 20,
  });
  const startedAt = Date.now();
  try {
    const status = await new Promise((resolveResponse, reject) => {
      const request = httpRequest({
        host: '127.0.0.1',
        port: proxy.port,
        path: `${origin}/timeout`,
        headers: { 'proxy-authorization': proxyAuthorization(proxy) },
      }, (response) => {
        response.resume();
        response.once('end', () => resolveResponse(response.statusCode));
      });
      request.once('error', reject);
      request.end();
    });
    assert.equal(status, 403);
    assert.equal(dials, 0);
    assert.ok(Date.now() - startedAt < 1_000);
  } finally {
    await proxy.close();
  }
});

test('pinned proxy close immediately destroys a connecting upstream and drains its handler', async () => {
  let announceDial;
  const dialStarted = new Promise((resolveStarted) => { announceDial = resolveStarted; });
  let upstreamWrites = 0;
  const upstream = new EventEmitter();
  upstream.destroyed = false;
  upstream.setTimeout = () => {};
  upstream.write = () => { upstreamWrites += 1; };
  upstream.destroy = () => {
    if (upstream.destroyed) return;
    upstream.destroyed = true;
    queueMicrotask(() => upstream.emit('close'));
  };
  const origin = 'http://app.test';
  const proxy = await startPinnedProxy({ allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [], auth_origins: [], is_prod: false }, {
    lookup: async () => [{ address: '203.0.113.10', family: 4 }],
    dial: () => { announceDial(); return upstream; },
  });
  const client = connectTCP(proxy.port, '127.0.0.1');
  client.once('error', () => {});
  await new Promise((resolveConnect) => client.once('connect', resolveConnect));
  const clientClosed = new Promise((resolveClose) => client.once('close', resolveClose));
  client.write(`GET ${origin}/connecting HTTP/1.1\r\nHost: app.test\r\n${optionalProxyAuthorizationLine(proxy)}\r\n`);
  await dialStarted;
  await proxy.close();
  await clientClosed;
  assert.equal(upstream.destroyed, true);
  assert.equal(upstreamWrites, 0);
  assert.equal(client.destroyed, true);
});

test('pinned proxy rejects mixed DNS answers and routes an allowed loopback target through the proxy', async () => {
  let upstreamHits = 0;
  let upstreamHost = '';
  const upstream = createHTTPServer((request, response) => {
    upstreamHits += 1;
    upstreamHost = request.headers.host ?? '';
    response.end('proxied');
  });
  await new Promise((resolveListen, reject) => {
    upstream.once('error', reject);
    upstream.listen(0, '127.0.0.1', resolveListen);
  });
  const upstreamAddress = upstream.address();
  const origin = `http://app.test:${upstreamAddress.port}`;
  const policy = {
    allowed_origins: [origin],
    application_origins: [origin],
    start_origins: [origin],
    private_origins: [origin],
    auth_origins: [],
    is_prod: false,
  };
  await assert.rejects(
    resolvePinnedTarget(`${origin}/mixed`, { ...policy, private_origins: [] }, async () => [
      { address: '203.0.113.10', family: 4 },
      { address: '127.0.0.1', family: 4 },
    ]),
    /private/,
  );

  let lookups = 0;
  const proxy = await startPinnedProxy(policy, {
    lookup: async (host) => {
      assert.equal(host, 'app.test');
      lookups += 1;
      return [{ address: '127.0.0.1', family: 4 }];
    },
  });
  try {
    const body = await new Promise((resolveResponse, reject) => {
      const request = httpRequest({
        host: '127.0.0.1',
        port: proxy.port,
        method: 'GET',
        path: `${origin}/through-proxy`,
        headers: { host: `app.test:${upstreamAddress.port}`, 'proxy-authorization': proxyAuthorization(proxy) },
      }, (response) => {
        const chunks = [];
        response.on('data', (chunk) => chunks.push(chunk));
        response.on('end', () => resolveResponse(Buffer.concat(chunks).toString('utf8')));
      });
      request.once('error', reject);
      request.end();
    });
    assert.equal(body, 'proxied');
    assert.equal(upstreamHits, 1);
    assert.equal(upstreamHost, `app.test:${upstreamAddress.port}`);
    assert.equal(lookups, 1);
    assert.deepEqual(proxy.stats(), { http: 1, connect: 0, websocket: 0 });

    const launch = chromiumLaunchOptions(true, proxy.playwrightProxy());
    assert.equal(launch.proxy.server, proxy.url);
    assert.equal(launch.proxy.bypass, '<-loopback>');
    assert.equal(launch.proxy.username, proxy.playwrightProxy().username);
    assert.equal(launch.proxy.password, proxy.playwrightProxy().password);
    for (const flag of [
      '--disable-quic',
      '--force-webrtc-ip-handling-policy=disable_non_proxied_udp',
      '--host-resolver-rules=MAP * ~NOTFOUND',
    ]) {
      assert.ok(launch.args.includes(flag), flag);
    }
  } finally {
    await proxy.close();
    await new Promise((resolveClose) => upstream.close(resolveClose));
  }
});

test('pinned dial rejects a connected socket whose actual peer differs from the selected DNS answer', async () => {
  const socket = new EventEmitter();
  socket.remoteAddress = '127.0.0.2';
  socket.remotePort = 443;
  socket.destroyed = false;
  socket.setTimeout = () => {};
  socket.destroy = () => { socket.destroyed = true; };
  const dial = (options) => {
    assert.deepEqual(options, { host: '203.0.113.10', port: 443, family: 4 });
    queueMicrotask(() => socket.emit('connect'));
    return socket;
  };
  await assert.rejects(
    dialPinnedTarget({ addresses: [{ address: '203.0.113.10', family: 4 }], port: 443 }, dial),
    /peer did not match/,
  );
  assert.equal(socket.destroyed, true);
});

test('pinned dial deterministically races every validated IPv6 and IPv4 candidate without resolving again', async () => {
  const calls = [];
  const dial = (options) => {
    calls.push(options.host);
    const socket = new EventEmitter();
    socket.remoteAddress = options.host;
    socket.remotePort = options.port;
    socket.destroyed = false;
    socket.setTimeout = () => {};
    socket.destroy = () => { socket.destroyed = true; queueMicrotask(() => socket.emit('close')); };
    queueMicrotask(() => socket.emit('error', new Error('candidate unavailable')));
    return socket;
  };
  await assert.rejects(dialPinnedTarget({
    addresses: [
      { address: '2001:db8::1', family: 6 },
      { address: '2001:db8::2', family: 6 },
      { address: '203.0.113.10', family: 4 },
      { address: '203.0.113.11', family: 4 },
    ],
    port: 443,
  }, dial, { staggerMs: 1, connectTimeoutMs: 200 }), /failed/);
  assert.deepEqual(calls, ['2001:db8::1', '203.0.113.10', '2001:db8::2', '203.0.113.11']);
});

test('pinned dial falls back across address families and returns only an exact connected peer', async () => {
  const calls = [];
  const dial = (options) => {
    calls.push(options.host);
    const socket = new EventEmitter();
    socket.remoteAddress = options.host;
    socket.remotePort = options.port;
    socket.destroyed = false;
    socket.setTimeout = () => {};
    socket.destroy = () => { socket.destroyed = true; queueMicrotask(() => socket.emit('close')); };
    queueMicrotask(() => {
      if (options.family === 6) socket.emit('error', new Error('IPv6 unavailable'));
      else socket.emit('connect');
    });
    return socket;
  };
  const socket = await dialPinnedTarget({
    addresses: [{ address: '2001:db8::1', family: 6 }, { address: '203.0.113.10', family: 4 }],
    port: 443,
  }, dial, { staggerMs: 1, connectTimeoutMs: 200 });
  assert.equal(socket.remoteAddress, '203.0.113.10');
  assert.deepEqual(calls, ['2001:db8::1', '203.0.113.10']);
  socket.destroy();
});

async function readSocketUntil(socket, predicate) {
  let content = Buffer.alloc(0);
  return new Promise((resolveRead, reject) => {
    const onData = (chunk) => {
      content = Buffer.concat([content, chunk]);
      if (!predicate(content)) return;
      cleanup();
      resolveRead(content);
    };
    const onError = (error) => { cleanup(); reject(error); };
    const onClose = () => {
      if (predicate(content)) return;
      cleanup();
      reject(new Error('socket closed before expected response'));
    };
    const cleanup = () => {
      socket.off('data', onData);
      socket.off('error', onError);
      socket.off('close', onClose);
    };
    socket.on('data', onData);
    socket.once('error', onError);
    socket.once('close', onClose);
  });
}

test('pinned proxy enforces CONNECT authentication, origin, and exact port and peer', async () => {
  let upstreamConnections = 0;
  const upstream = createTCPServer((socket) => {
    upstreamConnections += 1;
    socket.pipe(socket);
  });
  await new Promise((resolveListen, reject) => {
    upstream.once('error', reject);
    upstream.listen(0, '127.0.0.1', resolveListen);
  });
  const { port } = upstream.address();
  const origin = `https://secure.test:${port}`;
  const policy = { allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [origin], auth_origins: [], is_prod: false };
  const proxy = await startPinnedProxy(policy, { lookup: async () => [{ address: '127.0.0.1', family: 4 }] });
  try {
    const client = connectTCP(proxy.port, '127.0.0.1');
    await new Promise((resolveConnect, reject) => {
      client.once('connect', resolveConnect);
      client.once('error', reject);
    });
    client.write(`CONNECT secure.test:${port} HTTP/1.1\r\nHost: secure.test:${port}\r\nProxy-Authorization: ${proxyAuthorization(proxy)}\r\n\r\n`);
    await readSocketUntil(client, (content) => content.includes('\r\n\r\n'));
    client.write('through-tunnel');
    const tunneled = await readSocketUntil(client, (content) => content.includes('through-tunnel'));
    assert.ok(tunneled.includes('through-tunnel'));
    client.destroy();

    const credentialed = connectTCP(proxy.port, '127.0.0.1');
    await new Promise((resolveConnect, reject) => {
      credentialed.once('connect', resolveConnect);
      credentialed.once('error', reject);
    });
    credentialed.write(`CONNECT secure.test:${port} HTTP/1.1\r\nHost: secure.test:${port}\r\nProxy-Authorization: Basic incorrect\r\n\r\n`);
    const credentialResponse = await readSocketUntil(credentialed, (content) => content.includes('\r\n\r\n'));
    assert.match(credentialResponse.toString('latin1'), /^HTTP\/1\.1 407/);
    credentialed.destroy();

    const wrongPort = connectTCP(proxy.port, '127.0.0.1');
    await new Promise((resolveConnect, reject) => {
      wrongPort.once('connect', resolveConnect);
      wrongPort.once('error', reject);
    });
    wrongPort.write(`CONNECT secure.test:${port + 1} HTTP/1.1\r\nHost: secure.test:${port + 1}\r\nProxy-Authorization: ${proxyAuthorization(proxy)}\r\n\r\n`);
    const blockedResponse = await readSocketUntil(wrongPort, (content) => content.includes('\r\n\r\n'));
    assert.match(blockedResponse.toString('latin1'), /^HTTP\/1\.1 403/);
    wrongPort.destroy();

    assert.equal(upstreamConnections, 1);
    assert.deepEqual(proxy.stats(), { http: 0, connect: 1, websocket: 0 });
    await proxy.close();
    await assert.rejects(new Promise((resolveConnect, reject) => {
      const afterClose = connectTCP(proxy.port, '127.0.0.1');
      afterClose.once('connect', () => { afterClose.destroy(); resolveConnect(); });
      afterClose.once('error', reject);
    }));
  } finally {
    await proxy.close();
    await new Promise((resolveClose) => upstream.close(resolveClose));
  }
});

test('pinned CONNECT preserves the original TLS SNI for HTTPS and WSS tunnels', async () => {
  let resolveClientHello;
  const clientHello = new Promise((resolveHello) => { resolveClientHello = resolveHello; });
  const upstream = createTCPServer((socket) => {
    socket.once('data', (content) => resolveClientHello(content));
  });
  await new Promise((resolveListen, reject) => {
    upstream.once('error', reject);
    upstream.listen(0, '127.0.0.1', resolveListen);
  });
  const { port } = upstream.address();
  const origin = `https://secure.test:${port}`;
  const policy = { allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [origin], auth_origins: [], is_prod: false };
  const proxy = await startPinnedProxy(policy, { lookup: async () => [{ address: '127.0.0.1', family: 4 }] });
  let tlsClient;
  try {
    const client = connectTCP(proxy.port, '127.0.0.1');
    await new Promise((resolveConnect, reject) => {
      client.once('connect', resolveConnect);
      client.once('error', reject);
    });
    client.write(`CONNECT secure.test:${port} HTTP/1.1\r\nHost: secure.test:${port}\r\nProxy-Authorization: ${proxyAuthorization(proxy)}\r\n\r\n`);
    await readSocketUntil(client, (content) => content.includes('\r\n\r\n'));
    tlsClient = connectTLS({ socket: client, servername: 'secure.test', rejectUnauthorized: false });
    tlsClient.once('error', () => {});
    const hello = await clientHello;
    assert.ok(hello.includes(Buffer.from('secure.test')), 'TLS ClientHello omitted the original SNI');
    assert.deepEqual(proxy.stats(), { http: 0, connect: 1, websocket: 0 });
  } finally {
    tlsClient?.destroy();
    await proxy.close();
    await new Promise((resolveClose) => upstream.close(resolveClose));
  }
});

test('pinned proxy carries a WebSocket upgrade through the selected peer with the original host', async () => {
  let upstreamRequest = '';
  const upstream = createTCPServer((socket) => {
    socket.once('data', (chunk) => {
      upstreamRequest = chunk.toString('latin1');
      socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n');
    });
  });
  await new Promise((resolveListen, reject) => {
    upstream.once('error', reject);
    upstream.listen(0, '127.0.0.1', resolveListen);
  });
  const { port } = upstream.address();
  const origin = `http://ws.test:${port}`;
  const policy = { allowed_origins: [origin], application_origins: [origin], start_origins: [origin], private_origins: [origin], auth_origins: [], is_prod: false };
  const proxy = await startPinnedProxy(policy, { lookup: async () => [{ address: '127.0.0.1', family: 4 }] });
  try {
    const client = connectTCP(proxy.port, '127.0.0.1');
    await new Promise((resolveConnect, reject) => {
      client.once('connect', resolveConnect);
      client.once('error', reject);
    });
    client.write(`GET ws://ws.test:${port}/socket?state=opaque HTTP/1.1\r\nHost: ws.test:${port}\r\nProxy-Authorization: ${proxyAuthorization(proxy)}\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: opaque\r\nSec-WebSocket-Version: 13\r\n\r\n`);
    const response = await readSocketUntil(client, (content) => content.includes('\r\n\r\n'));
    assert.match(response.toString('latin1'), /^HTTP\/1\.1 101/);
    assert.match(upstreamRequest, /^GET \/socket\?state=opaque HTTP\/1\.1/m);
    assert.match(upstreamRequest.toLowerCase(), new RegExp(`host: ws\\.test:${port}`));
    assert.equal(upstreamRequest.toLowerCase().includes('proxy-authorization'), false);
    assert.deepEqual(proxy.stats(), { http: 0, connect: 0, websocket: 1 });
    client.destroy();
  } finally {
    await proxy.close();
    await new Promise((resolveClose) => upstream.close(resolveClose));
  }
});

test('pinned browser launch owns proxy teardown on success and launch failure', async () => {
  const policy = baseRequest().policy;
  const events = [];
  const startProxy = async () => ({
    url: 'http://127.0.0.1:34567',
    playwrightProxy: () => ({ server: 'http://127.0.0.1:34567', username: 'fixture-user', password: 'fixture-password', bypass: '<-loopback>' }),
    close: async () => { events.push('proxy:close'); },
  });
  const browser = { close: async () => { events.push('browser:close'); } };
  const chromium = {
    launch: async (options) => {
      events.push('chromium:launch');
      assert.equal(options.proxy.bypass, '<-loopback>');
      assert.ok(options.args.includes('--host-resolver-rules=MAP * ~NOTFOUND'));
      return browser;
    },
  };
  const launched = await launchPinnedBrowser(chromium, policy, true, startProxy);
  await launched.close();
  assert.deepEqual(events, ['chromium:launch', 'browser:close', 'proxy:close']);

  const launchFailureEvents = [];
  await assert.rejects(launchPinnedBrowser({
    launch: async () => { throw new Error('launch fixture secret'); },
  }, policy, false, async () => ({
    url: 'http://127.0.0.1:45678',
    playwrightProxy: () => ({ server: 'http://127.0.0.1:45678', username: 'fixture-user', password: 'fixture-password', bypass: '<-loopback>' }),
    close: async () => { launchFailureEvents.push('proxy:close'); },
  })), /launch fixture secret/);
  assert.deepEqual(launchFailureEvents, ['proxy:close']);
});

test('supervised context installs request, response, console, page, download, dialog, and WebSocket policy before its first page', async () => {
  const calls = [];
  const contextHandlers = new Map();
  const pageHandlers = new Map();
  const page = {
    setDefaultTimeout: () => calls.push('page:timeout'),
    setDefaultNavigationTimeout: () => calls.push('page:navigation-timeout'),
    on(event, handler) { pageHandlers.set(event, handler); calls.push(`page:on:${event}`); },
  };
  let httpRoute;
  let webSocketRoute;
  const context = {
    pages: () => [page],
    on(event, handler) { contextHandlers.set(event, handler); calls.push(`context:on:${event}`); },
    async route(pattern, handler) { assert.equal(pattern, '**/*'); httpRoute = handler; calls.push('context:route'); },
    async routeWebSocket(pattern, handler) { assert.equal(pattern, '**/*'); webSocketRoute = handler; calls.push('context:websocket'); },
    async newPage() { calls.push('context:new-page'); contextHandlers.get('page')(page); return page; },
  };
  let contextOptions;
  const browser = { async newContext(options) { contextOptions = options; return context; } };
  const hooks = {
    onPage: () => calls.push('hook:page'),
    onRequest: () => calls.push('hook:request'),
    onRequestFinished: () => calls.push('hook:requestfinished'),
    onRequestFailed: () => calls.push('hook:requestfailed'),
    onResponse: () => calls.push('hook:response'),
    onConsole: () => calls.push('hook:console'),
  };
  const policy = baseRequest().policy;
  const supervised = await createSupervisedBrowserContext(browser, {
    storageStateInput: { storageState: '/opaque/state.json' },
    policy,
    hooks,
    lookup: async () => [{ address: '203.0.113.10', family: 4 }],
  });
  assert.equal(supervised.context, context);
  assert.equal(supervised.page, page);
  assert.deepEqual(contextOptions, {
    storageState: '/opaque/state.json',
    serviceWorkers: 'block',
    acceptDownloads: false,
    viewport: { width: 1280, height: 720 },
  });
  for (const required of ['page', 'request', 'requestfinished', 'requestfailed', 'response', 'console', 'dialog', 'download']) assert.equal(typeof contextHandlers.get(required), 'function', required);
  assert.equal(contextHandlers.has('framenavigated'), false);
  assert.ok(calls.indexOf('context:on:dialog') < calls.indexOf('context:route'));
  assert.ok(calls.indexOf('context:on:download') < calls.indexOf('context:route'));
  assert.ok(calls.indexOf('context:on:page') < calls.indexOf('context:new-page'));
  assert.ok(calls.indexOf('context:route') < calls.indexOf('context:new-page'));
  assert.ok(calls.indexOf('context:websocket') < calls.indexOf('context:new-page'));
  assert.equal(pageHandlers.has('dialog'), false);
  assert.equal(pageHandlers.has('download'), false);

  let dismissed = false;
  let canceled = false;
  contextHandlers.get('dialog')({ dismiss: async () => { dismissed = true; } });
  contextHandlers.get('download')({ cancel: async () => { canceled = true; } });
  await Promise.resolve();
  assert.equal(dismissed, true);
  assert.equal(canceled, true);

  let continued = false;
  await httpRoute({
    request: () => ({ url: () => 'https://app.test/data' }),
    continue: async () => { continued = true; },
    abort: async () => { throw new Error('allowed request aborted'); },
  });
  assert.equal(continued, true);

  let connected = false;
  await webSocketRoute({
    url: () => 'wss://app.test/socket',
    connectToServer: () => { connected = true; },
    close: () => { throw new Error('allowed WebSocket closed'); },
  });
  assert.equal(connected, true);
  assert.equal(supervised.blocked(), false);

  contextHandlers.get('request')();
  contextHandlers.get('response')();
  contextHandlers.get('console')();
  assert.ok(calls.includes('hook:page'));
  assert.ok(calls.includes('hook:request'));
  assert.ok(calls.includes('hook:response'));
  assert.ok(calls.includes('hook:console'));
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

const loginPage = (url, passwordVisible = false, loginUIVisible = false) => ({
  url: () => url,
  locator: (selector) => {
    const visible = selector === 'input[type="password"]' && passwordVisible;
    return {
      count: async () => 1,
      nth: () => ({ isVisible: async () => visible }),
    };
  },
  getByRole: (role, { name } = {}) => {
    const visible = loginUIVisible && role === 'button' && name instanceof RegExp && name.test('Sign in');
    return {
      count: async () => (visible ? 1 : 0),
      nth: () => ({ isVisible: async () => visible }),
    };
  },
});

function accessibleLoginPage(url, { controls = [], testIDs = [] } = {}) {
  const visibleLocator = (matches) => ({
    count: async () => matches.length,
    nth: (index) => ({ isVisible: async () => matches[index]?.visible !== false }),
  });
  return {
    url: () => url,
    locator: (selector) => {
      if (selector === 'input[type="password"]') return visibleLocator([]);
      const matches = testIDs
        .filter(({ value }) => selector.includes('[data-testid*="login" i]') && value.toLowerCase().includes('login'));
      return visibleLocator(matches);
    },
    getByRole: (role, options = {}) => {
      const matches = controls.filter((control) => {
        if (control.role !== role) return false;
        if (options.name instanceof RegExp) {
          options.name.lastIndex = 0;
          return options.name.test(control.name);
        }
        return options.name === undefined || options.name === control.name;
      });
      return visibleLocator(matches);
    },
  };
}

function trackedLoginPage(initialURL, passwordVisible = false, loginUIVisible = false) {
  const page = new EventEmitter();
  let currentURL = initialURL;
  const mainFrame = { page: () => page, url: () => currentURL };
  page.url = () => currentURL;
  page.mainFrame = () => mainFrame;
  page.locator = loginPage(initialURL, passwordVisible, loginUIVisible).locator;
  page.navigate = (nextURL) => {
    currentURL = nextURL;
    page.emit('framenavigated', mainFrame);
  };
  page.closeForTest = () => page.emit('close');
  return page;
}

function loginBrowserRequest(page, rawURL, { navigation = true, frame = page.mainFrame() } = {}) {
  return {
    url: () => rawURL,
    isNavigationRequest: () => navigation,
    frame: () => frame,
  };
}

async function newExecuteAuthFailureTracker(policy, options = {}) {
  const worker = await import('./browser_worker.mjs');
  assert.equal(typeof worker.createExecuteAuthFailureTracker, 'function');
  return worker.createExecuteAuthFailureTracker(policy, options);
}

function executeBrowserRequest(page, rawURL, {
  navigation = false,
  resourceType = 'fetch',
  method = 'GET',
  frame = page.mainFrame(),
} = {}) {
  return {
    url: () => rawURL,
    method: () => method,
    resourceType: () => resourceType,
    isNavigationRequest: () => navigation,
    frame: () => frame,
  };
}

function executeBrowserResponse(request, status, headers = {}) {
  const normalizedHeaders = new Map(Object.entries(headers).map(([name, value]) => [name.toLowerCase(), value]));
  return {
    request: () => request,
    status: () => status,
    url: () => request.url(),
    headerValue: async (name) => normalizedHeaders.get(name.toLowerCase()) ?? null,
  };
}

test('execute auth tracking ignores optional resources and action-unrelated fetch failures', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy);
  const page = trackedLoginPage('https://app.test/users');

  const finishAction = tracker.beginAction(page, { action: 'click' });
  const optionalImage = executeBrowserRequest(page, 'https://app.test/favicon.ico', { resourceType: 'image' });
  tracker.observeRequest(optionalImage);
  finishAction();
  tracker.observeResponse(executeBrowserResponse(optionalImage, 403));

  const finishUntrustedAction = tracker.beginAction(page, { action: 'click' });
  const untrustedFetch = executeBrowserRequest(page, 'https://unconfigured.test/users');
  tracker.observeRequest(untrustedFetch);
  finishUntrustedAction();
  tracker.observeResponse(executeBrowserResponse(untrustedFetch, 403));

  for (const rawURL of ['https://app.test/analytics', 'https://api.test/optional']) {
    const optionalFetch = executeBrowserRequest(page, rawURL);
    tracker.observeRequest(optionalFetch);
    tracker.observeResponse(executeBrowserResponse(optionalFetch, 403));
  }
  assert.equal(tracker.active(), false);
});

test('execute auth tracking keeps main-document failures until the same page loads successfully', async () => {
  let now = 10_000;
  const tracker = await newExecuteAuthFailureTracker(baseRequest().policy, { now: () => now });
  const page = trackedLoginPage('https://app.test/users');
  const failedDocument = executeBrowserRequest(page, 'https://app.test/users', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(failedDocument);
  tracker.observeResponse(executeBrowserResponse(failedDocument, 401));
  assert.equal(tracker.active(), true);
  now += 10_000;
  assert.equal(tracker.active(), true);
  assert.equal(await tracker.settle(), true);

  const recoveredDocument = executeBrowserRequest(page, 'https://app.test/users?retry=1', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(recoveredDocument);
  tracker.observeResponse(executeBrowserResponse(recoveredDocument, 200));
  assert.equal(tracker.active(), false);
});

test('executeAction scopes allowed fetch failures to the current controlled action', async () => {
  const worker = await import('./browser_worker.mjs');
  assert.equal(typeof worker.executeAction, 'function');
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy);
  const page = trackedLoginPage('https://app.test/users');
  const criticalRequest = executeBrowserRequest(page, 'https://api.test/users/search', { method: 'POST' });
  page.getByRole = () => ({
    first: () => ({
      click: async () => {
        tracker.observeRequest(criticalRequest);
        await tracker.observeResponse(executeBrowserResponse(criticalRequest, 403, { 'WWW-Authenticate': 'Bearer realm="test"' }));
      },
    }),
  });
  const action = { id: 'search', action: 'click', locator: { kind: 'role', value: 'button', name: 'Search' } };
  await worker.executeAction(page, action, { ...baseRequest(), policy }, 0, async () => ({ loginRequired: false, path: '' }), tracker);
  assert.equal(tracker.active(), true);
});

test('execute auth tracking keeps API 401 active past quiet time until the same action and request semantic recovers', async () => {
  let now = 20_000;
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy, { now: () => now });
  const page = trackedLoginPage('https://app.test/users');
  const failedRequest = executeBrowserRequest(page, 'https://api.test/users/search', { method: 'POST' });
  let finishAction = tracker.beginAction(page, { action: 'press' });
  tracker.observeRequest(failedRequest);
  tracker.observeResponse(executeBrowserResponse(failedRequest, 401));
  assert.equal(tracker.active(), true);
  now += 10_000;
  assert.equal(await tracker.settle(), true);
  assert.equal(tracker.active(), true);

  const retrySuccess = executeBrowserRequest(page, 'https://api.test/users/search', { method: 'POST' });
  tracker.observeRequest(retrySuccess);
  tracker.observeResponse(executeBrowserResponse(retrySuccess, 200));
  finishAction();
  assert.equal(tracker.active(), false);
});

test('execute auth scopes bind fetches to one page main frame and action token with bounded post-action grace', async () => {
  let now = 30_000;
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy, {
    now: () => now,
    requestStartGraceMs: 100,
  });
  const page = trackedLoginPage('https://app.test/users');
  const otherPage = trackedLoginPage('https://app.test/other');
  const otherFrame = { page: () => page };

  const finishAction = tracker.beginAction(page, { action: 'click' });
  for (const request of [
    executeBrowserRequest(otherPage, 'https://api.test/other-page'),
    executeBrowserRequest(page, 'https://api.test/subframe', { frame: otherFrame }),
    executeBrowserRequest(page, 'https://api.test/background', { frame: null }),
  ]) {
    tracker.observeRequest(request);
    tracker.observeResponse(executeBrowserResponse(request, 401));
  }
  assert.equal(tracker.active(), false);

  finishAction();
  now += 50;
  const debounced = executeBrowserRequest(page, 'https://api.test/debounced');
  tracker.observeRequest(debounced);
  tracker.observeResponse(executeBrowserResponse(debounced, 401));
  assert.equal(tracker.active(), true);

  const recovery = executeBrowserRequest(page, 'https://app.test/users', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(recovery);
  tracker.observeResponse(executeBrowserResponse(recovery, 200));
  assert.equal(tracker.active(), false);

  now += 51;
  const late = executeBrowserRequest(page, 'https://api.test/late');
  tracker.observeRequest(late);
  tracker.observeResponse(executeBrowserResponse(late, 401));
  assert.equal(tracker.active(), false);

  const finishWait = tracker.beginAction(page, { action: 'wait_for' });
  const waitFetch = executeBrowserRequest(page, 'https://api.test/wait');
  tracker.observeRequest(waitFetch);
  finishWait();
  tracker.observeResponse(executeBrowserResponse(waitFetch, 401));
  assert.equal(tracker.active(), false);
});

test('execute auth action tokens prevent a later action from clearing an earlier action failure', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy);
  const page = trackedLoginPage('https://app.test/users');

  const finishFirst = tracker.beginAction(page, { action: 'click' });
  const failed = executeBrowserRequest(page, 'https://api.test/users/search', { method: 'POST' });
  tracker.observeRequest(failed);
  tracker.observeResponse(executeBrowserResponse(failed, 401));
  finishFirst();

  const finishSecond = tracker.beginAction(page, { action: 'press' });
  const unrelatedSuccess = executeBrowserRequest(page, 'https://api.test/users/search', { method: 'POST' });
  tracker.observeRequest(unrelatedSuccess);
  tracker.observeResponse(executeBrowserResponse(unrelatedSuccess, 200));
  finishSecond();
  assert.equal(tracker.active(), true);
});

test('execute auth documents accept only the latest allowed main-frame navigation response and clear on page close', async () => {
  const tracker = await newExecuteAuthFailureTracker(baseRequest().policy);
  const page = trackedLoginPage('https://app.test/users');
  const older = executeBrowserRequest(page, 'https://app.test/older', { navigation: true, resourceType: 'document' });
  const latest = executeBrowserRequest(page, 'https://app.test/latest', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(older);
  tracker.observeRequest(latest);
  tracker.observeResponse(executeBrowserResponse(latest, 200));
  tracker.observeResponse(executeBrowserResponse(older, 401));
  assert.equal(tracker.active(), false);

  const failedLatest = executeBrowserRequest(page, 'https://app.test/failing', { navigation: true, resourceType: 'document' });
  const staleSuccess = executeBrowserRequest(page, 'https://app.test/stale', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(staleSuccess);
  tracker.observeRequest(failedLatest);
  tracker.observeResponse(executeBrowserResponse(failedLatest, 401));
  tracker.observeResponse(executeBrowserResponse(staleSuccess, 200));
  assert.equal(tracker.active(), true);
  page.closeForTest();
  assert.equal(tracker.active(), false);

  const untrustedPage = trackedLoginPage('https://app.test/users');
  const untrusted = executeBrowserRequest(untrustedPage, 'https://unconfigured.test/login', { navigation: true, resourceType: 'document' });
  tracker.observeRequest(untrusted);
  tracker.observeResponse(executeBrowserResponse(untrusted, 401));
  assert.equal(tracker.active(), false);
});

test('execute auth API semantics include query and ignore stale concurrent responses', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy);
  const page = trackedLoginPage('https://app.test/users');
  const finishAction = tracker.beginAction(page, { action: 'click' });

  const queryFailure = executeBrowserRequest(page, 'https://api.test/users?q=one');
  const otherQuerySuccess = executeBrowserRequest(page, 'https://api.test/users?q=two');
  tracker.observeRequest(queryFailure);
  tracker.observeRequest(otherQuerySuccess);
  tracker.observeResponse(executeBrowserResponse(queryFailure, 401));
  tracker.observeResponse(executeBrowserResponse(otherQuerySuccess, 200));
  assert.equal(tracker.active(), true);

  const older = executeBrowserRequest(page, 'https://api.test/session');
  const latest = executeBrowserRequest(page, 'https://api.test/session');
  tracker.observeRequest(older);
  tracker.observeRequest(latest);
  tracker.observeResponse(executeBrowserResponse(latest, 200));
  tracker.observeResponse(executeBrowserResponse(older, 401));
  assert.equal(tracker.active(), true, 'stale response must not disturb existing query failure');

  const olderFailure = executeBrowserRequest(page, 'https://api.test/profile');
  const latestRecovery = executeBrowserRequest(page, 'https://api.test/profile');
  tracker.observeRequest(olderFailure);
  tracker.observeRequest(latestRecovery);
  tracker.observeResponse(executeBrowserResponse(olderFailure, 401));
  tracker.observeResponse(executeBrowserResponse(latestRecovery, 200));

  const queryRecovery = executeBrowserRequest(page, 'https://api.test/users?q=one');
  tracker.observeRequest(queryRecovery);
  tracker.observeResponse(executeBrowserResponse(queryRecovery, 200));
  finishAction();
  assert.equal(tracker.active(), false);
});

test('execute auth treats 403 as authentication only with an explicit challenge', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy);
  const page = trackedLoginPage('https://app.test/users');
  const finishAction = tracker.beginAction(page, { action: 'click' });

  const authorizationDenied = executeBrowserRequest(page, 'https://api.test/admin');
  tracker.observeRequest(authorizationDenied);
  await tracker.observeResponse(executeBrowserResponse(authorizationDenied, 403));
  assert.equal(tracker.active(), false);

  const authenticationDenied = executeBrowserRequest(page, 'https://api.test/session');
  tracker.observeRequest(authenticationDenied);
  await tracker.observeResponse(executeBrowserResponse(authenticationDenied, 403, { 'WWW-Authenticate': 'Bearer realm="test"' }));
  finishAction();
  assert.equal(tracker.active(), true);
});

test('execute auth bounds an unavailable 403 challenge header lookup', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy, { responseCheckTimeoutMs: 1 });
  const page = trackedLoginPage('https://app.test/users');
  const finish = tracker.beginAction(page, { action: 'click' });
  const request = executeBrowserRequest(page, 'https://api.test/forbidden');
  tracker.observeRequest(request);
  const response = executeBrowserResponse(request, 403);
  response.headerValue = async () => new Promise(() => {});
  const result = await Promise.race([
    tracker.observeResponse(response),
    new Promise((resolveTimeout) => setTimeout(() => resolveTimeout('unbounded'), 20)),
  ]);
  finish();
  assert.equal(result, false);
  assert.equal(await tracker.settle(), false);
});

test('execute auth tracking fails closed at fixed capacity and releases settled and closed entries', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const page = trackedLoginPage('https://app.test/users');
  const reusable = await newExecuteAuthFailureTracker(policy, {
    maxTrackedPages: 1,
    maxTrackedAPISemantics: 1,
    maxActionScopes: 1,
  });
  const finish = reusable.beginAction(page, { action: 'click' });
  const settled = executeBrowserRequest(page, 'https://api.test/settled');
  reusable.observeRequest(settled);
  reusable.observeResponse(executeBrowserResponse(settled, 200));
  const replacement = executeBrowserRequest(page, 'https://api.test/replacement');
  reusable.observeRequest(replacement);
  reusable.observeResponse(executeBrowserResponse(replacement, 401));
  finish();
  assert.equal(reusable.active(), true, 'a settled semantic must release its slot for a later tracked failure');
  page.closeForTest();
  assert.equal(reusable.active(), false, 'closing a page must clear its failures and capacity');
  const reusedPage = trackedLoginPage('https://app.test/reused');
  const finishReused = reusable.beginAction(reusedPage, { action: 'click' });
  const reusedFailure = executeBrowserRequest(reusedPage, 'https://api.test/reused');
  reusable.observeRequest(reusedFailure);
  reusable.observeResponse(executeBrowserResponse(reusedFailure, 401));
  finishReused();
  assert.equal(reusable.active(), true, 'a closed page must release its page and action slots');

  const overflowing = await newExecuteAuthFailureTracker(policy, {
    maxTrackedPages: 1,
    maxTrackedAPISemantics: 1,
    maxActionScopes: 1,
  });
  const overflowPage = trackedLoginPage('https://app.test/users');
  const finishOverflow = overflowing.beginAction(overflowPage, { action: 'click' });
  const first = executeBrowserRequest(overflowPage, 'https://api.test/first');
  const second = executeBrowserRequest(overflowPage, 'https://api.test/second');
  overflowing.observeRequest(first);
  overflowing.observeRequest(second);
  finishOverflow();
  assert.equal(overflowing.active(), true, 'capacity overflow must set a fail-closed sentinel');

  let now = 40_000;
  const expiredScope = await newExecuteAuthFailureTracker(policy, {
    now: () => now,
    requestStartGraceMs: 10,
    maxTrackedPages: 1,
    maxTrackedAPISemantics: 1,
    maxActionScopes: 1,
  });
  const scopePage = trackedLoginPage('https://app.test/users');
  expiredScope.beginAction(scopePage, { action: 'click' })();
  now += 11;
  const finishReplacementScope = expiredScope.beginAction(scopePage, { action: 'press' });
  const scopedFailure = executeBrowserRequest(scopePage, 'https://api.test/scoped');
  expiredScope.observeRequest(scopedFailure);
  expiredScope.observeResponse(executeBrowserResponse(scopedFailure, 401));
  finishReplacementScope();
  assert.equal(expiredScope.active(), true, 'an expired action scope must release its slot without overflowing');

  const pageOverflow = await newExecuteAuthFailureTracker(policy, { maxTrackedPages: 1 });
  const firstPage = trackedLoginPage('https://app.test/first');
  const secondPage = trackedLoginPage('https://app.test/second');
  pageOverflow.observeRequest(executeBrowserRequest(firstPage, 'https://app.test/first', { navigation: true, resourceType: 'document' }));
  pageOverflow.observeRequest(executeBrowserRequest(secondPage, 'https://app.test/second', { navigation: true, resourceType: 'document' }));
  assert.equal(pageOverflow.active(), true, 'page capacity overflow must fail closed');
});

test('execute auth tracking releases failed requests that never receive a response', async () => {
  const policy = baseRequest().policy;
  policy.allowed_origins.push('https://api.test');
  const tracker = await newExecuteAuthFailureTracker(policy, {
    maxTrackedPages: 1,
    maxTrackedAPISemantics: 1,
    maxActionScopes: 1,
    maxPendingRequests: 1,
  });
  assert.equal(typeof tracker.observeRequestSettled, 'function');
  const page = trackedLoginPage('https://app.test/users');
  const finish = tracker.beginAction(page, { action: 'click' });
  const failedTransport = executeBrowserRequest(page, 'https://api.test/transport-failure');
  tracker.observeRequest(failedTransport);
  tracker.observeRequestSettled(failedTransport);
  const replacement = executeBrowserRequest(page, 'https://api.test/replacement');
  tracker.observeRequest(replacement);
  tracker.observeResponse(executeBrowserResponse(replacement, 401));
  finish();
  assert.equal(tracker.active(), true, 'request settlement must release classification and semantic capacity');
});

test('login does not complete on a public shell before a delayed auth redirect', async () => {
  const policy = baseLoginRequest().policy;
  const initial = await observeLoginState([loginPage('https://app.test')], policy, false, false);
  assert.deepEqual(initial, { started: false, ready: false });

  const redirected = await observeLoginState([loginPage('https://login.test/sso')], policy, initial.started, false);
  assert.deepEqual(redirected, { started: true, ready: false });

  const knownRoute = await observeLoginState([loginPage('https://app.test/sign-in')], policy, false, false);
  assert.deepEqual(knownRoute, { started: true, ready: false });
});

test('visible non-password login UI starts login and blocks completion', async () => {
  const policy = baseLoginRequest().policy;
  const observed = await observeLoginState([loginPage('https://app.test/', false, true)], policy, false, false);
  assert.deepEqual(observed, { started: true, ready: false });
});

test('text-only Sign in and Log in actions block login completion by accessible role and name', async () => {
  const policy = baseLoginRequest().policy;
  for (const control of [
    { role: 'button', name: 'Sign in' },
    { role: 'link', name: 'Log in' },
  ]) {
    const observed = await observeLoginState([
      accessibleLoginPage('https://app.test/users', { controls: [control] }),
    ], policy, true, false);
    assert.deepEqual(observed, { started: true, ready: false }, `${control.role} ${control.name}`);
  }
});

test('unrelated login history test IDs do not masquerade as a visible login action', async () => {
  const policy = baseLoginRequest().policy;
  const observed = await observeLoginState([
    accessibleLoginPage('https://app.test/users', { testIDs: [{ value: 'last-login' }] }),
  ], policy, true, false);
  assert.deepEqual(observed, { started: true, ready: true });
});

test('401 or 403 never starts login and blocks completion during its quiet window', async () => {
  const policy = baseLoginRequest().policy;
  const forbidden = await observeLoginState([loginPage('https://app.test')], policy, false, true);
  assert.deepEqual(forbidden, { started: false, ready: false });

  const publicShell = await observeLoginState([loginPage('https://app.test')], policy, forbidden.started, false);
  assert.deepEqual(publicShell, { started: false, ready: false });

  const authPage = await observeLoginState([loginPage('https://login.test/sso')], policy, false, false);
  assert.deepEqual(authPage, { started: true, ready: false });

  const recentFailure = await observeLoginState([loginPage('https://app.test/users')], policy, authPage.started, true);
  assert.deepEqual(recentFailure, { started: true, ready: false });

  const quiet = await observeLoginState([loginPage('https://app.test/users')], policy, recentFailure.started, false);
  assert.deepEqual(quiet, { started: true, ready: true });
});

test('auth failure activity stays active until responses are quiet for the stability window', () => {
  let now = 10_000;
  const tracker = createLoginAuthFailureTracker(() => now);
  assert.equal(tracker.active(), false);

  tracker.observeStatus(401);
  assert.equal(tracker.active(), true);
  now += 999;
  assert.equal(tracker.active(), true);

  tracker.observeStatus(403);
  now += 999;
  assert.equal(tracker.active(), true);
  now += 2;
  assert.equal(tracker.active(), false);

  tracker.observeStatus(200);
  assert.equal(tracker.active(), false);
});

test('login navigation history ignores auth subresources, status fetches, and subframe navigations', () => {
  const policy = baseLoginRequest().policy;
  const page = trackedLoginPage('https://app.test/oauth/start?state=opaque');
  const tracker = createLoginNavigationTracker(policy);
  tracker.trackPage(page);
  tracker.observeRequest(loginBrowserRequest(page, 'https://login.test/oauth/pixel.png', { navigation: false }));
  tracker.observeRequest(loginBrowserRequest(page, 'https://app.test/login/status', { navigation: false }));
  const subframe = { page: () => page };
  tracker.observeRequest(loginBrowserRequest(page, 'https://login.test/oauth/frame', { frame: subframe }));
  assert.equal(tracker.started(), false);

  tracker.observeRequest(loginBrowserRequest(page, 'https://login.test/oauth/authorize'));
  assert.equal(tracker.started(), true);
});

test('login navigation history remembers a fast top-level auth redirect completed before the first poll', async () => {
  let now = 10_000;
  const policy = baseLoginRequest().policy;
  const page = trackedLoginPage('https://app.test/oauth/start?state=opaque');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  tracker.trackPage(page);
  page.navigate('https://login.test/oauth/authorize');
  page.navigate('https://app.test/oauth/callback');
  const observed = await observeLoginState([page], policy, tracker.started(), false);
  assert.deepEqual(observed, { started: true, ready: true });
  assert.equal(tracker.completionStable(observed.ready), false);
  now += 1_001;
  assert.equal(tracker.completionStable(observed.ready), true);
});

test('SPA OAuth callback waits for asynchronous session storage before completion can save', async () => {
  let now = 20_000;
  let tokenWritten = false;
  let saves = 0;
  const policy = baseLoginRequest().policy;
  const page = trackedLoginPage('https://app.test/oauth/start?state=opaque');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  tracker.trackPage(page);
  page.navigate('https://login.test/oauth/authorize');
  page.navigate('https://app.test/oauth/callback');
  const maybeSave = async () => {
    const observed = await observeLoginState([page], policy, tracker.started(), false);
    if (tracker.completionStable(observed.ready)) {
      assert.equal(tokenWritten, true);
      saves += 1;
    }
  };
  await maybeSave();
  now += 999;
  tokenWritten = true;
  await maybeSave();
  assert.equal(saves, 0);
  now += 2;
  await maybeSave();
  assert.equal(saves, 1);
});

test('closing an auth popup resets OAuth completion stability and excludes the closed page', async () => {
  let now = 30_000;
  const policy = baseLoginRequest().policy;
  const application = trackedLoginPage('https://app.test/users');
  const popup = trackedLoginPage('about:blank');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  tracker.trackPage(application);
  tracker.trackPage(popup);
  popup.navigate('https://login.test/oauth/authorize');
  popup.closeForTest();
  const observed = await observeLoginState([application], policy, tracker.started(), false);
  assert.deepEqual(observed, { started: true, ready: true });
  assert.equal(tracker.completionStable(observed.ready), false);
  now += 1_001;
  assert.equal(tracker.completionStable(observed.ready), true);
});

test('an open about:blank OAuth popup blocks completion while it waits for delayed navigation', async () => {
  let now = 35_000;
  const policy = baseLoginRequest().policy;
  const application = trackedLoginPage('https://app.test/users');
  const popup = trackedLoginPage('about:blank');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  tracker.trackPage(application);
  application.navigate('https://login.test/oauth/authorize');
  application.navigate('https://app.test/users');
  tracker.trackPage(popup);

  let observed = await observeLoginState([application, popup], policy, tracker.started(), false);
  assert.deepEqual(observed, { started: true, ready: false });
  now += 1_001;
  assert.equal(tracker.completionStable(observed.ready), false);

  popup.navigate('https://login.test/oauth/authorize');
  observed = await observeLoginState([application, popup], policy, tracker.started(), false);
  assert.deepEqual(observed, { started: true, ready: false });
  assert.equal(tracker.completionStable(observed.ready), false);
});

test('a tracked page navigating to a non-HTTP URL resets stability and blocks completion', async () => {
  let now = 37_000;
  const policy = baseLoginRequest().policy;
  const application = trackedLoginPage('https://app.test/users');
  const auxiliary = trackedLoginPage('https://app.test/complete');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  tracker.trackPage(application);
  tracker.trackPage(auxiliary);
  application.navigate('https://login.test/oauth/authorize');
  application.navigate('https://app.test/users');
  let observed = await observeLoginState([application, auxiliary], policy, tracker.started(), false);
  assert.equal(observed.ready, true);
  assert.equal(tracker.completionStable(observed.ready), false);
  now += 1_001;
  assert.equal(tracker.completionStable(observed.ready), true);

  auxiliary.navigate('data:text/html,still-loading');
  observed = await observeLoginState([application, auxiliary], policy, tracker.started(), false);
  assert.deepEqual(observed, { started: true, ready: false });
  assert.equal(tracker.completionStable(observed.ready), false);
});

test('OAuth completion waits through auth failure quiet and stability windows', async () => {
  let now = 40_000;
  const policy = baseLoginRequest().policy;
  const application = trackedLoginPage('https://app.test/users');
  const tracker = createLoginNavigationTracker(policy, { now: () => now, stableWindowMs: 1_000 });
  const failures = createLoginAuthFailureTracker(() => now, 1_000);
  tracker.trackPage(application);
  application.navigate('https://login.test/oauth/authorize');
  application.navigate('https://app.test/users');
  failures.observeStatus(401);
  tracker.observeAuthFailure(401);

  now += 999;
  let observed = await observeLoginState([application], policy, tracker.started(), failures.active());
  assert.equal(observed.ready, false);
  assert.equal(tracker.completionStable(observed.ready), false);

  now += 2;
  observed = await observeLoginState([application], policy, tracker.started(), failures.active());
  assert.equal(observed.ready, true);
  assert.equal(tracker.completionStable(observed.ready), false);
  now += 1_001;
  assert.equal(tracker.completionStable(observed.ready), true);
});

test('login completion rejects every open HTTP page outside the application origin', async () => {
  const policy = baseLoginRequest().policy;
  const application = loginPage('https://app.test/users');
  const openAuthPopup = loginPage('https://login.test/oauth/authorize');
  const observed = await observeLoginState([application, openAuthPopup], policy, true, false);
  assert.deepEqual(observed, { started: true, ready: false });
});

test('guarded login context protects the initial page and every popup before use', async () => {
  const calls = [];
  let pageListener;
  let webSocketHandler;
  const contextHandlers = new Map();
  const fakePage = (name) => {
    const handlers = new Map();
    return {
      name,
      handlers,
      timeout: 0,
      navigationTimeout: 0,
      setDefaultTimeout(value) {
        calls.push(`${name}:timeout`);
        this.timeout = value;
      },
      setDefaultNavigationTimeout(value) {
        calls.push(`${name}:navigation-timeout`);
        this.navigationTimeout = value;
      },
      on(event, handler) {
        calls.push(`${name}:on:${event}`);
        assert.equal(handlers.has(event), false, `${name} ${event} guard installed twice`);
        handlers.set(event, handler);
      },
    };
  };
  const initialPage = fakePage('initial');
  const context = {
    pages: () => [],
    on(event, handler) {
      calls.push(`context:on:${event}`);
      contextHandlers.set(event, handler);
      if (event === 'page') pageListener = handler;
    },
    async routeWebSocket(pattern, handler) {
      calls.push('context:websocket');
      assert.equal(pattern, '**/*');
      webSocketHandler = handler;
    },
    async newPage() {
      calls.push('context:new-page');
      pageListener(initialPage);
      return initialPage;
    },
  };
  let contextOptions;
  const browser = {
    async newContext(options) {
      contextOptions = options;
      return context;
    },
  };

  const guarded = await createGuardedLoginContext(browser, { storageState: '/opaque/state.json' });
  assert.equal(guarded.context, context);
  assert.equal(guarded.page, initialPage);
  assert.deepEqual(contextOptions, {
    storageState: '/opaque/state.json',
    serviceWorkers: 'block',
    acceptDownloads: false,
    viewport: { width: 1280, height: 720 },
  });
  assert.ok(calls.indexOf('context:on:page') < calls.indexOf('context:new-page'));
  assert.ok(calls.indexOf('context:on:dialog') < calls.indexOf('context:new-page'));
  assert.ok(calls.indexOf('context:on:download') < calls.indexOf('context:new-page'));
  assert.ok(calls.indexOf('context:websocket') < calls.indexOf('context:new-page'));

  const popup = fakePage('popup');
  let dismissed = false;
  let canceled = false;
  contextHandlers.get('dialog')({ dismiss: async () => { dismissed = true; } });
  contextHandlers.get('download')({ cancel: async () => { canceled = true; } });
  await Promise.resolve();
  assert.equal(dismissed, true);
  assert.equal(canceled, true);
  pageListener(popup);
  for (const page of [initialPage, popup]) {
    assert.equal(page.timeout, 15_000);
    assert.equal(page.navigationTimeout, 30_000);
    assert.equal(page.handlers.has('dialog'), false);
    assert.equal(page.handlers.has('download'), false);
  }

  let webSocketClosed = false;
  webSocketHandler({ close: () => { webSocketClosed = true; } });
  assert.equal(webSocketClosed, true);
});

test('login completes only after password or auth UI returns to the app', async () => {
  const policy = baseLoginRequest().policy;
  const prompted = await observeLoginState([loginPage('https://app.test/login', true)], policy, false, false);
  assert.deepEqual(prompted, { started: true, ready: false });

  const completed = await observeLoginState([loginPage('https://app.test/users')], policy, prompted.started, false);
  assert.deepEqual(completed, { started: true, ready: true });
});

test('login waits for an OAuth popup and completes after the auth popup closes', async () => {
  const policy = baseLoginRequest().policy;
  const popup = await observeLoginState([
    loginPage('https://app.test'),
    loginPage('https://login.test/oauth/authorize'),
  ], policy, false, false);
  assert.deepEqual(popup, { started: true, ready: false });

  const closed = await observeLoginState([loginPage('https://app.test/users')], policy, popup.started, false);
  assert.deepEqual(closed, { started: true, ready: true });
});

test('login storageState atomically replaces a pre-created 0600 target', async () => {
  const temporary = mkdtempSync(join(tmpdir(), 'tshoot-login-state-'));
  const target = join(temporary, 'hashed-session.json');
  writeFileSync(target, '{"cookies":[{"value":"old"}]}', { mode: 0o600 });
  const context = {
    storageState: async ({ path }) => {
      assert.equal(statSync(path).mode & 0o777, 0o600);
      assert.equal(readFileSync(target, 'utf8').includes('old'), true);
      writeFileSync(path, '{"cookies":[{"value":"new"}]}');
    },
  };
  try {
    await saveLoginStorageState(context, target);
    assert.equal(readFileSync(target, 'utf8').includes('new'), true);
    assert.equal(statSync(target).mode & 0o777, 0o600);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
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
    const captured = await captureSafePNG(page, request, 'transient.png', () => false, async () => {
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

test('safe screenshot rechecks auth failure raised during capture', async () => {
  const temporary = mkdtempSync(join(tmpdir(), 'tshoot-live-auth-screenshot-'));
  const output = join(temporary, 'transient.png');
  let authFailure = false;
  const page = {
    url: () => 'https://app.test/users',
    locator: () => ({ count: async () => 0 }),
  };
  const request = baseRequest();
  request.staging_dir = temporary;
  try {
    const captured = await captureSafePNG(page, request, 'transient.png', () => authFailure, async () => {
      writeFileSync(output, Buffer.from('png bytes'));
      authFailure = true;
      return 'browser/transient.png';
    });
    assert.deepEqual(
      { captured, pngExists: existsSync(output) },
      { captured: { loginRequired: true, path: '' }, pngExists: false },
    );
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
});

test('safe screenshot refuses capture when an OAuth popup is on an auth origin', async () => {
  const primary = loginPage('https://app.test/users');
  const popup = loginPage('https://login.test/oauth/authorize');
  let captureCalls = 0;
  const captured = await captureSafePNG(
    primary,
    baseRequest(),
    'must-not-exist.png',
    () => false,
    async () => { captureCalls += 1; return 'browser/must-not-exist.png'; },
    () => [primary, popup],
  );
  assert.deepEqual(captured, { loginRequired: true, path: '' });
  assert.equal(captureCalls, 0);
});

test('screenshot capture is viewport-bounded and deletes the just-written file when the attempt budget is exceeded', async () => {
  const temporary = mkdtempSync(join(tmpdir(), 'tshoot-bounded-screenshot-'));
  let screenshotOptions;
  const page = {
    screenshot: async (options) => {
      screenshotOptions = options;
      writeFileSync(options.path, Buffer.from('\x89PNG\r\n\x1a\nfixture'));
    },
  };
  try {
    await assert.rejects(
      capturePNG(page, temporary, 'too-large.png', createArtifactBudget({ maxFiles: 4, maxBytes: 8 })),
      /artifact budget/,
    );
    assert.equal(screenshotOptions.fullPage, false);
    assert.equal(screenshotOptions.type, 'png');
    assert.equal(existsSync(join(temporary, 'too-large.png')), false);
    assert.deepEqual(readdirSync(temporary), []);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
});

test('attempt artifact budget enforces both file-count and total-byte limits', () => {
  const byCount = createArtifactBudget({ maxFiles: 2, maxBytes: 100 });
  assert.equal(byCount.reserve(10), true);
  assert.equal(byCount.reserve(10), true);
  assert.equal(byCount.reserve(1), false);
  const byBytes = createArtifactBudget({ maxFiles: 10, maxBytes: 20 });
  assert.equal(byBytes.reserve(20), true);
  assert.equal(byBytes.reserve(1), false);
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
  assert.equal((source.match(/import\('playwright'\)/g) ?? []).length, 3);
  assert.equal(source.includes("from 'playwright'"), false);
  assert.equal(source.includes("context.route('**/*'"), true);
});

test('execute, login, and probe can only launch Chromium through the pinned proxy helper', () => {
  const workerPath = fileURLToPath(new URL('./browser_worker.mjs', import.meta.url));
  const source = readFileSync(workerPath, 'utf8');
  assert.equal((source.match(/chromium\.launch\(/g) ?? []).length, 1);
  assert.equal((source.match(/launchPinnedBrowser\(chromium,/g) ?? []).length, 4);
});

test('unsupported CLI mode emits exactly one final JSON object and no progress on stdout', () => {
  const workerPath = fileURLToPath(new URL('./browser_worker.mjs', import.meta.url));
  const run = spawnSync(process.execPath, [workerPath, '--mode', 'unsupported'], { encoding: 'utf8' });
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
