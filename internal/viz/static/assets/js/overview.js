// Overview tab: stats, treemap, activity heatmap.

async function loadFacts() {
  if (factsData) return factsData;
  if (factsPromise) return factsPromise;

  factsPromise = (async () => {
    const res = await fetch(`${BASE}/api/facts`);
    if (!res.ok) throw new Error(`facts request failed: ${res.status}`);
    factsData = await res.json();
    renderStats(factsData.nodes);
    renderTreemap(factsData.nodes);
    renderForgotten(factsData.nodes);
    renderHeatmap(factsData.nodes);
    return factsData;
  })().catch(error => {
    factsPromise = null;
    throw error;
  });

  return factsPromise;
}

async function initFacts() {
  if (!factsData) {
    document.getElementById('treemap').innerHTML = '<div class="loading"><div class="spinner"></div>Loading knowledge map...</div>';
  }
  try {
    await loadFacts();
  } catch (e) {
    renderFactsFailure(document.getElementById('treemap'), 'the overview', initFacts);
  }
}

function renderStats(nodes) {
  const ns = [...new Set(nodes.map(n => normalizeNamespace(n.namespace)))].length;
  const permanent = nodes.filter(n => n.permanent).length;
  const neverRecalled = nodes.filter(n => n.recall_count === 0).length;
  document.getElementById('stats').innerHTML = `
    <span><span class="stat-num">${nodes.length}</span> facts</span>
    <span><span class="stat-num">${ns}</span> namespaces</span>
    <span><span class="stat-num">${permanent}</span> permanent</span>
    <span><span class="stat-num">${neverRecalled}</span> never recalled</span>
  `;
  document.getElementById('forgot-badge').textContent = neverRecalled;
}

function renderTreemap(nodes) {
  const container = document.getElementById('treemap');
  if (nodes.length === 0) {
    container.innerHTML = '<div class="empty-state">No facts have been stored yet.</div>';
    document.getElementById('activity-section').hidden = true;
    document.getElementById('heatmap-grid').replaceChildren();
    return;
  }
  const nsByNs = {};
  nodes.forEach(n => {
    const ns = normalizeNamespace(n.namespace);
    if (!nsByNs[ns]) nsByNs[ns] = {};
    const group = primaryTag(n) || '_no_primary_tag';
    if (!nsByNs[ns][group]) nsByNs[ns][group] = 0;
    nsByNs[ns][group]++;
  });

  container.innerHTML = '';
  const sorted = Object.entries(nsByNs).sort((a, b) => {
    const countA = Object.values(a[1]).reduce((s, v) => s + v, 0);
    const countB = Object.values(b[1]).reduce((s, v) => s + v, 0);
    return countB - countA;
  });

  for (const [ns, projects] of sorted) {
    const total = Object.values(projects).reduce((s, v) => s + v, 0);
    const color = nsColor(ns);
    const div = document.createElement('div');
    div.className = 'treemap-ns';
    div.style.flex = `${Math.min(Math.max(Math.sqrt(total) / 3, 1), 4)}`;

    const groupsSorted = Object.entries(projects).sort((a, b) => b[1] - a[1]);
    const tiles = groupsSorted.map(([group, count]) => {
      const size = Math.round(Math.min(72 + Math.sqrt(count) * 16, 220));
      const alpha = group === '_no_primary_tag' ? '33' : '55';
      const name = group === '_no_primary_tag' ? 'no primary tag' : group;
      const tag = group === '_no_primary_tag' ? '' : group;
      return `<button class="treemap-tile" type="button" style="background:${color}${alpha};min-width:${size}px"
        data-namespace="${escapeAttr(graphNamespaceFilter(ns))}" data-tag="${escapeAttr(tag)}">
        <span class="tile-name">${escapeHtml(name)}</span>
        <span class="tile-count">${count} fact${count === 1 ? '' : 's'}</span>
      </button>`;
    }).join('');

    div.innerHTML = `
      <h3><span class="ns-dot" style="background:${color}"></span> ${escapeHtml(ns)} <span class="ns-count">${total}</span></h3>
      <div class="treemap-projects">${tiles}</div>
    `;
    container.appendChild(div);
  }

  container.querySelectorAll('.treemap-tile').forEach(tile => {
    tile.addEventListener('click', () => {
      navigateToGraph(tile.dataset.namespace || '', tile.dataset.tag || '');
    });
  });
}

function renderHeatmap(nodes) {
  const section = document.getElementById('activity-section');
  const grid = document.getElementById('heatmap-grid');
  const datesMap = {};
  nodes.forEach(n => {
    const d = (n.created_at || '').slice(0, 10);
    if (d) datesMap[d] = (datesMap[d] || 0) + 1;
  });

  const dates = Object.keys(datesMap).sort();
  if (dates.length === 0) {
    section.hidden = true;
    grid.replaceChildren();
    return;
  }

  const first = new Date(dates[0]);
  const last = new Date(dates[dates.length - 1]);
  const maxCount = Math.max(...Object.values(datesMap));
  const gridStart = new Date(first);
  gridStart.setUTCDate(gridStart.getUTCDate() - gridStart.getUTCDay());
  const gridEnd = new Date(last);
  gridEnd.setUTCDate(gridEnd.getUTCDate() + (6 - gridEnd.getUTCDay()));
  grid.replaceChildren();
  for (let d = new Date(gridStart); d <= gridEnd; d.setUTCDate(d.getUTCDate() + 1)) {
    const key = d.toISOString().slice(0, 10);
    const count = datesMap[key] || 0;
    const intensity = count > 0 ? Math.min(0.3 + (count / maxCount) * 0.7, 1) : 0;
    const color = count > 0 ? `rgba(88, 166, 255, ${intensity})` : 'var(--surface-2)';
    const cell = document.createElement('div');
    cell.className = 'heatmap-cell';
    cell.style.background = color;
    cell.setAttribute('role', 'listitem');
    cell.setAttribute('aria-label', `${key}: ${count} fact${count === 1 ? '' : 's'}`);
    cell.title = `${key}: ${count} facts`;
    grid.appendChild(cell);
  }
  section.hidden = false;
}

function navigateToGraph(namespace, projectTag) {
  graphFilter = { namespace: graphNamespaceFilter(namespace), projectTag: '', primaryTag: projectTag || '', text: '' };
  activateTab('graph');
  const sel = document.getElementById('ns-filter');
  if (sel) sel.value = graphFilter.namespace;
  if (typeof loadGraph === 'function') loadGraph();
}
