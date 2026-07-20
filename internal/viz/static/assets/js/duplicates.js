// Duplicates tab: bounded, paginated scan results.

let duplicatesLoaded = false;
let duplicatesPromise = null;
let duplicatesData = [];
let duplicatesShown = 50;

async function loadDuplicates(maxNodes = 1000) {
  if (duplicatesPromise) return duplicatesPromise;
  const container = document.getElementById('dup-content');
  container.innerHTML = '<div class="loading"><div class="spinner"></div>Scanning for duplicates...</div>';
  duplicatesPromise = (async () => {
    const res = await fetch(`${BASE}/api/duplicates?threshold=0.90&max_nodes=${maxNodes}`);
    if (!res.ok) {
      const error = new Error(await responseMessage(res));
      error.status = res.status;
      throw error;
    }
    const data = await res.json();
    duplicatesData = Array.isArray(data) ? data : (data.pairs || []);
    duplicatesShown = 50;
    duplicatesLoaded = true;
    renderDuplicates();
    return duplicatesData;
  })().catch(error => {
    duplicatesPromise = null;
    duplicatesLoaded = false;
    const retryLimit = error.status === 422 && maxNodes < 5000 ? 5000 : maxNodes;
    renderRetry(container, `Duplicate scan could not run: ${error.message || error}`, () => loadDuplicates(retryLimit));
    if (retryLimit === 5000 && maxNodes !== 5000) container.querySelector('button').textContent = 'Scan up to 5,000 facts';
    document.getElementById('dup-badge').textContent = '—';
    return null;
  });
  return duplicatesPromise;
}

function renderDuplicates() {
  const container = document.getElementById('dup-content');
  document.getElementById('dup-badge').textContent = duplicatesData.length;
  document.getElementById('dup-count').textContent = `${duplicatesData.length} duplicate pairs found`;
  if (duplicatesData.length === 0) {
    container.innerHTML = '<div class="empty-state">No near-duplicates found. Memory is clean!</div>';
    return;
  }
  const visible = duplicatesData.slice(0, duplicatesShown);
  container.innerHTML = `<div class="dup-list">${visible.map(duplicateHTML).join('')}</div>`;
  if (duplicatesShown < duplicatesData.length) {
    const more = document.createElement('button');
    more.type = 'button'; more.className = 'toolbar-btn load-more';
    more.textContent = `Show ${Math.min(50, duplicatesData.length - duplicatesShown)} more pairs (${duplicatesData.length - duplicatesShown} remaining)`;
    more.addEventListener('click', () => { duplicatesShown += 50; renderDuplicates(); });
    container.appendChild(more);
  }
}

function duplicateHTML(pair) {
  const score = Number(pair.similarity ?? pair.score ?? 0);
  const scoreClass = score >= 0.95 ? 'high' : 'medium';
  const scoreLabel = score >= 0.97 ? 'near-identical' : score >= 0.95 ? 'very similar' : 'similar';
  return `<article class="dup-pair"><div class="dup-score ${scoreClass}">${(score * 100).toFixed(1)}% — ${scoreLabel}</div><div class="dup-facts">${duplicateFactHTML(pair.a || {})}${duplicateFactHTML(pair.b || {})}</div></article>`;
}

function duplicateFactHTML(fact) {
  const text = factText(fact);
  const tags = tagOptions([fact]).map(tag => tag.display).join(', ');
  return `<div>${escapeHtml(text.slice(0, 200))}${text.length > 200 ? '...' : ''}<div class="dup-meta">${escapeHtml(normalizeNamespace(fact.namespace))} | ${escapeHtml(tags)} | recalls: ${Number(fact.recall_count || 0)}</div></div>`;
}
