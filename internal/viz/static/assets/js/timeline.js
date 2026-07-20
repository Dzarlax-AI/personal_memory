// Timeline tab: facts plotted by created_at, grouped by namespace.

let timelinePromise = null;

async function loadTimeline() {
  if (timeline) return timeline;
  if (timelinePromise) return timelinePromise;
  const container = document.getElementById('timeline-container');
  container.innerHTML = '<div class="loading"><div class="spinner"></div>Loading timeline...</div>';
  timelinePromise = (async () => {
    if (!factsData) await loadFacts();
    const nodes = factsData.nodes.filter(node => node.created_at);
    if (nodes.length === 0) {
      container.innerHTML = '<div class="empty-state">No facts with creation dates are available for the timeline.</div>';
      return null;
    }
    if (!window.vis?.Timeline || !window.vis?.DataSet) throw new Error('Timeline library is unavailable');
    const namespaces = [...new Set(nodes.map(node => normalizeNamespace(node.namespace)))].sort();
    const groups = new vis.DataSet();
    namespaces.forEach(ns => groups.add({ id: ns, content: escapeHtml(ns), style: `color:${nsColor(ns)};` }));
    const items = new vis.DataSet(nodes.map((node, index) => ({
      id: index,
      content: escapeHtml(factText(node).length > 80 ? factText(node).slice(0, 80) + '...' : factText(node)),
      title: escapeHtml(factText(node)),
      start: node.created_at,
      group: normalizeNamespace(node.namespace),
      style: `background-color:${nsColor(node.namespace)};color:#0d1117;border-color:${nsColor(node.namespace)};`,
    })));
    timeline = new vis.Timeline(container, items, groups, {
      stack: true, showCurrentTime: true,
      zoomMin: 86400000, zoomMax: 86400000 * 365,
      orientation: 'top', margin: { item: 4 },
      tooltip: { followMouse: true, overflowMethod: 'cap' },
    });
    timeline.fit();
    return timeline;
  })().catch(error => {
    timeline = null;
    renderRetry(container, `Failed to load timeline: ${error.message || error}`, loadTimeline);
    return null;
  }).finally(() => {
    timelinePromise = null;
  });
  return timelinePromise;
}
