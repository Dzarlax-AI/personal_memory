// Documents tab: inventory is deliberately fetched only after activation.

let documentsData = null;
let documentsPromise = null;
let documentStatusPromise = null;
let documentsShown = 50;
const FILE_PAGE_SIZE = 50;
let visibleDocumentFolders = [];
let documentsFreshUntil = 0;

function documentsAreStale() {
  return !Number.isFinite(documentsFreshUntil) || documentsFreshUntil <= 0 || Date.now() >= documentsFreshUntil;
}

function setDocumentsVisible(visible) {
  const tab = document.getElementById('docs-tab');
  tab.style.display = visible ? '' : 'none';
  if (!visible && document.getElementById('documents-view').classList.contains('active')) {
    activateTab('overview', false);
  }
}

async function loadDocumentStatus() {
  if (documentStatusPromise) return documentStatusPromise;
  documentStatusPromise = (async () => {
    const res = await fetch(`${BASE}/api/documents/status`);
    if (!res.ok) throw new Error(await responseMessage(res));
    const status = await res.json();
    setDocumentsVisible(Boolean(status.enabled));
    // A direct Documents route can finish the full inventory before this
    // lightweight status request. Never downgrade its richer summary/badge.
    if (status.enabled && !documentsData) {
      // Status always has chunk count, but it is not a document count. Only
      // render a numeric badge once the cache supplies a real file total.
      document.getElementById('docs-badge').textContent = status.cached ? (status.total_files ?? 0) : '—';
      document.getElementById('docs-summary').textContent = status.cached
        ? `${status.total_files || 0} files · ${status.total_chunks || 0} chunks · ${status.total_folders || 0} folders`
        : `${status.total_chunks || 0} indexed chunks · select this tab for the full inventory`;
    }
    return status;
  })().catch(error => {
    documentStatusPromise = null;
    // Keep the tab reachable for a retry through its full loader when status is
    // temporarily unavailable; do not mistake an error for RAG being disabled.
    if (!documentsData) {
      document.getElementById('docs-summary').textContent = 'Document availability could not be checked. Select this tab to retry.';
    }
    throw error;
  });
  return documentStatusPromise;
}

async function loadDocuments(force = false) {
  if (documentsPromise) return documentsPromise;
  if (documentsData && !force && !documentsAreStale()) return documentsData;
  const content = document.getElementById('docs-content');
  const refresh = document.getElementById('docs-refresh');
  refresh.disabled = true;
  content.innerHTML = '<div class="loading"><div class="spinner"></div>Loading indexed documents...</div>';
  documentsPromise = (async () => {
    const endpoint = force ? `${BASE}/api/documents?refresh=1` : `${BASE}/api/documents`;
    const res = await fetch(endpoint);
    if (res.status === 404) {
      setDocumentsVisible(false);
      return null;
    }
    if (!res.ok) throw new Error(await responseMessage(res));
    documentsData = await res.json();
    const parsedExpiry = Date.parse(documentsData.cache_expires_at || '');
    documentsFreshUntil = Number.isNaN(parsedExpiry) ? 0 : parsedExpiry;
    setDocumentsVisible(true);
    documentsShown = 50;
    renderDocuments();
    return documentsData;
  })().catch(error => {
    documentsPromise = null;
    documentsFreshUntil = 0;
    renderRetry(content, `Failed to load documents: ${error.message || error}`, () => loadDocuments(force));
    return null;
  }).finally(() => {
    documentsPromise = null;
    refresh.disabled = false;
  });
  return documentsPromise;
}

function filteredDocumentFolders() {
  const query = document.getElementById('docs-search').value.trim().toLowerCase();
  return documentsData.folders.map(folder => {
    const files = (folder.files || []).filter(file => !query ||
      `${file.relative_path || file.path} ${file.first_heading || ''}`.toLowerCase().includes(query));
    return { ...folder, files, visible_chunk_count: files.reduce((sum, file) => sum + Number(file.chunks || 0), 0) };
  }).filter(folder => folder.files.length > 0 || (!query && (folder.files || []).length === 0));
}

