// Duplicates tab: near-duplicate fact pairs above a similarity threshold.

async function loadDuplicates(maxNodes = 1000) {
  const container = document.getElementById('dup-content');
  container.innerHTML = '<div class="loading"><div class="spinner"></div>Scanning for duplicates...</div>';
  let res;
  try {
    res = await fetch(`${BASE}/api/duplicates?threshold=0.90&max_nodes=${maxNodes}`);
  } catch (err) {
    container.innerHTML = `<div class="empty-state">Duplicate scan failed: ${escapeHtml(err.message)}. <button onclick="loadDuplicates(${maxNodes})">Retry</button></div>`;
    return;
  }
  if (!res.ok) {
    const message = (await res.text()).trim() || `HTTP ${res.status}`;
    const retry = res.status === 422 && maxNodes < 5000
      ? ' <button onclick="loadDuplicates(5000)">Scan up to 5,000 facts</button>'
      : ` <button onclick="loadDuplicates(${maxNodes})">Retry</button>`;
    container.innerHTML = `<div class="empty-state">Duplicate scan could not run: ${escapeHtml(message)}.${retry}</div>`;
    document.getElementById('dup-badge').textContent = '—';
    return;
  }
  const data = await res.json();
  const pairs = Array.isArray(data) ? data : (data.pairs || []);
  document.getElementById('dup-badge').textContent = pairs.length;

  if (pairs.length === 0) {
    container.innerHTML = '<div class="empty-state">No near-duplicates found. Memory is clean!</div>';
    return;
  }

  container.innerHTML = '<div class="dup-list">' + pairs.map(p => {
    const score = p.similarity ?? p.score ?? 0;
    const scoreClass = score >= 0.95 ? 'high' : 'medium';
    const scoreLabel = score >= 0.97 ? 'near-identical' : score >= 0.95 ? 'very similar' : 'similar';
    return `<div class="dup-pair">
      <div class="dup-score ${scoreClass}">${(score * 100).toFixed(1)}% — ${scoreLabel}</div>
      <div class="dup-facts">
        <div>
          ${escapeHtml(factText(p.a).slice(0, 200))}${factText(p.a).length > 200 ? '...' : ''}
          <div class="dup-meta">${escapeHtml(normalizeNamespace(p.a.namespace))} | ${escapeHtml((p.a.tags || []).join(', '))} | recalls: ${Number(p.a.recall_count || 0)}</div>
        </div>
        <div>
          ${escapeHtml(factText(p.b).slice(0, 200))}${factText(p.b).length > 200 ? '...' : ''}
          <div class="dup-meta">${escapeHtml(normalizeNamespace(p.b.namespace))} | ${escapeHtml((p.b.tags || []).join(', '))} | recalls: ${Number(p.b.recall_count || 0)}</div>
        </div>
      </div>
    </div>`;
  }).join('') + '</div>';
}
