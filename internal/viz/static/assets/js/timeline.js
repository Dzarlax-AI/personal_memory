// Timeline tab: facts plotted by created_at, grouped by namespace.

async function loadTimeline() {
  if (!factsData) {
    try {
      await loadFacts();
    } catch (e) {
      document.getElementById('timeline-container').innerHTML =
        `<div class="empty-state">Failed to load timeline: ${escapeHtml(e.message)}</div>`;
      return;
    }
  }
  const nodes = factsData.nodes.filter(n => n.created_at);
  const namespaces = [...new Set(nodes.map(n => n.namespace))].sort();
  const groups = new vis.DataSet();
  namespaces.forEach(ns => groups.add({ id: ns, content: escapeHtml(ns), style: `color:${nsColor(ns)};` }));

  const items = new vis.DataSet(nodes.map((n, i) => ({
    id: i,
    content: escapeHtml(n.text.length > 80 ? n.text.slice(0, 80) + '...' : n.text),
    title: escapeHtml(n.text),
    start: n.created_at,
    group: n.namespace,
    style: `background-color:${nsColor(n.namespace)};color:#0d1117;border-color:${nsColor(n.namespace)};`,
  })));

  const container = document.getElementById('timeline-container');
  timeline = new vis.Timeline(container, items, groups, {
    stack: true, showCurrentTime: true,
    zoomMin: 86400000, zoomMax: 86400000 * 365,
    orientation: 'top', margin: { item: 4 },
    tooltip: { followMouse: true, overflowMethod: 'cap' },
  });
  timeline.fit();
}
