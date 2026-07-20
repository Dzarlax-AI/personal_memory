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
  const keys = Array.isArray(fact.payload_keys) && fact.payload_keys.length > 0
    ? ` Payload keys: ${fact.payload_keys.join(', ')}.`
    : '';
  return `No text payload stored for point ${fact.id || 'unknown'}.${keys}`;
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
let graphFilter = { namespace: '', projectTag: '', primaryTag: '', text: '' };
