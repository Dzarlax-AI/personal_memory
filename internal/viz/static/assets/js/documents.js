// Documents tab: collapsible folder tree of indexed RAG documents.
// Hidden entirely when the server reports RAG disabled (404 on /api/documents).

let documentsData = null;

async function loadDocuments() {
  try {
    const res = await fetch(`${BASE}/api/documents`);
    if (res.status === 404) {
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
    documentsData = await res.json();
    document.getElementById('docs-tab').style.display = '';
    renderDocuments();
  } catch (e) {
    document.getElementById('docs-content').innerHTML =
      `<div class="empty-state">Failed to load documents: ${escapeHtml(e.message)}</div>`;
  }
}

function renderDocuments() {
  const data = documentsData;
  const s = data.stats || {};
  const summary = `${s.total_files || 0} files · ${s.total_chunks || 0} chunks · ${s.total_folders || 0} folders`
    + (s.last_indexed ? ` · last indexed ${s.last_indexed.slice(0, 19).replace('T', ' ')} UTC` : '')
    + (data.documents_dir ? ` · root: ${data.documents_dir}` : '');
  document.getElementById('docs-summary').textContent = summary;
  document.getElementById('docs-badge').textContent = s.total_files || 0;

  const content = document.getElementById('docs-content');
  if (!data.folders || data.folders.length === 0) {
    content.innerHTML = '<div class="empty-state">No documents indexed yet. Run <code>reindex_documents()</code> to populate the index.</div>';
    return;
  }

  const query = document.getElementById('docs-search').value.trim().toLowerCase();
  const folders = data.folders.map(folder => {
    const files = (folder.files || []).filter(file => {
      if (!query) return true;
      return `${file.relative_path || file.path} ${file.first_heading || ''}`.toLowerCase().includes(query);
    });
    const chunkCount = files.reduce((sum, file) => sum + Number(file.chunks || 0), 0);
    return { ...folder, files, visible_chunk_count: chunkCount };
  }).filter(folder => folder.files.length > 0 || (!query && (folder.files || []).length === 0));

  const filesWithoutChunks = data.folders.flatMap(f => f.files || []).filter(file => Number(file.chunks || 0) === 0).length;
  const lowChunkFolders = data.folders.filter(f => Number(f.file_count || 0) > 0 && Number(f.chunk_count || 0) < Number(f.file_count || 0)).length;
  const health = [];
  if (filesWithoutChunks > 0) health.push(`${filesWithoutChunks} files without chunks`);
  if (lowChunkFolders > 0) health.push(`${lowChunkFolders} low-chunk folders`);
  document.getElementById('docs-health').textContent = health.length ? health.join(' · ') : `${folders.length} folders shown`;

  if (folders.length === 0) {
    content.innerHTML = '<div class="empty-state">No indexed documents match this search.</div>';
    return;
  }

  const html = folders.map(f => {
    const rel = f.relative_path || '/';
    const files = (f.files || []).map(file => {
      const heading = file.first_heading ? `<div class="file-heading">${escapeHtml(file.first_heading)}</div>` : '';
      const date = (file.last_indexed || '').slice(0, 10);
      const chunks = Number(file.chunks || 0);
      const stale = chunks === 0 ? '<span class="status-warn">no chunks</span><br>' : '';
      return `<div class="file-row">
        <div>
          <div class="file-name">${escapeHtml(file.relative_path || file.path)}</div>
          ${heading}
        </div>
        <div class="file-meta">${stale}${chunks} chunk${chunks === 1 ? '' : 's'}<br>${date}</div>
      </div>`;
    }).join('');
    const visibleChunks = Number(f.visible_chunk_count || 0);
    const folderWarn = visibleChunks < f.files.length ? ' · low chunks' : '';
    return `<div class="folder-row ${query ? 'expanded' : ''}">
      <div class="folder-header" onclick="this.parentNode.classList.toggle('expanded')">
        <span><span class="folder-chevron">▶</span> <span class="folder-path">${escapeHtml(rel)}</span></span>
        <span class="folder-meta">${f.files.length} file${f.files.length === 1 ? '' : 's'} · ${visibleChunks} chunks${folderWarn}</span>
      </div>
      <div class="folder-files">${files}</div>
    </div>`;
  }).join('');

  content.innerHTML = `<div class="docs-list">${html}</div>`;
}

document.getElementById('docs-search').addEventListener('input', () => {
  if (documentsData) renderDocuments();
});
