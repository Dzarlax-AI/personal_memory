// Documents tab: collapsible folder tree of indexed RAG documents.
// Hidden entirely when the server reports RAG disabled (404 on /api/documents).

async function loadDocuments() {
  try {
    const res = await fetch(`${BASE}/api/documents`);
    if (res.status === 404) {
      // RAG disabled — hide the tab and bounce back to overview if the
      // user had deeplinked to /viz/documents.
      document.getElementById('docs-tab').style.display = 'none';
      const docsView = document.getElementById('documents-view');
      if (docsView && docsView.classList.contains('active')) {
        activateTab('overview', false);
      }
      return;
    }
    if (!res.ok) {
      document.getElementById('docs-content').innerHTML =
        `<div class="empty-state">Failed to load documents (${res.status}).</div>`;
      return;
    }
    const data = await res.json();
    document.getElementById('docs-tab').style.display = '';
    renderDocuments(data);
  } catch (e) {
    document.getElementById('docs-content').innerHTML =
      `<div class="empty-state">Failed to load documents: ${escapeHtml(e.message)}</div>`;
  }
}

function renderDocuments(data) {
  const s = data.stats || {};
  const summary = `${s.total_files || 0} files · ${s.total_chunks || 0} chunks · ${s.total_folders || 0} folders`
    + (s.last_indexed ? ` · last indexed ${s.last_indexed.slice(0, 19).replace('T', ' ')} UTC` : '')
    + (data.documents_dir ? ` · root: ${escapeHtml(data.documents_dir)}` : '');
  document.getElementById('docs-summary').innerHTML = summary;
  document.getElementById('docs-badge').textContent = s.total_files || 0;

  const content = document.getElementById('docs-content');
  if (!data.folders || data.folders.length === 0) {
    content.innerHTML = '<div class="empty-state">No documents indexed yet. Run <code>reindex_documents()</code> to populate the index.</div>';
    return;
  }

  const html = data.folders.map(f => {
    const rel = f.relative_path || '/';
    const files = (f.files || []).map(file => {
      const heading = file.first_heading ? `<div class="file-heading">${escapeHtml(file.first_heading)}</div>` : '';
      const date = (file.last_indexed || '').slice(0, 10);
      return `<div class="file-row">
        <div>
          <div class="file-name">${escapeHtml(file.relative_path || file.path)}</div>
          ${heading}
        </div>
        <div class="file-meta">${file.chunks} chunk${file.chunks === 1 ? '' : 's'}<br>${date}</div>
      </div>`;
    }).join('');
    return `<div class="folder-row">
      <div class="folder-header" onclick="this.parentNode.classList.toggle('expanded')">
        <span><span class="folder-chevron">▶</span> <span class="folder-path">${escapeHtml(rel)}</span></span>
        <span class="folder-meta">${f.file_count} file${f.file_count === 1 ? '' : 's'} · ${f.chunk_count} chunks</span>
      </div>
      <div class="folder-files">${files}</div>
    </div>`;
  }).join('');

  content.innerHTML = `<div class="docs-list">${html}</div>`;
}
