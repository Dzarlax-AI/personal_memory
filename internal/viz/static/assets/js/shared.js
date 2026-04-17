// Shared state and helpers used by every view. Loaded first so later
// scripts can reference these globals.

const BASE = '/viz';

const NS_COLORS = {};
const PALETTE = ['#58a6ff','#f78166','#7ee787','#d2a8ff','#ffa657','#79c0ff','#ff7b72','#56d364','#bc8cff','#e3b341'];
let colorIdx = 0;
function nsColor(ns) {
  if (!NS_COLORS[ns]) NS_COLORS[ns] = PALETTE[colorIdx++ % PALETTE.length];
  return NS_COLORS[ns];
}

const PROJECT_TAGS = new Set([
  'personal-assistant','personal-memory','personal-ai-stack','rss-summariser',
  'health','finance','clinics','meta-enricher','city-dashboard','homedash',
  'jerkyvault','spotify-mcp','todoist-bot','insights','pm-enforcement','clui-cc'
]);

function getProjectTag(tags) {
  for (const t of (tags || [])) {
    if (PROJECT_TAGS.has(t)) return t;
  }
  return null;
}

function escapeHtml(s) {
  return (s || '').replace(/[&<>"']/g, c => ({
    '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'
  }[c]));
}

// Cross-view state. Mutated by the view modules; read by init/router.
let factsData = null;
let graphDataCache = null;
let network = null;
let timeline = null;
let graphFilter = { namespace: '', projectTag: '' };
