// Shared state and helpers used by every view. Loaded first so later
// scripts can reference these globals.

const BASE = '/viz';
const UNCLASSIFIED_NAMESPACE = 'unclassified';
const UNCLASSIFIED_NAMESPACE_FILTER = '__missing__';

const NS_COLORS = {};
const PALETTE = ['#58a6ff','#f78166','#7ee787','#d2a8ff','#ffa657','#79c0ff','#ff7b72','#56d364','#bc8cff','#e3b341'];
let colorIdx = 0;
function nsColor(ns) {
  const key = normalizeNamespace(ns);
  if (!NS_COLORS[key]) NS_COLORS[key] = PALETTE[colorIdx++ % PALETTE.length];
  return NS_COLORS[key];
}

function tagsList(tags) {
  return Array.isArray(tags)
    ? tags.map(t => typeof t === 'string' ? t.trim() : '').filter(Boolean)
    : [];
}

// Old imports occasionally stored a tag as a quoted JSON-ish value. Keep the
// source value for API filters, but make the human-facing label predictable.
function normalizeTagDisplay(value) {
  let normalized = typeof value === 'string' ? value.trim() : '';
  if (!normalized) return '';
  if (normalized.startsWith('[') && normalized.endsWith(']')) {
    normalized = normalized.slice(1, -1).trim();
  }
  if ((normalized.startsWith('"') && normalized.endsWith('"')) ||
      (normalized.startsWith("'") && normalized.endsWith("'"))) {
    normalized = normalized.slice(1, -1).trim();
  }
  return normalized || value.trim();
}

function tagOptions(nodes) {
  const originalsByDisplay = new Map();
  nodes.flatMap(node => tagsList(node.tags)).forEach(original => {
    const display = normalizeTagDisplay(original);
    if (!display) return;
    if (!originalsByDisplay.has(display)) originalsByDisplay.set(display, new Set());
    originalsByDisplay.get(display).add(original);
  });
  return [...originalsByDisplay.entries()].flatMap(([normalized, originals]) => {
    const exact = [...originals].sort();
    return exact.map(original => ({
      original,
      // Never map a collision to an arbitrary stored tag. The raw value is
      // included only when a human needs to choose between legacy variants.
      display: exact.length === 1 ? normalized : `${normalized} (${original})`,
    }));
  }).sort((a, b) => a.display.localeCompare(b.display));
}

function setTagDatalist(inputID, listID, options, selectedOriginal = '') {
  const input = document.getElementById(inputID);
  const list = document.getElementById(listID);
  list.innerHTML = '';
  const displayByOriginal = new Map();
  options.forEach(({ display, original }) => {
    const option = document.createElement('option');
    option.value = display;
    list.appendChild(option);
    displayByOriginal.set(original, display);
  });
  input.dataset.originalByDisplay = JSON.stringify(Object.fromEntries(options.map(o => [o.display, o.original])));
  input.value = displayByOriginal.get(selectedOriginal) || selectedOriginal || '';
}

function originalTagFilter(input) {
  const value = input.value.trim();
  if (!value) return '';
  try {
    return JSON.parse(input.dataset.originalByDisplay || '{}')[value] || value;
  } catch (_) {
    return value;
  }
}

function primaryTag(fact) {
  const primary = typeof fact.primary_tag === 'string' ? fact.primary_tag.trim() : '';
  return primary && tagsList(fact.tags).includes(primary) ? primary : null;
}

function normalizeNamespace(ns) {
  if (ns === null || ns === undefined || ns === '' || ns === 'null') return UNCLASSIFIED_NAMESPACE;
  return String(ns);
}

function graphNamespaceFilter(ns) {
  return normalizeNamespace(ns) === UNCLASSIFIED_NAMESPACE ? UNCLASSIFIED_NAMESPACE_FILTER : normalizeNamespace(ns);
}

function matchesNamespaceFilter(nodeNamespace, filter) {
  if (!filter) return true;
  return graphNamespaceFilter(nodeNamespace) === filter;
}

function escapeHtml(s) {
  return String(s || '').replace(/[&<>"']/g, c => ({
    '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'
  }[c]));
}

function escapeAttr(s) {
  return escapeHtml(String(s || ''));
}

function factText(fact) {
  const text = typeof fact.text === 'string' ? fact.text.trim() : '';
  if (text) return text;
  return `No text stored for point ${fact.id || 'unknown'}.`;
}

