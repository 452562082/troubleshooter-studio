import test from 'node:test';
import assert from 'node:assert/strict';

import { redactConsoleText, sanitizeURL, safeResponseRecord } from './sanitize.mjs';

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
