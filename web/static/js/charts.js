/* Charts: hand-rolled SVG, no dependencies, so the container stays tiny.
 *
 * Every chart here follows the same rules: one y-axis (never two), thin marks,
 * recessive grid, a legend whenever more than one series is drawn, and a hover
 * layer with a crosshair and tooltip. Series colours come from the validated
 * categorical order in app.css and are assigned by slot, never cycled. */

const SERIES = ['--series-1','--series-2','--series-3','--series-4','--series-5','--series-6','--series-7','--series-8'];

export function seriesColor(i) {
  return getComputedStyle(document.documentElement).getPropertyValue(SERIES[i % SERIES.length]).trim();
}

const STATUS_COLORS = { good: '--good', warning: '--warning', serious: '--serious', critical: '--critical' };
export function statusColor(s) {
  const v = STATUS_COLORS[s] || '--fg-dim';
  return getComputedStyle(document.documentElement).getPropertyValue(v).trim();
}

const SVG_NS = 'http://www.w3.org/2000/svg';
function el(name, attrs = {}, text) {
  const n = document.createElementNS(SVG_NS, name);
  for (const [k, v] of Object.entries(attrs)) {
    if (v !== null && v !== undefined) n.setAttribute(k, String(v));
  }
  if (text !== undefined) n.textContent = text;
  return n;
}

/* ── Tooltip ─────────────────────────────────────────────────────────── */

let tipEl = null;
function tooltip() {
  if (!tipEl) {
    tipEl = document.createElement('div');
    tipEl.className = 'tooltip';
    document.body.appendChild(tipEl);
  }
  return tipEl;
}
function showTip(html, x, y) {
  const t = tooltip();
  t.innerHTML = html;
  t.classList.add('show');
  const r = t.getBoundingClientRect();
  // Keep the tooltip on screen on narrow viewports.
  let left = x + 14;
  if (left + r.width > window.innerWidth - 8) left = x - r.width - 14;
  if (left < 8) left = 8;
  let top = y - r.height - 12;
  if (top < 8) top = y + 18;
  t.style.left = `${left}px`;
  t.style.top = `${top}px`;
}
function hideTip() { if (tipEl) tipEl.classList.remove('show'); }

/* ── Formatting ──────────────────────────────────────────────────────── */

/* A date-only value is stored as midnight UTC. Rendering that with the local
 * timezone shifts it back a day for anyone west of Greenwich, so parse it as a
 * local date instead of an instant. Real timestamps are parsed normally. */
function asLocalDate(iso) {
  if (!iso) return null;
  const dateOnly = iso.length === 10 ? iso : (/T00:00:00(\.0+)?Z$/.test(iso) ? iso.slice(0, 10) : null);
  const d = dateOnly ? new Date(`${dateOnly}T00:00:00`) : new Date(iso);
  return Number.isNaN(d.getTime()) ? null : d;
}

export const fmt = {
  num(v, dp) {
    if (v === null || v === undefined || Number.isNaN(v)) return '—';
    if (dp !== undefined) return v.toFixed(dp);
    const a = Math.abs(v);
    if (a >= 1000) return v.toLocaleString(undefined, { maximumFractionDigits: 0 });
    if (a >= 10) return v.toFixed(1).replace(/\.0$/, '');
    return v.toFixed(2).replace(/\.?0+$/, '');
  },
  money(cents, currency = 'CAD') {
    const v = (cents || 0) / 100;
    try {
      return v.toLocaleString(undefined, { style: 'currency', currency, maximumFractionDigits: v >= 1000 ? 0 : 2 });
    } catch { return `$${v.toFixed(2)}`; }
  },
  date(iso) {
    const d = asLocalDate(iso);
    if (!d) return '—';
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  },
  shortDate(iso) {
    const d = asLocalDate(iso);
    if (!d) return '';
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  },
  month(ym) {
    const d = new Date(`${ym}-01T00:00:00`);
    if (Number.isNaN(d.getTime())) return ym;
    return d.toLocaleDateString(undefined, { month: 'short', year: '2-digit' });
  },
};