const LIFECYCLE_STATES = new Set(['current', 'historical', 'superseded', 'disputed', 'invalid']);

function normalizedLifecycle(fact) {
  const value = fact && typeof fact.lifecycle === 'object' && fact.lifecycle ? fact.lifecycle : {};
  let state = typeof value.state === 'string' ? value.state.toLowerCase().trim() : '';
  if (value.valid === false || !LIFECYCLE_STATES.has(state)) state = value.valid === false ? 'invalid' : 'current';
  return {
    ...value,
    state,
    valid: value.valid !== false,
    legacy: Boolean(value.legacy),
    canonical: Boolean(value.canonical),
    supersedes: Array.isArray(value.supersedes) ? value.supersedes.map(String) : [],
    superseded_by: Array.isArray(value.superseded_by) ? value.superseded_by.map(String) : [],
  };
}

function lifecycleBadgeHTML(fact) {
  const lifecycle = normalizedLifecycle(fact);
  const labels = {
    current: '● Current', historical: '◷ Historical', superseded: '↘ Superseded',
    disputed: '⚠ Disputed', invalid: '! Invalid metadata',
  };
  return `<span class="lifecycle-badge lifecycle-${escapeAttr(lifecycle.state)}">${escapeHtml(labels[lifecycle.state])}</span>`;
}

function authoritySignals(fact) {
  const lifecycle = normalizedLifecycle(fact);
  const signals = [];
  if (!lifecycle.valid) return signals;
  if (lifecycle.canonical) signals.push({ key: 'canonical', label: 'Canonical' });
  if (typeof lifecycle.verified_at === 'string' && lifecycle.verified_at) signals.push({ key: 'verified', label: 'Verified' });
  if (lifecycle.legacy) signals.push({ key: 'legacy', label: 'Legacy' });
  if (lifecycle.provenance && lifecycle.provenance.source_present) signals.push({ key: 'provenance', label: 'Has provenance' });
  return signals;
}

function authorityBadgesHTML(fact) {
  return authoritySignals(fact).map(signal =>
    `<span class="authority-badge authority-${escapeAttr(signal.key)}">${escapeHtml(signal.label)}</span>`
  ).join('');
}

function authoritySignalSummary(fact) {
  const labels = authoritySignals(fact).map(signal => signal.label);
  return labels.length ? labels.join(', ') : 'No authority signals';
}

function provenanceDisplayHTML(fact) {
  const provenance = normalizedLifecycle(fact).provenance;
  if (!provenance || !provenance.source_present) return '<span>Not recorded</span>';
  const source = provenance.source_redacted
    ? 'Source hidden'
    : provenance.source ? `Source: ${provenance.source}` : 'Source recorded';
  let reference = '';
  if (provenance.has_reference) {
    reference = provenance.reference_redacted || !provenance.reference
      ? 'Reference hidden'
      : `Reference: ${provenance.reference}`;
  }
  return [source, reference].filter(Boolean).map(value => `<span>${escapeHtml(value)}</span>`).join('');
}

function matchesAuthorityFilter(fact, filter) {
  if (!filter) return true;
  return authoritySignals(fact).some(signal => signal.key === filter || (filter === 'has-provenance' && signal.key === 'provenance'));
}

async function responseMessage(res) {
  const body = await res.text();
  if (!body) return `HTTP ${res.status}`;
  try {
    const data = JSON.parse(body);
    return data.error || data.message || body;
  } catch (_) {
    return body.trim() || `HTTP ${res.status}`;
  }
}

function renderRetry(container, message, onRetry) {
  container.replaceChildren();
  const state = document.createElement('div');
  state.className = 'empty-state';
  state.textContent = message;
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'toolbar-btn';
  button.textContent = 'Retry';
  button.addEventListener('click', onRetry);
  state.appendChild(document.createTextNode(' '));
  state.appendChild(button);
  container.appendChild(state);
}

function renderFactsFailure(container, context, retry) {
  renderRetry(container, `Failed to load facts for ${context}.`, retry);
}

// Cross-view state. Mutated by the view modules; read by init/router.
let factsData = null;
let factsPromise = null;
let graphDataCache = null;
let network = null;
let timeline = null;
let graphFilter = { namespace: '', projectTag: '', primaryTag: '', text: '', lifecycle: '', authority: '' };
