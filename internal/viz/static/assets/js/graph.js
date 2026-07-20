// Graph tab: summaries drive the graph; selected details are fetched separately.

let selectedFact = null;
let graphLoaded = false;
let graphPromise = null;
let graphResultsShown = 50;
let detailRequest = 0;
let detailReturnFocus = null;
const pendingTagSaves = new Set();
let graphAbortController = null;
let graphRequest = 0;

function resetGraphNetwork() {
  if (network) network.destroy();
  network = null;
}

async function loadGraph(maxNodes = 1000) {
  const request = ++graphRequest;
  if (graphAbortController) graphAbortController.abort();
  graphAbortController = new AbortController();
  graphLoaded = false;
  const requestFilter = { ...graphFilter };
  const status = document.getElementById('graph-status');
  const container = document.getElementById('graph-container');
  resetGraphNetwork();
  status.textContent = 'Loading graph…';
  container.innerHTML = '<div class="loading"><div class="spinner"></div>Loading graph…</div>';
  graphPromise = (async () => {
    const threshold = document.getElementById('threshold').value;
    const selectedNamespace = requestFilter.namespace || document.getElementById('ns-filter').value;
    const selectedTag = requestFilter.projectTag || originalTagFilter(document.getElementById('tag-filter'));
    const selectedPrimaryTag = requestFilter.primaryTag || '';
    const selectedText = requestFilter.text || document.getElementById('text-filter').value;
    try { await loadFacts(); } catch (_) { /* graph can still supply filters */ }
    if (request !== graphRequest) return null;
    const params = new URLSearchParams({ threshold, max_nodes: String(maxNodes) });
    if (selectedNamespace) params.set('namespace', selectedNamespace);
    if (selectedPrimaryTag) params.set('primary_tag', selectedPrimaryTag);
    else if (selectedTag) params.set('tag', selectedTag);
    if (selectedText) params.set('text', selectedText);
    const res = await fetch(`${BASE}/api/graph?${params}`, { signal: graphAbortController.signal });
    if (!res.ok) {
      const error = new Error(await responseMessage(res));
      error.status = res.status;
      throw error;
    }
    const graphData = await res.json();
    if (request !== graphRequest) return null;
    graphDataCache = graphData;
    const filterNodes = factsData?.nodes || graphDataCache.nodes || [];
    populateNsFilter(filterNodes, selectedNamespace);
    populateTagFilter(filterNodes, selectedNamespace, selectedTag);
    const tagLabel = document.getElementById('tag-filter-label');
    if (selectedPrimaryTag) {
      graphFilter.projectTag = '';
      tagLabel.textContent = `primary: ${normalizeTagDisplay(selectedPrimaryTag)}`;
      tagLabel.style.display = '';
    } else if (selectedTag) {
      graphFilter.projectTag = selectedTag;
      tagLabel.textContent = `#${normalizeTagDisplay(selectedTag)}`;
      tagLabel.style.display = '';
    } else {
      graphFilter.projectTag = '';
      tagLabel.style.display = 'none';
    }
    graphFilter.text = selectedText;
    graphResultsShown = 50;
    graphLoaded = true;
    renderGraphVis(graphDataCache, request);
    return graphDataCache;
  })().catch(error => {
    if (request !== graphRequest || error.name === 'AbortError') return null;
    graphLoaded = false;
    status.textContent = '';
    const retryLimit = error.status === 422 && maxNodes < 5000 ? 5000 : maxNodes;
    renderRetry(container, `Graph could not load: ${error.message || error}`, () => loadGraph(retryLimit));
    if (retryLimit === 5000 && maxNodes !== 5000) container.querySelector('button').textContent = 'Retry with up to 5,000 nodes';
    renderGraphResults([]);
    return null;
  }).finally(() => {
    if (request === graphRequest) {
      graphPromise = null;
      graphAbortController = null;
    }
  });
  return graphPromise;
}

function populateNsFilter(nodes, selectedNamespace = '') {
  const sel = document.getElementById('ns-filter');
  const namespaces = [...new Set(nodes.map(node => normalizeNamespace(node.namespace)))].sort();
  sel.innerHTML = '<option value="">All</option>';
  namespaces.forEach(ns => {
    const opt = document.createElement('option');
    opt.value = graphNamespaceFilter(ns); opt.textContent = ns;
    sel.appendChild(opt);
  });
  sel.value = selectedNamespace || '';
}

