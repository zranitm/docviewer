// Demo UI for the docviewer API. Plain JavaScript, no build step. Everything
// rendered from API data goes through esc().
(() => {
  'use strict';

  // ---- configuration: same host by default, each base overridable ----------
  const params = new URLSearchParams(location.search);
  const host = location.hostname || 'localhost';
  const base = (key, port) => (params.get(key) || `${location.protocol === 'file:' ? 'http:' : location.protocol}//${host}:${port}`).replace(/\/$/, '');
  const API = base('api', 8080);
  const GRAFANA = base('grafana', 3000);
  const MOCKS = [
    { name: 'sales', label: 'Sales system', base: base('sales', 8081) },
    { name: 'service', label: 'Service system', base: base('service', 8082) },
  ];
  const FIXTURES = [
    { vin: '1M8GDM9AXKP042788', note: 'both systems, 5 + 5' },
    { vin: 'WVWZZZ1JZXW000002', note: 'Sales only' },
    { vin: 'JH4KA7561PC000003', note: 'Service only' },
    { vin: '5YJ3E1EA7KF000004', note: '128 documents, many pages' },
    { vin: '1HGCM82633A000005', note: 'no documents' },
  ];
  const PRESETS = [
    { key: 'normal', label: 'Normal', fault: {} },
    { key: 'slow', label: 'Slow 2.5 s', fault: { latency_ms: 2500 } },
    { key: 'flaky', label: 'Flaky 50%', fault: { error_rate: 0.5 } },
    { key: 'e503', label: 'Error 503', fault: { status: 503 } },
    { key: 'e403', label: 'Reject 403', fault: { status: 403 } },
    { key: 'hang', label: 'Hang', fault: { hang: true } },
  ];
  const TYPE_ORDER = ['purchase_agreement', 'sales_invoice', 'finance_contract', 'registration', 'pre_delivery_inspection', 'repair_order', 'service_invoice', 'inspection_report', 'recall_notice'];
  const STATUS_WORDS = { ok: 'answered', timeout: 'timed out', error: 'failed', done: 'nothing more', unavailable: 'unavailable' };

  // ---- helpers -------------------------------------------------------------
  const $ = id => document.getElementById(id);
  const esc = s => String(s).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const typeName = t => t === 'other' ? 'Other' : t.replace(/_/g, ' ').replace(/^\w/, c => c.toUpperCase());
  const typeClass = t => { const i = TYPE_ORDER.indexOf(t); return i < 0 ? '' : ` t${i}`; };
  const pad = n => String(n).padStart(2, '0');
  const fmtDay = d => `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
  const fmtTime = d => (d.getUTCHours() || d.getUTCMinutes()) ? `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())} UTC` : '';
  const monthName = d => d.toLocaleString('en', { month: 'long', year: 'numeric', timeZone: 'UTC' });
  const clock = () => new Date().toTimeString().slice(0, 8);
  const grafanaTrace = id => `${GRAFANA}/explore?schemaVersion=1&panes=${encodeURIComponent(JSON.stringify({ a: { datasource: 'tempo', queries: [{ refId: 'A', queryType: 'traceql', query: id }] } }))}`;

  let toastTimer;
  function toast(text) {
    const el = $('toast');
    el.textContent = text;
    el.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.remove('show'), 1600);
  }
  async function copy(text, what) {
    try { await navigator.clipboard.writeText(text); toast(`${what} copied`); } catch { toast('Copy failed'); }
  }

  // ---- state ----------------------------------------------------------------
  const state = {
    vin: '', limit: 20, cursor: null, pages: 0, loading: false,
    retryable: false,   // the last answer was a 502 that a retry may fix
    docs: [],           // flattened rows: {date, source, id, url, type, title, summary}
    sources: [],        // latest per-source statuses
    hidden: { sources: new Set(), types: new Set() },
    text: '',
    trace: '',
    log: [],
  };

  // ---- theme ----------------------------------------------------------------
  const root = document.documentElement;
  try { const saved = params.get('theme') || localStorage.getItem('theme'); if (saved === 'light' || saved === 'dark') root.dataset.theme = saved; } catch { /* storage unavailable */ }
  $('theme').addEventListener('click', () => {
    const dark = root.dataset.theme ? root.dataset.theme === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
    root.dataset.theme = dark ? 'light' : 'dark';
    try { localStorage.setItem('theme', root.dataset.theme); } catch { /* ignore */ }
  });

  // ---- header, fixtures -------------------------------------------------------
  $('api-base').textContent = API;
  $('grafana-link').href = `${GRAFANA}/`;
  $('fixtures').innerHTML = FIXTURES.map(f =>
    `<button type="button" class="chip" data-vin="${esc(f.vin)}"><code>${esc(f.vin)}</code><small>${esc(f.note)}</small></button>`).join('');
  $('fixtures').addEventListener('click', e => {
    const chip = e.target.closest('.chip');
    if (!chip) return;
    $('vin').value = chip.dataset.vin;
    $('search').requestSubmit();
  });

  // ---- VIN hint (the API is the judge; this only guides typing) --------------
  const vinInput = $('vin');
  function vinHint() {
    const v = vinInput.value.trim().toUpperCase();
    const hint = $('vin-hint');
    hint.className = 'hint';
    if (!v) { hint.textContent = '17 letters and digits; I, O and Q are never used in a VIN.'; return; }
    const bad = v.match(/[IOQ]/g);
    if (bad) { hint.textContent = `${bad.join(', ')} cannot appear in a VIN.`; hint.className = 'hint bad'; return; }
    if (/[^A-Z0-9]/.test(v)) { hint.textContent = 'Only letters and digits.'; hint.className = 'hint bad'; return; }
    hint.textContent = v.length === 17 ? 'Looks like a VIN. Press Enter.' : `${v.length} of 17 characters.`;
  }
  vinInput.addEventListener('input', vinHint);
  vinHint();
  document.addEventListener('keydown', e => {
    if (e.key === '/' && !/input|select|textarea/i.test(document.activeElement.tagName)) { e.preventDefault(); vinInput.focus(); vinInput.select(); }
  });

  // ---- notices -----------------------------------------------------------------
  function notice(kind, title, meta) {
    const slot = $('notice');
    if (!title) { slot.innerHTML = ''; return; }
    const icon = { bad: '⚠', warn: '◔', info: 'ⓘ' }[kind] || '';
    slot.innerHTML = `<div class="notice ${kind}"><span class="n-icon" aria-hidden="true">${icon}</span><div class="n-body"><div class="n-title">${esc(title)}</div>${meta ? `<div class="n-meta">${meta}</div>` : ''}</div></div>`;
  }
  $('notice').addEventListener('click', e => {
    const b = e.target.closest('[data-copy]');
    if (b) copy(b.dataset.copy, b.dataset.what || 'Text');
    if (e.target.closest('[data-retry]')) fetchPage();
  });

  // ---- status strip -----------------------------------------------------------
  function renderStatus() {
    const sec = $('status');
    if (!state.sources.length && !state.docs.length) { sec.hidden = true; return; }
    sec.hidden = false;
    const counts = {};
    for (const r of state.docs) counts[r.source] = (counts[r.source] || 0) + 1;
    $('sources').innerHTML = state.sources.map(s => `
      <div class="src-card ${esc(s.status)}" title="${esc(s.status)}">
        <span class="dot" aria-hidden="true"></span>
        <div><div class="src-name">${esc(s.source)}</div><div class="src-word">${esc(STATUS_WORDS[s.status] || s.status)}</div></div>
        <span class="src-count">${counts[s.source] || 0} loaded</span>
      </div>`).join('');
    const shown = visibleRows().length;
    const filtered = shown !== state.docs.length ? ` · showing ${shown}` : '';
    $('summary').textContent = `${state.docs.length} document${state.docs.length === 1 ? '' : 's'} loaded${filtered} · ${state.pages} page${state.pages === 1 ? '' : 's'}${state.cursor ? ' · more available' : state.pages ? ' · end of list' : ''}`;
    $('trace').innerHTML = state.trace
      ? `trace <code>${esc(state.trace)}</code> <button type="button" class="linkish" data-copy="${esc(state.trace)}" data-what="Trace ID">copy</button> <a href="${esc(grafanaTrace(state.trace))}" target="_blank" rel="noopener">open in Grafana</a>`
      : '';
    // The failure explanation the API's statuses imply, when the page itself succeeded.
    const failed = state.sources.filter(s => ['timeout', 'error', 'unavailable'].includes(s.status));
    if (failed.length && !$('notice').querySelector('.bad')) {
      const transient = failed.filter(s => s.status !== 'unavailable').map(s => s.source);
      const permanent = failed.filter(s => s.status === 'unavailable').map(s => s.source);
      const parts = [];
      if (transient.length) parts.push(`${transient.join(' and ')} did not answer in time; its documents may appear on later pages.`);
      if (permanent.length) parts.push(`${permanent.join(' and ')} rejected the request; its documents are missing from this list.`);
      notice('warn', 'Partial result', esc(parts.join(' ')));
    }
  }
  $('trace').addEventListener('click', e => { const b = e.target.closest('[data-copy]'); if (b) copy(b.dataset.copy, b.dataset.what); });

  // ---- filters -----------------------------------------------------------------
  function visibleRows() {
    const q = state.text.trim().toLowerCase();
    return state.docs.filter(r => !state.hidden.sources.has(r.source) && !state.hidden.types.has(r.type) &&
      (!q || r.title.toLowerCase().includes(q) || (r.summary || '').toLowerCase().includes(q)));
  }
  function renderFilters() {
    const sec = $('filters');
    if (!state.docs.length) { sec.hidden = true; return; }
    sec.hidden = false;
    const count = key => state.docs.reduce((m, r) => (m[r[key]] = (m[r[key]] || 0) + 1, m), {});
    const bySource = count('source'), byType = count('type');
    const toggle = (kind, value, label, n, cls) => `<label class="toggle"><input type="checkbox" data-kind="${kind}" value="${esc(value)}"${state.hidden[kind].has(value) ? '' : ' checked'}><span class="badge ${cls}">${esc(label)}</span><span class="n">${n}</span></label>`;
    $('source-filters').innerHTML = Object.keys(bySource).sort().map(s => toggle('sources', s, s, bySource[s], esc(s))).join('');
    $('type-filters').innerHTML = Object.keys(byType).sort((a, b) => byType[b] - byType[a] || a.localeCompare(b)).map(t => toggle('types', t, typeName(t), byType[t], typeClass(t).trim())).join('');
  }
  $('filters').addEventListener('change', e => {
    const box = e.target.closest('input[type=checkbox]');
    if (!box) return;
    const set = state.hidden[box.dataset.kind];
    box.checked ? set.delete(box.value) : set.add(box.value);
    renderDocs(); renderStatus();
  });
  $('text-filter').addEventListener('input', e => { state.text = e.target.value; renderDocs(); renderStatus(); });

  // ---- results -----------------------------------------------------------------
  function renderDocs() {
    const el = $('results');
    const rows = visibleRows();
    if (!rows.length) {
      el.innerHTML = state.docs.length
        ? `<div class="empty">Nothing matches the current filters.</div>`
        : state.pages && !state.cursor ? `<div class="empty">No documents found for <strong>${esc(state.vin)}</strong> in either system.</div>` : '';
      return;
    }
    const groups = new Map();
    for (const r of rows) {
      const key = `${r.date.getUTCFullYear()}-${pad(r.date.getUTCMonth() + 1)}`;
      if (!groups.has(key)) groups.set(key, { label: monthName(r.date), rows: [] });
      groups.get(key).rows.push(r);
    }
    el.innerHTML = [...groups.values()].map(g => `
      <section class="group">
        <div class="group-head"><span>${esc(g.label)}</span><span class="n">${g.rows.length}</span></div>
        ${g.rows.map(r => `
          <article class="row">
            <div class="date">${esc(fmtDay(r.date))}${fmtTime(r.date) ? `<small>${esc(fmtTime(r.date))}</small>` : ''}</div>
            <div>
              <div class="title">${esc(r.title)}</div>
              ${r.summary ? `<div class="summary">${esc(r.summary)}</div>` : ''}
              <div class="badges"><span class="badge ${esc(r.source)}">${esc(r.source)}</span><span class="badge${typeClass(r.type)}">${esc(typeName(r.type))}</span><span class="badge" title="Document ID">${esc(r.id)}</span></div>
            </div>
            <a class="open" href="${esc(r.url)}" target="_blank" rel="noopener">Open ↗</a>
          </article>`).join('')}
      </section>`).join('');
  }
  function renderSkeleton(n) {
    $('results').insertAdjacentHTML('beforeend', `<section class="group skeleton" id="skeleton"><div class="group-head"><span class="sk" style="width:120px"></span></div>${Array.from({ length: n }, () => `<article class="row"><div><span class="sk" style="width:80px"></span></div><div><span class="sk" style="width:60%"></span><span class="sk" style="width:40%;margin-top:8px"></span></div><div><span class="sk" style="width:44px"></span></div></article>`).join('')}</section>`);
  }
  const removeSkeleton = () => $('skeleton')?.remove();

  // ---- request log -------------------------------------------------------------
  function logRequest(entry) {
    state.log.unshift(entry);
    state.log.length = Math.min(state.log.length, 60);
    $('log-count').textContent = `· ${state.log.length}`;
    $('log').querySelector('tbody').innerHTML = state.log.map(l => `
      <tr>
        <td>${esc(l.at)}</td>
        <td class="req">${esc(l.req)}</td>
        <td class="st s${String(l.status)[0]}">${esc(l.status || 'network error')}</td>
        <td>${l.ms} ms</td>
        <td>${esc(l.sources || '')}</td>
        <td>${l.trace ? `<a href="${esc(grafanaTrace(l.trace))}" target="_blank" rel="noopener" title="${esc(l.trace)}">${esc(l.trace.slice(0, 8))}…</a>` : ''}</td>
      </tr>`).join('');
  }
  $('clear-log').addEventListener('click', () => { state.log = []; $('log-count').textContent = ''; $('log').querySelector('tbody').innerHTML = ''; });

  // ---- fetching ----------------------------------------------------------------
  function setLoading(on) {
    state.loading = on;
    $('go').disabled = on;
    const more = $('more');
    more.disabled = on;
    if (on) { more.textContent = 'Loading…'; renderSkeleton(Math.min(state.limit, 5)); } else removeSkeleton();
  }
  function syncURL() {
    const keep = {};
    for (const k of ['api', 'sales', 'service', 'grafana', 'theme']) if (params.get(k)) keep[k] = params.get(k);
    history.replaceState(null, '', `?${new URLSearchParams({ ...keep, vin: state.vin, limit: state.limit })}`);
  }

  async function fetchPage() {
    if (state.loading || !state.vin) return;
    setLoading(true);
    notice('', '');
    const url = new URL(`${API}/v1/vehicles/${encodeURIComponent(state.vin)}/documents`);
    url.searchParams.set('limit', state.limit);
    if (state.cursor) url.searchParams.set('cursor', state.cursor);
    const req = `GET ${url.pathname}?limit=${state.limit}${state.cursor ? '&cursor=…' : ''}`;
    const t0 = performance.now();
    try {
      const resp = await fetch(url);
      const ms = Math.round(performance.now() - t0);
      const body = await resp.json().catch(() => ({}));
      const trace = resp.headers.get('X-Trace-Id') || '';
      const sourcesWord = (body.sources || []).map(s => `${s.source} ${s.status}`).join(', ');
      logRequest({ at: clock(), req, status: resp.status, ms, trace, sources: sourcesWord + (body.documents ? ` · ${body.documents.length} docs` : '') });
      state.trace = trace;
      if (!resp.ok) {
        const code = body.error?.code || '';
        const title = body.error ? `${body.error.message}` : `HTTP ${resp.status}`;
        if (body.sources) state.sources = body.sources;
        const retryable = code === 'sources_unavailable' && /retry/.test(body.error.message);
        state.retryable = retryable;
        const meta = [
          code ? `<code>${esc(code)}</code>` : '',
          trace ? `trace <code>${esc(trace)}</code> <button type="button" class="linkish" data-copy="${esc(trace)}" data-what="Trace ID">copy</button> <a href="${esc(grafanaTrace(trace))}" target="_blank" rel="noopener">open in Grafana</a>` : '',
          retryable ? `<button type="button" class="linkish" data-retry>Retry now</button>` : '',
        ].filter(Boolean).join(' · ');
        setLoading(false);
        renderStatus();
        notice('bad', title, meta); // after the status strip, so the API's own message is what stays on screen
        if (code === 'cursor_outdated') state.cursor = null;
        $('more').hidden = !retryable;
        $('more').textContent = 'Retry';
        $('auto-wrap').hidden = true;
        return;
      }
      state.pages++;
      state.retryable = false;
      state.sources = body.sources || [];
      for (const d of body.documents || []) {
        for (const s of d.sources || []) {
          state.docs.push({ date: new Date(d.issued_at), source: s.source, id: s.id, url: s.url, type: d.type, title: d.title, summary: d.summary || '' });
        }
      }
      state.cursor = body.next_cursor || null;
      setLoading(false);
      renderFilters(); renderDocs(); renderStatus();
      $('more').hidden = !state.cursor;
      $('more').textContent = 'Load more';
      $('auto-wrap').hidden = !state.cursor;
      if (state.cursor && $('auto').checked) observeSentinel();
    } catch (e) {
      const ms = Math.round(performance.now() - t0);
      logRequest({ at: clock(), req, status: 0, ms });
      setLoading(false);
      notice('bad', `The API at ${API} could not be reached.`, esc(`${e.message}. Is it running, and is this page's origin in server.cors_origins?`));
      $('more').hidden = true;
    }
  }

  // Auto-load: fetch the next page when the sentinel scrolls into view.
  const io = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
    if (entries.some(en => en.isIntersecting) && $('auto').checked && state.cursor && !state.loading) fetchPage();
  }, { rootMargin: '200px' }) : null;
  function observeSentinel() { if (io) { io.unobserve($('sentinel')); io.observe($('sentinel')); } }
  $('auto').addEventListener('change', e => { if (e.target.checked) observeSentinel(); else io?.unobserve($('sentinel')); });

  function startSearch() {
    state.vin = vinInput.value.trim().toUpperCase();
    vinHint();
    state.limit = Number($('limit').value) || 20;
    Object.assign(state, { cursor: null, pages: 0, docs: [], sources: [], trace: '', text: '', retryable: false });
    state.hidden.sources.clear(); state.hidden.types.clear();
    $('text-filter').value = '';
    $('results').innerHTML = ''; $('sources').innerHTML = ''; $('summary').textContent = ''; $('trace').innerHTML = '';
    $('status').hidden = true; $('filters').hidden = true;
    $('more').hidden = true; $('auto-wrap').hidden = true;
    notice('', '');
    syncURL();
    fetchPage();
  }
  $('search').addEventListener('submit', e => { e.preventDefault(); startSearch(); });
  // Only a next page or a retry may be fetched from here: at the end of the
  // list a stray click must not fetch page one again and append duplicates.
  $('more').addEventListener('click', () => { if (state.cursor || state.retryable) fetchPage(); });
  $('rerun').addEventListener('click', () => { if (state.vin) startSearch(); else vinInput.focus(); });

  // ---- demo controls: the mocks' fault switches --------------------------------
  function sameFault(a, b) {
    const n = f => ({ latency_ms: f.latency_ms || 0, error_rate: f.error_rate || 0, hang: !!f.hang, status: f.status || 0 });
    return JSON.stringify(n(a)) === JSON.stringify(n(b));
  }
  function describeFault(f) {
    if (!f || sameFault(f, {})) return 'normal';
    const p = PRESETS.find(x => sameFault(x.fault, f));
    if (p) return p.label.toLowerCase();
    return [f.latency_ms ? `${f.latency_ms} ms latency` : '', f.error_rate ? `${Math.round(f.error_rate * 100)}% errors` : '', f.hang ? 'hang' : '', f.status ? `status ${f.status}` : ''].filter(Boolean).join(', ');
  }
  function renderMocks() {
    $('mocks').innerHTML = MOCKS.map(m => `
      <div class="mock" data-mock="${m.name}">
        <div class="mock-head"><strong>${esc(m.label)}</strong><span class="cur" aria-live="polite">…</span></div>
        <div class="presets">${PRESETS.map(p => `<button type="button" data-preset="${p.key}">${esc(p.label)}</button>`).join('')}</div>
        <div class="err"></div>
      </div>`).join('');
  }
  function showMock(m, fault, err) {
    const card = $('mocks').querySelector(`[data-mock="${m.name}"]`);
    const cur = card.querySelector('.cur');
    card.querySelector('.err').textContent = err || '';
    if (err) { cur.textContent = 'unreachable'; cur.className = 'cur'; return; }
    const word = describeFault(fault);
    cur.textContent = word;
    cur.className = word === 'normal' ? 'cur' : 'cur active';
    card.querySelectorAll('[data-preset]').forEach(b => b.classList.toggle('on', sameFault(PRESETS.find(p => p.key === b.dataset.preset).fault, fault)));
  }
  async function readFault(m) {
    try {
      const r = await fetch(`${m.base}/admin/faults`);
      showMock(m, await r.json());
    } catch (e) {
      showMock(m, null, `${m.base} did not answer (${e.message}).`);
    }
  }
  async function setFault(m, fault) {
    try {
      const r = await fetch(`${m.base}/admin/faults`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(fault) });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      showMock(m, await r.json());
      toast(`${m.label}: ${describeFault(fault)}`);
    } catch (e) {
      showMock(m, null, `Could not set the fault (${e.message}).`);
    }
  }
  renderMocks();
  $('mocks').addEventListener('click', e => {
    const b = e.target.closest('[data-preset]');
    if (!b) return;
    const m = MOCKS.find(x => x.name === b.closest('[data-mock]').dataset.mock);
    setFault(m, PRESETS.find(p => p.key === b.dataset.preset).fault);
  });
  $('reset-faults').addEventListener('click', () => MOCKS.forEach(m => setFault(m, {})));
  $('demo-panel').addEventListener('toggle', () => { if ($('demo-panel').open) MOCKS.forEach(readFault); });

  // ---- start from the URL ---------------------------------------------------------
  if (params.get('limit')) $('limit').value = params.get('limit');
  const initial = params.get('vin');
  if (initial) { vinInput.value = initial; vinHint(); startSearch(); }
})();
