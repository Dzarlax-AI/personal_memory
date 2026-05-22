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
  })();

  return factsPromise;
}

async function initFacts() {
  try {
    await loadFacts();
  } catch (e) {
    document.getElementById('treemap').innerHTML =
      `<div class="empty-state">Failed to load facts: ${escapeHtml(e.message)}</div>`;
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
      return `<div class="treemap-tile" style="background:${color}${alpha};min-width:${size}px"
        data-namespace="${escapeAttr(graphNamespaceFilter(ns))}" data-tag="${escapeAttr(tag)}">
        <span class="tile-name">${escapeHtml(name)}</span>
        <span class="tile-count">${count} fact${count === 1 ? '' : 's'}</span>
      </div>`;
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
  // Appended to the overview treemap container.
  const container = document.getElementById('treemap');
  const datesMap = {};
  nodes.forEach(n => {
    const d = (n.created_at || '').slice(0, 10);
    if (d) datesMap[d] = (datesMap[d] || 0) + 1;
  });

  const dates = Object.keys(datesMap).sort();
  if (dates.length === 0) return;

  const first = new Date(dates[0]);
  const last = new Date(dates[dates.length - 1]);
  const maxCount = Math.max(...Object.values(datesMap));

  let cells = '';
  for (let d = new Date(first); d <= last; d.setDate(d.getDate() + 1)) {
    const key = d.toISOString().slice(0, 10);
    const count = datesMap[key] || 0;
    const intensity = count > 0 ? Math.min(0.3 + (count / maxCount) * 0.7, 1) : 0;
    const color = count > 0 ? `rgba(88, 166, 255, ${intensity})` : 'var(--surface-2)';
    cells += `<div class="heatmap-cell" style="background:${color}" title="${key}: ${count} facts"></div>`;
  }

  const heatDiv = document.createElement('div');
  heatDiv.style.marginTop = '24px';
  heatDiv.innerHTML = `
    <div class="section-title" style="margin-bottom:8px">Activity</div>
    <div class="heatmap-grid">${cells}</div>
  `;
  container.appendChild(heatDiv);
}

function navigateToGraph(namespace, projectTag) {
  graphFilter = { namespace: graphNamespaceFilter(namespace), projectTag: projectTag || '', text: '' };
  activateTab('graph');
  const sel = document.getElementById('ns-filter');
  if (sel) sel.value = graphFilter.namespace;
  if (typeof loadGraph === 'function') loadGraph();
}
