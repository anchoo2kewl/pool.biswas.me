/* The application shell: pool selector, view routing, and each view's render.
 * State is deliberately a plain object — the app is small enough that a
 * framework would cost more than it saves. */

import { api, toast } from './api.js';
import { lineChart, barChart, areaChart, lsiGauge, cadenceStrip, legend, seriesColor, statusColor, fmt, escapeHtml, onResize } from './charts.js';

const state = {
  user: null,
  config: {},
  pools: [],
  pool: null,
  view: 'overview',
  summary: null,
  trends: null,
  costs: null,
  seasons: [],
  logEntries: [],
  tests: [],
  focusParam: 'ph',
  seasonFilter: 0,
};

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

/* ── Boot ────────────────────────────────────────────────────────────── */

async function boot() {
  try {
    const [cfg, me] = await Promise.all([api.config(), api.me()]);
    state.config = cfg;
    state.user = me.user;
    state.ai = me.ai;
    state.providers = me.providers || [];
  } catch (e) {
    window.location.href = '/login';
    return;
  }

  $('#user-email').textContent = state.user.name || state.user.email;
  if (state.config.demo_enabled && state.user.email === state.config.demo_email) showDemoBanner();
  await loadPools();
  wireChrome();
  onResize(() => render());
}

async function loadPools() {
  state.pools = await api.pools();
  const sel = $('#pool-select');
  if (!state.pools.length) {
    sel.innerHTML = '<option>No pools yet</option>';
    renderFirstRun();
    return;
  }
  const saved = Number(localStorage.getItem('pool_id'));
  state.pool = state.pools.find(p => p.id === saved) || state.pools[0];
  sel.innerHTML = state.pools.map(p =>
    `<option value="${p.id}" ${p.id === state.pool.id ? 'selected' : ''}>${escapeHtml(p.name)}</option>`).join('');
  await loadPool();
}

async function loadPool() {
  if (!state.pool) return;
  localStorage.setItem('pool_id', state.pool.id);
  setLoading(true);
  try {
    const [summary, seasons] = await Promise.all([
      api.summary(state.pool.id),
      api.seasons(state.pool.id),
    ]);
    state.summary = summary;
    state.seasons = seasons;
    // The summary is deliberately lean; notes and attachments come from the
    // test's own detail endpoint.
    state.detail = summary.latest_test ? await api.test(summary.latest_test.id).catch(() => null) : null;
    render();
  } catch (e) {
    toast(e.message, 'err');
  } finally {
    setLoading(false);
  }
}

function setLoading(on) {
  $('#loading').classList.toggle('hidden', !on);
}

function wireChrome() {
  $('#pool-select').addEventListener('change', async e => {
    state.pool = state.pools.find(p => p.id === Number(e.target.value));
    state.trends = state.costs = null;
    await loadPool();
  });
  $$('.tab[data-view]').forEach(t => t.addEventListener('click', () => switchView(t.dataset.view)));
  $('#sign-out').addEventListener('click', async () => {
    await api.logout();
    window.location.href = '/';
  });
  $('#add-test').addEventListener('click', () => openTestForm());
  $('#add-sheet').addEventListener('click', () => {
    // Reachable on first run, before there is a pool for a test to belong to.
    if (!state.pool) return toast('Add your pool first', 'err');
    openPhotoTestForm();
  });
  $('#add-log').addEventListener('click', () => openLogForm());
}

async function switchView(view) {
  state.view = view;
  $$('.tab[data-view]').forEach(t => t.classList.toggle('active', t.dataset.view === view));
  render();
  // Views load their data lazily, so switching stays instant.
  if (view === 'trends' && !state.trends) {
    state.trends = await api.trends({ pool_id: state.pool.id });
    render();
  }
  if (view === 'costs' && !state.costs) {
    state.costs = await api.costs({ pool_id: state.pool.id, season_id: state.seasonFilter || '' });
    render();
  }
  if (view === 'logbook' && !state.logEntries.length) {
    state.logEntries = await api.log({ pool_id: state.pool.id, limit: 300 });
    render();
  }
  if (view === 'tests' && !state.tests.length) {
    state.tests = await api.tests({ pool_id: state.pool.id, limit: 100 });
    render();
  }
}

/* ── Render ──────────────────────────────────────────────────────────── */

function render() {
  const root = $('#view');
  if (!state.pool) { renderFirstRun(); return; }
  switch (state.view) {
    case 'overview': renderOverview(root); break;
    case 'trends': renderTrends(root); break;
    case 'logbook': renderLogbook(root); break;
    case 'costs': renderCosts(root); break;
    case 'tests': renderTests(root); break;
    case 'settings': renderSettings(root); break;
  }
}

function renderFirstRun() {
  $('#view').innerHTML = `
    <div class="glass card card-pad-lg" style="max-width:560px;margin:2rem auto">
      <h2>Add your pool</h2>
      <p class="muted small">Everything — tests, costs, receipts — hangs off a pool. This takes about thirty seconds.</p>
      <div id="first-pool-form"></div>
    </div>`;
  renderPoolForm($('#first-pool-form'));
}

/* ── Overview ────────────────────────────────────────────────────────── */

function renderOverview(root) {
  const s = state.summary || {};
  const t = s.latest_test;
  const readings = s.readings || [];
  const alerts = s.alerts || [];

  if (!t) {
    root.innerHTML = `
      <div class="glass card card-pad-lg center" style="max-width:520px;margin:2rem auto">
        <div class="empty">
          <div class="big">🧪</div>
          <h3 style="margin-bottom:.5rem">No water tests yet</h3>
          <p class="muted small">Add your first test — from a strip, a kit, or a printout from the pool store — and the dashboard fills in. Photograph the printout and the readings transcribe themselves.</p>
          <div class="row" style="justify-content:center;gap:.5rem">
            <button class="btn btn-primary" onclick="window.__openPhotoTestForm()">📷 Upload a test sheet</button>
            <button class="btn" onclick="window.__openTestForm()">Enter one by hand</button>
          </div>
        </div>
      </div>`;
    return;
  }

  const score = t.score ?? 0;
  const scoreStatus = score >= 85 ? 'good' : score >= 60 ? 'warning' : 'serious';
  const pending = s.pending_treatments || 0;
  const seasonSpend = s.season_spend_cents || 0;
  const lifetime = s.lifetime_spend_cents || 0;

  root.innerHTML = `
    <div class="grid grid-4" style="margin-bottom:1rem">
      <div class="glass stat">
        <div class="label">Water quality</div>
        <div class="value">${score}<span class="unit">/100</span></div>
        <div class="sub"><span class="pill pill-${scoreStatus}"><span class="dot"></span>${scoreStatus === 'good' ? 'Healthy' : scoreStatus === 'warning' ? 'Needs attention' : 'Action required'}</span></div>
      </div>
      <div class="glass stat">
        <div class="label">Last tested</div>
        <div class="value" style="font-size:1.25rem">${fmt.date(t.tested_at)}</div>
        <div class="sub">${escapeHtml(t.company_name || t.operator || 'Self-tested')}${daysAgo(t.tested_at)}</div>
      </div>
      <div class="glass stat">
        <div class="label">Season spend</div>
        <div class="value">${fmt.money(seasonSpend)}</div>
        <div class="sub">${s.current_season ? escapeHtml(s.current_season.name) : 'No season open'} · ${s.season_entry_count || 0} entries</div>
      </div>
      <div class="glass stat">
        <div class="label">Lifetime spend</div>
        <div class="value">${fmt.money(lifetime)}</div>
        <div class="sub">${s.cost_per_10k_l_cents ? `${fmt.money(s.cost_per_10k_l_cents)} per 10,000 L` : `${s.lifetime_entry_count || 0} entries`}</div>
      </div>
    </div>

    <div class="grid overview-split" style="margin-bottom:1rem" id="overview-main">
      <div class="stack">
        <div class="glass card">
          <div class="card-head">
            <h3>Water balance</h3>
            <span class="sub">Saturation index — the single number for corrosive vs scaling</span>
          </div>
          <div id="lsi-gauge"></div>
          ${s.lsi_verdict ? `<p class="small muted" style="margin:.5rem 0 0">${escapeHtml(s.lsi_verdict)}</p>` : ''}
        </div>

        <div class="glass card">
          <div class="card-head">
            <h3>Readings</h3>
            <span class="sub">${readings.filter(r => r.status !== 'unknown').length} of ${readings.length} tested</span>
          </div>
          <div class="readings">${readings.map(readingTile).join('')}</div>
        </div>
      </div>

      <div class="stack">
        ${alerts.length ? `
        <div class="glass card">
          <div class="card-head"><h3>What this means</h3></div>
          ${alerts.map(a => `
            <div class="alert-item">
              <div class="ico ${a.severity}">${a.severity === 'good' ? '✓' : '!'}</div>
              <div><div class="t">${escapeHtml(a.title)}</div><div class="d">${escapeHtml(a.detail)}</div></div>
            </div>`).join('')}
        </div>` : ''}

        <div class="glass card">
          <div class="card-head">
            <h3>Treatment plan</h3>
            ${pending ? `<span class="pill pill-warning"><span class="dot"></span>${pending} to do</span>` : '<span class="pill pill-good"><span class="dot"></span>All done</span>'}
          </div>
          <div id="treatments"></div>
        </div>

        <div class="glass card">
          <div class="card-head">
            <h3>Notes &amp; analysis</h3>
            ${state.ai?.configured ? '' : '<span class="pill pill-unknown"><span class="dot"></span>AI not configured</span>'}
          </div>
          <div id="ai-panel"></div>
        </div>

        <div class="glass card">
          <div class="card-head">
            <h3>Test sheet</h3>
            <span class="sub">the printout this test came from</span>
          </div>
          <div id="sheet-panel"></div>
        </div>
      </div>
    </div>

    <div class="grid grid-2">
      <div class="glass card">
        <div class="card-head"><h3>Testing cadence</h3><span class="sub">tests per month</span></div>
        <div id="cadence"></div>
      </div>
      <div class="glass card">
        <div class="card-head"><h3>Recent activity</h3>
          <button class="btn btn-sm btn-ghost" onclick="window.__switchView('logbook')">View all</button></div>
        <div id="recent-log"></div>
      </div>
    </div>`;

  lsiGauge($('#lsi-gauge'), t.lsi ?? null);
  renderTreatments($('#treatments'), s.treatments || []);
  renderNotesPanel($('#ai-panel'), t);
  renderSheetPanel($('#sheet-panel'), t);
  cadenceStrip($('#cadence'), (state.summary?.cadence) || []);
  renderRecentLog($('#recent-log'), s.recent_log || []);

  // Cadence lives on the trends payload; fetch it quietly if absent.
  if (!state.summary?.cadence) {
    api.trends({ pool_id: state.pool.id, limit: 400 }).then(tr => {
      state.trends = tr;
      state.summary.cadence = tr.cadence;
      if (state.view === 'overview') cadenceStrip($('#cadence'), tr.cadence || []);
    }).catch(() => {});
  }
}

