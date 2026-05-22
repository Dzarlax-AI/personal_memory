// Graph tab: force-directed network of facts with similarity edges.

let selectedFact = null;

async function loadGraph() {
  const threshold = document.getElementById('threshold').value;
  const selectedNamespace = graphFilter.namespace || document.getElementById('ns-filter').value;
  const selectedTag = graphFilter.projectTag || document.getElementById('tag-filter').value;
  const selectedPrimaryTag = graphFilter.primaryTag || '';
  const selectedText = graphFilter.text || document.getElementById('text-filter').value;
  try {
    await loadFacts();
  } catch (e) {
    // The graph endpoint can still render without the lightweight filter source.
  }
  const params = new URLSearchParams({ threshold });
  if (selectedNamespace) params.set('namespace', selectedNamespace);
  if (selectedPrimaryTag) params.set('primary_tag', selectedPrimaryTag);
  else if (selectedTag) params.set('tag', selectedTag);
  if (selectedText) params.set('text', selectedText);
  const res = await fetch(`${BASE}/api/graph?${params.toString()}`);
  graphDataCache = await res.json();

  const filterNodes = factsData?.nodes || graphDataCache.nodes;
  populateNsFilter(filterNodes, selectedNamespace);
  populateTagFilter(filterNodes, selectedNamespace, selectedTag);
  const tagLabel = document.getElementById('tag-filter-label');
  if (selectedPrimaryTag) {
    graphFilter.projectTag = '';
    tagLabel.textContent = `primary: ${selectedPrimaryTag}`;
    tagLabel.style.display = '';
  } else if (selectedTag) {
    graphFilter.projectTag = selectedTag;
    tagLabel.textContent = `#${selectedTag}`;
    tagLabel.style.display = '';
  } else {
    graphFilter.projectTag = '';
    tagLabel.style.display = 'none';
  }
  graphFilter.text = selectedText;
  renderGraphVis(graphDataCache);
}

function populateNsFilter(nodes, selectedNamespace = '') {
  const sel = document.getElementById('ns-filter');
  const namespaces = [...new Set(nodes.map(n => normalizeNamespace(n.namespace)))].sort();
  sel.innerHTML = '<option value="">All</option>';
  namespaces.forEach(ns => {
    const opt = document.createElement('option');
    opt.value = graphNamespaceFilter(ns); opt.textContent = ns;
    sel.appendChild(opt);
  });
  if (selectedNamespace) sel.value = selectedNamespace;
}

function populateTagFilter(nodes, selectedNamespace = '', selectedTag = '') {
  const sel = document.getElementById('tag-filter');
  const scoped = selectedNamespace
    ? nodes.filter(n => matchesNamespaceFilter(n.namespace, selectedNamespace))
    : nodes;
  const tags = [...new Set(scoped.flatMap(n => tagsList(n.tags)))].sort();
  sel.innerHTML = '<option value="">All</option>';
  tags.forEach(tag => {
    const opt = document.createElement('option');
    opt.value = tag; opt.textContent = tag;
    sel.appendChild(opt);
  });
  if (selectedTag) sel.value = selectedTag;
}

