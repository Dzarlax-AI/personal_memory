// Browser-compatible JavaScript kept in a .ts asset for the workspace module.
// It intentionally uses no TypeScript-only syntax and needs no transpilation.

let memoryShown = 50;

function memoryFilters() {
  return {
    text: document.getElementById('memory-text-filter').value.trim().toLocaleLowerCase(),
    namespace: document.getElementById('memory-ns-filter').value,
    tag: originalTagFilter(document.getElementById('memory-tag-filter')),
    lifecycle: document.getElementById('memory-lifecycle-filter').value,
  };
}

function populateMemoryFilters(nodes) {
  const nsSelect = document.getElementById('memory-ns-filter');
  const tagInput = document.getElementById('memory-tag-filter');
  const currentNS = nsSelect.value;
  const currentTag = originalTagFilter(tagInput);
  const namespaces = [...new Set(nodes.map(node => normalizeNamespace(node.namespace)))].sort();
  nsSelect.innerHTML = '<option value="">All</option>' + namespaces
    .map(ns => `<option value="${escapeAttr(graphNamespaceFilter(ns))}">${escapeHtml(ns)}</option>`).join('');
  nsSelect.value = namespaces.some(ns => graphNamespaceFilter(ns) === currentNS) ? currentNS : '';
  const scoped = nsSelect.value ? nodes.filter(node => matchesNamespaceFilter(node.namespace, nsSelect.value)) : nodes;
  setTagDatalist('memory-tag-filter', 'memory-tag-options', tagOptions(scoped), currentTag);
}

function renderMemory(nodes) {
  populateMemoryFilters(nodes);
  renderMemoryList();
}

function filteredMemoryFacts() {
  const filter = memoryFilters();
  return (factsData?.nodes || []).filter(fact => {
    const text = factText(fact).toLocaleLowerCase();
    return (!filter.text || text.includes(filter.text)) &&
      (!filter.namespace || matchesNamespaceFilter(fact.namespace, filter.namespace)) &&
      (!filter.tag || tagsList(fact.tags).includes(filter.tag)) &&
      (!filter.lifecycle || normalizedLifecycle(fact).state === filter.lifecycle);
  }).sort((a, b) => (b.created_at || '').localeCompare(a.created_at || '') || String(a.id).localeCompare(String(b.id)));
}

function renderMemoryList() {
  const list = document.getElementById('memory-list');
  if (!list) return;
  const all = factsData?.nodes || [];
  const matches = filteredMemoryFacts();
  const summary = document.getElementById('memory-summary');
  summary.textContent = `${all.length} active facts · ${matches.length} match${matches.length === 1 ? '' : 'es'}${matches.length > memoryShown ? ` · showing ${Math.min(memoryShown, matches.length)}` : ''}`;
  if (!matches.length) {
    list.innerHTML = all.length
      ? '<div class="empty-state">No facts match these filters.</div>'
      : '<div class="empty-state">No facts have been stored yet.</div>';
    document.getElementById('memory-more').hidden = true;
    return;
  }
  list.replaceChildren();
  matches.slice(0, memoryShown).forEach(fact => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'memory-fact';
    button.dataset.factId = String(fact.id || '');
    const text = factText(fact);
    const tags = tagOptions([fact]).map(tag => tag.display);
    button.innerHTML = `<span class="memory-fact-main"><span class="memory-fact-text">${escapeHtml(text.slice(0, 260))}${text.length > 260 ? '…' : ''}</span><span class="lifecycle-badges">${lifecycleBadgeHTML(fact)}</span></span><span class="memory-fact-meta"><span>${escapeHtml(normalizeNamespace(fact.namespace))}</span><span>${escapeHtml(primaryTag(fact) || (tags.length ? tags.join(', ') : 'No tags'))}</span><span>${escapeHtml((fact.created_at || '').slice(0, 10) || 'Date unavailable')}</span></span>`;
    button.addEventListener('click', () => showDetail(fact.id, button));
    list.appendChild(button);
  });
  const more = document.getElementById('memory-more');
  more.hidden = memoryShown >= matches.length;
  more.textContent = `Show ${Math.min(50, matches.length - memoryShown)} more (${matches.length - memoryShown} remaining)`;
}

['memory-text-filter', 'memory-ns-filter', 'memory-tag-filter', 'memory-lifecycle-filter'].forEach(id => {
  const element = document.getElementById(id);
  if (!element) return;
  element.addEventListener(id === 'memory-text-filter' || id === 'memory-tag-filter' ? 'input' : 'change', () => {
    if (id === 'memory-ns-filter') populateMemoryFilters(factsData?.nodes || []);
    memoryShown = 50;
    renderMemoryList();
  });
});
document.getElementById('memory-reset').addEventListener('click', () => {
  document.getElementById('memory-text-filter').value = '';
  document.getElementById('memory-ns-filter').value = '';
  document.getElementById('memory-tag-filter').value = '';
  document.getElementById('memory-lifecycle-filter').value = 'current';
  memoryShown = 50;
  populateMemoryFilters(factsData?.nodes || []);
  renderMemoryList();
});
document.getElementById('memory-more').addEventListener('click', () => {
  memoryShown += 50;
  renderMemoryList();
});

document.getElementById('mobile-menu-toggle').addEventListener('click', event => {
  const nav = document.getElementById('primary-nav');
  const expanded = event.currentTarget.getAttribute('aria-expanded') === 'true';
  event.currentTarget.setAttribute('aria-expanded', String(!expanded));
  nav.classList.toggle('open', !expanded);
});