function readingTile(r) {
  const v = r.value;
  const has = v !== null && v !== undefined;
  const pos = has && r.ideal[1] > r.ideal[0]
    ? Math.max(0, Math.min(100, ((v - r.ideal[0]) / (r.ideal[1] - r.ideal[0])) * 100)) : null;
  // Temperature and TDS describe conditions rather than faults, so they are
  // shown without a status stripe — an amber bar on 21°C reads as a problem.
  const status = r.unscored ? 'neutral' : r.status;
  return `
    <div class="reading is-${status}">
      <div class="rl">${escapeHtml(r.label)}</div>
      <div class="rv">${has ? fmt.num(v) : '—'}${has && r.unit ? `<span class="u">${escapeHtml(r.unit)}</span>` : ''}</div>
      <div class="rr">ideal ${fmt.num(r.ideal[0])}–${fmt.num(r.ideal[1])}</div>
      ${has ? `<div class="range-bar">
        <div class="band" style="left:25%;width:50%"></div>
        <div class="marker" style="left:${25 + (pos ?? 50) * 0.5}%;background:${r.unscored ? 'var(--fg-muted)' : statusColor(r.status)}"></div>
      </div>` : ''}
    </div>`;
}

function renderTreatments(el, treatments) {
  if (!treatments.length) {
    el.innerHTML = '<div class="empty small">Nothing to add — the water is in range.</div>';
    return;
  }
  el.innerHTML = treatments.map(t => `
    <div class="alert-item">
      <input type="checkbox" ${t.applied ? 'checked' : ''} data-treatment="${t.id}" style="margin-top:3px">
      <div style="min-width:0">
        <div class="t">${escapeHtml(displayAmount(t))} ${escapeHtml(t.product)}</div>
        <div class="d">${escapeHtml(t.reason)}</div>
        ${t.note ? `<div class="d dim" style="margin-top:.25rem">${escapeHtml(t.note)}</div>` : ''}
        ${t.applied ? '' : `<button class="btn btn-sm" style="margin-top:.5rem" data-log-treatment="${t.id}">Log this purchase</button>`}
      </div>
    </div>`).join('');

  $$('[data-treatment]', el).forEach(cb => cb.addEventListener('change', async () => {
    try {
      await api.markTreatment(Number(cb.dataset.treatment), cb.checked);
      toast(cb.checked ? 'Marked as applied' : 'Marked as not applied', 'ok');
    } catch (e) { toast(e.message, 'err'); cb.checked = !cb.checked; }
  }));
  $$('[data-log-treatment]', el).forEach(b => b.addEventListener('click', () => {
    const t = treatments.find(x => x.id === Number(b.dataset.logTreatment));
    openLogForm({ item: t.product, category: 'chemical', quantity: t.amount, unit: t.unit });
  }));
}

function displayAmount(t) {
  if (t.amount === null || t.amount === undefined) return '';
  if (t.unit === 'g' && t.amount >= 1000) return `${(t.amount / 1000).toFixed(2)} kg`;
  if (t.unit === 'ml' && t.amount >= 1000) return `${(t.amount / 1000).toFixed(2)} L`;
  return `${fmt.num(t.amount)} ${t.unit}`;
}