function renderGraphVis(graphData) {
  const nsVal = document.getElementById('ns-filter').value;
  let filtered = graphData.nodes;
  if (nsVal) filtered = filtered.filter(n => matchesNamespaceFilter(n.namespace, nsVal));
  if (graphFilter.primaryTag) {
    filtered = filtered.filter(n => primaryTag(n) === graphFilter.primaryTag);
  } else if (graphFilter.projectTag) {
    filtered = filtered.filter(n => tagsList(n.tags).includes(graphFilter.projectTag));
  }
  if (graphFilter.text === 'missing') {
    filtered = filtered.filter(n => n.text_missing);
  } else if (graphFilter.text === 'present') {
    filtered = filtered.filter(n => !n.text_missing);
  }
  const filteredIds = new Set(filtered.map(n => n.id));

  const namespaces = [...new Set(filtered.map(n => normalizeNamespace(n.namespace)))];
  const clusterRadius = 300 + filtered.length * 2;
  const nsPositions = {};
  namespaces.forEach((ns, i) => {
    const angle = (2 * Math.PI * i) / namespaces.length - Math.PI / 2;
    nsPositions[ns] = { x: Math.cos(angle) * clusterRadius, y: Math.sin(angle) * clusterRadius };
  });

  const visNodes = filtered.map(n => {
    const ns = normalizeNamespace(n.namespace);
    const center = nsPositions[ns];
    const spread = 60 + Math.sqrt(filtered.filter(f => normalizeNamespace(f.namespace) === ns).length) * 12;
    const text = factText(n);
    return {
      id: n.id, label: '', title: escapeHtml(text),
      x: center.x + (Math.random() - 0.5) * spread,
      y: center.y + (Math.random() - 0.5) * spread,
      color: { background: nsColor(ns), border: nsColor(ns),
        highlight: { background: '#fff', border: nsColor(ns) },
        hover: { background: '#fff', border: nsColor(ns) } },
      font: { color: '#e6edf3', size: 12, strokeWidth: 3, strokeColor: '#0d1117' },
      size: 8 + Math.min(n.recall_count, 15),
      borderWidth: n.permanent ? 3 : 1, shape: 'dot', _data: n,
    };
  });

  namespaces.forEach(ns => {
    const center = nsPositions[ns];
    const count = filtered.filter(n => normalizeNamespace(n.namespace) === ns).length;
    visNodes.push({
      id: '__label__' + ns, label: `${ns} (${count})`,
      x: center.x, y: center.y - 50 - Math.sqrt(count) * 8,
      fixed: true, shape: 'text', physics: false,
      font: { color: nsColor(ns), size: 16, bold: true, strokeWidth: 4, strokeColor: '#0d1117' },
      size: 0, _data: null,
    });
  });

  const visEdges = graphData.edges.filter(e => filteredIds.has(e.from) && filteredIds.has(e.to))
    .map(e => ({ from: e.from, to: e.to, value: e.similarity,
      color: { color: 'rgba(88,166,255,0.12)', highlight: 'rgba(88,166,255,0.4)' } }));

  const container = document.getElementById('graph-container');
  const data = { nodes: new vis.DataSet(visNodes), edges: new vis.DataSet(visEdges) };

  if (network) network.destroy();
  network = new vis.Network(container, data, {
    layout: { improvedLayout: false },
    physics: { enabled: true, solver: 'barnesHut',
      barnesHut: { gravitationalConstant: -3000, centralGravity: 0.5, springLength: 120, springConstant: 0.02, damping: 0.3 },
      stabilization: { iterations: 100, updateInterval: 50 } },
    interaction: { hover: true, tooltipDelay: 200, zoomView: true, dragView: true },
    nodes: { borderWidth: 1, shadow: false },
    edges: { smooth: false, scaling: { min: 0.5, max: 2 } },
  });

  network.once('stabilizationIterationsDone', () => {
    network.setOptions({ physics: false });
    network.fit({ animation: false });
    const nsCounts = {};
    filtered.forEach(n => {
      const ns = normalizeNamespace(n.namespace);
      nsCounts[ns] = (nsCounts[ns] || 0) + 1;
    });
    document.getElementById('legend').innerHTML = Object.entries(nsCounts)
      .sort((a, b) => b[1] - a[1])
      .map(([ns, c]) => `<div class="legend-item"><span class="legend-dot" style="background:${nsColor(ns)}"></span>${escapeHtml(ns)} ${c}</div>`)
      .join('');
  });

  network.on('hoverNode', p => {
    const node = visNodes.find(n => n.id === p.node);
    if (node && node._data) {
      const text = factText(node._data);
      const short = text.length > 60 ? text.slice(0, 60) + '...' : text;
      data.nodes.update({ id: p.node, label: short });
    }
  });
  network.on('blurNode', p => { data.nodes.update({ id: p.node, label: '' }); });

  network.on('click', p => {
    if (p.nodes.length > 0 && !String(p.nodes[0]).startsWith('__label__')) {
      const node = visNodes.find(n => n.id === p.nodes[0]);
      if (node && node._data) showDetail(node._data);
    } else { hideDetail(); }
  });
}