function populateTagFilter(nodes, selectedNamespace = '', selectedTag = '') {
  const scoped = selectedNamespace ? nodes.filter(node => matchesNamespaceFilter(node.namespace, selectedNamespace)) : nodes;
  setTagDatalist('tag-filter', 'tag-filter-options', tagOptions(scoped), selectedTag);
}

function graphFilteredNodes(graphData) {
  const nsVal = document.getElementById('ns-filter').value;
  let filtered = graphData.nodes || [];
  if (nsVal) filtered = filtered.filter(node => matchesNamespaceFilter(node.namespace, nsVal));
  if (graphFilter.primaryTag) filtered = filtered.filter(node => primaryTag(node) === graphFilter.primaryTag);
  else if (graphFilter.projectTag) filtered = filtered.filter(node => tagsList(node.tags).includes(graphFilter.projectTag));
  if (graphFilter.text === 'missing') filtered = filtered.filter(node => node.text_missing);
  if (graphFilter.text === 'present') filtered = filtered.filter(node => !node.text_missing);
  return filtered;
}

function renderGraphVis(graphData, request = graphRequest) {
  if (request !== graphRequest) return;
  const filtered = graphFilteredNodes(graphData);
  const status = document.getElementById('graph-status');
  const container = document.getElementById('graph-container');
  renderGraphResults(filtered);
  if (filtered.length === 0) {
    resetGraphNetwork();
    container.innerHTML = '<div class="empty-state">No facts match the current graph filters.</div>';
    document.getElementById('legend').replaceChildren();
    status.textContent = 'No graph results.';
    return;
  }
  status.textContent = `${filtered.length} facts and ${(graphData.edges || []).length} candidate relationships loaded.`;
  const filteredIds = new Set(filtered.map(node => node.id));
  const namespaces = [...new Set(filtered.map(node => normalizeNamespace(node.namespace)))];
  const clusterRadius = 300 + filtered.length * 2;
  const nsPositions = {};
  namespaces.forEach((ns, index) => {
    const angle = (2 * Math.PI * index) / namespaces.length - Math.PI / 2;
    nsPositions[ns] = { x: Math.cos(angle) * clusterRadius, y: Math.sin(angle) * clusterRadius };
  });
  const perNamespace = Object.fromEntries(namespaces.map(ns => [ns, filtered.filter(node => normalizeNamespace(node.namespace) === ns).length]));
  const visNodes = filtered.map(node => {
    const ns = normalizeNamespace(node.namespace);
    const center = nsPositions[ns];
    const spread = 60 + Math.sqrt(perNamespace[ns]) * 12;
    return { id: node.id, label: '', title: escapeHtml(factText(node)), x: center.x + (Math.random() - 0.5) * spread, y: center.y + (Math.random() - 0.5) * spread,
      color: { background: nsColor(ns), border: nsColor(ns), highlight: { background: '#fff', border: nsColor(ns) }, hover: { background: '#fff', border: nsColor(ns) } },
      font: { color: '#e6edf3', size: 12, strokeWidth: 3, strokeColor: '#0d1117' }, size: 8 + Math.min(node.recall_count, 15), borderWidth: node.permanent ? 3 : 1, shape: 'dot', _data: node };
  });
  namespaces.forEach(ns => visNodes.push({ id: '__label__' + ns, label: `${ns} (${perNamespace[ns]})`, x: nsPositions[ns].x, y: nsPositions[ns].y - 50 - Math.sqrt(perNamespace[ns]) * 8, fixed: true, shape: 'text', physics: false, font: { color: nsColor(ns), size: 16, bold: true, strokeWidth: 4, strokeColor: '#0d1117' }, size: 0 }));
  const visEdges = (graphData.edges || []).filter(edge => filteredIds.has(edge.from) && filteredIds.has(edge.to)).map(edge => ({ from: edge.from, to: edge.to, value: edge.similarity, color: { color: 'rgba(88,166,255,0.12)', highlight: 'rgba(88,166,255,0.4)' } }));
  const data = { nodes: new vis.DataSet(visNodes), edges: new vis.DataSet(visEdges) };
  resetGraphNetwork();
  network = new vis.Network(container, data, { layout: { improvedLayout: false }, physics: { enabled: true, solver: 'barnesHut', barnesHut: { gravitationalConstant: -3000, centralGravity: 0.5, springLength: 120, springConstant: 0.02, damping: 0.3 }, stabilization: { iterations: 100, updateInterval: 50 } }, interaction: { hover: true, tooltipDelay: 200, zoomView: true, dragView: true }, nodes: { borderWidth: 1, shadow: false }, edges: { smooth: false, scaling: { min: 0.5, max: 2 } } });
  const activeNetwork = network;
  network.once('stabilizationIterationsDone', () => { if (network !== activeNetwork || request !== graphRequest) return; network.setOptions({ physics: false }); network.fit({ animation: false }); renderLegend(perNamespace); });
  network.on('hoverNode', point => { const node = visNodes.find(item => item.id === point.node); if (node?._data) { const text = factText(node._data); data.nodes.update({ id: point.node, label: text.length > 60 ? text.slice(0, 60) + '...' : text }); } });
  network.on('blurNode', point => data.nodes.update({ id: point.node, label: '' }));
  network.on('click', point => {
    if (point.nodes.length > 0 && !String(point.nodes[0]).startsWith('__label__')) showDetail(point.nodes[0], container);
    else hideDetail();
  });
}