/* ── Scales ──────────────────────────────────────────────────────────── */

function niceTicks(min, max, count = 5) {
  if (min === max) { min -= 1; max += 1; }
  const span = max - min;
  const raw = span / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const norm = raw / mag;
  const step = (norm >= 7.5 ? 10 : norm >= 3.5 ? 5 : norm >= 1.5 ? 2 : 1) * mag;
  const start = Math.ceil(min / step) * step;
  const out = [];
  for (let v = start; v <= max + step * 0.001; v += step) out.push(Math.round(v * 1e9) / 1e9);
  return out;
}

/* ── Line / area chart with ideal band ───────────────────────────────── */

/* opts: { points:[{at,value}], label, unit, ideal:[lo,hi], events:[{at,label}],
 *         color, height, dp } */
export function lineChart(container, opts) {
  const pts = (opts.points || []).filter(p => p.value !== null && p.value !== undefined);
  container.innerHTML = '';
  if (pts.length === 0) {
    container.innerHTML = `<div class="empty small">No readings yet for ${escapeHtml(opts.label || 'this parameter')}.</div>`;
    return;
  }

  const H = opts.height || 200;
  const M = { top: 12, right: 12, bottom: 26, left: 44 };
  const W = Math.max(container.clientWidth || 600, 260);
  const iw = W - M.left - M.right;
  const ih = H - M.top - M.bottom;

  const xs = pts.map(p => new Date(p.at).getTime());
  const ys = pts.map(p => p.value);

  let lo = Math.min(...ys), hi = Math.max(...ys);
  // Keep the ideal band in frame even when every reading sits outside it —
  // that gap is the whole point of the chart.
  if (opts.ideal) { lo = Math.min(lo, opts.ideal[0]); hi = Math.max(hi, opts.ideal[1]); }
  const pad = (hi - lo) * 0.12 || Math.abs(hi * 0.1) || 1;
  lo -= pad; hi += pad;

  const xMin = Math.min(...xs), xMax = Math.max(...xs);
  const X = t => M.left + (xMax === xMin ? iw / 2 : ((t - xMin) / (xMax - xMin)) * iw);
  const Y = v => M.top + ih - ((v - lo) / (hi - lo)) * ih;

  const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: '100%', height: H,
    role: 'img', 'aria-label': `${opts.label} over time` });

  // Ideal band, drawn behind everything.
  if (opts.ideal) {
    const yTop = Y(Math.min(opts.ideal[1], hi));
    const yBot = Y(Math.max(opts.ideal[0], lo));
    svg.appendChild(el('rect', { class: 'band', x: M.left, y: yTop, width: iw, height: Math.max(0, yBot - yTop) }));
    svg.appendChild(el('line', { class: 'band-line', x1: M.left, x2: M.left + iw, y1: yTop, y2: yTop }));
    svg.appendChild(el('line', { class: 'band-line', x1: M.left, x2: M.left + iw, y1: yBot, y2: yBot }));
  }

  // Gridlines and y ticks.
  for (const t of niceTicks(lo, hi, 4)) {
    const y = Y(t);
    if (y < M.top - 1 || y > M.top + ih + 1) continue;
    svg.appendChild(el('line', { class: 'grid-line', x1: M.left, x2: M.left + iw, y1: y, y2: y }));
    svg.appendChild(el('text', { class: 'tick', x: M.left - 7, y: y + 3, 'text-anchor': 'end' }, fmt.num(t, opts.dp)));
  }

  // Logbook events as vertical rules behind the line.
  for (const ev of opts.events || []) {
    const t = new Date(ev.at).getTime();
    if (t < xMin || t > xMax) continue;
    svg.appendChild(el('line', { class: 'event-line', x1: X(t), x2: X(t), y1: M.top, y2: M.top + ih }));
  }

  svg.appendChild(el('line', { class: 'axis-line', x1: M.left, x2: M.left + iw, y1: M.top + ih, y2: M.top + ih }));

  // X ticks: first, middle, last — enough to orient without collisions.
  const idx = pts.length <= 2 ? [0, pts.length - 1] : [0, Math.floor(pts.length / 2), pts.length - 1];
  for (const i of [...new Set(idx)]) {
    svg.appendChild(el('text', {
      class: 'tick', x: X(xs[i]), y: M.top + ih + 16,
      'text-anchor': i === 0 ? 'start' : i === pts.length - 1 ? 'end' : 'middle',
    }, fmt.shortDate(pts[i].at)));
  }

  const color = opts.color || seriesColor(0);
  const d = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${X(xs[i]).toFixed(1)},${Y(p.value).toFixed(1)}`).join(' ');
  svg.appendChild(el('path', { class: 'series-line', d, stroke: color }));

  for (let i = 0; i < pts.length; i++) {
    svg.appendChild(el('circle', { class: 'marker', cx: X(xs[i]), cy: Y(pts[i].value), r: 3.5, fill: color }));
  }

  // Hover layer: crosshair plus nearest-point tooltip.
  const hoverLine = el('line', { class: 'hover-line', y1: M.top, y2: M.top + ih, opacity: 0 });
  const hoverDot = el('circle', { r: 5.5, fill: color, stroke: 'var(--surface-chart)', 'stroke-width': 2, opacity: 0 });
  svg.appendChild(hoverLine); svg.appendChild(hoverDot);

  const overlay = el('rect', { x: M.left, y: M.top, width: iw, height: ih, fill: 'transparent', style: 'cursor:crosshair' });
  svg.appendChild(overlay);

  const move = e => {
    const rect = svg.getBoundingClientRect();
    const px = ((e.clientX ?? e.touches?.[0]?.clientX) - rect.left) * (W / rect.width);
    let best = 0, bestD = Infinity;
    for (let i = 0; i < pts.length; i++) {
      const d2 = Math.abs(X(xs[i]) - px);
      if (d2 < bestD) { bestD = d2; best = i; }
    }
    const p = pts[best];
    const cx = X(xs[best]), cy = Y(p.value);
    hoverLine.setAttribute('x1', cx); hoverLine.setAttribute('x2', cx); hoverLine.setAttribute('opacity', 1);
    hoverDot.setAttribute('cx', cx); hoverDot.setAttribute('cy', cy); hoverDot.setAttribute('opacity', 1);

    const near = (opts.events || []).filter(ev => ev.at.slice(0, 10) === String(p.at).slice(0, 10));
    const evHtml = near.length
      ? `<div class="tt-row dim" style="margin-top:.3rem">+ ${near.map(ev => escapeHtml(ev.label)).join(', ')}</div>` : '';
    showTip(
      `<div class="tt-title">${fmt.date(p.at)}</div>
       <div class="tt-row"><span class="swatch" style="width:9px;height:9px;border-radius:3px;background:${color}"></span>
       <strong>${fmt.num(p.value, opts.dp)}</strong> <span class="dim">${escapeHtml(opts.unit || '')}</span></div>${evHtml}`,
      e.clientX ?? e.touches?.[0]?.clientX, e.clientY ?? e.touches?.[0]?.clientY);
  };
  overlay.addEventListener('mousemove', move);
  overlay.addEventListener('touchstart', move, { passive: true });
  overlay.addEventListener('touchmove', move, { passive: true });
  const leave = () => { hideTip(); hoverLine.setAttribute('opacity', 0); hoverDot.setAttribute('opacity', 0); };
  overlay.addEventListener('mouseleave', leave);
  overlay.addEventListener('touchend', leave);

  container.appendChild(svg);
}

/* ── Bar chart ───────────────────────────────────────────────────────── */

/* opts: { bars:[{label,value,color,sub}], height, valueFmt, horizontal } */
export function barChart(container, opts) {
  const bars = opts.bars || [];
  container.innerHTML = '';
  if (!bars.length) { container.innerHTML = `<div class="empty small">${escapeHtml(opts.emptyText || 'Nothing to show yet.')}</div>`; return; }

  const valueFmt = opts.valueFmt || (v => fmt.num(v));

  if (opts.horizontal) {
    // Horizontal bars are plain HTML: labels never collide and it reflows on
    // mobile for free.
    const maxV = Math.max(...bars.map(b => b.value), 1);
    const wrap = document.createElement('div');
    wrap.className = 'stack';
    wrap.style.gap = '0.55rem';
    for (const [i, b] of bars.entries()) {
      const row = document.createElement('div');
      const color = b.color || seriesColor(i);
      row.innerHTML = `
        <div class="row" style="justify-content:space-between;gap:.5rem;margin-bottom:.25rem">
          <span class="small" style="display:flex;align-items:center;gap:.4rem;min-width:0">
            <span class="swatch" style="width:9px;height:9px;border-radius:3px;background:${color};flex-shrink:0"></span>
            <span style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${escapeHtml(b.label)}</span>
          </span>
          <span class="small mono nowrap" style="font-weight:600">${valueFmt(b.value)}</span>
        </div>
        <div style="height:8px;border-radius:4px;background:rgba(255,255,255,.06);overflow:hidden">
          <div style="height:100%;width:${(b.value / maxV) * 100}%;background:${color};border-radius:4px"></div>
        </div>
        ${b.sub ? `<div class="small dim" style="margin-top:.2rem">${escapeHtml(b.sub)}</div>` : ''}`;
      wrap.appendChild(row);
    }
    container.appendChild(wrap);
    return;
  }

  const H = opts.height || 200;
  const M = { top: 12, right: 8, bottom: 34, left: 52 };
  const W = Math.max(container.clientWidth || 600, 260);
  const iw = W - M.left - M.right;
  const ih = H - M.top - M.bottom;
  const maxV = Math.max(...bars.map(b => b.value), 1);

  const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: '100%', height: H, role: 'img',
    'aria-label': opts.ariaLabel || 'bar chart' });

  for (const t of niceTicks(0, maxV, 4)) {
    const y = M.top + ih - (t / maxV) * ih;
    svg.appendChild(el('line', { class: 'grid-line', x1: M.left, x2: M.left + iw, y1: y, y2: y }));
    svg.appendChild(el('text', { class: 'tick', x: M.left - 7, y: y + 3, 'text-anchor': 'end' }, opts.tickFmt ? opts.tickFmt(t) : fmt.num(t)));
  }
  svg.appendChild(el('line', { class: 'axis-line', x1: M.left, x2: M.left + iw, y1: M.top + ih, y2: M.top + ih }));

  // A 2px surface gap between adjacent bars keeps them separable.
  const slot = iw / bars.length;
  const bw = Math.max(4, Math.min(46, slot - 6));
  for (const [i, b] of bars.entries()) {
    const h = (b.value / maxV) * ih;
    const x = M.left + slot * i + (slot - bw) / 2;
    const y = M.top + ih - h;
    const color = b.color || seriesColor(i);
    // 4px rounded top, square foot: the bar stays anchored to the baseline.
    const r = Math.min(4, h);
    const path = h <= 0.5 ? '' :
      `M${x},${M.top + ih} L${x},${y + r} Q${x},${y} ${x + r},${y} L${x + bw - r},${y} Q${x + bw},${y} ${x + bw},${y + r} L${x + bw},${M.top + ih} Z`;
    if (path) {
      const p = el('path', { d: path, fill: color, style: 'cursor:pointer' });
      p.addEventListener('mousemove', e => showTip(
        `<div class="tt-title">${escapeHtml(b.label)}</div><div class="tt-row"><strong>${valueFmt(b.value)}</strong></div>${b.sub ? `<div class="tt-row dim">${escapeHtml(b.sub)}</div>` : ''}`,
        e.clientX, e.clientY));
      p.addEventListener('mouseleave', hideTip);
      svg.appendChild(p);
    }
    if (bars.length <= 14) {
      // Only truncate when the slot genuinely cannot fit the label; a label
      // clipped to "Season …" tells the reader nothing.
      const maxChars = Math.max(4, Math.floor(slot / 7));
      const label = b.short || b.label;
      svg.appendChild(el('text', { class: 'tick', x: x + bw / 2, y: M.top + ih + 15, 'text-anchor': 'middle' },
        label.length > maxChars ? label.slice(0, maxChars - 1) + '…' : label));
    }
  }
  container.appendChild(svg);
}

/* ── Cumulative area (running cost) ──────────────────────────────────── */

export function areaChart(container, opts) {
  const pts = opts.points || [];
  container.innerHTML = '';
  if (pts.length === 0) { container.innerHTML = `<div class="empty small">${escapeHtml(opts.emptyText || 'No spending recorded yet.')}</div>`; return; }
  if (pts.length === 1) {
    // A single point is not a trend; show it as a figure instead of a line.
    container.innerHTML = `<div class="center" style="padding:2rem 0">
      <div class="stat"><div class="label">${escapeHtml(pts[0].label)}</div>
      <div class="value">${opts.valueFmt ? opts.valueFmt(pts[0].value) : fmt.num(pts[0].value)}</div></div></div>`;
    return;
  }

  const H = opts.height || 200;
  const M = { top: 12, right: 12, bottom: 28, left: 58 };
  const W = Math.max(container.clientWidth || 600, 260);
  const iw = W - M.left - M.right;
  const ih = H - M.top - M.bottom;
  const maxV = Math.max(...pts.map(p => p.value), 1);
  const X = i => M.left + (pts.length === 1 ? iw / 2 : (i / (pts.length - 1)) * iw);
  const Y = v => M.top + ih - (v / maxV) * ih;
  const color = opts.color || seriesColor(0);

  const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: '100%', height: H, role: 'img',
    'aria-label': opts.ariaLabel || 'cumulative total over time' });

  for (const t of niceTicks(0, maxV, 4)) {
    const y = Y(t);
    svg.appendChild(el('line', { class: 'grid-line', x1: M.left, x2: M.left + iw, y1: y, y2: y }));
    svg.appendChild(el('text', { class: 'tick', x: M.left - 7, y: y + 3, 'text-anchor': 'end' }, opts.tickFmt ? opts.tickFmt(t) : fmt.num(t)));
  }
  svg.appendChild(el('line', { class: 'axis-line', x1: M.left, x2: M.left + iw, y1: M.top + ih, y2: M.top + ih }));

  const gid = `grad${Math.random().toString(36).slice(2, 8)}`;
  const defs = el('defs');
  const lg = el('linearGradient', { id: gid, x1: 0, y1: 0, x2: 0, y2: 1 });
  lg.appendChild(el('stop', { offset: '0%', 'stop-color': color, 'stop-opacity': 0.3 }));
  lg.appendChild(el('stop', { offset: '100%', 'stop-color': color, 'stop-opacity': 0.02 }));
  defs.appendChild(lg);
  svg.appendChild(defs);

  const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${X(i).toFixed(1)},${Y(p.value).toFixed(1)}`).join(' ');
  svg.appendChild(el('path', { d: `${line} L${X(pts.length - 1)},${M.top + ih} L${X(0)},${M.top + ih} Z`, fill: `url(#${gid})` }));
  svg.appendChild(el('path', { class: 'series-line', d: line, stroke: color }));

  for (let i = 0; i < pts.length; i++) {
    svg.appendChild(el('circle', { class: 'marker', cx: X(i), cy: Y(pts[i].value), r: 3.5, fill: color }));
  }

  const step = Math.max(1, Math.ceil(pts.length / 6));
  for (let i = 0; i < pts.length; i += step) {
    svg.appendChild(el('text', { class: 'tick', x: X(i), y: M.top + ih + 16, 'text-anchor': 'middle' }, pts[i].label));
  }

  const hoverLine = el('line', { class: 'hover-line', y1: M.top, y2: M.top + ih, opacity: 0 });
  const hoverDot = el('circle', { r: 5.5, fill: color, stroke: 'var(--surface-chart)', 'stroke-width': 2, opacity: 0 });
  svg.appendChild(hoverLine); svg.appendChild(hoverDot);
  const overlay = el('rect', { x: M.left, y: M.top, width: iw, height: ih, fill: 'transparent', style: 'cursor:crosshair' });
  svg.appendChild(overlay);

  const move = e => {
    const rect = svg.getBoundingClientRect();
    const px = ((e.clientX ?? e.touches?.[0]?.clientX) - rect.left) * (W / rect.width);
    const i = Math.max(0, Math.min(pts.length - 1, Math.round(((px - M.left) / iw) * (pts.length - 1))));
    hoverLine.setAttribute('x1', X(i)); hoverLine.setAttribute('x2', X(i)); hoverLine.setAttribute('opacity', 1);
    hoverDot.setAttribute('cx', X(i)); hoverDot.setAttribute('cy', Y(pts[i].value)); hoverDot.setAttribute('opacity', 1);
    showTip(`<div class="tt-title">${escapeHtml(pts[i].fullLabel || pts[i].label)}</div>
      <div class="tt-row"><strong>${opts.valueFmt ? opts.valueFmt(pts[i].value) : fmt.num(pts[i].value)}</strong></div>
      ${pts[i].sub ? `<div class="tt-row dim">${escapeHtml(pts[i].sub)}</div>` : ''}`,
      e.clientX ?? e.touches?.[0]?.clientX, e.clientY ?? e.touches?.[0]?.clientY);
  };
  overlay.addEventListener('mousemove', move);
  overlay.addEventListener('touchstart', move, { passive: true });
  overlay.addEventListener('touchmove', move, { passive: true });
  const leave = () => { hideTip(); hoverLine.setAttribute('opacity', 0); hoverDot.setAttribute('opacity', 0); };
  overlay.addEventListener('mouseleave', leave);
  overlay.addEventListener('touchend', leave);

  container.appendChild(svg);
}