function showDetail(fact) {
  selectedFact = fact;
  const detailText = document.getElementById('detail-text');
  detailText.textContent = factText(fact);
  detailText.classList.toggle('missing-text', Boolean(fact.text_missing));
  const id = fact.id || '';
  const keys = Array.isArray(fact.payload_keys) ? fact.payload_keys : [];
  document.getElementById('detail-meta').innerHTML = `
    <span>ID: ${escapeHtml(id.slice(0, 12))}${id.length > 12 ? '...' : ''}</span><br>
    <span style="color:${nsColor(fact.namespace)}">${escapeHtml(normalizeNamespace(fact.namespace))}</span>
    ${primaryTag(fact) ? `<span class="tag-chip">primary: ${escapeHtml(primaryTag(fact))}</span>` : ''}
    ${tagsList(fact.tags).map(t => `<span class="tag-chip">#${escapeHtml(t)}</span>`).join('')}<br>
    <span>Created: ${escapeHtml((fact.created_at || '').slice(0, 10))}</span>
    <span>Recalls: ${Number(fact.recall_count || 0)}</span>
    ${fact.permanent ? '<span style="color:var(--orange)">Permanent</span>' : ''}
  `;
  document.getElementById('detail-tags').value = tagsList(fact.tags).join(', ');
  document.getElementById('detail-primary-tag').value = primaryTag(fact) || '';
  document.getElementById('tag-save-status').textContent = '';
  const payloadDetails = document.getElementById('payload-details');
  const payloadKeys = document.getElementById('payload-keys');
  const payloadJSON = document.getElementById('payload-json');
  if (keys.length > 0) {
    payloadKeys.textContent = keys.join(', ');
    payloadJSON.textContent = JSON.stringify(fact.payload || {}, null, 2);
    payloadDetails.style.display = '';
  } else {
    payloadKeys.textContent = '';
    payloadJSON.textContent = '';
    payloadDetails.style.display = 'none';
  }
  document.getElementById('detail-panel').classList.add('visible');
}
function hideDetail() {
  selectedFact = null;
  document.getElementById('detail-panel').classList.remove('visible');
}

async function saveSelectedTags() {
  if (!selectedFact || !selectedFact.id) return;
  const status = document.getElementById('tag-save-status');
  const tags = document.getElementById('detail-tags').value
    .split(',')
    .map(t => t.trim())
    .filter(Boolean);
  const primary_tag = document.getElementById('detail-primary-tag').value.trim();
  status.textContent = 'Saving...';
  try {
    const res = await fetch(`${BASE}/api/facts/${encodeURIComponent(selectedFact.id)}/tags`, {
      method: 'PATCH',
      headers: {
        'Content-Type': 'application/json',
        'X-Viz-Action': 'update-tags',
      },
      body: JSON.stringify({ tags, primary_tag }),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    selectedFact.tags = data.tags || tags;
    selectedFact.primary_tag = data.primary_tag || '';
    status.textContent = 'Saved';
    showDetail(selectedFact);
  } catch (err) {
    status.textContent = `Error: ${err.message || err}`;
  }
}

// Graph-specific control listeners. Registered once the script loads — the
// elements exist in the initial DOM so there's no timing issue.
document.getElementById('detail-close').addEventListener('click', hideDetail);
document.getElementById('save-tags').addEventListener('click', saveSelectedTags);

document.getElementById('reset-graph-filters').addEventListener('click', () => {
  graphFilter = { namespace: '', projectTag: '', primaryTag: '', text: '' };
  document.getElementById('ns-filter').value = '';
  document.getElementById('tag-filter').value = '';
  document.getElementById('text-filter').value = '';
  document.getElementById('threshold').value = '0.85';
  document.getElementById('threshold-val').textContent = '0.85';
  loadGraph();
});

document.getElementById('threshold').addEventListener('input', e => {
  document.getElementById('threshold-val').textContent = e.target.value;
});
document.getElementById('threshold').addEventListener('change', loadGraph);
document.getElementById('ns-filter').addEventListener('change', () => {
  graphFilter = {
    namespace: document.getElementById('ns-filter').value,
    projectTag: document.getElementById('tag-filter').value,
    primaryTag: '',
    text: document.getElementById('text-filter').value,
  };
  loadGraph();
});
document.getElementById('tag-filter').addEventListener('change', () => {
  graphFilter = {
    namespace: document.getElementById('ns-filter').value,
    projectTag: document.getElementById('tag-filter').value,
    primaryTag: '',
    text: document.getElementById('text-filter').value,
  };
  loadGraph();
});
document.getElementById('text-filter').addEventListener('change', () => {
  graphFilter = {
    namespace: document.getElementById('ns-filter').value,
    projectTag: document.getElementById('tag-filter').value,
    primaryTag: '',
    text: document.getElementById('text-filter').value,
  };
  loadGraph();
});