function renderNotesPanel(el, test) {
  const notes = (state.detail?.notes) || [];
  el.innerHTML = `
    <div id="notes-list" class="stack" style="gap:.6rem">
      ${notes.length ? notes.map(renderNote).join('')
        : `<p class="small muted" style="margin:0">No notes yet. Add what you observed, or let the AI read the history and the logbook and explain what changed.</p>`}
    </div>
    <div class="field" style="margin-top:.85rem">
      <label for="note-body">Add a note</label>
      <textarea id="note-body" placeholder="Water was cloudy after the storm; brushed the walls and ran the pump overnight." style="min-height:64px"></textarea>
    </div>
    <div class="row" style="gap:.5rem;margin-top:.5rem">
      <button class="btn btn-sm" id="save-note">Save note</button>
      <div class="spacer"></div>
      <button class="btn btn-sm btn-primary" id="gen-insight">
        ${notes.some(n => n.kind === 'ai') ? 'Re-analyse' : 'Analyse with AI'}
      </button>
    </div>`;

  $('#save-note', el).addEventListener('click', async () => {
    const body = $('#note-body', el).value.trim();
    if (!body) return toast('Write something first', 'err');
    try {
      await api.createNote({ pool_id: state.pool.id, test_id: test.id, body });
      toast('Note saved', 'ok');
      state.detail = await api.test(test.id);
      renderNotesPanel(el, test);
    } catch (e) { toast(e.message, 'err'); }
  });

  $('#gen-insight', el).addEventListener('click', async () => {
    const btn = $('#gen-insight', el);
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span> Analysing — up to a minute…';
    try {
      await api.insight(test.id);
      state.detail = await api.test(test.id);
      renderNotesPanel(el, test);
      toast('Analysis complete', 'ok');
    } catch (e) {
      toast(e.message, 'err');
      btn.disabled = false;
      btn.textContent = 'Analyse with AI';
    }
  });

  $$('[data-del-note]', el).forEach(b => b.addEventListener('click', async () => {
    if (!confirm('Delete this note?')) return;
    try {
      await api.deleteNote(Number(b.dataset.delNote), state.pool.id);
      state.detail = await api.test(test.id);
      renderNotesPanel(el, test);
      toast('Note deleted', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
}

// The scanned or photographed analysis sheet a test came from.
function renderSheetPanel(el, test) {
  const sheets = (state.detail?.attachments) || [];
  el.innerHTML = `
    ${sheets.length ? `<div class="thumb-grid" style="margin-bottom:.75rem">
      ${sheets.map(a => `
        <a href="/api/attachments/${a.id}/file" target="_blank" rel="noopener" title="${escapeHtml(a.filename)}">
          ${a.content_type.startsWith('image/')
            ? `<img class="thumb" src="/api/attachments/${a.id}/file" alt="${escapeHtml(a.filename)}" loading="lazy">`
            : `<div class="thumb" style="display:grid;place-items:center;font-size:.75rem;color:var(--fg-muted)">${escapeHtml(a.filename)}</div>`}
        </a>`).join('')}
    </div>` : `<p class="small muted" style="margin:0 0 .75rem">Photograph the printout and keep it with the readings — useful when a number looks wrong months later.</p>`}
    <input type="file" id="sheet-file" accept="image/*,application/pdf" style="display:none">
    <button class="btn btn-sm btn-block" id="sheet-upload">${sheets.length ? 'Add another photo' : 'Attach the test sheet'}</button>
    ${sheets.some(a => a.content_type.startsWith('image/')) ? `
      <button class="btn btn-sm btn-block" style="margin-top:.4rem" id="sheet-parse"
        data-id="${sheets.find(a => a.content_type.startsWith('image/')).id}">
        Read this sheet into a new test</button>
      <span class="hint">Extracts the readings from the stored image — useful for a sheet filed before this existed.</span>` : ''}`;

  $('#sheet-upload', el).addEventListener('click', () => $('#sheet-file', el).click());

  const parseBtn = $('#sheet-parse', el);
  if (parseBtn) {
    parseBtn.addEventListener('click', async () => {
      parseBtn.disabled = true;
      parseBtn.innerHTML = '<span class="spinner"></span> Reading the sheet…';
      try {
        const detail = await api.parseAttachment(Number(parseBtn.dataset.id));
        toast('Read into a new test', 'ok');
        state.tests = []; state.trends = null;
        await loadPool();
        modal('Read from the stored sheet', '<div class="stack"></div>', (body, close) => {
          renderPhotoResult(body, detail, close);
        });
      } catch (e) {
        toast(e.message, 'err');
        parseBtn.disabled = false;
        parseBtn.textContent = 'Read this sheet into a new test';
      }
    });
  }
  $('#sheet-file', el).addEventListener('change', async e => {
    const file = e.target.files[0];
    if (!file) return;
    const btn = $('#sheet-upload', el);
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span> Uploading…';
    const form = new FormData();
    form.append('file', file);
    form.append('pool_id', state.pool.id);
    form.append('test_id', test.id);
    form.append('kind', 'test_sheet');
    try {
      const rec = await api.upload('/api/attachments', form);
      const saved = rec.original_bytes && rec.size_bytes < rec.original_bytes
        ? ` — ${Math.round((1 - rec.size_bytes / rec.original_bytes) * 100)}% smaller` : '';
      toast(`Sheet attached${saved}`, 'ok');
      state.detail = await api.test(test.id);
      renderSheetPanel(el, test);
    } catch (err) {
      toast(err.message, 'err');
      btn.disabled = false;
      btn.textContent = 'Attach the test sheet';
    }
  });
}

function renderNote(n) {
  return `<div class="note ${n.kind === 'ai' ? 'ai' : ''}">
    <div class="note-head">
      <span class="pill ${n.kind === 'ai' ? 'pill-accent' : 'pill-unknown'}"><span class="dot"></span>${n.kind === 'ai' ? 'AI' : 'Note'}</span>
      <span>${fmt.date(n.created_at)}</span>
      ${n.model ? `<span class="dim">${escapeHtml(n.model)}</span>` : ''}
      <div class="spacer"></div>
      <button class="btn btn-sm btn-ghost btn-danger" data-del-note="${n.id}" title="Delete note" style="min-height:24px;padding:0 .4rem">×</button>
    </div>
    <div class="body">${markdownish(n.body)}</div>
  </div>`;
}

/* Just enough markdown for what the model returns: bold and lists. */
function markdownish(s) {
  return escapeHtml(s)
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/^- /gm, '• ');
}

function renderRecentLog(el, entries) {
  if (!entries.length) {
    el.innerHTML = `<div class="empty small">Nothing logged yet.<br><button class="btn btn-sm" style="margin-top:.75rem" onclick="window.__openLogForm()">Log a purchase</button></div>`;
    return;
  }
  el.innerHTML = entries.map(e => `
    <div class="row" style="justify-content:space-between;padding:.5rem 0;border-bottom:1px solid rgba(255,255,255,.05);gap:.5rem">
      <div style="min-width:0">
        <div class="small" style="font-weight:560;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${escapeHtml(e.item)}</div>
        <div class="small dim">${fmt.date(e.occurred_on)} · ${escapeHtml(e.category)}${e.quantity ? ` · ${fmt.num(e.quantity)} ${escapeHtml(e.unit)}` : ''}</div>
      </div>
      <div class="mono small nowrap" style="font-weight:600">${e.cost_cents ? fmt.money(e.cost_cents, e.currency) : '—'}</div>
    </div>`).join('');
}

function daysAgo(iso) {
  const d = Math.floor((Date.now() - new Date(iso).getTime()) / 86400000);
  if (d <= 0) return ' · today';
  return ` · ${d} day${d === 1 ? '' : 's'} ago`;
}

/* ── Trends ──────────────────────────────────────────────────────────── */

function renderTrends(root) {
  const tr = state.trends;
  if (!tr) { root.innerHTML = skeletonCards(3); return; }

  const series = tr.series.filter(s => s.points.some(p => p.value !== null && p.value !== undefined));
  if (!series.length) {
    root.innerHTML = `<div class="glass card empty">Add a couple of tests and the trends appear here.</div>`;
    return;
  }

  const events = (tr.events || []).map(e => ({ at: e.occurred_on, label: `${e.item}${e.quantity ? ` ${fmt.num(e.quantity)}${e.unit}` : ''}` }));

  root.innerHTML = `
    <div class="glass card" style="margin-bottom:1rem">
      <div class="card-head">
        <h3>Parameter</h3>
        <span class="sub">Vertical marks show logbook entries — what went into the pool</span>
      </div>
      <div class="tabs" id="param-tabs">
        ${series.map(s => `<button class="tab ${s.key === state.focusParam ? 'active' : ''}" data-param="${s.key}">${escapeHtml(s.label)}</button>`).join('')}
      </div>
    </div>
    <div class="glass card" style="margin-bottom:1rem">
      <div class="card-head"><h3 id="focus-title"></h3><span class="sub" id="focus-sub"></span></div>
      <div id="focus-chart"></div>
      <div class="legend" id="focus-legend"></div>
    </div>
    <div class="grid grid-2" id="small-multiples"></div>`;

  $$('#param-tabs .tab').forEach(t => t.addEventListener('click', () => {
    state.focusParam = t.dataset.param;
    renderTrends(root);
  }));

  const focus = series.find(s => s.key === state.focusParam) || series[0];
  state.focusParam = focus.key;
  $('#focus-title').textContent = focus.label;
  $('#focus-sub').textContent = focus.group === 'derived'
    ? 'derived from the other readings'
    : `ideal ${fmt.num(focus.ideal[0])}–${fmt.num(focus.ideal[1])} ${focus.unit}`;

  lineChart($('#focus-chart'), {
    points: focus.points, label: focus.label, unit: focus.unit,
    ideal: focus.group === 'derived' && focus.key === 'score' ? null : focus.ideal,
    events, height: 260, color: seriesColor(0),
    dp: focus.key === 'lsi' ? 2 : undefined,
  });
  legend($('#focus-legend'), [
    { color: seriesColor(0), label: focus.label },
    { color: 'rgba(12,163,12,.35)', label: 'ideal range' },
    { color: 'rgba(255,255,255,.18)', label: 'logbook entry' },
  ]);

  // Small multiples for everything else — same scale rules, no dual axes.
  const others = series.filter(s => s.key !== focus.key).slice(0, 8);
  $('#small-multiples').innerHTML = others.map(s => `
    <div class="glass card">
      <div class="card-head"><h3>${escapeHtml(s.label)}</h3>
        <span class="sub">${s.group === 'derived' ? '' : `${fmt.num(s.ideal[0])}–${fmt.num(s.ideal[1])} ${escapeHtml(s.unit)}`}</span></div>
      <div id="sm-${s.key}"></div>
    </div>`).join('');
  others.forEach((s, i) => lineChart($(`#sm-${s.key}`), {
    points: s.points, label: s.label, unit: s.unit,
    ideal: s.key === 'score' ? null : s.ideal,
    height: 150, color: seriesColor(i + 1), dp: s.key === 'lsi' ? 2 : undefined,
  }));
}

/* ── Costs ───────────────────────────────────────────────────────────── */

function renderCosts(root) {
  const c = state.costs;
  if (!c) { root.innerHTML = skeletonCards(4); return; }

  const cumulative = (c.cumulative || []).map(p => ({
    label: fmt.month(p.month), fullLabel: fmt.month(p.month), value: p.cumulative_cents / 100,
    sub: `${fmt.money(p.total_cents)} that month`,
  }));

  root.innerHTML = `
    <div class="glass card" style="margin-bottom:1rem">
      <div class="row">
        <div class="field" style="min-width:200px">
          <label for="season-filter">Season</label>
          <select id="season-filter">
            <option value="0">All time</option>
            ${state.seasons.map(s => `<option value="${s.id}" ${state.seasonFilter === s.id ? 'selected' : ''}>${escapeHtml(s.name)} (${fmt.money(s.total_cents)})</option>`).join('')}
          </select>
        </div>
        <div class="spacer"></div>
        <button class="btn btn-sm" id="new-season">New season</button>
        <button class="btn btn-sm btn-primary" onclick="window.__openLogForm()">Log a cost</button>
      </div>
    </div>

    <div class="grid grid-3" style="margin-bottom:1rem">
      <div class="glass stat">
        <div class="label">Total spend</div>
        <div class="value">${fmt.money(c.total_cents)}</div>
        <div class="sub">${c.entry_count} entries</div>
      </div>
      <div class="glass stat">
        <div class="label">Biggest category</div>
        <div class="value" style="font-size:1.35rem">${c.by_category?.[0] ? escapeHtml(titleCase(c.by_category[0].category)) : '—'}</div>
        <div class="sub">${c.by_category?.[0] ? fmt.money(c.by_category[0].total_cents) : 'nothing logged'}</div>
      </div>
      <div class="glass stat">
        <div class="label">Average per month</div>
        <div class="value">${c.by_month?.length ? fmt.money(Math.round(c.total_cents / c.by_month.length)) : '—'}</div>
        <div class="sub">over ${c.by_month?.length || 0} month${c.by_month?.length === 1 ? '' : 's'}</div>
      </div>
    </div>

    <div class="grid grid-2" style="margin-bottom:1rem">
      <div class="glass card span-2">
        <div class="card-head"><h3>Running total</h3><span class="sub">what this pool has cost, cumulatively</span></div>
        <div id="cost-cumulative"></div>
      </div>
      <div class="glass card">
        <div class="card-head"><h3>Spend by month</h3></div>
        <div id="cost-month"></div>
      </div>
      <div class="glass card">
        <div class="card-head"><h3>Where the money went</h3></div>
        <div id="cost-category"></div>
      </div>
      <div class="glass card">
        <div class="card-head"><h3>Top items</h3></div>
        <div id="cost-item"></div>
      </div>
      <div class="glass card">
        <div class="card-head"><h3>Season over season</h3></div>
        <div id="cost-season"></div>
      </div>
    </div>`;

  $('#season-filter').addEventListener('change', async e => {
    state.seasonFilter = Number(e.target.value);
    state.costs = await api.costs({ pool_id: state.pool.id, season_id: state.seasonFilter || '' });
    render();
  });
  $('#new-season').addEventListener('click', openSeasonForm);

  areaChart($('#cost-cumulative'), {
    points: cumulative, height: 220, color: seriesColor(0),
    valueFmt: v => fmt.money(v * 100), tickFmt: v => fmt.money(v * 100),
    emptyText: 'Log a purchase and the running total starts here.',
  });
  barChart($('#cost-month'), {
    bars: (c.by_month || []).map(m => ({ label: fmt.month(m.month), value: m.total_cents / 100, color: seriesColor(0), sub: `${m.count} entries` })),
    height: 200, valueFmt: v => fmt.money(v * 100), tickFmt: v => fmt.money(v * 100),
    emptyText: 'No spending logged yet.', ariaLabel: 'spend by month',
  });
  barChart($('#cost-category'), {
    horizontal: true,
    bars: (c.by_category || []).map((x, i) => ({ label: titleCase(x.category), value: x.total_cents / 100, color: seriesColor(i), sub: `${x.count} entries` })),
    valueFmt: v => fmt.money(v * 100), emptyText: 'No spending logged yet.',
  });
  barChart($('#cost-item'), {
    horizontal: true,
    bars: (c.by_item || []).slice(0, 8).map(x => ({
      label: x.item, value: x.total_cents / 100, color: seriesColor(0),
      sub: x.quantity ? `${fmt.num(x.quantity)} ${x.unit} across ${x.count}` : `${x.count} purchases`,
    })),
    valueFmt: v => fmt.money(v * 100), emptyText: 'No spending logged yet.',
  });
  barChart($('#cost-season'), {
    bars: (c.seasons || []).map(s => ({
      label: s.name, short: s.name.replace(/^Season\s+/i, ''),
      value: s.total_cents / 100, color: seriesColor(0), sub: `${s.entry_count} entries`,
    })),
    height: 200, valueFmt: v => fmt.money(v * 100), tickFmt: v => fmt.money(v * 100),
    emptyText: 'Create a season to compare year over year.', ariaLabel: 'spend by season',
  });
}

function titleCase(s) { return String(s || '').replace(/\b\w/g, c => c.toUpperCase()); }

/* ── Logbook ─────────────────────────────────────────────────────────── */

function renderLogbook(root) {
  const entries = state.logEntries;
  root.innerHTML = `
    <div class="glass card" style="margin-bottom:1rem">
      <div class="row">
        <h3 style="margin:0">Logbook</h3>
        <span class="small muted">Everything that went into or onto the pool</span>
        <div class="spacer"></div>
        <button class="btn btn-sm" id="upload-receipt">Upload receipt</button>
        <button class="btn btn-sm btn-primary" onclick="window.__openLogForm()">Add entry</button>
      </div>
    </div>
    <div class="glass card">
      ${entries.length ? `
      <div class="table-wrap">
        <table>
          <thead><tr>
            <th>Date</th><th>Category</th><th>Item</th><th class="num">Qty</th>
            <th>Vendor</th><th class="num">Cost</th><th></th>
          </tr></thead>
          <tbody>${entries.map(e => `
            <tr>
              <td class="nowrap mono small">${fmt.date(e.occurred_on)}</td>
              <td><span class="pill pill-unknown"><span class="dot"></span>${escapeHtml(e.category)}</span></td>
              <td>${escapeHtml(e.item)}${e.notes ? `<div class="small dim">${escapeHtml(e.notes)}</div>` : ''}</td>
              <td class="num">${e.quantity ? `${fmt.num(e.quantity)} ${escapeHtml(e.unit)}` : '—'}</td>
              <td class="small">${escapeHtml(e.vendor || e.company_name || '—')}</td>
              <td class="num">${e.cost_cents ? fmt.money(e.cost_cents, e.currency) : '—'}</td>
              <td class="nowrap">
                <button class="btn btn-sm btn-ghost" data-edit-log="${e.id}">Edit</button>
                <button class="btn btn-sm btn-ghost btn-danger" data-del-log="${e.id}">×</button>
              </td>
            </tr>`).join('')}
          </tbody>
        </table>
      </div>
      <div class="row" style="margin-top:1rem;justify-content:flex-end">
        <strong class="mono">Total: ${fmt.money(entries.reduce((a, e) => a + e.cost_cents, 0))}</strong>
      </div>` : `<div class="empty">
        <div class="big">📒</div>
        <h3 style="margin-bottom:.5rem">Nothing logged yet</h3>
        <p class="muted small">Log chemicals, filters, salt cells, service calls — anything you spend on the pool.<br>You can backdate entries to catch up on the season.</p>
        <button class="btn btn-primary" onclick="window.__openLogForm()">Add your first entry</button>
      </div>`}
    </div>`;

  $('#upload-receipt').addEventListener('click', openUploadForm);
  $$('[data-del-log]').forEach(b => b.addEventListener('click', async () => {
    if (!confirm('Delete this entry?')) return;
    try {
      await api.deleteLog(Number(b.dataset.delLog));
      state.logEntries = await api.log({ pool_id: state.pool.id, limit: 300 });
      state.costs = null;
      render();
      toast('Entry deleted', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
  $$('[data-edit-log]').forEach(b => b.addEventListener('click', () => {
    openLogForm(entries.find(e => e.id === Number(b.dataset.editLog)));
  }));
}

/* ── Tests list ──────────────────────────────────────────────────────── */

function renderTests(root) {
  const tests = state.tests;
  root.innerHTML = `
    <div class="glass card" style="margin-bottom:1rem">
      <div class="row">
        <h3 style="margin:0">Water tests</h3>
        <div class="spacer"></div>
        <button class="btn btn-sm" onclick="window.__openPhotoTestForm()">📷 Upload a test sheet</button>
        <button class="btn btn-sm btn-primary" onclick="window.__openTestForm()">Add test</button>
      </div>
    </div>
    <div class="glass card">
      ${tests.length ? `<div class="table-wrap"><table>
        <thead><tr>
          <th>Date</th><th class="num">Score</th><th class="num">LSI</th><th class="num">FC</th>
          <th class="num">pH</th><th class="num">TA</th><th class="num">CH</th><th class="num">CYA</th>
          <th class="num">Salt</th><th>Tested by</th><th></th>
        </tr></thead>
        <tbody>${tests.map(t => `
          <tr>
            <td class="nowrap mono small">${fmt.date(t.tested_at)}</td>
            <td class="num">${t.score ?? '—'}</td>
            <td class="num">${t.lsi !== null && t.lsi !== undefined ? (t.lsi > 0 ? '+' : '') + t.lsi.toFixed(2) : '—'}</td>
            <td class="num">${fmt.num(t.free_chlorine)}</td>
            <td class="num">${fmt.num(t.ph)}</td>
            <td class="num">${fmt.num(t.total_alkalinity)}</td>
            <td class="num">${fmt.num(t.calcium_hardness)}</td>
            <td class="num">${fmt.num(t.cyanuric_acid)}</td>
            <td class="num">${fmt.num(t.total_salt)}</td>
            <td class="small">${escapeHtml(t.company_name || t.operator || 'Self')}</td>
            <td><button class="btn btn-sm btn-ghost btn-danger" data-del-test="${t.id}">×</button></td>
          </tr>`).join('')}
        </tbody></table></div>`
      : `<div class="empty"><div class="big">🧪</div><p class="muted">No tests recorded yet.</p>
         <div class="row" style="justify-content:center;gap:.5rem">
           <button class="btn btn-primary" onclick="window.__openPhotoTestForm()">📷 Upload a test sheet</button>
           <button class="btn" onclick="window.__openTestForm()">Enter one by hand</button>
         </div></div>`}
    </div>`;

  $$('[data-del-test]').forEach(b => b.addEventListener('click', async () => {
    if (!confirm('Delete this test?')) return;
    try {
      await api.deleteTest(Number(b.dataset.delTest));
      state.tests = await api.tests({ pool_id: state.pool.id, limit: 100 });
      state.trends = null;
      await loadPool();
      toast('Test deleted', 'ok');
    } catch (e) { toast(e.message, 'err'); }
  }));
}

/* ── Settings ────────────────────────────────────────────────────────── */

async function renderSettings(root) {
  root.innerHTML = `
    <div class="grid grid-2">
      <div class="glass card">
        <div class="card-head"><h3>Pool</h3></div>
        <div id="pool-form"></div>
      </div>
      <div class="stack">
        <div class="glass card">
          <div class="card-head"><h3>AI insights</h3></div>
          <p class="small muted">Insights use any OpenAI-compatible endpoint — NVIDIA NIM, OpenRouter, OpenAI, or a local model. Your key is stored against your account and used only for your pools.</p>
          <div class="stack" style="gap:.75rem">
            <div class="field"><label for="ai-base">Base URL</label>
              <input id="ai-base" placeholder="https://integrate.api.nvidia.com/v1" value="${escapeHtml(state.ai?.base_url || '')}"></div>
            <div class="field"><label for="ai-model">Model</label>
              <input id="ai-model" placeholder="deepseek-ai/deepseek-v4-pro" value="${escapeHtml(state.ai?.model || '')}"></div>
            <div class="field"><label for="ai-key">API key</label>
              <input id="ai-key" type="password" placeholder="${state.ai?.using_own_key ? '•••••••• (saved)' : 'paste your key'}">
              <span class="hint">Leave blank to keep the current key. Clear it by saving a single space.</span></div>
            <button class="btn btn-primary" id="save-ai">Save AI settings</button>
          </div>
        </div>

        <div class="glass card">
          <div class="card-head"><h3>API keys</h3>
            <button class="btn btn-sm" id="new-key">New key</button></div>
          <p class="small muted">Everything in this app is available over the API — create a key and point a script or an agent at it. <a href="/docs" target="_blank">Read the API docs</a>.</p>
          <div id="keys-list"></div>
        </div>

        <div class="glass card">
          <div class="card-head"><h3>Seasons</h3>
            <button class="btn btn-sm" id="add-season">Add season</button></div>
          <div id="seasons-list"></div>
        </div>

        <div class="glass card">
          <div class="card-head"><h3>Account</h3></div>
          <p class="small muted">${escapeHtml(state.user.email)}${state.providers?.length ? ` · signed in with ${state.providers.join(', ')}` : ''}</p>
        </div>
      </div>
    </div>`;

  renderPoolForm($('#pool-form'), state.pool);
  renderSeasonsList($('#seasons-list'));

  $('#save-ai').addEventListener('click', async () => {
    const key = $('#ai-key').value;
    try {
      await api.setAI({
        api_key: key === '' ? (state.ai?.using_own_key ? undefined : '') : key.trim(),
        base_url: $('#ai-base').value.trim(),
        model: $('#ai-model').value.trim(),
      });
      toast('AI settings saved', 'ok');
      const me = await api.me();
      state.ai = me.ai;
    } catch (e) { toast(e.message, 'err'); }
  });

  $('#add-season').addEventListener('click', openSeasonForm);
  $('#new-key').addEventListener('click', openKeyForm);
  await refreshKeys();
}

async function refreshKeys() {
  const el = $('#keys-list');
  if (!el) return;
  try {
    const keys = await api.keys();
    el.innerHTML = keys.length ? keys.map(k => `
      <div class="row" style="justify-content:space-between;padding:.5rem 0;border-bottom:1px solid rgba(255,255,255,.05)">
        <div style="min-width:0">
          <div class="small" style="font-weight:560">${escapeHtml(k.name)} ${k.revoked_at ? '<span class="pill pill-unknown"><span class="dot"></span>revoked</span>' : ''}</div>
          <div class="small dim mono">${escapeHtml(k.prefix)}… · ${escapeHtml(k.scopes || 'read')} · ${k.last_used_at ? `last used ${fmt.date(k.last_used_at)}` : 'never used'}</div>
        </div>
        ${k.revoked_at ? '' : `<button class="btn btn-sm btn-ghost btn-danger" data-revoke="${k.id}">Revoke</button>`}
      </div>`).join('') : '<div class="empty small">No API keys yet.</div>';
    $$('[data-revoke]', el).forEach(b => b.addEventListener('click', async () => {
      if (!confirm('Revoke this key? Anything using it will stop working.')) return;
      await api.revokeKey(Number(b.dataset.revoke));
      toast('Key revoked', 'ok');
      refreshKeys();
    }));
  } catch (e) { el.innerHTML = `<div class="small err">${escapeHtml(e.message)}</div>`; }
}

function renderSeasonsList(el) {
  if (!state.seasons.length) { el.innerHTML = '<div class="empty small">No seasons yet. A season groups costs between opening and closing the pool.</div>'; return; }
  el.innerHTML = state.seasons.map(s => `
    <div class="row" style="justify-content:space-between;padding:.5rem 0;border-bottom:1px solid rgba(255,255,255,.05)">
      <div>
        <div class="small" style="font-weight:560">${escapeHtml(s.name)}</div>
        <div class="small dim">${fmt.date(s.opened_on)} → ${s.closed_on ? fmt.date(s.closed_on) : 'open'} · ${s.entry_count} entries</div>
      </div>
      <div class="mono small" style="font-weight:600">${fmt.money(s.total_cents)}</div>
    </div>`).join('');
}

/* ── Forms ───────────────────────────────────────────────────────────── */

function modal(title, bodyHTML, onMount) {
  const back = document.createElement('div');
  back.className = 'modal-backdrop';
  back.innerHTML = `<div class="glass modal">
    <div class="modal-head"><h2>${escapeHtml(title)}</h2><button class="modal-close" aria-label="Close">×</button></div>
    <div class="modal-body">${bodyHTML}</div>
  </div>`;
  document.body.appendChild(back);
  const close = () => back.remove();
  back.querySelector('.modal-close').addEventListener('click', close);
  back.addEventListener('click', e => { if (e.target === back) close(); });
  document.addEventListener('keydown', function esc(e) {
    if (e.key === 'Escape') { close(); document.removeEventListener('keydown', esc); }
  });
  if (onMount) onMount(back.querySelector('.modal-body'), close);
  return close;
}

function renderPoolForm(el, pool) {
  const p = pool || {};
  el.innerHTML = `
    <div class="stack" style="gap:.75rem">
      <div class="field"><label for="p-name">Pool name</label>
        <input id="p-name" value="${escapeHtml(p.name || '')}" placeholder="Backyard pool"></div>
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="p-volume">Volume (litres)</label>
          <input id="p-volume" type="number" step="100" value="${p.volume_l || ''}" placeholder="58000"></div>
        <div class="field"><label for="p-sanitizer">Sanitizer</label>
          <select id="p-sanitizer">
            ${['chlorine', 'salt', 'bromine', 'mineral'].map(s => `<option value="${s}" ${p.sanitizer === s ? 'selected' : ''}>${titleCase(s)}</option>`).join('')}
          </select></div>
        <div class="field"><label for="p-surface">Surface</label>
          <select id="p-surface">
            ${['vinyl', 'concrete', 'fiberglass', 'painted'].map(s => `<option value="${s}" ${p.surface === s ? 'selected' : ''}>${titleCase(s)}</option>`).join('')}
          </select></div>
        <div class="field"><label for="p-location">Location</label>
          <select id="p-location">
            ${['Outdoor', 'Indoor'].map(s => `<option value="${s}" ${p.location === s ? 'selected' : ''}>${s}</option>`).join('')}
          </select></div>
      </div>
      <div class="field"><label for="p-address">Site address (optional)</label>
        <input id="p-address" value="${escapeHtml(p.site_address || '')}"></div>
      <button class="btn btn-primary" id="save-pool">${pool ? 'Save pool' : 'Create pool'}</button>
    </div>`;

  $('#save-pool', el).addEventListener('click', async () => {
    const body = {
      name: $('#p-name', el).value.trim(),
      volume_l: Number($('#p-volume', el).value),
      sanitizer: $('#p-sanitizer', el).value,
      surface: $('#p-surface', el).value,
      location: $('#p-location', el).value,
      site_address: $('#p-address', el).value.trim(),
      salt_pool: $('#p-sanitizer', el).value === 'salt',
    };
    if (!body.name) return toast('Give the pool a name', 'err');
    if (!body.volume_l) return toast('Volume is required — every dose is calculated from it', 'err');
    try {
      if (pool) { await api.updatePool(pool.id, body); toast('Pool saved', 'ok'); }
      else { await api.createPool(body); toast('Pool created', 'ok'); }
      state.trends = state.costs = null;
      await loadPools();
    } catch (e) { toast(e.message, 'err'); }
  });
}

const TEST_FIELDS = [
  { key: 'free_chlorine', label: 'Free chlorine', unit: 'ppm', step: '0.01' },
  { key: 'total_chlorine', label: 'Total chlorine', unit: 'ppm', step: '0.01' },
  { key: 'combined_chlorine', label: 'Combined chlorine', unit: 'ppm', step: '0.01', hint: 'left blank, calculated from total − free' },
  { key: 'ph', label: 'pH', unit: '', step: '0.01' },
  { key: 'total_alkalinity', label: 'Total alkalinity', unit: 'ppm', step: '1' },
  { key: 'calcium_hardness', label: 'Calcium hardness', unit: 'ppm', step: '1' },
  { key: 'cyanuric_acid', label: 'Stabilizer (CYA)', unit: 'ppm', step: '1' },
  { key: 'total_salt', label: 'Salt', unit: 'ppm', step: '1' },
  { key: 'temperature', label: 'Temperature', unit: '°C', step: '0.1' },
  { key: 'tds', label: 'Total dissolved solids', unit: 'ppm', step: '1' },
  { key: 'phosphate', label: 'Phosphate', unit: 'ppb', step: '1' },
  { key: 'borate', label: 'Borate', unit: 'ppm', step: '1' },
  { key: 'total_copper', label: 'Total copper', unit: 'ppm', step: '0.01' },
  { key: 'iron', label: 'Iron', unit: 'ppm', step: '0.01' },
  { key: 'bromine', label: 'Bromine', unit: 'ppm', step: '0.01' },
];

function openTestForm(parsed, photo) {
  const read = (parsed && parsed.values) || {};
  const val = key => (read[key] === undefined || read[key] === null ? '' : read[key]);
  modal(parsed ? 'Check the readings from the sheet' : 'Add a water test', `
    <div class="stack" style="gap:.85rem">
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="t-date">Date tested</label>
          <input id="t-date" type="date" value="${escapeHtml((parsed && parsed.tested_at) || new Date().toISOString().slice(0, 10))}"></div>
        <div class="field"><label for="t-company">Tested by (optional)</label>
          <input id="t-company" placeholder="Jameson Pool & Spa, or your own name"
            value="${escapeHtml((parsed && parsed.company) || '')}"></div>
      </div>
      ${parsed ? `<div class="glass card" style="padding:.6rem">
        <p class="small" style="margin:0"><strong>Read from your photo${parsed.model ? ` by ${escapeHtml(parsed.model)}` : ''}.</strong>
          Nothing has been saved yet. Check each number against the sheet — a transcription is a reading of a
          photograph, and the fields it left blank were marked N/T or could not be read.</p>
        ${parsed.notes ? `<p class="small dim" style="margin:.4rem 0 0">${escapeHtml(parsed.notes)}</p>` : ''}
        ${(parsed.rejected || []).length ? `<p class="small dim" style="margin:.4rem 0 0">Discarded as impossible:
          <span class="mono">${parsed.rejected.map(escapeHtml).join(', ')}</span></p>` : ''}
      </div>` : '<p class="small dim" style="margin:0">Fill in whatever you have — every field is optional. A strip test with three numbers still produces a full analysis.</p>'}
      <div class="grid grid-2" style="gap:.75rem">
        ${TEST_FIELDS.map(f => `
          <div class="field">
            <label for="t-${f.key}">${f.label}${f.unit ? ` <span class="dim">(${f.unit})</span>` : ''}</label>
            <input id="t-${f.key}" type="number" step="${f.step}" inputmode="decimal" placeholder="—"
              value="${val(f.key)}" ${parsed && val(f.key) !== '' ? 'data-from-sheet="1"' : ''}>
            ${f.hint ? `<span class="hint">${f.hint}</span>` : ''}
          </div>`).join('')}
      </div>
      <div class="field"><label for="t-notes">Notes (optional)</label>
        <textarea id="t-notes" placeholder="Water was cloudy after the storm…"></textarea></div>
      <button class="btn btn-primary btn-block" id="save-test">${parsed ? 'Save this test' : 'Save test'}</button>
    </div>`, (body, close) => {
    $('#save-test', body).addEventListener('click', async () => {
      const payload = { pool_id: state.pool.id, tested_at: $('#t-date', body).value };
      for (const f of TEST_FIELDS) {
        const v = $(`#t-${f.key}`, body).value;
        if (v !== '') payload[f.key] = Number(v);
      }
      const company = $('#t-company', body).value.trim();
      if (company) payload.company_name = company;
      const notes = $('#t-notes', body).value.trim();
      if (notes) payload.notes = notes;

      const hasReading = TEST_FIELDS.some(f => payload[f.key] !== undefined);
      if (!hasReading) return toast('Enter at least one reading', 'err');

      try {
        const detail = await api.createTest(payload);
        // The sheet belongs with the test it produced, so it is uploaded once
        // the readings have actually been accepted rather than before.
        if (photo && detail && detail.test) {
          const sheet = new FormData();
          sheet.append('file', photo);
          sheet.append('pool_id', state.pool.id);
          sheet.append('test_id', detail.test.id);
          sheet.append('kind', 'test_sheet');
          api.upload('/api/attachments', sheet).catch(() => toast('The test was saved, but the photo could not be attached', 'err'));
        }
        close();
        toast('Test saved', 'ok');
        state.tests = []; state.trends = null;
        await loadPool();
      } catch (e) { toast(e.message, 'err'); }
    });
  });
}

/* True on a phone or tablet: a coarse pointer means there is a camera worth
 * leading with, and a file picker is the awkward path rather than the obvious
 * one. Used only to decide which button looks primary. */
function touchDevice() {
  return window.matchMedia && window.matchMedia('(pointer: coarse)').matches;
}

/* ── Add a test from a photograph ─────────────────────────────────────
 *
 * The whole printout — twenty readings, the date, who tested it — becomes a
 * scored test without anyone typing a number. The model transcribes, the
 * server scores and doses it exactly as it would a typed one, and the analysis
 * runs in the same request.
 */
function openPhotoTestForm() {
  modal('Add a test from a photo', `
    <div class="stack" style="gap:.85rem" id="photo-step-1">
      <p class="small muted" style="margin:0">Point your phone at the printout from the pool store, or upload a
        picture you already have. The readings, the date and who tested it are read off the sheet and filled in
        for you.</p>
      <div class="field">
        <label>Photo of the test sheet</label>
        <div class="grid grid-2" style="gap:.5rem">
          <button type="button" class="btn ${touchDevice() ? 'btn-primary' : ''}" id="p-shoot">📷 Take a photo</button>
          <button type="button" class="btn ${touchDevice() ? '' : 'btn-primary'}" id="p-choose">🖼 Choose a file</button>
        </div>
        <span class="hint">Images only, up to 25 MB. Held straight, in focus, and filling the frame reads best.</span>
        <!-- Two inputs rather than one: capture="environment" opens the phone's
             rear camera directly, which is what you want with the sheet in your
             hand, but it also stops that input offering the photo library or a
             file on a desktop. So each way in gets its own. -->
        <input id="p-camera" type="file" accept="image/*" capture="environment" style="display:none">
        <input id="p-file" type="file" accept="image/*" style="display:none">
      </div>
      <div id="p-preview"></div>
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="p-date">Date tested</label>
          <input id="p-date" type="date">
          <span class="hint">Leave blank to use the date on the sheet</span></div>
        <div class="field"><label for="p-company">Tested by</label>
          <input id="p-company" placeholder="Read from the sheet"></div>
      </div>
      <div class="field"><label for="p-hint">Anything the photo does not say (optional)</label>
        <input id="p-hint" placeholder="The salt row is smudged"></div>
      <div class="field">
        <label for="p-mode">What should happen next?</label>
        <select id="p-mode">
          <option value="review">Fill the form in and let me check it first</option>
          <option value="store">Save it as a test straight away, and analyse it</option>
        </select>
        <span class="hint">A transcription is a model reading a photograph, not a measurement — checking twenty numbers against the sheet is quicker than typing them.</span>
      </div>
      <button class="btn btn-primary btn-block" id="p-go" disabled>Read the sheet</button>
    </div>`, (body, close) => {
    // Whichever way the picture arrived, everything downstream is the same.
    let chosen = null;

    const takeFile = file => {
      chosen = file || null;
      const preview = $('#p-preview', body);
      if (!chosen) { preview.innerHTML = ''; return; }
      const url = URL.createObjectURL(chosen);
      preview.innerHTML = `<img src="${url}" alt="The sheet you are about to read"
        style="width:100%;max-height:260px;object-fit:contain;border-radius:10px;border:1px solid var(--glass-border)">
        <p class="small dim" style="margin:.4rem 0 0">${escapeHtml(chosen.name || 'photo')} · ${(chosen.size / (1 << 20)).toFixed(1)} MB</p>`;
      preview.firstElementChild.addEventListener('load', () => URL.revokeObjectURL(url), { once: true });
      $('#p-go', body).disabled = false;
    };

    $('#p-shoot', body).addEventListener('click', () => $('#p-camera', body).click());
    $('#p-choose', body).addEventListener('click', () => $('#p-file', body).click());
    $('#p-camera', body).addEventListener('change', e => takeFile(e.target.files[0]));
    $('#p-file', body).addEventListener('change', e => takeFile(e.target.files[0]));

    $('#p-go', body).addEventListener('click', async () => {
      const file = chosen;
      if (!file) return toast('Take a photo or choose a file first', 'err');

      const btn = $('#p-go', body);
      btn.disabled = true;
      btn.innerHTML = '<span class="spinner"></span> Reading the sheet…';

      const review = $('#p-mode', body).value === 'review';
      const form = new FormData();
      form.append('file', file);
      form.append('pool_id', state.pool.id);
      form.append('analyse', review ? 'false' : 'true');
      if (review) form.append('dry_run', 'true');
      const date = $('#p-date', body).value;
      if (date) form.append('tested_at', date);
      const company = $('#p-company', body).value.trim();
      if (company) form.append('company_name', company);
      const hint = $('#p-hint', body).value.trim();
      if (hint) form.append('hint', hint);

      try {
        const result = await api.createTestFromPhoto(form);
        if (review) {
          // Nothing was stored: hand the readings to the ordinary test form
          // so they are checked against the paper before they become a test.
          close();
          openTestForm(result.parsed, file);
          return;
        }
        renderPhotoResult(body, result, close);
        state.tests = []; state.trends = null;
        loadPool();
      } catch (e) {
        toast(e.message, 'err');
        btn.disabled = false;
        btn.textContent = 'Read the sheet';
      }
    });
  });
}

/* What was read, what was not, and what the analysis made of it. Everything
 * here is already stored — this is a receipt, not a confirmation step. */
function renderPhotoResult(body, detail, close) {
  const parsed = detail.parsed || {};
  const readings = (detail.readings || []).filter(r => r.value !== null && r.value !== undefined);
  const insight = detail.insight;
  const confidence = Math.round((parsed.confidence || 0) * 100);

  body.innerHTML = `
    <div class="stack" style="gap:.85rem">
      <div class="row" style="align-items:baseline">
        <h3 style="margin:0">Read ${readings.length} reading${readings.length === 1 ? '' : 's'}</h3>
        <div class="spacer"></div>
        <span class="small dim">${parsed.test_count ? `test #${parsed.test_count} · ` : ''}${escapeHtml(parsed.model || '')}${confidence ? ` · ${confidence}% confident` : ''}</span>
      </div>

      ${readings.length ? `<div class="grid grid-2" style="gap:.5rem">
        ${readings.map(r => `
          <div class="row small" style="gap:.5rem;padding:.35rem .6rem;border-radius:8px;background:rgba(255,255,255,.03)">
            <span>${escapeHtml(r.label)}</span><div class="spacer"></div>
            <span class="mono">${fmt.num(r.value)}</span>
            <span class="dim">${escapeHtml(r.unit || '')}</span>
            <span class="pill pill-${r.status}"><span class="dot"></span></span>
          </div>`).join('')}
      </div>` : ''}

      ${parsed.notes ? `<p class="small muted" style="margin:0">${escapeHtml(parsed.notes)}</p>` : ''}

      ${(parsed.rejected || []).length ? `<div class="glass card" style="padding:.75rem">
        <p class="small" style="margin:0"><strong>Discarded as impossible:</strong>
          <span class="mono">${parsed.rejected.map(escapeHtml).join(', ')}</span>.
          A misread digit cannot be corrected safely, so these were thrown away — check them against
          the sheet and enter them by hand if they are real.</p>
      </div>` : ''}

      ${poolProfileHTML(parsed.pool)}

      ${detail.insight_error ? `<p class="small" style="margin:0;color:var(--warning)">
        The test was saved, but the analysis failed: ${escapeHtml(detail.insight_error)}. You can run it
        again from the test.</p>` : ''}

      ${insight ? `<div class="note ai">
        ${insight.headline ? `<strong>${escapeHtml(insight.headline)}</strong>` : ''}
        ${insight.summary ? `<p class="small" style="margin:.4rem 0 0">${escapeHtml(insight.summary)}</p>` : ''}
        ${(insight.actions || []).length ? `<p class="small" style="margin:.5rem 0 0"><strong>Do now:</strong>
          ${escapeHtml(insight.actions[0])}</p>` : ''}
      </div>` : ''}

      <button class="btn btn-primary btn-block" id="p-done">Open the test</button>
    </div>`;

  $('#p-done', body).addEventListener('click', () => { close(); switchView('overview'); });

  const applyBtn = $('#p-apply-pool', body);
  if (applyBtn) {
    applyBtn.addEventListener('click', async () => {
      applyBtn.disabled = true;
      applyBtn.innerHTML = '<span class="spinner"></span> Updating…';
      try {
        await api.updatePool(state.pool.id, poolPatch(parsed.pool));
        applyBtn.textContent = 'Pool updated';
        toast('Pool profile updated from the sheet', 'ok');
        await loadPools();
      } catch (e) {
        toast(e.message, 'err');
        applyBtn.disabled = false;
        applyBtn.textContent = 'Update the pool to match';
      }
    });
  }
}

/* The pool description printed on the sheet. It is only ever offered, never
 * applied on its own: volume is what every dose is calculated from, and a
 * number read off a photograph should not rewrite it behind the owner's back.
 * Only the fields that actually differ are shown, so an unchanged profile
 * stays quiet. */
const POOL_SHEET_FIELDS = [
  ['volume_l', 'Volume', v => `${Number(v).toLocaleString()} L`],
  ['surface', 'Surface'],
  ['sanitizer', 'Sanitizer'],
  ['water_type', 'Type'],
  ['treatment_profile', 'Treatment profile'],
  ['grade', 'Grade'],
  ['location', 'Location'],
  ['customer_name', 'Customer'],
  ['site_address', 'Site'],
];

function poolDifferences(sheet) {
  if (!sheet || !state.pool) return [];
  return POOL_SHEET_FIELDS
    .filter(([key]) => {
      const read = sheet[key], have = state.pool[key];
      if (read === null || read === undefined || read === '') return false;
      if (key === 'volume_l') return Math.abs(Number(read) - Number(have || 0)) > 1;
      return String(read).toLowerCase() !== String(have || '').toLowerCase();
    });
}

function poolPatch(sheet) {
  const patch = {};
  for (const [key] of poolDifferences(sheet)) patch[key] = sheet[key];
  return patch;
}

function poolProfileHTML(sheet) {
  const diffs = poolDifferences(sheet);
  if (!diffs.length) return '';
  return `<div class="glass card" style="padding:.75rem">
    <p class="small" style="margin:0 0 .5rem"><strong>The sheet describes this pool differently.</strong>
      Volume is what every dose is calculated from, so nothing has been changed.</p>
    <div class="stack" style="gap:.3rem">
      ${diffs.map(([key, label, fmtFn]) => {
        const read = sheet[key];
        const have = state.pool[key];
        return `<div class="row small" style="gap:.5rem">
          <span class="dim" style="min-width:8.5rem">${escapeHtml(label)}</span>
          <span class="dim" style="text-decoration:line-through">${escapeHtml(String(have || '—'))}</span>
          <span>→</span>
          <strong>${escapeHtml(fmtFn ? fmtFn(read) : String(read))}</strong>
        </div>`;
      }).join('')}
    </div>
    <button class="btn btn-sm btn-block" style="margin-top:.6rem" id="p-apply-pool">Update the pool to match</button>
  </div>`;
}

const CATEGORIES = ['chemical', 'equipment', 'service', 'maintenance', 'utility', 'opening', 'closing', 'other'];
const UNITS = ['', 'L', 'ml', 'kg', 'g', 'bag', 'jug', 'puck', 'tablet', 'each', 'hour'];

function openLogForm(entry) {
  const e = entry || {};
  const editing = Boolean(entry?.id);
  modal(editing ? 'Edit entry' : 'Log something', `
    <div class="stack" style="gap:.85rem">
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="l-date">Date</label>
          <input id="l-date" type="date" value="${e.occurred_on || new Date().toISOString().slice(0, 10)}">
          <span class="hint">Backdate freely — entries file into the right season automatically.</span></div>
        <div class="field"><label for="l-category">Category</label>
          <select id="l-category">${CATEGORIES.map(c => `<option value="${c}" ${e.category === c ? 'selected' : ''}>${titleCase(c)}</option>`).join('')}</select></div>
      </div>
      <div class="field"><label for="l-item">What was it?</label>
        <input id="l-item" value="${escapeHtml(e.item || '')}" placeholder="Chlorine, salt, cartridge filter, salt cell, service call…"></div>
      <div class="grid grid-3" style="gap:.75rem">
        <div class="field"><label for="l-qty">Quantity</label>
          <input id="l-qty" type="number" step="0.01" inputmode="decimal" value="${e.quantity ?? ''}" placeholder="10"></div>
        <div class="field"><label for="l-unit">Unit</label>
          <select id="l-unit">${UNITS.map(u => `<option value="${u}" ${e.unit === u ? 'selected' : ''}>${u || '—'}</option>`).join('')}</select></div>
        <div class="field"><label for="l-cost">Cost</label>
          <input id="l-cost" type="number" step="0.01" inputmode="decimal" value="${e.cost_cents ? (e.cost_cents / 100).toFixed(2) : ''}" placeholder="42.50"></div>
      </div>
      <div class="field"><label for="l-vendor">Vendor (optional)</label>
        <input id="l-vendor" value="${escapeHtml(e.vendor || '')}" placeholder="Jameson Pool & Spa"></div>
      <div class="field"><label for="l-notes">Notes (optional)</label>
        <textarea id="l-notes" placeholder="Replaced after the cell started throwing low-flow errors">${escapeHtml(e.notes || '')}</textarea></div>
      <button class="btn btn-primary btn-block" id="save-log">${editing ? 'Save entry' : 'Add entry'}</button>
    </div>`, (body, close) => {
    $('#save-log', body).addEventListener('click', async () => {
      const payload = {
        pool_id: state.pool.id,
        occurred_on: $('#l-date', body).value,
        category: $('#l-category', body).value,
        item: $('#l-item', body).value.trim(),
        unit: $('#l-unit', body).value,
        vendor: $('#l-vendor', body).value.trim(),
        notes: $('#l-notes', body).value.trim(),
      };
      const q = $('#l-qty', body).value;
      if (q !== '') payload.quantity = Number(q);
      const c = $('#l-cost', body).value;
      if (c !== '') payload.cost = Number(c);
      if (!payload.item) return toast('What was it?', 'err');

      try {
        if (editing) await api.updateLog(entry.id, payload);
        else await api.createLog(payload);
        close();
        toast(editing ? 'Entry saved' : 'Entry added', 'ok');
        state.logEntries = await api.log({ pool_id: state.pool.id, limit: 300 });
        state.costs = null;
        await loadPool();
      } catch (err) { toast(err.message, 'err'); }
    });
  });
}

function openSeasonForm() {
  const year = new Date().getFullYear();
  modal('New season', `
    <div class="stack" style="gap:.85rem">
      <p class="small muted" style="margin:0">A season groups costs between opening and closing the pool, so you can compare one year against the next.</p>
      <div class="field"><label for="s-name">Name</label><input id="s-name" value="Season ${year}"></div>
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="s-open">Opened on</label>
          <input id="s-open" type="date" value="${year}-05-01"></div>
        <div class="field"><label for="s-close">Closed on (leave blank if still open)</label>
          <input id="s-close" type="date"></div>
      </div>
      <button class="btn btn-primary btn-block" id="save-season">Create season</button>
    </div>`, (body, close) => {
    $('#save-season', body).addEventListener('click', async () => {
      try {
        await api.createSeason({
          pool_id: state.pool.id,
          name: $('#s-name', body).value.trim(),
          opened_on: $('#s-open', body).value,
          closed_on: $('#s-close', body).value,
        });
        close();
        toast('Season created — existing entries were filed into it', 'ok');
        state.costs = null;
        await loadPool();
        render();
      } catch (e) { toast(e.message, 'err'); }
    });
  });
}

function openUploadForm() {
  modal('Upload a receipt', `
    <div class="stack" style="gap:.85rem">
      <p class="small muted" style="margin:0">Images are automatically resized and recompressed, so a phone photo lands at a fraction of its original size. Limit 25 MB.</p>
      <div class="field"><label for="u-file">File</label>
        <input id="u-file" type="file" accept="image/*,application/pdf"></div>
      <div class="grid grid-2" style="gap:.75rem">
        <div class="field"><label for="u-total">Total</label><input id="u-total" type="number" step="0.01" inputmode="decimal" placeholder="86.40"></div>
        <div class="field"><label for="u-date">Purchased on</label><input id="u-date" type="date" value="${new Date().toISOString().slice(0, 10)}"></div>
      </div>
      <div class="field"><label for="u-vendor">Vendor</label><input id="u-vendor" placeholder="Jameson Pool & Spa"></div>
      <div class="field"><label for="u-notes">Notes</label><input id="u-notes" placeholder="Salt and stabilizer"></div>
      <button class="btn btn-primary btn-block" id="do-upload">Upload</button>
    </div>`, (body, close) => {
    $('#do-upload', body).addEventListener('click', async () => {
      const file = $('#u-file', body).files[0];
      if (!file) return toast('Choose a file first', 'err');
      const btn = $('#do-upload', body);
      btn.disabled = true;
      btn.innerHTML = '<span class="spinner"></span> Uploading…';
      const form = new FormData();
      form.append('file', file);
      form.append('pool_id', state.pool.id);
      form.append('kind', 'receipt');
      form.append('total', $('#u-total', body).value || '0');
      form.append('purchased_on', $('#u-date', body).value);
      form.append('vendor', $('#u-vendor', body).value);
      form.append('notes', $('#u-notes', body).value);
      try {
        const rec = await api.upload('/api/attachments', form);
        close();
        const saved = rec.original_bytes && rec.size_bytes
          ? ` (${Math.round((1 - rec.size_bytes / rec.original_bytes) * 100)}% smaller)` : '';
        toast(`Receipt uploaded${saved}`, 'ok');
      } catch (e) {
        toast(e.message, 'err');
        btn.disabled = false;
        btn.textContent = 'Upload';
      }
    });
  });
}

function openKeyForm() {
  modal('New API key', `
    <div class="stack" style="gap:.85rem">
      <div class="field"><label for="k-name">What is it for?</label>
        <input id="k-name" placeholder="Home automation script"></div>
      <div class="field"><label for="k-scopes">What may it do?</label>
        <select id="k-scopes">
          <option value="read,write">Read and write — everything you can do</option>
          <option value="read">Read only — cannot change anything</option>
        </select>
        <span class="hint">Enforced on the request method: a read key cannot POST, PATCH or DELETE.</span></div>
      <button class="btn btn-primary btn-block" id="do-key">Create key</button>
      <div id="key-result"></div>
    </div>`, (body) => {
    $('#do-key', body).addEventListener('click', async () => {
      try {
        const res = await api.createKey({
          name: $('#k-name', body).value.trim() || 'API key',
          scopes: $('#k-scopes', body).value,
        });
        const secret = res.key.key;
        const discovery = res.discovery || {};
        $('#key-result', body).innerHTML = `
          <div class="note" style="border-left-color:var(--warning)">
            <div class="note-head"><strong>Copy this now — it is shown only once.</strong></div>
            <pre style="margin:0;white-space:pre-wrap;word-break:break-all">${escapeHtml(secret)}</pre>
            <button class="btn btn-sm" style="margin-top:.6rem" id="copy-key">Copy</button>
          </div>
          ${discovery.example ? `<div class="note" style="margin-top:.6rem">
            <div class="note-head"><strong>Try it</strong></div>
            <pre style="margin:0;white-space:pre-wrap;word-break:break-all">${escapeHtml(discovery.example)}</pre>
            <p class="small dim" style="margin:.6rem 0 0">The full API description is at
              <a href="${escapeHtml(discovery.spec_url || '/api/openapi.yaml')}">${escapeHtml(discovery.spec_url || '/api/openapi.yaml')}</a>
              — hand it and this key to a script or an LLM and it can work the rest out.</p>
          </div>` : ''}`;
        $('#copy-key', body).addEventListener('click', () => {
          navigator.clipboard.writeText(secret).then(() => toast('Copied', 'ok'));
        });
        $('#do-key', body).disabled = true;
        refreshKeys();
      } catch (e) { toast(e.message, 'err'); }
    });
  });
}

function skeletonCards(n) {
  return `<div class="grid grid-2">${Array.from({ length: n }, () =>
    '<div class="glass card"><div class="skeleton" style="height:180px"></div></div>').join('')}</div>`;
}

/* A visitor in the demo should know it is a demo, and be able to put it back
 * the way they found it. */
function showDemoBanner() {
  const bar = document.createElement('div');
  bar.className = 'demo-bar';
  bar.innerHTML = `
    <span><strong>Demo account.</strong> Change anything you like — the data rebuilds itself regularly.</span>
    <div class="spacer"></div>
    <button class="btn btn-sm" id="demo-reset">Reset the data</button>
    <a class="btn btn-sm btn-primary" href="/login?mode=register">Create your own</a>`;
  document.querySelector('.appbar').after(bar);
  $('#demo-reset').addEventListener('click', async () => {
    const btn = $('#demo-reset');
    btn.disabled = true;
    btn.innerHTML = '<span class="spinner"></span> Resetting…';
    try {
      await api.post('/api/demo/reset', {});
      toast('Demo data reset', 'ok');
      state.trends = state.costs = null;
      state.logEntries = []; state.tests = [];
      await loadPools();
    } catch (e) { toast(e.message, 'err'); }
    btn.disabled = false;
    btn.textContent = 'Reset the data';
  });
}

/* Inline handlers in generated markup need these on the window. */
window.__openTestForm = openTestForm;
window.__openPhotoTestForm = openPhotoTestForm;
window.__openLogForm = openLogForm;
window.__switchView = switchView;

boot();
