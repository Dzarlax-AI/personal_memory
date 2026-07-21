// Forgotten tab: local filtering over the facts response, rendered in pages.

let forgottenData = [];
let forgottenShown = 50;

async function loadForgotten() {
  const container = document.getElementById('forgotten-list');
  if (!factsData) container.innerHTML = '<div class="loading"><div class="spinner"></div>Loading never-recalled facts...</div>';
  try {
    await loadFacts();
  } catch (_) {
    renderFactsFailure(container, 'the never-recalled list', loadForgotten);
  }
}

function renderForgotten(nodes) {
  forgottenData = nodes.filter(node => node.recall_count === 0);
  populateForgottenFilters(forgottenData);
  forgottenShown = 50;
  renderForgottenList();
}

function populateForgottenFilters(nodes) {
  const nsSelect = document.getElementById('forgotten-ns-filter');
  const tagInput = document.getElementById('forgotten-tag-filter');
  const currentNs = nsSelect.value;
  const currentTag = originalTagFilter(tagInput);
  const namespaces = [...new Set(nodes.map(node => normalizeNamespace(node.namespace)))].sort();
  nsSelect.innerHTML = '<option value="">All</option>' + namespaces
    .map(ns => `<option value="${escapeAttr(graphNamespaceFilter(ns))}">${escapeHtml(ns)}</option>`).join('');
  nsSelect.value = currentNs;
  const scoped = currentNs ? nodes.filter(node => matchesNamespaceFilter(node.namespace, currentNs)) : nodes;
  setTagDatalist('forgotten-tag-filter', 'forgotten-tag-options', tagOptions(scoped), currentTag);
}

function filteredForgotten() {
  const nsFilter = document.getElementById('forgotten-ns-filter').value;
  const tagFilter = originalTagFilter(document.getElementById('forgotten-tag-filter'));
  const sortMode = document.getElementById('forgotten-sort').value;
  const permanentOnly = document.getElementById('forgotten-permanent-only').checked;
  const forgotten = forgottenData.filter(node =>
    (!nsFilter || matchesNamespaceFilter(node.namespace, nsFilter)) &&
    (!tagFilter || tagsList(node.tags).includes(tagFilter)) &&
    (!permanentOnly || node.permanent));
  forgotten.sort((a, b) => {
    if (sortMode === 'newest') return (b.created_at || '').localeCompare(a.created_at || '');
    if (sortMode === 'namespace') return normalizeNamespace(a.namespace).localeCompare(normalizeNamespace(b.namespace));
    return (a.created_at || '').localeCompare(b.created_at || '');
  });
  return forgotten;
}

function renderForgottenList() {
  const forgotten = filteredForgotten();
  const container = document.getElementById('forgotten-list');
  const more = document.getElementById('forgotten-more');
  document.getElementById('forgotten-count').textContent = `${forgotten.length} matching facts${forgotten.length > forgottenShown ? ` · showing ${forgottenShown}` : ''}`;
  if (forgotten.length === 0) {
    container.innerHTML = '<div class="empty-state">No facts match these filters.</div>';
    more.hidden = true;
    return;
  }
  container.innerHTML = forgotten.slice(0, forgottenShown).map(forgottenHTML).join('');
  more.hidden = forgottenShown >= forgotten.length;
  more.textContent = `Show ${Math.min(50, forgotten.length - forgottenShown)} more (${forgotten.length - forgottenShown} remaining)`;
}

function forgottenHTML(fact) {
  const text = factText(fact);
  const primary = primaryTag(fact);
  return `<article class="forgotten-item"><div class="fact-text">${escapeHtml(text.slice(0, 200))}${text.length > 200 ? '...' : ''}</div><div class="meta-col"><span style="color:${nsColor(fact.namespace)}">${escapeHtml(normalizeNamespace(fact.namespace))}</span><span>${escapeHtml(primary || 'no primary tag')}</span><span>${escapeHtml((fact.created_at || '').slice(0, 10))}</span>${fact.permanent ? '<span style="color:var(--orange)">permanent</span>' : ''}</div></article>`;
}

['forgotten-ns-filter', 'forgotten-tag-filter', 'forgotten-sort', 'forgotten-permanent-only'].forEach(id => {
  document.getElementById(id).addEventListener(id.includes('tag') ? 'input' : 'change', () => {
    if (id === 'forgotten-ns-filter') populateForgottenFilters(forgottenData);
    forgottenShown = 50;
    renderForgottenList();
  });
});
document.getElementById('forgotten-more').addEventListener('click', () => {
  forgottenShown += 50;
  renderForgottenList();
});