function renderLegend(counts) {
  document.getElementById('legend').innerHTML = Object.entries(counts).sort((a, b) => b[1] - a[1]).map(([ns, count]) => `<div class="legend-item"><span class="legend-dot" style="background:${nsColor(ns)}"></span>${escapeHtml(ns)} ${count}</div>`).join('');
}

function renderGraphResults(nodes) {
  const list = document.getElementById('graph-results-list');
  const more = document.getElementById('graph-results-more');
  document.getElementById('graph-results-count').textContent = nodes.length ? `${nodes.length} facts match the graph filters.` : '';
  list.replaceChildren();
  nodes.slice(0, graphResultsShown).forEach(node => {
    const button = document.createElement('button');
    button.type = 'button'; button.className = 'graph-result';
    button.textContent = `${normalizeNamespace(node.namespace)} · ${factText(node).slice(0, 110)}${factText(node).length > 110 ? '…' : ''}`;
    button.addEventListener('click', () => showDetail(node.id, button));
    list.appendChild(button);
  });
  more.hidden = graphResultsShown >= nodes.length;
  more.textContent = `Show ${Math.min(50, nodes.length - graphResultsShown)} more (${nodes.length - graphResultsShown} remaining)`;
}

async function showDetail(id, opener) {
  detailReturnFocus = opener || document.activeElement;
  const panel = document.getElementById('detail-panel');
  const state = document.getElementById('detail-state');
  const content = document.getElementById('detail-content');
  const save = document.getElementById('save-tags');
  const request = ++detailRequest;
  selectedFact = null;
  content.hidden = true; save.disabled = true;
  state.textContent = 'Loading fact details…';
  panel.classList.add('visible'); panel.focus();
  try {
    const res = await fetch(`${BASE}/api/facts/${encodeURIComponent(id)}`);
    if (!res.ok) throw new Error(await responseMessage(res));
    const fact = await res.json();
    if (request !== detailRequest) return;
    selectedFact = fact;
    renderDetail(fact);
    state.textContent = '';
    content.hidden = false;
    save.disabled = pendingTagSaves.has(fact.id);
    if (save.disabled) document.getElementById('tag-save-status').textContent = 'Saving changes…';
  } catch (error) {
    if (request !== detailRequest) return;
    state.textContent = `Could not load fact details: ${error.message || error}`;
    const retry = document.createElement('button');
    retry.type = 'button'; retry.className = 'toolbar-btn'; retry.textContent = 'Retry';
    retry.addEventListener('click', () => showDetail(id, detailReturnFocus));
    state.appendChild(document.createTextNode(' ')); state.appendChild(retry);
  }
}

function renderDetail(fact) {
  const id = String(fact.id || '');
  document.getElementById('detail-text').textContent = factText(fact);
  document.getElementById('detail-text').classList.toggle('missing-text', Boolean(fact.text_missing));
  document.getElementById('detail-meta').innerHTML = `<span>ID: ${escapeHtml(id.slice(0, 12))}${id.length > 12 ? '...' : ''}</span><br><span style="color:${nsColor(fact.namespace)}">${escapeHtml(normalizeNamespace(fact.namespace))}</span>${primaryTag(fact) ? `<span class="tag-chip">primary: ${escapeHtml(normalizeTagDisplay(primaryTag(fact)))}</span>` : ''}${tagOptions([fact]).map(tag => `<span class="tag-chip">#${escapeHtml(tag.display)}</span>`).join('')}<br><span>Created: ${escapeHtml((fact.created_at || '').slice(0, 10))}</span><span>Recalls: ${Number(fact.recall_count || 0)}</span>${fact.permanent ? '<span style="color:var(--orange)">Permanent</span>' : ''}`;
  document.getElementById('detail-tags').value = tagsList(fact.tags).join(', ');
  document.getElementById('detail-primary-tag').value = primaryTag(fact) || '';
  document.getElementById('tag-save-status').textContent = '';
  const payloadDetails = document.getElementById('payload-details');
  const keys = Array.isArray(fact.payload_keys) ? fact.payload_keys : [];
  document.getElementById('payload-keys').textContent = keys.join(', ');
  document.getElementById('payload-json').textContent = JSON.stringify(fact.payload || {}, null, 2);
  payloadDetails.style.display = keys.length ? '' : 'none';
}

