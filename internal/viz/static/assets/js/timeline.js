// Timeline tab: a recent, bounded visual range with a readable list alternative.

const TIMELINE_LIST_PAGE_SIZE = 50;
let timelinePromise = null;
let timelineNodes = [];
let timelineListNodes = [];
let timelineListShown = TIMELINE_LIST_PAGE_SIZE;

async function loadTimeline() {
  if (timelinePromise) return timelinePromise;
  const container = document.getElementById('timeline-container');
  const status = document.getElementById('timeline-status');
  container.replaceChildren();
  status.textContent = 'Loading timeline…';
  timelinePromise = (async () => {
    if (!factsData) await loadFacts();
    timelineNodes = factsData.nodes.filter(node => node.created_at && !Number.isNaN(Date.parse(node.created_at)));
    if (!timelineNodes.length) {
      status.textContent = 'No facts with creation dates are available for the timeline.';
      renderTimelineList([], true);
      return null;
    }
    if (!window.vis?.Timeline || !window.vis?.DataSet) throw new Error('Timeline library is unavailable');
    renderTimeline();
    return timeline;
  })().catch(error => {
    timeline = null;
    renderRetry(container, `Failed to load timeline: ${error.message || error}`, loadTimeline);
    status.textContent = 'Timeline could not be loaded.';
    return null;
  }).finally(() => { timelinePromise = null; });
  return timelinePromise;
}

function visibleTimelineNodes() {
  const range = document.getElementById('timeline-range').value;
  if (range === 'all') return timelineNodes;
  const days = Number(range) || 90;
  const cutoff = Date.now() - days * 86400000;
  return timelineNodes.filter(node => Date.parse(node.created_at) >= cutoff);
}

function selectedTimelineRangeLabel() {
  const select = document.getElementById('timeline-range');
  return select.options[select.selectedIndex]?.textContent || 'selected range';
}

function renderTimeline() {
  const container = document.getElementById('timeline-container');
  const status = document.getElementById('timeline-status');
  const nodes = visibleTimelineNodes();
  const rangeLabel = selectedTimelineRangeLabel();
  if (timeline) { timeline.destroy(); timeline = null; }
  if (!nodes.length) {
    container.replaceChildren();
    status.textContent = `No dated facts fall within ${rangeLabel.toLocaleLowerCase()}. ${timelineNodes.length} dated facts are available across all dates.`;
    renderTimelineList(nodes, true);
    return;
  }
  const namespaces = [...new Set(nodes.map(node => normalizeNamespace(node.namespace)))].sort();
  const groups = new vis.DataSet(namespaces.map(ns => ({ id: ns, content: escapeHtml(ns), style: `color:${nsColor(ns)};` })));
  const items = new vis.DataSet(nodes.map((node, index) => {
    const text = factText(node);
    return { id: index, content: escapeHtml(text.length > 80 ? `${text.slice(0, 80)}…` : text), title: escapeHtml(text), start: node.created_at, group: normalizeNamespace(node.namespace), style: `background-color:${nsColor(node.namespace)};color:#0d1117;border-color:${nsColor(node.namespace)};` };
  }));
  container.replaceChildren();
  timeline = new vis.Timeline(container, items, groups, {
    stack: true, showCurrentTime: true, zoomMin: 86400000, zoomMax: 86400000 * 365,
    orientation: 'top', margin: { item: 4 }, tooltip: { followMouse: true, overflowMethod: 'cap' },
  });
  const end = Date.now();
  const range = document.getElementById('timeline-range').value;
  const start = range === 'all'
    ? nodes.reduce((minimum, node) => Math.min(minimum, Date.parse(node.created_at)), Date.parse(nodes[0].created_at))
    : end - (Number(range) || 90) * 86400000;
  timeline.setWindow(start, end, { animation: false });
  status.textContent = `${nodes.length} dated facts shown for ${rangeLabel.toLocaleLowerCase()}; ${timelineNodes.length} dated facts are available across all dates.`;
  renderTimelineList(nodes, true);
}

function renderTimelineList(nodes, resetPage = false) {
  const list = document.getElementById('timeline-list-items');
  const more = document.getElementById('timeline-list-more');
  if (resetPage) timelineListShown = TIMELINE_LIST_PAGE_SIZE;
  timelineListNodes = nodes.slice().sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
  list.replaceChildren();
  timelineListNodes.slice(0, timelineListShown).forEach(node => {
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'timeline-list-item';
    button.textContent = `${(node.created_at || '').slice(0, 10)} · ${normalizeNamespace(node.namespace)} · ${factText(node)}`;
    button.addEventListener('click', () => showDetail(node.id, button));
    list.appendChild(button);
  });
  more.hidden = timelineListShown >= timelineListNodes.length;
  if (!more.hidden) more.textContent = `Load ${Math.min(TIMELINE_LIST_PAGE_SIZE, timelineListNodes.length - timelineListShown)} more events (${timelineListNodes.length - timelineListShown} remaining)`;
}

document.getElementById('timeline-range').addEventListener('change', () => {
  if (timelineNodes.length && window.vis?.Timeline) renderTimeline();
});
document.getElementById('timeline-list-toggle').addEventListener('click', event => {
  const list = document.getElementById('timeline-list');
  const expanded = event.currentTarget.getAttribute('aria-expanded') === 'true';
  event.currentTarget.setAttribute('aria-expanded', String(!expanded));
  event.currentTarget.textContent = expanded ? 'Show accessible list' : 'Hide accessible list';
  list.hidden = expanded;
});
document.getElementById('timeline-list-more').addEventListener('click', () => {
  timelineListShown += TIMELINE_LIST_PAGE_SIZE;
  renderTimelineList(timelineListNodes, false);
});
