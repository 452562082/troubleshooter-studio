const REDACTED = '[REDACTED]';
const INVALID_URL = '[INVALID_URL]';
const MAX_CONSOLE_BYTES = 8 * 1024;
const MAX_ID_BYTES = 128;
const MAX_CONTENT_TYPE_BYTES = 256;
const MAX_CAUSAL_TEXT_BYTES = 512;
const MAX_INITIATOR_FRAMES = 12;
const SENSITIVE_QUERY_KEY = /token|password|secret|code|session|auth|cookie|key/i;
const REQUEST_ID_HEADERS = ['x-request-id', 'request-id', 'x-correlation-id', 'correlation-id', 'x-amzn-requestid'];
const TRACE_ID_HEADERS = ['x-trace-id', 'trace-id', 'traceparent'];

const credentialPatterns = [
  /\b(?:proxy-authorization|authorization|set-cookie|cookie|www-authenticate|proxy-authenticate)\s*:/i,
  /\bbearer\s+[A-Za-z0-9._~+/=-]{3,}/i,
  /(?:^|[?&;,\s{])["']?(?:[A-Za-z0-9_.-]+[._-])?(?:password|passwd|access[_-]?token|token|api[_-]?key|client[_-]?secret|secret[_-]?access[_-]?key|access[_-]?key|private[_-]?key|authorization|auth|cookie|secret)["']?\s*[:=]\s*["']?[^\s&,;}"']+/i,
  /-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----/i,
  /\b(?:github_pat_[A-Za-z0-9_]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|(?:AKIA|ASIA|A3T[A-Z0-9])[A-Z0-9]{12,})\b/i,
];

export function boundedUTF8(value, maxBytes) {
  const bytes = new TextEncoder().encode(String(value ?? ''));
  if (bytes.length <= maxBytes) return new TextDecoder().decode(bytes);
  for (let end = maxBytes; end > 0; end -= 1) {
    try {
      return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, end));
    } catch {
      // A UTF-8 code point crossed the boundary; drop its incomplete suffix.
    }
  }
  return '';
}

function containsCredential(value) {
  return credentialPatterns.some((pattern) => pattern.test(value));
}

function normalizedHeaders(headers) {
  const result = new Map();
  const add = (name, value) => {
    const key = String(name ?? '').trim().toLowerCase();
    if (!key || result.has(key)) return;
    const first = Array.isArray(value) ? value[0] : value;
    result.set(key, String(first ?? ''));
  };

  if (headers && typeof headers.forEach === 'function') {
    headers.forEach((value, name) => add(name, value));
  } else if (Array.isArray(headers)) {
    for (const entry of headers) {
      if (Array.isArray(entry) && entry.length >= 2) add(entry[0], entry[1]);
    }
  } else if (headers && typeof headers === 'object') {
    for (const [name, value] of Object.entries(headers)) add(name, value);
  }
  return result;
}

function firstHeader(headers, allowlist) {
  for (const name of allowlist) {
    if (headers.has(name)) return headers.get(name);
  }
  return '';
}

function safeIdentifier(value) {
  const text = String(value ?? '').trim();
  if (!text) return '';
  if (new TextEncoder().encode(text).length > MAX_ID_BYTES) return REDACTED;
  if (text.includes('\r') || text.includes('\n') || containsCredential(text)) return REDACTED;
  if (!/^[A-Za-z0-9._~:/+=-]+$/.test(text)) return REDACTED;
  return text;
}

function safeMethod(value) {
  const method = String(value ?? '').trim().toUpperCase();
  if (!/^[A-Z][A-Z0-9!#$%&'*+.^_`|~-]{0,15}$/.test(method)) return '';
  return method;
}

function safeContentType(value) {
  const contentType = String(value ?? '').trim();
  if (!contentType || contentType.includes('\r') || contentType.includes('\n')) return '';
  if (new TextEncoder().encode(contentType).length > MAX_CONTENT_TYPE_BYTES || containsCredential(contentType)) return '';
  return contentType;
}

function safeNonNegativeNumber(value, integerOnly = false) {
  const number = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(number) || number < 0 || (integerOnly && !Number.isInteger(number))) return 0;
  return number;
}

function safeCausalText(value, maxBytes = MAX_CAUSAL_TEXT_BYTES) {
  const text = boundedUTF8(value, maxBytes).trim();
  if (!text) return '';
  if (text.includes('\r') || text.includes('\n') || containsCredential(text)) return REDACTED;
  return text;
}

function safeTimestamp(value) {
  const text = String(value ?? '').trim();
  if (!text) return '';
  const parsed = new Date(text);
  if (Number.isNaN(parsed.getTime()) || parsed.toISOString() !== text) return '';
  return text;
}

function safeEnum(value, allowed) {
  const text = String(value ?? '').trim().toLowerCase();
  return allowed.has(text) ? text : '';
}

function safeInitiatorStack(value) {
  if (!Array.isArray(value)) return [];
  return value.slice(0, MAX_INITIATOR_FRAMES).map((frame) => ({
    function_name: safeCausalText(frame?.function_name, 256),
    url: frame?.url ? sanitizeURL(frame.url) : '',
    source_map_url: frame?.source_map_url ? sanitizeURL(frame.source_map_url) : '',
    line: safeNonNegativeNumber(frame?.line, true),
    column: safeNonNegativeNumber(frame?.column, true),
  }));
}

export function sanitizeURL(rawURL) {
  try {
    const parsed = new URL(String(rawURL));
    parsed.username = '';
    parsed.password = '';
    const sanitized = new URLSearchParams();
    for (const [name, value] of parsed.searchParams.entries()) {
      sanitized.append(name, SENSITIVE_QUERY_KEY.test(name) ? REDACTED : value);
    }
    parsed.search = sanitized.toString();
    return parsed.toString();
  } catch {
    return INVALID_URL;
  }
}

export function redactConsoleText(input) {
  const bounded = boundedUTF8(input, MAX_CONSOLE_BYTES);
  return containsCredential(bounded) ? REDACTED : bounded;
}

export function safeResponseRecord(input = {}) {
  const headers = normalizedHeaders(input.headers);
  const contentType = input.content_type ?? firstHeader(headers, ['content-type']);
  const contentLength = input.content_length ?? firstHeader(headers, ['content-length']);
  return {
    action_id: safeIdentifier(input.action_id),
    started_at: safeTimestamp(input.started_at),
    method: safeMethod(input.method),
    url: sanitizeURL(input.url),
    resource_type: safeEnum(input.resource_type, new Set(['document', 'stylesheet', 'image', 'media', 'font', 'script', 'texttrack', 'xhr', 'fetch', 'eventsource', 'websocket', 'manifest', 'other'])),
    outcome: safeEnum(input.outcome, new Set(['response', 'failed', 'redirected'])),
    failure_reason: safeCausalText(input.failure_reason),
    status: safeNonNegativeNumber(input.status, true),
    duration_ms: safeNonNegativeNumber(input.duration_ms),
    content_type: safeContentType(contentType),
    content_length: safeNonNegativeNumber(contentLength, true),
    request_id: safeIdentifier(firstHeader(headers, REQUEST_ID_HEADERS)),
    trace_id: safeIdentifier(firstHeader(headers, TRACE_ID_HEADERS)),
    initiator_type: safeEnum(input.initiator_type, new Set(['parser', 'script', 'preload', 'signedexchange', 'preflight', 'other'])),
    initiator_stack: safeInitiatorStack(input.initiator_stack),
  };
}