function hideDetail() {
  const panel = document.getElementById('detail-panel');
  if (!panel.classList.contains('visible')) return;
  detailRequest++;
  selectedFact = null;
  panel.classList.remove('visible');
  if (detailReturnFocus?.focus) detailReturnFocus.focus();
}

async function saveSelectedTags() {
  if (!selectedFact?.id || pendingTagSaves.has(selectedFact.id)) return;
  const status = document.getElementById('tag-save-status');
  const save = document.getElementById('save-tags');
  const factAtSave = selectedFact;
  const detailAtSave = detailRequest;
  const factID = factAtSave.id;
  const tags = document.getElementById('detail-tags').value.split(',').map(tag => tag.trim()).filter(Boolean);
  const primary_tag = document.getElementById('detail-primary-tag').value.trim();
  let saveSucceeded = false;
  let saveFailureMessage = '';
  pendingTagSaves.add(factID); save.disabled = true; status.textContent = 'Saving…';
  try {
    const res = await fetch(`${BASE}/api/facts/${encodeURIComponent(factID)}/tags`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-Viz-Action': 'update-tags' }, body: JSON.stringify({ tags, primary_tag }) });
    if (!res.ok) throw new Error(await responseMessage(res));
    const data = await res.json();
    saveSucceeded = true;
    if (selectedFact !== factAtSave || detailRequest !== detailAtSave) return;
    factAtSave.tags = data.tags || tags; factAtSave.primary_tag = data.primary_tag || '';
    renderDetail(factAtSave); status.textContent = 'Saved.';
  } catch (error) {
    saveFailureMessage = error.message || String(error);
    if (selectedFact !== factAtSave || detailRequest !== detailAtSave) return;
    status.textContent = `Save failed: ${saveFailureMessage}`;
  } finally {
    pendingTagSaves.delete(factID);
    if (selectedFact === factAtSave && detailRequest === detailAtSave) save.disabled = false;
    if (selectedFact?.id === factID && selectedFact !== factAtSave) {
      save.disabled = false;
      status.textContent = saveSucceeded
        ? 'Saved. Reopen details to refresh tags.'
        : `Save failed: ${saveFailureMessage || 'unknown error'}`;
    }
  }
}

document.getElementById('detail-close').addEventListener('click', hideDetail);
document.getElementById('save-tags').addEventListener('click', saveSelectedTags);
document.addEventListener('keydown', event => { if (event.key === 'Escape') hideDetail(); });
document.getElementById('graph-results-more').addEventListener('click', () => { graphResultsShown += 50; renderGraphResults(graphFilteredNodes(graphDataCache)); });
document.getElementById('reset-graph-filters').addEventListener('click', () => { graphFilter = { namespace: '', projectTag: '', primaryTag: '', text: '' }; document.getElementById('ns-filter').value = ''; document.getElementById('tag-filter').value = ''; document.getElementById('text-filter').value = ''; document.getElementById('threshold').value = '0.85'; document.getElementById('threshold-val').textContent = '0.85'; graphLoaded = false; loadGraph(); });
document.getElementById('threshold').addEventListener('input', event => { document.getElementById('threshold-val').textContent = event.target.value; });
document.getElementById('threshold').addEventListener('change', () => { graphLoaded = false; loadGraph(); });
['ns-filter', 'tag-filter', 'text-filter'].forEach(id => document.getElementById(id).addEventListener(id === 'tag-filter' ? 'change' : 'change', () => { graphFilter = { namespace: document.getElementById('ns-filter').value, projectTag: originalTagFilter(document.getElementById('tag-filter')), primaryTag: '', text: document.getElementById('text-filter').value }; graphLoaded = false; loadGraph(); }));