function renderDocuments() {
  const data = documentsData;
  const stats = data.stats || {};
  document.getElementById('docs-summary').textContent = `${stats.total_files || 0} files · ${stats.total_chunks || 0} chunks · ${stats.total_folders || 0} folders`
    + (stats.last_indexed ? ` · last indexed ${stats.last_indexed.slice(0, 19).replace('T', ' ')} UTC` : '');
  document.getElementById('docs-badge').textContent = stats.total_files || 0;

  const content = document.getElementById('docs-content');
  const more = document.getElementById('docs-more');
  if (!data.folders || data.folders.length === 0) {
    content.innerHTML = '<div class="empty-state">No documents indexed yet. Run <code>reindex_documents()</code> to populate the index.</div>';
    more.hidden = true;
    return;
  }

  const folders = filteredDocumentFolders();
  const filesWithoutChunks = data.folders.flatMap(folder => folder.files || []).filter(file => Number(file.chunks || 0) === 0).length;
  document.getElementById('docs-health').textContent = filesWithoutChunks
    ? `${folders.length} matching folders · ${filesWithoutChunks} files without chunks`
    : `${folders.length} matching folders`;
  if (folders.length === 0) {
    content.innerHTML = '<div class="empty-state">No indexed documents match this search.</div>';
    more.hidden = true;
    return;
  }

  const visible = folders.slice(0, documentsShown);
  visibleDocumentFolders = visible;
  content.innerHTML = `<div class="docs-list">${visible.map((folder, index) => documentFolderHTML(folder, index)).join('')}</div>`;
  more.hidden = documentsShown >= folders.length;
  more.textContent = `Show ${Math.min(50, folders.length - documentsShown)} more folders (${folders.length - documentsShown} remaining)`;
  content.querySelectorAll('.folder-header').forEach(button => {
    button.addEventListener('click', () => toggleDocumentFolder(button));
  });
}

function documentFolderHTML(folder, index) {
  const rel = folder.relative_path || '/';
  const folderID = `folder-files-${index}`;
  const visibleChunks = Number(folder.visible_chunk_count || 0);
  return `<div class="folder-row"><button class="folder-header" type="button" data-folder-index="${index}" aria-expanded="false" aria-controls="${folderID}"><span><span class="folder-chevron" aria-hidden="true">▶</span> <span class="folder-path">${escapeHtml(rel)}</span></span><span class="folder-meta">${folder.files.length} file${folder.files.length === 1 ? '' : 's'} · ${visibleChunks} chunks</span></button><div class="folder-files" id="${folderID}" hidden data-files-rendered="0"></div></div>`;
}

function toggleDocumentFolder(button) {
  const expanded = button.getAttribute('aria-expanded') === 'true';
  const files = document.getElementById(button.getAttribute('aria-controls'));
  if (!expanded && files.dataset.filesRendered === '0') {
    const folder = visibleDocumentFolders[Number(button.dataset.folderIndex)];
    renderDocumentFilePage(files, folder?.files || []);
  }
  button.setAttribute('aria-expanded', String(!expanded));
  files.hidden = expanded;
  button.closest('.folder-row').classList.toggle('expanded', !expanded);
}

// File rows are created only when their folder opens, and in bounded pages, so
// a large inventory cannot create a huge hidden DOM on first render.
function renderDocumentFilePage(container, files) {
  const start = Number(container.dataset.filesRendered || 0);
  const page = files.slice(start, start + FILE_PAGE_SIZE);
  page.forEach(file => container.appendChild(documentFileRow(file)));
  const rendered = start + page.length;
  container.dataset.filesRendered = String(rendered);
  const existingMore = container.querySelector('.folder-files-more');
  if (existingMore) existingMore.remove();
  if (rendered < files.length) {
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'toolbar-btn load-more folder-files-more';
    more.textContent = `Show ${Math.min(FILE_PAGE_SIZE, files.length - rendered)} more files (${files.length - rendered} remaining)`;
    more.addEventListener('click', () => renderDocumentFilePage(container, files));
    container.appendChild(more);
  }
}

function documentFileRow(file) {
  const row = document.createElement('div');
  row.className = 'file-row';
  const detail = document.createElement('div');
  const name = document.createElement('div');
  name.className = 'file-name';
  name.textContent = file.relative_path || file.path || '';
  detail.appendChild(name);
  if (file.first_heading) {
    const heading = document.createElement('div');
    heading.className = 'file-heading';
    heading.textContent = file.first_heading;
    detail.appendChild(heading);
  }
  const meta = document.createElement('div');
  meta.className = 'file-meta';
  const chunks = Number(file.chunks || 0);
  if (chunks === 0) {
    const warning = document.createElement('span');
    warning.className = 'status-warn';
    warning.textContent = 'no chunks';
    meta.append(warning, document.createElement('br'));
  }
  meta.append(`${chunks} chunk${chunks === 1 ? '' : 's'}`, document.createElement('br'), (file.last_indexed || '').slice(0, 10));
  row.append(detail, meta);
  return row;
}

document.getElementById('docs-search').addEventListener('input', () => {
  documentsShown = 50;
  if (documentsData) renderDocuments();
});
document.getElementById('docs-more').addEventListener('click', () => {
  documentsShown += 50;
  renderDocuments();
});
document.getElementById('docs-refresh').addEventListener('click', () => loadDocuments(true));
