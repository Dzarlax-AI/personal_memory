// Forgotten tab: facts with recall_count === 0.
// Renders synchronously from the already-loaded factsData (populated by overview.js).

function renderForgotten(nodes) {
  const forgotten = nodes.filter(n => n.recall_count === 0)
    .sort((a, b) => (a.created_at || '').localeCompare(b.created_at || ''));
  const container = document.getElementById('forgotten-list');

  if (forgotten.length === 0) {
    container.innerHTML = '<div class="empty-state">All facts have been recalled at least once!</div>';
    return;
  }

  container.innerHTML = forgotten.map(n => {
    const date = (n.created_at || '').slice(0, 10);
    const proj = getProjectTag(n.tags);
    return `<div class="forgotten-item">
      <div class="fact-text">${escapeHtml((n.text || '').slice(0, 200))}${(n.text || '').length > 200 ? '...' : ''}</div>
      <div class="meta-col">
        <span style="color:${nsColor(n.namespace)}">${n.namespace}</span>
        ${proj ? `<span>${proj}</span>` : ''}
        <span>${date}</span>
        ${n.permanent ? '<span style="color:var(--orange)">permanent</span>' : ''}
      </div>
    </div>`;
  }).join('');
}
