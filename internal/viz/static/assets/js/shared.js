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

// Cross-view state. Mutated by the view modules; read by init/router.
let factsData = null;
let factsPromise = null;
let graphDataCache = null;
let network = null;
let timeline = null;
let graphFilter = { namespace: '', projectTag: '', text: '' };
