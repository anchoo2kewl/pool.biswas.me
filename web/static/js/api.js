/* Thin wrapper over the JSON API. Every call goes through here so error
 * handling and the session-expiry redirect live in one place. */

async function request(method, path, body, opts = {}) {
  const init = { method, headers: {}, credentials: 'same-origin' };
  if (body instanceof FormData) {
    init.body = body; // the browser sets the multipart boundary itself
  } else if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(path, init);
  } catch (e) {
    // A fetch that never completes surfaces as "Load failed" or "Failed to
    // fetch", which tells the person nothing. Reading a photographed sheet is
    // the one call here slow enough to be cut off in transit, so say that.
    throw new Error(
      'The server did not answer. If you were reading a test sheet, the model '
      + 'may have taken too long — try again, or enter the readings by hand.');
  }

  if (res.status === 401 && !opts.allowUnauthorized) {
    window.location.href = '/login';
    throw new Error('Your session has expired. Please sign in again.');
  }

  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch { data = { error: text }; }
  }
  if (!res.ok) {
    throw new Error(data?.error || `Request failed (${res.status})`);
  }
  return data;
}

export const api = {
  get: (p) => request('GET', p),
  post: (p, b) => request('POST', p, b),
  patch: (p, b) => request('PATCH', p, b),
  put: (p, b) => request('PUT', p, b),
  del: (p) => request('DELETE', p),
  upload: (p, form) => request('POST', p, form),

  config: () => request('GET', '/api/config', undefined, { allowUnauthorized: true }),
  me: () => request('GET', '/api/me'),
  logout: () => request('POST', '/api/auth/logout', {}),

  pools: () => request('GET', '/api/pools'),
  createPool: (b) => request('POST', '/api/pools', b),
  updatePool: (id, b) => request('PATCH', `/api/pools/${id}`, b),
  deletePool: (id) => request('DELETE', `/api/pools/${id}`),

  tests: (params) => request('GET', `/api/tests?${new URLSearchParams(params)}`),
  test: (id) => request('GET', `/api/tests/${id}`),
  createTest: (b) => request('POST', '/api/tests', b),
  createTestFromPhoto: (form) => request('POST', '/api/tests/from-photo', form),
  updateTest: (id, b) => request('PATCH', `/api/tests/${id}`, b),
  deleteTest: (id) => request('DELETE', `/api/tests/${id}`),
  insight: (id) => request('POST', `/api/tests/${id}/insight`, {}),
  markTreatment: (id, applied) => request('POST', `/api/treatments/${id}/applied`, { applied }),

  notes: (params) => request('GET', `/api/notes?${new URLSearchParams(params)}`),
  createNote: (b) => request('POST', '/api/notes', b),
  deleteNote: (id, poolID) => request('DELETE', `/api/notes/${id}?pool_id=${poolID}`),

  seasons: (poolID) => request('GET', `/api/seasons?pool_id=${poolID}`),
  createSeason: (b) => request('POST', '/api/seasons', b),
  updateSeason: (id, b) => request('PATCH', `/api/seasons/${id}`, b),
  deleteSeason: (id, poolID) => request('DELETE', `/api/seasons/${id}?pool_id=${poolID}`),

  log: (params) => request('GET', `/api/log?${new URLSearchParams(params)}`),
  createLog: (b) => request('POST', '/api/log', b),
  updateLog: (id, b) => request('PATCH', `/api/log/${id}`, b),
  deleteLog: (id) => request('DELETE', `/api/log/${id}`),

  companies: () => request('GET', '/api/companies'),
  createCompany: (b) => request('POST', '/api/companies', b),

  attachments: (params) => request('GET', `/api/attachments?${new URLSearchParams(params)}`),
  deleteAttachment: (id) => request('DELETE', `/api/attachments/${id}`),
  linkAttachment: (id, b) => request('POST', `/api/attachments/${id}/link`, b),
  parseAttachment: (id, b) => request('POST', `/api/attachments/${id}/parse`, b || {}),

  keys: () => request('GET', '/api/keys'),
  createKey: (b) => request('POST', '/api/keys', b),
  revokeKey: (id) => request('DELETE', `/api/keys/${id}`),
  setAI: (b) => request('PUT', '/api/me/ai', b),
  aiProviders: () => request('GET', '/api/me/ai/providers'),
  setAIProvider: (b) => request('PUT', '/api/me/ai/providers', b),
  deleteAIProvider: (kind, slot) => request('DELETE', `/api/me/ai/providers/${kind}/${slot}`),
  aiBalance: () => request('GET', '/api/me/ai/balance'),

  summary: (poolID) => request('GET', `/api/analytics/summary?pool_id=${poolID}`),
  costs: (params) => request('GET', `/api/analytics/costs?${new URLSearchParams(params)}`),
  trends: (params) => request('GET', `/api/analytics/trends?${new URLSearchParams(params)}`),
};

/* ── Toasts ──────────────────────────────────────────────────────────── */

let toastHost = null;
export function toast(message, kind = '') {
  if (!toastHost) {
    toastHost = document.createElement('div');
    toastHost.className = 'toasts';
    document.body.appendChild(toastHost);
  }
  const t = document.createElement('div');
  t.className = `toast ${kind}`;
  t.textContent = message;
  toastHost.appendChild(t);
  setTimeout(() => {
    t.style.opacity = '0';
    t.style.transition = 'opacity 200ms';
    setTimeout(() => t.remove(), 220);
  }, kind === 'err' ? 6000 : 3200);
}
