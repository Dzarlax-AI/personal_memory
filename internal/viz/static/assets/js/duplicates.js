// Duplicates tab: near-duplicate fact pairs above a similarity threshold.

async function loadDuplicates() {
  const res = await fetch(`${BASE}/api/duplicates?threshold=0.90`);
  const data = await res.json();
  const pairs = Array.isArray(data) ? data : (data.pairs || []);
  const container = document.getElementById('dup-content');
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