/* ── Saturation index gauge ──────────────────────────────────────────── */

/* The LSI scale is diverging around zero: corrosive on one side, scaling on
 * the other, balanced in the middle. A linear gauge reads better than a dial. */
export function lsiGauge(container, lsi) {
  container.innerHTML = '';
  const W = Math.max(container.clientWidth || 400, 240);
  const H = 78;
  const M = { left: 10, right: 10 };
  const iw = W - M.left - M.right;
  const lo = -1.0, hi = 1.0;
  const X = v => M.left + ((Math.max(lo, Math.min(hi, v)) - lo) / (hi - lo)) * iw;

  const svg = el('svg', { class: 'chart', viewBox: `0 0 ${W} ${H}`, width: '100%', height: H,
    role: 'img', 'aria-label': `Saturation index ${lsi === null ? 'not available' : lsi.toFixed(2)}` });

  const y = 34, h = 12;
  const zones = [
    { from: lo, to: -0.5, color: 'var(--critical)', label: 'Corrosive' },
    { from: -0.5, to: -0.3, color: 'var(--serious)', label: '' },
    { from: -0.3, to: 0.3, color: 'var(--good)', label: 'Balanced' },
    { from: 0.3, to: 0.5, color: 'var(--serious)', label: '' },
    { from: 0.5, to: hi, color: 'var(--critical)', label: 'Scaling' },
  ];
  for (const z of zones) {
    // 2px gap between zones so the boundaries read as distinct segments.
    svg.appendChild(el('rect', { x: X(z.from) + 1, y, width: Math.max(0, X(z.to) - X(z.from) - 2), height: h,
      rx: 3, fill: z.color, opacity: 0.34 }));
  }
  svg.appendChild(el('text', { class: 'tick', x: X(-0.65), y: y + h + 15, 'text-anchor': 'middle' }, 'Corrosive'));
  svg.appendChild(el('text', { class: 'tick', x: X(0), y: y + h + 15, 'text-anchor': 'middle' }, 'Balanced'));
  svg.appendChild(el('text', { class: 'tick', x: X(0.65), y: y + h + 15, 'text-anchor': 'middle' }, 'Scaling'));
  for (const t of [-0.5, -0.3, 0.3, 0.5]) {
    svg.appendChild(el('text', { class: 'tick', x: X(t), y: y - 6, 'text-anchor': 'middle' }, t > 0 ? `+${t}` : `${t}`));
  }

  if (lsi !== null && lsi !== undefined) {
    const x = X(lsi);
    svg.appendChild(el('path', { d: `M${x},${y - 3} l-5,-7 l10,0 Z`, fill: 'var(--fg)' }));
    svg.appendChild(el('rect', { x: x - 1.5, y: y - 2, width: 3, height: h + 4, rx: 1.5, fill: 'var(--fg)' }));
    svg.appendChild(el('text', {
      x: Math.max(26, Math.min(W - 26, x)), y: 16, 'text-anchor': 'middle',
      fill: 'var(--fg)', 'font-size': 15, 'font-weight': 660,
    }, (lsi > 0 ? '+' : '') + lsi.toFixed(2)));
  } else {
    svg.appendChild(el('text', { x: W / 2, y: 16, 'text-anchor': 'middle', fill: 'var(--fg-dim)', 'font-size': 12 },
      'Not enough readings'));
  }
  container.appendChild(svg);
}

