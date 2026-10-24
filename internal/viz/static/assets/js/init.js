// Router + bootstrap. Loaded last so all view-module functions are defined.

const VIEWS = ['memory', 'overview', 'duplicates', 'forgotten', 'timeline', 'graph', 'quality', 'quality-maintenance', 'documents', 'ai-review'];

function parseTabFromPath() {
  const path = window.location.pathname;
  const rel = path.startsWith(BASE) ? path.slice(BASE.length) : path;
  const segments = rel.split('/').filter(Boolean);
  const tab = segments[0] || 'memory';
  if (tab === 'quality' && new URLSearchParams(window.location.search).get('check') === 'maintenance') return 'quality-maintenance';
  return VIEWS.includes(tab) ? tab : 'memory';
}

function sectionForView(name) {
  if (['duplicates', 'forgotten', 'quality', 'quality-maintenance'].includes(name)) return 'quality';
  if (name === 'ai-review') return 'ai-review';
  if (name === 'documents') return 'documents';
  return 'memory';
}

function isTabAvailable(name) {
  const el = document.querySelector(`.primary-nav-item[data-section="${sectionForView(name)}"]`);
  return Boolean(el && getComputedStyle(el).display !== 'none');
}

function activateTab(name, pushHistory = true) {
  if (!VIEWS.includes(name)) name = 'memory';
  if (!isTabAvailable(name)) name = 'memory';
  const section = sectionForView(name);
  document.querySelectorAll('.primary-nav-item').forEach(item => {
    const active = item.dataset.section === section;
    item.classList.toggle('active', active);
    if (active) item.setAttribute('aria-current', 'page');
    else item.removeAttribute('aria-current');
  });
  document.querySelectorAll('.tab-nav-item').forEach(item => {
    const active = item.dataset.view === name;
    item.classList.toggle('active', active);
    if (active) item.setAttribute('aria-current', 'page');
    else item.removeAttribute('aria-current');
  });
  document.querySelectorAll('.view').forEach(view => view.classList.remove('active'));
  const viewName = name === 'quality-maintenance' ? 'quality' : (name === 'quality' ? 'duplicates' : name);
  const view = document.getElementById(viewName + '-view');
  if (view) view.classList.add('active');
  document.getElementById('memory-subnav').classList.toggle('section-open', section === 'memory');
  document.getElementById('quality-subnav').classList.toggle('section-open', section === 'quality');
  document.getElementById('primary-nav').classList.remove('open');
  document.getElementById('mobile-menu-toggle').setAttribute('aria-expanded', 'false');

  if (name === 'memory' && !factsData) initFacts();
  if (name === 'overview' && !factsData) initFacts();
  if ((name === 'duplicates' || name === 'quality') && !duplicatesLoaded && !duplicatesPromise) loadDuplicates();
  if (name === 'forgotten' && !factsData) loadForgotten();
  if (name === 'timeline' && !timeline && !timelinePromise) loadTimeline();
  if (name === 'graph' && !graphLoaded && !graphPromise) loadGraph();
  if (name === 'quality-maintenance') {
    document.getElementById('maintenance-status-filter').value = 'quarantined';
    loadQuarantinedFacts();
  }
  if (name === 'documents' && !documentsPromise && (!documentsData || documentsAreStale())) {
    loadDocuments(Boolean(documentsData && documentsAreStale()));
  }
  if (name === 'ai-review' && !aiReviewLoaded) loadAIReview();

  if (pushHistory) {
    const href = name === 'quality-maintenance'
      ? `${BASE}/quality?check=maintenance` : `${BASE}/${name}`;
    if (`${window.location.pathname}${window.location.search}` !== href) window.history.pushState({ tab: name }, '', href);
  }
}

// Keep real links available for modified clicks and opening in a new tab.
document.querySelectorAll('a[data-view]').forEach(link => {
  link.addEventListener('click', event => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.target === '_blank') return;
    event.preventDefault();
    activateTab(link.dataset.view);
  });
});

window.addEventListener('popstate', () => activateTab(parseTabFromPath(), false));
activateTab(parseTabFromPath(), false);
initFacts();
loadDocumentStatus().catch(() => {});
