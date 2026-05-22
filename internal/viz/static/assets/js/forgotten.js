// Forgotten tab: facts with recall_count === 0.
// Renders synchronously from the already-loaded factsData.

let forgottenData = [];

function renderForgotten(nodes) {
  forgottenData = nodes.filter(n => n.recall_count === 0);
  populateForgottenFilters(forgottenData);
  renderForgottenList();
}

function populateForgottenFilters(nodes) {
  const nsSelect = document.getElementById('forgotten-ns-filter');
  const tagSelect = document.getElementById('forgotten-tag-filter');
  const currentNs = nsSelect.value;
  const currentTag = tagSelect.value;

  const namespaces = [...new Set(nodes.map(n => normalizeNamespace(n.namespace)))].sort();
  nsSelect.innerHTML = '<option value="">All</option>' + namespaces
    .map(ns => `<option value="${escapeAttr(graphNamespaceFilter(ns))}">${escapeHtml(ns)}</option>`)
    .join('');
  nsSelect.value = currentNs;

  const scoped = currentNs ? nodes.filter(n => matchesNamespaceFilter(n.namespace, currentNs)) : nodes;
  const tags = [...new Set(scoped.flatMap(n => tagsList(n.tags)))].sort();
  tagSelect.innerHTML = '<option value="">All</option>' + tags
    .map(tag => `<option value="${escapeAttr(tag)}">${escapeHtml(tag)}</option>`)
    .join('');
  tagSelect.value = currentTag;
}

function renderForgottenList() {
  const nsFilter = document.getElementById('forgotten-ns-filter').value;
  const tagFilter = document.getElementById('forgotten-tag-filter').value;
  const sortMode = document.getElementById('forgotten-sort').value;
  const permanentOnly = document.getElementById('forgotten-permanent-only').checked;

  let forgotten = [...forgottenData];
  if (nsFilter) forgotten = forgotten.filter(n => matchesNamespaceFilter(n.namespace, nsFilter));
  if (tagFilter) forgotten = forgotten.filter(n => tagsList(n.tags).includes(tagFilter));
  if (permanentOnly) forgotten = forgotten.filter(n => n.permanent);

  forgotten.sort((a, b) => {
    if (sortMode === 'newest') return (b.created_at || '').localeCompare(a.created_at || '');
    if (sortMode === 'namespace') return normalizeNamespace(a.namespace).localeCompare(normalizeNamespace(b.namespace));
    return (a.created_at || '').localeCompare(b.created_at || '');
  });

  const container = document.getElementById('forgotten-list');
  document.getElementById('forgotten-count').textContent = `${forgotten.length} shown`;

  if (forgotten.length === 0) {
    container.innerHTML = '<div class="empty-state">No facts match these filters.</div>';
    return;
  }

  container.innerHTML = forgotten.map(n => {
    const date = (n.created_at || '').slice(0, 10);
    const primary = primaryTag(n);
    const text = factText(n);
    return `<div class="forgotten-item">
      <div class="fact-text">${escapeHtml(text.slice(0, 200))}${text.length > 200 ? '...' : ''}</div>
      <div class="meta-col">
        <span style="color:${nsColor(n.namespace)}">${escapeHtml(normalizeNamespace(n.namespace))}</span>
        ${primary ? `<span>${escapeHtml(primary)}</span>` : '<span>no primary tag</span>'}
        <span>${escapeHtml(date)}</span>
        ${n.permanent ? '<span style="color:var(--orange)">permanent</span>' : ''}
      </div>
    </div>`;
  }).join('');
}

['forgotten-ns-filter', 'forgotten-tag-filter', 'forgotten-sort', 'forgotten-permanent-only'].forEach(id => {
  document.getElementById(id).addEventListener('change', () => {
    if (id === 'forgotten-ns-filter') populateForgottenFilters(forgottenData);
    renderForgottenList();
  });
});