/* ── Cadence heatmap ─────────────────────────────────────────────────── */

export function cadenceStrip(container, months) {
  container.innerHTML = '';
  if (!months || !months.length) { container.innerHTML = '<div class="empty small">No tests recorded yet.</div>'; return; }
  const maxC = Math.max(...months.map(m => m.count), 1);
  const wrap = document.createElement('div');
  wrap.style.cssText = 'display:flex;gap:3px;flex-wrap:wrap;align-items:flex-end';
  for (const m of months) {
    const cell = document.createElement('div');
    // Sequential encoding: one hue, opacity carries magnitude.
    const intensity = 0.18 + (m.count / maxC) * 0.82;
    cell.style.cssText = `width:22px;height:${14 + (m.count / maxC) * 26}px;border-radius:4px;background:var(--series-1);opacity:${intensity};cursor:pointer`;
    cell.setAttribute('title', `${fmt.month(m.month)}: ${m.count} test${m.count === 1 ? '' : 's'}`);
    cell.addEventListener('mousemove', e => showTip(
      `<div class="tt-title">${fmt.month(m.month)}</div><div class="tt-row"><strong>${m.count}</strong> test${m.count === 1 ? '' : 's'}</div>`,
      e.clientX, e.clientY));
    cell.addEventListener('mouseleave', hideTip);
    wrap.appendChild(cell);
  }
  container.appendChild(wrap);
}

/* ── Legend ──────────────────────────────────────────────────────────── */

export function legend(container, items) {
  container.innerHTML = items.map(i =>
    `<span class="key"><span class="swatch" style="background:${i.color}"></span>${escapeHtml(i.label)}</span>`).join('');
}

export function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/* Charts are sized from clientWidth, so they must be redrawn on resize. */
const redrawers = new Set();
export function onResize(fn) { redrawers.add(fn); }
let resizeTimer;
window.addEventListener('resize', () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => redrawers.forEach(f => { try { f(); } catch { /* a stale view is not fatal */ } }), 180);
});
