// Graph tab: force-directed network of facts with similarity edges.

async function loadGraph() {
  const threshold = document.getElementById('threshold').value;
  const res = await fetch(`${BASE}/api/graph?threshold=${threshold}`);
  graphDataCache = await res.json();

  populateNsFilter(graphDataCache.nodes);
  const tagLabel = document.getElementById('tag-filter-label');
  const clearBtn = document.getElementById('clear-tag-filter');
  if (graphFilter.projectTag) {
    tagLabel.textContent = `#${graphFilter.projectTag}`;
    tagLabel.style.display = '';
    clearBtn.style.display = '';
  } else {
    tagLabel.style.display = 'none';
    clearBtn.style.display = 'none';
  }
  renderGraphVis(graphDataCache);
}

function populateNsFilter(nodes) {
  const sel = document.getElementById('ns-filter');
  const namespaces = [...new Set(nodes.map(n => n.namespace))].sort();
  sel.innerHTML = '<option value="">All</option>';
  namespaces.forEach(ns => {
    const opt = document.createElement('option');
    opt.value = ns; opt.textContent = ns;
    sel.appendChild(opt);
  });
}

function renderGraphVis(graphData) {
  const nsVal = document.getElementById('ns-filter').value;
  let filtered = graphData.nodes;
  if (nsVal) filtered = filtered.filter(n => n.namespace === nsVal);
  if (graphFilter.projectTag) {
    filtered = filtered.filter(n => (n.tags || []).includes(graphFilter.projectTag));
  }
  const filteredIds = new Set(filtered.map(n => n.id));

  const namespaces = [...new Set(filtered.map(n => n.namespace))];
  const clusterRadius = 300 + filtered.length * 2;
  const nsPositions = {};
  namespaces.forEach((ns, i) => {
    const angle = (2 * Math.PI * i) / namespaces.length - Math.PI / 2;
    nsPositions[ns] = { x: Math.cos(angle) * clusterRadius, y: Math.sin(angle) * clusterRadius };
  });

  const visNodes = filtered.map(n => {
    const center = nsPositions[n.namespace];
    const spread = 60 + Math.sqrt(filtered.filter(f => f.namespace === n.namespace).length) * 12;
    return {
      id: n.id, label: '', title: n.text,
      x: center.x + (Math.random() - 0.5) * spread,
      y: center.y + (Math.random() - 0.5) * spread,
      color: { background: nsColor(n.namespace), border: nsColor(n.namespace),
        highlight: { background: '#fff', border: nsColor(n.namespace) },
        hover: { background: '#fff', border: nsColor(n.namespace) } },
      font: { color: '#e6edf3', size: 12, strokeWidth: 3, strokeColor: '#0d1117' },
      size: 8 + Math.min(n.recall_count, 15),
      borderWidth: n.permanent ? 3 : 1, shape: 'dot', _data: n,
    };
  });

  namespaces.forEach(ns => {
    const center = nsPositions[ns];
    const count = filtered.filter(n => n.namespace === ns).length;
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
    filtered.forEach(n => nsCounts[n.namespace] = (nsCounts[n.namespace] || 0) + 1);
    document.getElementById('legend').innerHTML = Object.entries(nsCounts)
      .sort((a, b) => b[1] - a[1])
      .map(([ns, c]) => `<div class="legend-item"><span class="legend-dot" style="background:${nsColor(ns)}"></span>${ns} ${c}</div>`)
      .join('');
  });

  network.on('hoverNode', p => {
    const node = visNodes.find(n => n.id === p.node);
    if (node && node._data) {
      const short = node._data.text.length > 60 ? node._data.text.slice(0, 60) + '...' : node._data.text;
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
  document.getElementById('detail-text').textContent = fact.text;
  document.getElementById('detail-meta').innerHTML = `
    <span style="color:${nsColor(fact.namespace)}">${fact.namespace}</span>
    ${(fact.tags || []).map(t => `<span>#${t}</span>`).join('')}<br>
    <span>Created: ${(fact.created_at || '').slice(0, 10)}</span>
    <span>Recalls: ${fact.recall_count}</span>
    ${fact.permanent ? '<span style="color:var(--orange)">Permanent</span>' : ''}
  `;
  document.getElementById('detail-panel').classList.add('visible');
}
function hideDetail() { document.getElementById('detail-panel').classList.remove('visible'); }

// Graph-specific control listeners. Registered once the script loads — the
// elements exist in the initial DOM so there's no timing issue.
document.getElementById('detail-close').addEventListener('click', hideDetail);

document.getElementById('clear-tag-filter').addEventListener('click', () => {
  graphFilter.projectTag = '';
  if (graphDataCache) {
    document.getElementById('tag-filter-label').style.display = 'none';
    document.getElementById('clear-tag-filter').style.display = 'none';
    renderGraphVis(graphDataCache);
  }
});

document.getElementById('threshold').addEventListener('input', e => {
  document.getElementById('threshold-val').textContent = e.target.value;
});
document.getElementById('threshold').addEventListener('change', loadGraph);
document.getElementById('ns-filter').addEventListener('change', () => {
  graphFilter.projectTag = '';
  loadGraph();
});
