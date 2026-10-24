// Read-only maintenance history and private human assessments.
let aiReviewLoaded = false;
let aiReviewLoading = false;
let aiReviewRuns = [];
let aiReviewCursor = '';
let aiReviewComplete = false;
let aiReviewStatusData = null;
let aiReviewSelection = null;
let aiReviewSelectionGeneration = 0;
let aiReviewPageGeneration = 0;
let aiReviewPageLoading = false;
let aiReviewProposalRows = [];
let aiReviewProposalReadFailures = 0;
const aiReviewDraftCache = new Map();
const aiReviewDraftData = new Map();
const aiReviewAssessmentCache = new Map();
const aiReviewVersionCache = new Map();
function fetchAIReviewDraft(runID, digest) {
  const key = `${runID}:${digest}`;
  if (!aiReviewDraftCache.has(key)) {
    const promise = aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(runID)}/drafts/${encodeURIComponent(digest)}`)
      .then(data => { aiReviewDraftData.set(key, data); return data; })
      .catch(error => { aiReviewDraftCache.delete(key); throw error; });
    aiReviewDraftCache.set(key, promise);
  }
  return aiReviewDraftCache.get(key);
}
function fetchAIReviewAssessments(runID, digest) {
  const key = `${runID}:${digest}`;
  if (!aiReviewAssessmentCache.has(key)) {
    const promise = aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(runID)}/drafts/${encodeURIComponent(digest)}/assessments`)
      .catch(error => { aiReviewAssessmentCache.delete(key); throw error; });
    aiReviewAssessmentCache.set(key, promise);
  }
  return aiReviewAssessmentCache.get(key);
}
let aiReviewFilters = { from: '', to: '', outcome: '', project: '' };

const reviewEl = id => document.getElementById(id);
const reviewText = value => value === null || value === undefined || value === '' ? 'Not recorded' : String(value);
const reviewNode = (tag, text, className) => {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = String(text);
  if (className) node.className = className;
  return node;
};
const reviewDate = value => {
  if (!value) return 'Time unavailable';
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? String(value) : d.toLocaleString(undefined, { timeZoneName: 'short' });
};
async function reviewMapLimit(values, limit, callback) {
  const results = new Array(values.length);
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, values.length) }, async () => {
    while (true) {
      const index = next++;
      if (index >= values.length) return;
      results[index] = await callback(values[index], index);
    }
  });
  await Promise.all(workers);
  return results;
}
const reviewDuration = value => value === null || value === undefined ? 'Unknown' : `${(value / 1000).toFixed(1)} s`;
const reviewUsage = (known, input, output) => known ? `${Number(input || 0).toLocaleString()} in · ${Number(output || 0).toLocaleString()} out` : 'Unknown';

function reviewSignInLink() {
  const link = reviewNode('a', 'Sign in again', 'btn btn--secondary');
  link.href = `${BASE}/ai-review`;
  link.target = '_blank';
  link.rel = 'noopener noreferrer';
  return link;
}

async function aiReviewRequest(path, options = {}) {
  // Authentication must run as a navigation, never a cross-origin fetch redirect.
  const response = await fetch(`${BASE}${path}`, { cache: 'no-store', ...options, redirect: 'manual' });
  if (response.type === 'opaqueredirect' || response.status === 401) {
    const error = new Error('Sign-in is required. Open AI Review in another tab, sign in, then return and retry');
    error.status = 401;
    error.authRequired = true;
    throw error;
  }
  if (!response.ok) {
    const responseText = await response.text();
    let details = responseText;
    try { details = JSON.parse(responseText).error || responseText; } catch (_) { /* Keep only for local error classification. */ }
    const error = new Error(`Request failed (${response.status})`);
    error.status = response.status;
    error.details = details;
    throw error;
  }
  return response.json();
}

async function loadAIReview(refresh = false) {
  if (aiReviewLoading) return;
  aiReviewLoading = true;
  if (refresh) {
    aiReviewPageGeneration++;
    aiReviewRuns = [];
    aiReviewCursor = '';
    aiReviewComplete = false;
    aiReviewLoaded = false;
    aiReviewDraftCache.clear();
    aiReviewDraftData.clear();
    aiReviewAssessmentCache.clear();
  }
  const from = reviewEl('ai-review-from');
  const to = reviewEl('ai-review-to');
  const timezoneLabel = reviewEl('ai-review-timezone');
  if (timezoneLabel) {
    const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local time';
    timezoneLabel.textContent = `Dates interpreted in browser local timezone (${zone})`;
  }
  if (!from.value && !to.value) {
    [from.value, to.value] = aiReviewDates.defaultLast14Days(new Date());
  }
  aiReviewFilters = { from: from.value, to: to.value, outcome: reviewEl('ai-review-outcome').value, project: reviewEl('ai-review-project').value };
  const statusBox = reviewEl('ai-review-status');
  statusBox.replaceChildren(reviewNode('p', 'Loading proposal history…', 'ai-review-muted'));
  try {
    const status = await aiReviewRequest('/api/ai-review/status');
    aiReviewStatusData = status;
    renderAIReviewStatus(status);
    const initial = await loadAIReviewPage(true);
    if (initial) aiReviewLoaded = true;
    if (aiReviewStatusData?.availability === 'available') statusBox.replaceChildren();
  } catch (error) {
    statusBox.replaceChildren(reviewNode('p', `Unable to load AI Review: ${error.message}`, 'ai-review-error'));
    if (error.authRequired) statusBox.append(reviewSignInLink());
    aiReviewLoaded = true;
  } finally { aiReviewLoading = false; }
}

function renderAIReviewStatus(status) {
  const box = reviewEl('ai-review-diagnostics-status');
  if (!box) return;
  box.replaceChildren();
  const line = reviewNode('div', undefined, 'ai-review-status-line');
  line.append(reviewNode('strong', `Availability: ${reviewText(status.availability)}`));
  line.append(reviewNode('span', `Model: ${reviewText(status.model)}`));
  line.append(reviewNode('span', `Mode: ${reviewText(status.mode)}`));
  if (status.publication) line.append(reviewNode('span', `Publication: ${reviewText(status.publication)}`));
  line.append(reviewNode('span', `Scheduler: ${status.scheduler_enabled === true ? 'Enabled' : status.scheduler_enabled === false ? 'Disabled' : 'Unknown'}`));
  line.append(reviewNode('span', `Assessment: ${status.assessment_writable ? 'Writable' : 'Read only'}`));
  line.append(reviewNode('span', `History: ${reviewText(status.history)}`));
  box.append(line);
  const note = status.availability === 'available' ? 'This page reads recorded runs and the currently configured model and scheduler state. Assessments are saved for review only.' : 'Review history is unavailable for the current configuration.';
  box.append(reviewNode('p', note, status.availability === 'available' ? 'ai-review-muted' : 'ai-review-error'));
  const visibleStatus = reviewEl('ai-review-status');
  if (visibleStatus) {
    visibleStatus.replaceChildren();
    if (status.availability !== 'available') visibleStatus.append(reviewNode('p', `AI review history: ${reviewText(status.availability)}.`, 'ai-review-warning'));
  }
}

function rangeParams() {
  const p = new URLSearchParams();
  p.set('limit', '50');
  if (aiReviewFilters.from && aiReviewFilters.to) {
    const range = aiReviewDates.localDateRange(aiReviewFilters.from, aiReviewFilters.to);
    p.set('from', range.from);
    p.set('to', range.to);
  } else if (aiReviewFilters.from) p.set('from', aiReviewDates.localDateStart(aiReviewFilters.from));
  else if (aiReviewFilters.to) p.set('to', aiReviewDates.localDateEnd(aiReviewFilters.to));
  if (aiReviewCursor) p.set('cursor', aiReviewCursor);
  return p.toString();
}

async function loadAIReviewPage(reset = false) {
  const requestGeneration = aiReviewPageGeneration;
  const selectionGeneration = aiReviewSelectionGeneration;
  try {
    const result = await aiReviewRequest(`/api/ai-review/runs?${rangeParams()}`);
    if (requestGeneration !== aiReviewPageGeneration) return false;
    if (result.availability !== 'available') {
      reviewEl('ai-review-runs').replaceChildren(reviewNode('p', `History: ${reviewText(result.availability)}`, 'empty-state'));
      reviewEl('ai-review-list-meta').textContent = 'No run list is available.';
      return true;
    }
    if (reset) aiReviewRuns = [];
    const knownIDs = new Set(aiReviewRuns.map(run => run.run_id));
    aiReviewRuns.push(...(Array.isArray(result.runs) ? result.runs : []).filter(run => !knownIDs.has(run.run_id)));
    aiReviewCursor = result.next_cursor || '';
    aiReviewComplete = result.complete === true;
    await updateAIReviewOptions();
    if (requestGeneration !== aiReviewPageGeneration) return false;
    renderAIReviewDiagnosticsRuns();
    renderAIReviewRuns();
    await rebuildAIReviewProposals(requestGeneration);
    if (requestGeneration !== aiReviewPageGeneration) return false;
    reviewEl('ai-review-list-meta').textContent = `${aiReviewRuns.length} runs loaded${result.complete ? ' · complete for this date range' : ' · partial history; load older runs for more'}`;
    reviewEl('ai-review-more').hidden = !aiReviewCursor;
    const urlRun = new URLSearchParams(location.search).get('run');
    const urlDraft = new URLSearchParams(location.search).get('draft');
    if (urlRun && selectionGeneration === aiReviewSelectionGeneration && (!aiReviewSelection || reset)) {
      let run = aiReviewRuns.find(item => item.run_id === urlRun);
      if (!run) {
        try { run = (await aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(urlRun)}`)).run; }
        catch (error) {
          if (selectionGeneration === aiReviewSelectionGeneration && requestGeneration === aiReviewPageGeneration) {
            reviewEl('ai-review-detail').replaceChildren(reviewNode('p', `Run unavailable. It may be outside retained history or the link may be invalid (${error.message}).`, 'ai-review-error'));
            if (error.authRequired) reviewEl('ai-review-detail').append(reviewSignInLink());
          }
        }
      }
      if (run && selectionGeneration === aiReviewSelectionGeneration && requestGeneration === aiReviewPageGeneration) await openAIReviewRun(run.run_id, urlDraft || '', new URLSearchParams(location.search).get('project') || '', false);
    } else if (aiReviewSelection && reset) {
      const run = aiReviewRuns.find(item => item.run_id === aiReviewSelection.runID);
      if (run) await openAIReviewRun(run.run_id, aiReviewSelection.digest, aiReviewSelection.projectID, false);
    }
    return true;
  } catch (error) {
    if (requestGeneration !== aiReviewPageGeneration) return false;
    reviewEl('ai-review-runs').replaceChildren(reviewNode('p', `Unable to load run history: ${error.message}`, 'ai-review-error'));
    if (error.authRequired) reviewEl('ai-review-runs').append(reviewSignInLink());
    return false;
  }
}

function outcomeOf(run) { return run.completion?.outcome || run.history_status || 'in_progress'; }
function runStarted(run) { return run.started?.started_at || run.run_id; }
function draftLabel(ref) {
  if (!ref) return 'No draft recorded';
  if (ref.status && ref.status !== 'available') return `Draft ${ref.status}`;
  return ref.sha256 ? `Draft ${ref.sha256.slice(0, 12)}` : 'Draft reference incomplete';
}

function assessmentStatus(row) {
  if (!row.assessmentKnown) return 'Assessment unknown';
  const value = row.assessment;
  if (!value) return 'Unreviewed';
  if (value.drift) return 'Stale assessment';
  return ({ acceptable: 'Acceptable', needs_correction: 'Needs correction', rejected: 'Rejected' })[value.request?.verdict] || 'Assessment unknown';
}

function publicationStatus(project) {
  if (/^[a-f0-9]{64}$/.test(project.current_entry_hash || '') && /^[a-f0-9]{64}$/.test(project.proposed_entry_hash || '')) {
    return project.current_entry_hash === project.proposed_entry_hash ? 'Current catalog matches proposal' : 'Proposal not current';
  }
  return 'Publication state unknown';
}

async function rebuildAIReviewProposals(requestGeneration = aiReviewPageGeneration) {
  const references = [];
  for (const run of aiReviewRuns) for (const ref of run.drafts || []) if (ref.sha256) references.push({ run, ref });
  const details = await reviewMapLimit(references, 4, async item => {
    try { return { ...item, detail: await fetchAIReviewDraft(item.run.run_id, item.ref.sha256) }; }
    catch (_) { return { ...item, error: true }; }
  });
  if (requestGeneration !== aiReviewPageGeneration) return;
  const groups = new Map();
  let failures = 0;
  for (const item of details) {
    if (item.error) { failures++; continue; }
    for (const project of item.detail.projects || []) {
      if (!/^[a-f0-9]{64}$/.test(project.project_id || '') || !/^[a-f0-9]{64}$/.test(project.proposed_entry_hash || '')) { failures++; continue; }
      const key = `${project.project_id}:${project.proposed_entry_hash}`;
      const row = groups.get(key) || { key, project, candidates: [] };
      row.candidates.push({ run: item.run, draft: item.ref, project, detail: item.detail });
      groups.set(key, row);
    }
  }
  const rows = [...groups.values()];
  for (const row of rows) {
    row.candidates.sort((a, b) => Date.parse(runStarted(b.run)) - Date.parse(runStarted(a.run)) || b.run.run_id.localeCompare(a.run.run_id));
    const newest = row.candidates[0];
    row.run = newest.run; row.draft = newest.draft; row.project = newest.project; row.detail = newest.detail;
    row.assessmentKnown = true; row.assessment = null;
    const checks = await reviewMapLimit(row.candidates, 4, async candidate => {
      try { return { candidate, saved: await fetchAIReviewAssessments(candidate.run.run_id, candidate.draft.sha256) }; }
      catch (_) { return { error: true }; }
    });
    if (requestGeneration !== aiReviewPageGeneration) return;
    const matching = [];
    for (const result of checks) {
      if (result.error) { row.assessmentKnown = false; continue; }
      for (const assessment of result.saved.assessments || []) {
        if (assessment.project_id === row.project.project_id && assessment.request?.proposed_entry_hash === row.project.proposed_entry_hash) matching.push({ assessment, runID: result.candidate.run.run_id });
      }
    }
    matching.sort((a, b) => Date.parse(b.assessment.saved_at || '') - Date.parse(a.assessment.saved_at || '') || (b.assessment.revision || 0) - (a.assessment.revision || 0) || b.runID.localeCompare(a.runID));
    row.assessment = matching[0]?.assessment || null;
  }
  if (requestGeneration !== aiReviewPageGeneration) return;
  aiReviewProposalRows = rows.sort((a, b) => Date.parse(runStarted(b.run)) - Date.parse(runStarted(a.run)) || a.project.project_id.localeCompare(b.project.project_id));
  aiReviewProposalReadFailures = failures;
  renderAIReviewRuns();
  renderAIReviewMetrics(aiReviewComplete, requestGeneration);
}

function filteredAIReviewProposals() {
  return aiReviewProposalRows.filter(row => {
    if (aiReviewFilters.outcome && outcomeOf(row.run) !== aiReviewFilters.outcome) return false;
    if (aiReviewFilters.project && row.project.project_id !== aiReviewFilters.project) return false;
    return true;
  });
}

function countProposalAssessments(rows) {
  const counts = { acceptable: 0, needs_correction: 0, rejected: 0, unreviewed: 0, stale: 0, unknown: 0 };
  for (const row of rows) {
    if (!row.assessmentKnown) counts.unknown++;
    else if (!row.assessment) counts.unreviewed++;
    else if (row.assessment.drift) counts.stale++;
    else if (Object.hasOwn(counts, row.assessment.request?.verdict)) counts[row.assessment.request.verdict]++;
    else counts.unknown++;
  }
  return counts;
}

function renderAIReviewDiagnosticsRuns() {
  const root = reviewEl('ai-review-diagnostics-runs');
  if (!root) return;
  root.replaceChildren();
  root.append(reviewNode('p', 'Run history uses the selected date, outcome, and project filters.', 'ai-review-muted'));
  const runs = aiReviewRuns.filter(run => {
    if (aiReviewFilters.outcome && outcomeOf(run) !== aiReviewFilters.outcome) return false;
    if (aiReviewFilters.project && !(run.drafts || []).some(ref => (aiReviewDraftData.get(`${run.run_id}:${ref.sha256}`)?.projects || []).some(project => project.project_id === aiReviewFilters.project))) return false;
    return true;
  });
  for (const run of runs) {
    const card = reviewNode('button', undefined, 'ai-review-run'); card.type = 'button';
    card.append(reviewNode('span', `${outcomeOf(run)} · ${reviewDate(runStarted(run))}`, 'ai-review-run-header'));
    card.append(reviewNode('span', `Model: ${reviewText(run.started?.model)} · ${reviewDuration(run.completion?.duration_ms)} · ${run.completion?.provider_attempts ?? 'Unknown'} attempts`, 'ai-review-run-meta'));
    const reasons = [...new Set([run.completion?.reason_code, ...(run.events || []).map(e => e.reason_code)].filter(Boolean))];
    if (reasons.length) card.append(reviewNode('span', `Reasons: ${reasons.join(', ')}`, 'ai-review-reasons'));
    card.addEventListener('click', () => openAIReviewRun(run.run_id));
    root.append(card);
  }
}

function renderAIReviewRuns() {
  const root = reviewEl('ai-review-runs');
  root.replaceChildren();
  const rows = filteredAIReviewProposals();
  const counts = countProposalAssessments(rows);
  const completeScope = aiReviewComplete && aiReviewProposalReadFailures === 0 && counts.unknown === 0;
  const scope = `${aiReviewFilters.from || 'Any date'} to ${aiReviewFilters.to || 'Any date'} · ${completeScope ? 'complete loaded scope' : 'partial loaded scope'}`;
  const summary = reviewEl('ai-updates-summary');
  if (summary) {
    summary.replaceChildren();
    summary.append(reviewNode('p', `${scope}${aiReviewProposalReadFailures ? ` · ${aiReviewProposalReadFailures} proposal detail${aiReviewProposalReadFailures === 1 ? '' : 's'} unavailable` : ''}. Assessments summarize only readable proposals; unknown reads are excluded from counts.`, 'ai-review-metric-note'));
    const badges = reviewNode('div', undefined, 'ai-updates-counts');
    for (const [key, label] of [['unreviewed','Unreviewed'],['stale','Stale assessment'],['acceptable','Acceptable'],['needs_correction','Needs correction'],['rejected','Rejected']]) {
      const badge = reviewNode('span', undefined, 'ai-updates-count'); badge.append(reviewNode('strong', String(counts[key])), reviewNode('span', label)); badges.append(badge);
    }
    if (counts.unknown > 0) badges.append(reviewNode('span', `Unknown · ${counts.unknown}`, 'ai-updates-count ai-updates-count--unknown'));
    summary.append(badges);
    summary.append(reviewNode('p', `${rows.length} distinct proposal${rows.length === 1 ? '' : 's'} · ${aiReviewRuns.length} run${aiReviewRuns.length === 1 ? '' : 's'} loaded${aiReviewComplete ? '' : ' · more history may contain additional proposals'}`, 'ai-review-muted'));
  }
  if (!rows.length) { root.append(reviewNode('p', aiReviewRuns.length ? 'No proposals match these filters.' : 'No project proposals in this loaded period.', 'empty-state')); return; }
  for (const row of rows) {
    const project = row.project;
    const card = reviewNode('button', undefined, 'ai-updates-proposal'); card.type = 'button';
    const title = project.proposed?.name || project.tag || project.project_id;
    card.append(reviewNode('span', title, 'ai-updates-proposal-title'));
    card.append(reviewNode('span', `${reviewText(project.namespace)}/${reviewText(project.tag)} · ${reviewDate(runStarted(row.run))}`, 'ai-review-run-meta'));
    const states = reviewNode('span', undefined, 'ai-updates-proposal-states');
    states.append(reviewNode('span', assessmentStatus(row), `ai-updates-pill ${row.assessmentKnown ? 'ai-updates-pill--assessment' : 'ai-updates-pill--unknown'}`));
    const publication = publicationStatus(project);
    states.append(reviewNode('span', publication, `ai-updates-pill ${publication === 'Current catalog matches proposal' ? 'ai-updates-pill--published' : 'ai-updates-pill--pending'}`));
    card.append(states);
    card.append(reviewNode('span', `Latest run: ${outcomeOf(row.run)} · ${row.candidates.length} matching proposal record${row.candidates.length === 1 ? '' : 's'}`, 'ai-review-run-meta'));
    card.setAttribute('aria-label', `Open proposal for ${title}; ${assessmentStatus(row)}; ${publicationStatus(project)}`);
    card.addEventListener('click', () => openAIReviewProposal(row));
    root.append(card);
  }
}

async function updateAIReviewOptions() {
  const outcome = reviewEl('ai-review-outcome');
  const project = reviewEl('ai-review-project');
  const oldOutcome = outcome.value, oldProject = project.value;
  const outcomes = [...new Set(aiReviewRuns.filter(run => outcomeOf(run) !== 'no_changes').map(outcomeOf))].sort();
  const projectNames = new Map();
  const jobs = [...aiReviewRuns].flatMap(run => (run.drafts || []).filter(d => d.sha256).map(d => ({ run, draft: d })));
  await reviewMapLimit(jobs, 4, async ({ run, draft }) => {
    try {
      const detail = await fetchAIReviewDraft(run.run_id, draft.sha256);
      for (const project of detail.projects || []) if (project.proposed_entry_hash) projectNames.set(project.project_id, `${project.proposed?.name || project.tag || project.project_id} · ${project.namespace}/${project.tag}`);
    } catch (_) { /* Unavailable drafts do not create selectable project filters. */ }
  });
  outcome.replaceChildren(new Option('All outcomes', ''), ...outcomes.map(v => new Option(v, v)));
  project.replaceChildren(new Option('All projects', ''), ...[...projectNames].map(([v, label]) => new Option(label, v)));
  outcome.value = oldOutcome; project.value = oldProject;
}

async function renderAIReviewMetrics(complete, requestGeneration = aiReviewPageGeneration) {
  const root = reviewEl('ai-review-metrics-content');
  const summary = reviewEl('ai-review-metrics-summary');
  if (!root || !summary) return;
  if (requestGeneration !== aiReviewPageGeneration) return;
  const rows = filteredAIReviewProposals();
  const completeScope = complete && aiReviewProposalReadFailures === 0 && rows.every(row => row.assessmentKnown);
  summary.textContent = `Diagnostics · ${aiReviewRuns.length} runs · ${completeScope ? 'complete' : 'partial'} proposal scope`;
  root.replaceChildren();
  root.append(reviewNode('p', `Date range ${aiReviewFilters.from || 'any'} to ${aiReviewFilters.to || 'any'} · ${completeScope ? 'complete loaded scope' : 'partial loaded scope'}${aiReviewProposalReadFailures ? ` · ${aiReviewProposalReadFailures} proposal details unavailable` : ''}.`, 'ai-review-metric-note'));
  root.append(reviewNode('h4', 'Run reliability · recorded outcomes', 'ai-review-metric-heading'));
  const runOutcomes = new Map();
  aiReviewRuns.forEach(run => runOutcomes.set(outcomeOf(run), (runOutcomes.get(outcomeOf(run)) || 0) + 1));
  const reliability = reviewNode('div', undefined, 'ai-review-metric-cards');
  for (const [label, count] of [...runOutcomes.entries()].sort((a,b) => a[0].localeCompare(b[0]))) {
    const card = reviewNode('div', undefined, 'ai-review-metric'); card.append(reviewNode('strong', String(count)), reviewNode('span', label)); reliability.append(card);
  }
  if (!runOutcomes.size) reliability.append(reviewNode('p', 'No runs in this date range.', 'ai-review-muted'));
  root.append(reliability);
  const counts = countProposalAssessments(rows);
  root.append(reviewNode('h4', 'Proposal assessments · project level', 'ai-review-metric-heading'));
  root.append(reviewNode('p', 'Distinct project proposals are deduplicated by project ID and proposed entry hash. Counts cover the loaded period and filters. A failed assessment read is unknown, never unreviewed.', 'ai-review-metric-note'));
  const metrics = reviewNode('div', undefined, 'ai-review-metric-cards');
  for (const [key, label] of [['acceptable','Acceptable'],['needs_correction','Needs correction'],['rejected','Rejected'],['unreviewed','Unreviewed'],['stale','Stale assessment'],['unknown','Unknown']]) {
    const card = reviewNode('div', undefined, 'ai-review-metric'); card.append(reviewNode('strong', String(counts[key])), reviewNode('span', label)); metrics.append(card);
  }
  root.append(metrics);
}

function syncAIReviewURL(runID = '', digest = '', projectID = '', push = true) {
  const url = new URL(location.href);
  if (runID) url.searchParams.set('run', runID); else url.searchParams.delete('run');
  if (digest) url.searchParams.set('draft', digest); else url.searchParams.delete('draft');
  if (projectID) url.searchParams.set('project', projectID); else url.searchParams.delete('project');
  history[push ? 'pushState' : 'replaceState']({ tab: 'ai-review' }, '', url);
}

function confirmAIReviewNavigation() {
  const dirtyForms = [...document.querySelectorAll('.ai-review-assessment[data-dirty="true"]')].filter(form => form.isConnected);
  if (!dirtyForms.length) return true;
  if (!window.confirm('This assessment has unsaved changes. Leave this proposal and discard them?')) return false;
  dirtyForms.forEach(form => { form.dataset.dirty = 'false'; });
  return true;
}

function openAIReviewProposal(row) {
  if (!confirmAIReviewNavigation()) return;
  aiReviewSelection = { runID: row.run.run_id, digest: row.draft.sha256, projectID: row.project.project_id };
  syncAIReviewURL(row.run.run_id, row.draft.sha256, row.project.project_id);
  renderAIReviewRuns();
  reviewEl('ai-review-detail').replaceChildren(reviewNode('p', 'Loading project comparison…', 'ai-review-muted'));
  loadAIReviewDraft(row.run, row.draft.sha256, row.project.project_id);
}

async function openAIReviewRun(runID, digest = '', projectID = '', push = true) {
  if (!confirmAIReviewNavigation()) return;
  const selectionGeneration = ++aiReviewSelectionGeneration;
  if (digest && projectID) {
    let run = aiReviewRuns.find(item => item.run_id === runID);
    if (!run) try { run = (await aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(runID)}`)).run; } catch (_) { /* Existing fallback below reports the run lookup failure. */ }
    if (selectionGeneration !== aiReviewSelectionGeneration) return;
    if (run) {
      aiReviewSelection = { runID, digest, projectID };
      syncAIReviewURL(runID, digest, projectID, push);
      renderAIReviewRuns();
      reviewEl('ai-review-detail').replaceChildren(reviewNode('p', 'Loading project comparison…', 'ai-review-muted'));
      await loadAIReviewDraft(run, digest, projectID);
      return;
    }
  }
  aiReviewSelection = { runID, digest, projectID };
  syncAIReviewURL(runID, digest, projectID, push);
  renderAIReviewRuns();
  const panel = reviewEl('ai-review-detail'); panel.replaceChildren(reviewNode('p', 'Loading run details…', 'ai-review-muted'));
  try {
    const data = await aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(runID)}`);
    if (selectionGeneration !== aiReviewSelectionGeneration) return;
    renderAIReviewDetail(data.run, digest, projectID);
  } catch (error) {
    if (selectionGeneration === aiReviewSelectionGeneration) {
      panel.replaceChildren(reviewNode('p', `Unable to load run details: ${error.message}`, 'ai-review-error'));
      if (error.authRequired) panel.append(reviewSignInLink());
    }
  }
}

function renderAIReviewDetail(run, selectedDigest = '', selectedProjectID = '') {
  const panel = reviewEl('ai-review-detail'); panel.replaceChildren();
  const head = reviewNode('header', undefined, 'ai-review-detail-head');
  head.append(reviewNode('h3', `${outcomeOf(run)} · ${reviewDate(runStarted(run))}`));
  head.append(reviewNode('p', `Run ${run.run_id} · ${reviewText(run.started?.scope)} · ${reviewText(run.started?.model)}`, 'ai-review-muted'));
  const summary = reviewNode('dl', undefined, 'ai-review-summary');
  for (const [label, value] of [['Duration', reviewDuration(run.completion?.duration_ms)], ['Provider attempts', run.completion?.provider_attempts ?? 'Unknown'], ['Input / output tokens', reviewUsage(run.completion?.usage_known, run.completion?.input_tokens, run.completion?.output_tokens)], ['Projects', run.completion?.counts_known ? `${run.completion.projects_completed ?? 0} completed · ${run.completion.projects_skipped ?? 0} skipped` : 'Unknown']]) appendDefinition(summary, label, value);
  panel.append(head, summary);
  const draftsSection = reviewNode('section', undefined, 'ai-review-section'); draftsSection.append(reviewNode('h4', 'Drafts'));
  if (!(run.drafts || []).length) draftsSection.append(reviewNode('p', 'No draft recorded for this run.', 'ai-review-muted'));
  for (const ref of run.drafts || []) {
    const row = reviewNode('div', undefined, 'ai-review-draft-ref');
    row.append(reviewNode('span', `${draftLabel(ref)} · ${reviewText(ref.status)}`));
    if (ref.sha256) { const b = reviewNode('button', 'Inspect draft', 'btn btn--secondary'); b.type = 'button'; b.addEventListener('click', () => loadAIReviewDraft(run, ref.sha256)); row.append(b); }
    draftsSection.append(row);
  }
  panel.append(draftsSection);
  const events = reviewNode('section', undefined, 'ai-review-section'); events.append(reviewNode('h4', 'Run events'));
  if (!(run.events || []).length) events.append(reviewNode('p', 'No events recorded.', 'ai-review-muted'));
  for (const event of run.events || []) {
    const item = reviewNode('div', undefined, 'ai-review-event');
    item.append(reviewNode('time', reviewDate(event.at)), reviewNode('strong', event.type || 'Event'));
    item.append(reviewNode('span', [event.reason_code, event.count !== undefined ? `count ${event.count}` : '', event.count_skipped !== undefined ? `skipped ${event.count_skipped}` : ''].filter(Boolean).join(' · ') || 'No additional details'));
    events.append(item);
  }
  panel.append(events);
  if (selectedDigest) loadAIReviewDraft(run, selectedDigest, selectedProjectID);
}

function appendDefinition(root, label, value) {
  root.append(reviewNode('dt', label), reviewNode('dd', reviewText(value)));
}

async function loadProjectVersions(card, project, cursor = '') {
  const projectID = project.project_id;
  const previous = card.querySelector('.ai-review-catalog-history'); if (previous) previous.remove();
  const section = reviewNode('section', undefined, 'ai-review-section ai-review-catalog-history');
  section.append(reviewNode('h4', 'Published description history'));
  section.append(reviewNode('p', 'Versions show changes to this project card. Restoring creates a new version and pauses automatic publication for this project.', 'ai-review-muted'));
  const status = reviewNode('p', 'Loading version history…', 'ai-review-muted'); section.append(status); card.append(section);
  try {
    const data = await aiReviewRequest(`/api/ai-review/projects/${encodeURIComponent(projectID)}/versions?limit=30${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    const prior = cursor && aiReviewVersionCache.get(projectID);
    if (prior && prior.current_hash === data.current_hash) data.versions = [...(prior.versions || []), ...(data.versions || [])];
    aiReviewVersionCache.set(projectID, data);
    status.remove();
    const policy = data.policy || {};
    const holdLine = reviewNode('p', policy.hold ? 'Automatic publication is on hold for this project.' : 'No project publication hold is set. The scheduler and publication policy above determine whether automatic updates can run.', policy.hold ? 'ai-review-warning' : 'ai-review-muted');
    section.append(holdLine);
    const versions = data.versions || [];
    const activeVersionIndex = data.current_entry && policy.current_entry_hash ? versions.findIndex(version => version.entry_hash === policy.current_entry_hash) : -1;
    for (const [versionIndex, version] of versions.entries()) {
      const row = reviewNode('article', undefined, 'ai-review-version-row');
      let versionLabel = 'Historical source unknown';
      if (version.kind === 'maintenance' || version.provenance === 'maintenance') versionLabel = 'Automatically published';
      else if (version.kind === 'restore' || version.provenance === 'restore') versionLabel = 'Manually restored';
      else if (version.kind === 'registration' || version.provenance === 'registration') versionLabel = 'Project registered';
      else if (version.kind === 'manual' || version.provenance === 'publish') versionLabel = 'Manually published';
      const activeLabel = versionIndex === activeVersionIndex ? ' · Current description' : '';
      row.append(reviewNode('strong', `${versionLabel}${activeLabel} · ${reviewDate(version.created_at)}`));
      row.append(reviewNode('p', (version.diff || []).join(' · ') || 'No card description diff recorded.', 'ai-review-muted'));
      if (version.model || version.run_id || version.rules || version.proposal_checksum) {
        const provenance = reviewNode('p', [version.model && `Model ${version.model}`, version.rules && `Rules ${version.rules}`, version.proposal_checksum && `Proposal ${version.proposal_checksum.slice(0, 16)}`].filter(Boolean).join(' · '), 'ai-review-muted');
        if (version.run_id) {
          const runLink = reviewNode('a', `Source run ${version.run_id}`); runLink.href = `${BASE}/ai-review?run=${encodeURIComponent(version.run_id)}`;
          runLink.addEventListener('click', event => {
            if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
            event.preventDefault();
            openAIReviewRun(version.run_id);
          });
          provenance.append(document.createTextNode(' · '), runLink);
        }
        row.append(provenance);
      }
      if (version.operation_id) {
        const opLink = reviewNode('button', 'Check operation', 'btn btn--secondary'); opLink.type = 'button';
        opLink.addEventListener('click', async () => {
          try { const op = await aiReviewRequest(`/api/ai-review/catalog-operations/${encodeURIComponent(version.operation_id)}`); status.textContent = `Operation ${op.status} · ${op.kind} · ${reviewDate(op.created_at)}`; }
          catch (error) { status.textContent = `Operation unavailable: ${error.message}`; }
          if (!status.isConnected) section.append(status);
        });
        row.append(opLink);
      }
      if (version.snapshot_hash !== data.current_hash) {
        const previewButton = reviewNode('button', 'Preview restore', 'btn btn--secondary'); previewButton.type = 'button';
        if (!data.current_entry) { previewButton.disabled = true; previewButton.title = 'This project is absent from the current catalog.'; }
        previewButton.addEventListener('click', () => previewProjectRestore(section, projectID, version, data));
        row.append(previewButton);
      }
      section.append(row);
    }
    if (!versions.length) section.append(reviewNode('p', data.next_cursor ? 'No description changes on this page. Load older versions to continue.' : 'No retained description versions are available.', 'ai-review-muted'));
    if (data.next_cursor) {
      const older = reviewNode('button', 'Load older description versions', 'btn btn--secondary'); older.type = 'button';
      older.addEventListener('click', () => { older.disabled = true; loadProjectVersions(card, project, data.next_cursor); }); section.append(older);
    }
    const holdButton = reviewNode('button', policy.hold ? 'Resume automatic publication' : 'Pause automatic publication', 'btn btn--secondary'); holdButton.type = 'button';
    if (!data.current_entry || !policy.current_entry_hash) { holdButton.disabled = true; holdButton.title = 'This project is absent from the current catalog.'; }
    holdButton.addEventListener('click', async () => {
      holdButton.disabled = true;
      try {
        await aiReviewRequest(`/api/ai-review/projects/${encodeURIComponent(projectID)}/publication-hold`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hold: !policy.hold, reason: policy.hold ? 'Operator resumed automatic publication' : 'Operator paused automatic publication', expected_policy_revision: policy.revision || 0, expected_current_entry_hash: policy.current_entry_hash }) });
        await loadProjectVersions(card, project);
      } catch (error) { holdButton.disabled = false; status.textContent = error.status === 409 ? 'Catalog changed. Refresh AI Review and inspect the current version before changing the hold.' : `Unable to change publication hold: ${error.message}`; if (!status.isConnected) section.append(status); }
    });
    section.append(holdButton);
  } catch (error) {
    status.textContent = `Version history unavailable: ${error.message}`;
    if (error.authRequired) section.append(reviewSignInLink());
  }
}

function catalogDescriptionDiff(before, after) {
  const fields = ['summary','aliases','owned_components','boundaries','uses','shared_with','positive_examples','negative_examples'];
  const changes = [];
  for (const field of fields) {
    const a = Array.isArray(before[field]) ? before[field].join(' · ') : (before[field] || '');
    const b = Array.isArray(after[field]) ? after[field].join(' · ') : (after[field] || '');
    if (a !== b) changes.push(`${field}: ${a || '∅'} → ${b || '∅'}`);
  }
  return changes;
}

async function previewProjectRestore(section, projectID, version, binding) {
  const oldPreview = section.querySelector('.ai-review-restore-preview'); if (oldPreview) oldPreview.remove();
  const panel = reviewNode('div', undefined, 'ai-review-restore-preview');
  panel.append(reviewNode('p', 'Preparing a read-only preview…', 'ai-review-muted')); section.append(panel);
  const current = binding.versions?.[0];
  if (!current || !binding.current_hash || !binding.policy?.current_entry_hash) { panel.replaceChildren(reviewNode('p', 'Current version bindings are unavailable. Refresh the history before restoring.', 'ai-review-warning')); return; }
  const target = { project_id: projectID, historical_hash: version.snapshot_hash, historical_entry_hash: version.entry_hash, expected_current_hash: binding.current_hash, expected_current_entry_hash: binding.policy.current_entry_hash, expected_policy_revision: binding.policy.revision || 0 };
  try {
    const preview = await aiReviewRequest(`/api/ai-review/projects/${encodeURIComponent(projectID)}/restore-preview`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(target) });
    panel.replaceChildren(reviewNode('strong', 'Restore preview · one project card'));
    const changes = catalogDescriptionDiff(binding.current_entry || {}, preview.entry || {});
    panel.append(reviewNode('p', changes.length ? changes.join('\n') : 'The selected description matches the current description.', 'ai-review-evidence'));
    panel.append(reviewNode('p', 'Confirming creates a new catalog version and holds this project’s automatic publication.', 'ai-review-warning'));
    const confirm = reviewNode('button', 'Restore this version and pause publication', 'btn btn--primary'); confirm.type = 'button';
    confirm.addEventListener('click', () => confirmProjectRestore(panel, projectID, version, binding, target)); panel.append(confirm);
  } catch (error) {
    panel.replaceChildren(reviewNode('p', error.status === 409 ? 'The catalog changed. Refresh history and preview again before confirming.' : `Restore preview failed: ${error.message}`, 'ai-review-warning'));
    if (error.authRequired) panel.append(reviewSignInLink());
  }
}

async function confirmProjectRestore(panel, projectID, version, binding, target) {
  const confirm = panel.querySelector('button'); if (confirm) confirm.disabled = true;
  const reason = 'Restore selected catalog description';
  const randomID = new Uint8Array(32); crypto.getRandomValues(randomID);
  const operationID = Array.from(randomID, b => b.toString(16).padStart(2, '0')).join('');
  const payload = { ...target, operation_id: operationID, reason };
  try {
    const result = await aiReviewRequest(`/api/ai-review/projects/${encodeURIComponent(projectID)}/restore`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    panel.replaceChildren(reviewNode('p', `Restore ${result.status}. Automatic publication is held for this project.`, 'ai-review-warning'));
    const card = panel.closest('.ai-review-project-card');
    if (card) await loadProjectVersions(card, { project_id: projectID });
  } catch (error) {
    if (error.status === 409) {
      panel.replaceChildren(reviewNode('p', 'Catalog changed after the preview. Refresh the version history and create a new preview before confirming.', 'ai-review-warning'));
      return;
    }
    try {
      const operation = await aiReviewRequest(`/api/ai-review/catalog-operations/${encodeURIComponent(operationID)}`);
      panel.replaceChildren(reviewNode('p', `Restore operation ${operation.status}. Refresh the version history to confirm the active description.`, 'ai-review-warning'));
    } catch (_) {
      panel.replaceChildren(reviewNode('p', `Restore response was not confirmed. Operation ID ${operationID}; look it up before taking another action.`, 'ai-review-warning'));
    }
  }
}

async function loadAIReviewDraft(run, digest, projectID = '', replacePanel = Boolean(projectID)) {
  const selectionGeneration = ++aiReviewSelectionGeneration;
  syncAIReviewURL(run.run_id, digest, projectID, false);
  const panel = reviewEl('ai-review-detail');
  if (replacePanel) panel.replaceChildren(reviewNode('p', 'Loading project comparison…', 'ai-review-muted'));
  let draft;
  try { draft = await fetchAIReviewDraft(run.run_id, digest); }
  catch (error) {
    if (selectionGeneration === aiReviewSelectionGeneration) {
      const message = reviewNode('p', `${error.authRequired ? 'Unable to load draft' : 'Draft unavailable or corrupt'}: ${error.message}`, 'ai-review-error');
      if (replacePanel) panel.replaceChildren(message); else panel.append(message);
      if (error.authRequired) panel.append(reviewSignInLink());
    }
    return;
  }
  if (selectionGeneration !== aiReviewSelectionGeneration) return;
  const section = reviewNode('section', undefined, 'ai-review-section ai-review-draft-detail');
  section.append(reviewNode('h4', `Proposal ${digest.slice(0, 16)}`));
  section.append(reviewNode('p', `Draft status: ${reviewText(draft.availability)} · catalog state: ${reviewText(draft.current_status)}`, 'ai-review-muted'));
  section.append(reviewNode('p', 'Check the recorded temporal scope; a dependency mention is not evidence of current enablement.', 'ai-review-muted'));
  if (draft.missing?.length) section.append(reviewNode('p', `Missing: ${draft.missing.join(', ')}`, 'ai-review-reasons'));
  const selectedProjects = projectID ? (draft.projects || []).filter(project => project.project_id === projectID) : (draft.projects || []);
  for (const project of selectedProjects) {
    const card = reviewNode('article', undefined, 'ai-review-project-card');
    card.id = `review-project-${project.project_id}`;
    card.append(reviewNode('h5', `${project.proposed?.name || project.tag || project.project_id} · ${project.namespace}/${project.tag}`));
    card.append(reviewNode('p', project.drift ? 'Current catalog has drifted since this proposal.' : 'Current catalog matches the recorded base.', project.drift ? 'ai-review-warning' : 'ai-review-muted'));
    card.append(renderProposalDiff(project));
    const evidence = document.createElement('details'); evidence.append(reviewNode('summary', 'Evidence and source references'));
    if (draft.evidence?.length) draft.evidence.forEach(item => evidence.append(reviewNode('p', `${item.kind}: ${item.available ? item.text : 'Unavailable'}`, 'ai-review-evidence')));
    if (project.proposed?.client_evidence?.length) project.proposed.client_evidence.forEach(item => evidence.append(reviewNode('p', `${item.kind}: ${item.available ? item.text : 'Unavailable'}`, 'ai-review-evidence')));
    if (!draft.evidence?.length && !project.proposed?.client_evidence?.length) evidence.append(reviewNode('p', 'No evidence was recorded for this proposal.', 'ai-review-muted'));
    card.append(evidence);
    card.append(renderAssessmentForm(run, draft, project));
    const versions = reviewNode('div', undefined, 'ai-review-versions');
    for (const [key, label] of [['original','Original client declaration'], ['base','Base at run'], ['proposed','Proposal'], ['current','Current catalog']]) {
      const entry = project[key];
      const article = reviewNode('section', undefined, 'ai-review-version'); article.append(reviewNode('h6', label));
      if (!entry && key === 'current' && project.current_state === 'absent') article.append(reviewNode('p', 'Project absent from current catalog.', 'ai-review-warning'));
      else if (!entry && key === 'current' && project.current_state === 'identity_mismatch') article.append(reviewNode('p', 'A current card may represent this project, but its identity differs from the reviewed source.', 'ai-review-warning'));
      else if (!entry && key === 'current' && draft.current_status !== 'available') article.append(reviewNode('p', 'Current catalog unavailable.', 'ai-review-muted'));
      else if (!entry) article.append(reviewNode('p', 'Unavailable', 'ai-review-muted'));
      else renderReviewEntry(article, entry, draft.evidence || []);
      versions.append(article);
    }
    const versionDisclosure = document.createElement('details'); versionDisclosure.className = 'ai-review-reference';
    versionDisclosure.append(reviewNode('summary', 'Full Original, Base, Proposed, and Current versions'));
    versionDisclosure.append(versions);
    card.append(versionDisclosure);
    section.append(card);
    loadProjectVersions(card, project);
    if (projectID && project.project_id === projectID) setTimeout(() => card.scrollIntoView({ block: 'start' }), 0);
  }
  if (!selectedProjects.length) section.append(reviewNode('p', projectID ? 'This proposal is not available in the selected draft.' : draft.draft_status === 'no_changes' ? 'This run recorded no proposed catalog changes.' : 'No comparable project entries are available in this draft.', 'ai-review-muted'));
  if (draft.evidence?.length) {
    const details = document.createElement('details'); details.append(reviewNode('summary', 'All draft evidence'));
    draft.evidence.forEach(item => details.append(reviewNode('p', `${item.kind}: ${item.available ? item.text : 'Unavailable'}`, 'ai-review-evidence')));
    section.append(details);
  }
  const diagnostics = document.createElement('details'); diagnostics.className = 'ai-review-run-diagnostics';
  diagnostics.append(reviewNode('summary', 'Technical run events'));
  diagnostics.append(reviewNode('p', `${outcomeOf(run)} · ${reviewDate(runStarted(run))} · ${reviewText(run.started?.model)} · ${reviewDuration(run.completion?.duration_ms)} · ${run.completion?.provider_attempts ?? 'Unknown'} provider attempts`, 'ai-review-muted'));
  for (const event of run.events || []) diagnostics.append(reviewNode('p', `${reviewDate(event.at)} · ${event.type || 'Event'} · ${event.reason_code || 'No reason recorded'}`, 'ai-review-run-meta'));
  section.append(diagnostics);
  if (selectionGeneration !== aiReviewSelectionGeneration) return;
  if (replacePanel) panel.replaceChildren();
  panel.append(section);
  if (aiReviewSelection) { aiReviewSelection.digest = digest; aiReviewSelection.projectID = projectID; }
}

function renderProposalDiff(project) {
  const section = reviewNode('section', undefined, 'ai-review-proposal-diff');
  section.append(reviewNode('h6', 'Changes proposed vs base'));
  if (!project.base) {
    section.append(reviewNode('p', 'Cannot compare: the base entry is unavailable.', 'ai-review-muted'));
    return section;
  }
  const base = project.base;
  const proposed = project.proposed;
  const changes = [];
  for (const field of ['namespace', 'tag', 'name', 'summary', 'review_status']) {
    const before = base[field] ?? '';
    const after = proposed?.[field] ?? '';
    if (before !== after) changes.push({ field, before: String(before), after: String(after), type: 'field' });
  }
  const listFields = ['aliases','owned_components','boundaries','uses','shared_with','positive_examples','negative_examples'];
  for (const field of listFields) {
    const before = Array.isArray(base[field]) ? base[field].map(String) : [];
    const after = Array.isArray(proposed?.[field]) ? proposed[field].map(String) : [];
    const remaining = new Map();
    for (const value of before) remaining.set(value, (remaining.get(value) || 0) + 1);
    const added = [];
    for (const value of after) {
      const count = remaining.get(value) || 0;
      if (count > 0) remaining.set(value, count - 1);
      else added.push(value);
    }
    const removed = [];
    for (const [value, count] of remaining) for (let i = 0; i < count; i++) removed.push(value);
    if (added.length || removed.length) changes.push({ field, before, after, added, removed, type: 'list' });
  }
  if (!changes.length) {
    section.append(reviewNode('p', 'No changes proposed compared with the recorded base.', 'ai-review-muted'));
    return section;
  }
  const list = document.createElement('ul');
  for (const change of changes) {
    const item = document.createElement('li');
    item.className = 'ai-review-change';
    item.append(reviewNode('strong', change.field));
    if (change.type === 'field') {
      const values = reviewNode('div', undefined, 'ai-review-change-values');
      const before = reviewNode('div', undefined, 'ai-review-change-value'); before.append(reviewNode('span', `Before: ${change.before}`));
      const proposed = reviewNode('div', undefined, 'ai-review-change-value'); proposed.append(reviewNode('span', `Proposed: ${change.after}`));
      values.append(before, proposed); item.append(values);
    } else {
      const values = reviewNode('div', undefined, 'ai-review-change-values');
      values.append(reviewNode('div', `Before: ${change.before.length ? change.before.join(' · ') : 'None'}`, 'ai-review-change-value'));
      values.append(reviewNode('div', `Proposed: ${change.after.length ? change.after.join(' · ') : 'None'}`, 'ai-review-change-value'));
      item.append(values);
    }
    list.append(item);
  }
  section.append(list);
  return section;
}

function renderReviewEntry(root, entry, draftEvidence = []) {
  appendDefinition(root, 'Name', entry.name); appendDefinition(root, 'Summary', entry.summary);
  for (const key of ['aliases','owned_components','boundaries','uses','shared_with','positive_examples','negative_examples']) appendDefinition(root, key.replaceAll('_',' '), (entry[key] || []).join(' · ') || 'None recorded');
  appendDefinition(root, 'Review status', entry.review_status);
  const sourceEvidence = [...(entry.client_evidence || []), ...draftEvidence];
  for (const value of entry.evidence_refs || []) {
    const ref = typeof value === 'string' ? { ref: value } : (value || {});
    const reference = reviewNode('details', undefined, 'ai-review-reference');
    const available = ref.available === true;
    const summary = [reviewText(ref.ref), ref.kind, ref.digest].filter(Boolean).join(' · ');
    reference.append(reviewNode('summary', `${summary} · ${available ? 'Available' : 'Unavailable'}`));
    reference.append(reviewNode('p', `Reference: ${reviewText(ref.ref)}`));
    if (ref.kind) reference.append(reviewNode('p', `Kind: ${ref.kind}`));
    if (ref.digest) reference.append(reviewNode('p', `Digest: ${ref.digest}`));
    reference.append(reviewNode('p', available ? 'Reference is available.' : 'Reference is marked unavailable.', available ? 'ai-review-muted' : 'ai-review-warning'));
    const cited = sourceEvidence.find(item => item.alias && item.alias === ref.ref);
    if (cited) reference.append(reviewNode('p', cited.available ? `${cited.kind}: ${cited.text}` : `${cited.kind}: matching source evidence is unavailable.`, cited.available ? 'ai-review-evidence' : 'ai-review-warning'));
    root.append(reference);
  }
  for (const evidence of entry.client_evidence || []) {
    const d = document.createElement('details'); d.append(reviewNode('summary', `${evidence.kind} evidence`)); d.append(reviewNode('p', evidence.available ? evidence.text : 'Evidence unavailable.'));
    root.append(d);
  }
}

function renderAssessmentForm(run, draft, project) {
  const form = reviewNode('form', undefined, 'ai-review-assessment');
  form.dataset.projectId = project.project_id;
  form.dataset.dirty = 'false';
  form.append(reviewNode('h6', 'Human assessment'));
  const state = reviewNode('p', 'Loading saved assessment…', 'ai-review-muted'); form.append(state);
  const verdict = document.createElement('select'); verdict.setAttribute('aria-label','Assessment');
  for (const [value,label] of [['acceptable','Acceptable'],['needs_correction','Needs correction'],['rejected','Rejected']]) verdict.add(new Option(label,value));
  const reason = document.createElement('select'); reason.setAttribute('aria-label','Reason');
  for (const [value,label] of [['none','No specific reason'],['source_mismatch','Source mismatch'],['proposal_error','Proposal error'],['insufficient_evidence','Insufficient evidence'],['identity_mismatch','Identity mismatch'],['other','Other']]) reason.add(new Option(label,value));
  const note = document.createElement('textarea'); note.maxLength = 2048; note.rows = 3; note.placeholder = 'Optional note'; note.setAttribute('aria-label','Assessment note');
  const save = reviewNode('button','Save assessment','btn btn--primary'); save.type = 'submit';
  const controls = reviewNode('div',undefined,'ai-review-assessment-controls'); controls.append(verdict, reason, note, save); form.append(controls);
  const retryRead = reviewNode('button', 'Retry saved assessment read', 'btn btn--secondary'); retryRead.type = 'button'; retryRead.hidden = true; form.append(retryRead);
  const signInAgain = reviewSignInLink(); signInAgain.hidden = true; form.append(signInAgain);
  const currentKnownAbsent = project.current_state === 'absent' && !project.current && project.current_entry_hash === '';
  const editable = draft.assessment_writable !== false && project.identity_status === 'verified' &&
    /^[a-f0-9]{64}$/.test(project.base_entry_hash || '') && /^[a-f0-9]{64}$/.test(project.proposed_entry_hash || '') &&
    (/^[a-f0-9]{64}$/.test(project.current_entry_hash || '') || currentKnownAbsent);
  if (!editable) {
    const cause = draft.assessment_writable === false ? 'Assessment writing is disabled for this draft.' : project.identity_status !== 'verified' ? 'Project identity is not verified; assessment is disabled.' : !/^[a-f0-9]{64}$/.test(project.base_entry_hash || '') ? 'Base entry hash is unavailable; assessment is disabled.' : !/^[a-f0-9]{64}$/.test(project.proposed_entry_hash || '') ? 'Proposal entry hash is unavailable; assessment is disabled.' : draft.current_status !== 'available' ? 'Current catalog is unavailable; assessment is disabled.' : project.current_state === 'identity_mismatch' ? 'Current project identity differs from the reviewed source; assessment is disabled.' : 'Current entry state is not verified; assessment is disabled.';
    form.append(reviewNode('p', cause, 'ai-review-warning'));
    controls.querySelectorAll('select, textarea, button').forEach(control => { control.disabled = true; });
  }
  let revision = 0;
  let latestAssessment = null;
  let conflictLocked = false;
  let formDirty = false;
  let assessmentReadGeneration = 0;
  let assessmentReadReady = false;
  const setFormEnabled = enabled => controls.querySelectorAll('select, textarea, button').forEach(control => { control.disabled = !enabled; });
  const conflictPanel = reviewNode('section', undefined, 'ai-review-conflict'); conflictPanel.hidden = true;
  const conflictMessage = reviewNode('p', undefined, 'ai-review-warning');
  const latestSummary = reviewNode('dl', undefined, 'ai-review-summary');
  const adoptLatest = reviewNode('button', 'Use latest revision and keep my draft', 'btn btn--secondary'); adoptLatest.type = 'button'; adoptLatest.hidden = true;
  conflictPanel.append(conflictMessage, latestSummary, adoptLatest); form.append(conflictPanel);
  const loadSaved = async ({ applyToForm = true, manageControls = false } = {}) => {
    const generation = ++assessmentReadGeneration;
    if (manageControls) { setFormEnabled(false); retryRead.hidden = true; state.textContent = 'Loading saved assessment…'; }
    try {
      const saved = await fetchAIReviewAssessments(run.run_id, draft.sha256);
      const item = (saved.assessments || []).find(a => a.project_id === project.project_id);
      if (generation !== assessmentReadGeneration || !form.isConnected) return item;
      if (applyToForm && !formDirty) {
        revision = item?.revision || 0;
        if (item) {
          verdict.value = item.request.verdict; reason.value = item.request.reason; note.value = item.request.note || '';
          state.textContent = `Saved ${reviewDate(item.saved_at)} · revision ${revision}${item.drift ? ' · saved assessment is stale' : ''}`;
        } else state.textContent = 'No saved assessment';
      }
      if (manageControls) { assessmentReadReady = true; setFormEnabled(editable); retryRead.hidden = true; signInAgain.hidden = true; }
      return item;
    } catch (error) {
      if (generation === assessmentReadGeneration && manageControls && form.isConnected) {
        assessmentReadReady = false;
        setFormEnabled(false);
        retryRead.hidden = false;
        state.textContent = `Saved assessment unavailable: ${error.message}. Controls are read only until the read succeeds.`;
        signInAgain.hidden = !error.authRequired;
      }
      throw error;
    }
  };
  loadSaved({ manageControls: true }).catch(() => {});
  retryRead.addEventListener('click', () => loadSaved({ manageControls: true }).catch(() => {}));
  controls.querySelectorAll('select, textarea').forEach(control => {
    const markDirty = () => {
      formDirty = true;
      form.dataset.dirty = 'true';
      if (!conflictLocked) state.textContent = 'Unsaved draft · changes are not recorded yet.';
    };
    control.addEventListener('input', markDirty);
    control.addEventListener('change', markDirty);
  });
  adoptLatest.addEventListener('click', () => {
    if (!latestAssessment) return;
    revision = latestAssessment.revision;
    conflictLocked = false;
    save.disabled = false;
    adoptLatest.hidden = true;
    state.textContent = `Latest peer assessment reviewed · revision ${revision}. Your form values remain an unsaved draft; Save will explicitly submit them as the next revision.`;
    conflictMessage.textContent = 'Latest saved assessment shown below. Your unsaved form values are preserved.';
  });
  form.addEventListener('submit', async event => {
    event.preventDefault(); if (!editable || !assessmentReadReady || conflictLocked) return; save.disabled = true; state.textContent = 'Saving…';
    const request = { expected_revision: revision, verdict: verdict.value, reason: reason.value, note: note.value,
      base_entry_hash: project.base_entry_hash, proposed_entry_hash: project.proposed_entry_hash, current_entry_hash: project.current_entry_hash };
    try {
      const result = await aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(run.run_id)}/drafts/${encodeURIComponent(draft.sha256)}/assessments/${encodeURIComponent(project.project_id)}`, { method: 'PUT', headers: { 'Content-Type':'application/json' }, body: JSON.stringify(request) });
      revision = result.revision || revision;
      formDirty = false;
      form.dataset.dirty = 'false';
      state.textContent = `Saved ${reviewDate(result.saved_at)} · revision ${revision}. Review only; no catalog change was made.`;
      await loadAIReview(true);
    } catch (error) {
      if (error.status === 409) {
        conflictLocked = true;
        save.disabled = true;
        conflictPanel.hidden = false;
        adoptLatest.hidden = true;
        latestSummary.replaceChildren();
        const revisionConflict = /assessment revision conflict/i.test(error.details || '');
        conflictMessage.textContent = revisionConflict
          ? 'Another reviewer saved a newer assessment. Your unsaved values remain in the form. Review the latest assessment below before choosing whether to resubmit.'
          : 'The catalog binding changed. Your unsaved values remain in the form, and saving is blocked. Refresh the draft and compare it before starting a new assessment.';
        state.textContent = revisionConflict ? 'Conflict · your assessment is unsaved and saving is locked until you review the latest revision.' : 'Catalog conflict · your assessment is unsaved and saving is locked.';
        aiReviewAssessmentCache.delete(`${run.run_id}:${draft.sha256}`);
        if (revisionConflict) {
          try {
            latestAssessment = await loadSaved({ applyToForm: false });
            if (latestAssessment) {
              appendDefinition(latestSummary, 'Latest saved verdict', latestAssessment.request.verdict);
              appendDefinition(latestSummary, 'Reason', latestAssessment.request.reason);
              appendDefinition(latestSummary, 'Note', latestAssessment.request.note || 'None');
              appendDefinition(latestSummary, 'Saved', `${reviewDate(latestAssessment.saved_at)} · revision ${latestAssessment.revision}`);
              adoptLatest.hidden = false;
            } else conflictMessage.textContent = 'A newer revision was reported, but no current assessment could be loaded. Your values remain unsaved and saving stays locked.';
          } catch (_) { conflictMessage.textContent = 'A newer revision was reported, but the latest assessment could not be loaded. Your values remain unsaved and saving stays locked.'; }
        }
      } else {
        state.textContent = `Save failed: ${error.message}. Your input is still here.`;
        signInAgain.hidden = !error.authRequired;
      }
    } finally { if (!conflictLocked) save.disabled = false; }
  });
  return form;
}

reviewEl('ai-review-refresh').addEventListener('click', () => { if (confirmAIReviewNavigation()) loadAIReview(true); });
reviewEl('ai-review-apply').addEventListener('click', () => {
  if (!confirmAIReviewNavigation()) return;
  aiReviewFilters = { from: reviewEl('ai-review-from').value, to: reviewEl('ai-review-to').value, outcome: reviewEl('ai-review-outcome').value, project: reviewEl('ai-review-project').value };
  aiReviewCursor = ''; aiReviewRuns = []; loadAIReview(true);
});
reviewEl('ai-review-more').addEventListener('click', async () => {
  if (!aiReviewCursor || aiReviewPageLoading) return;
  aiReviewPageLoading = true;
  const button = reviewEl('ai-review-more'); button.disabled = true;
  try { await loadAIReviewPage(false); } finally { aiReviewPageLoading = false; button.disabled = false; }
});
window.addEventListener('beforeunload', event => {
  if (![...document.querySelectorAll('.ai-review-assessment[data-dirty="true"]')].some(form => form.isConnected)) return;
  event.preventDefault();
  event.returnValue = '';
});
window.addEventListener('popstate', () => {
  if (parseTabFromPath() !== 'ai-review' || !aiReviewLoaded) return;
  if (!confirmAIReviewNavigation()) {
    syncAIReviewURL(aiReviewSelection?.runID || '', aiReviewSelection?.digest || '', aiReviewSelection?.projectID || '', false);
    return;
  }
  const query = new URLSearchParams(location.search); const run = aiReviewRuns.find(x => x.run_id === query.get('run'));
  const runID = query.get('run');
  if (!runID) {
    aiReviewSelectionGeneration++;
    aiReviewSelection = null;
    renderAIReviewRuns();
    reviewEl('ai-review-detail').replaceChildren(reviewNode('p', 'Select a project proposal to compare its changes and record an assessment.', 'empty-state'));
    return;
  }
  if (run) openAIReviewRun(run.run_id, query.get('draft') || '', query.get('project') || '', false);
  else if (runID) {
    const generation = ++aiReviewSelectionGeneration;
    aiReviewSelection = { runID, digest: query.get('draft') || '', projectID: query.get('project') || '' };
    renderAIReviewRuns();
    reviewEl('ai-review-detail').replaceChildren(reviewNode('p', 'Loading linked run…', 'ai-review-muted'));
    aiReviewRequest(`/api/ai-review/runs/${encodeURIComponent(runID)}`).then(data => {
      if (generation === aiReviewSelectionGeneration) openAIReviewRun(data.run.run_id, query.get('draft') || '', query.get('project') || '', false);
    }).catch(error => {
      if (generation !== aiReviewSelectionGeneration) return;
      aiReviewSelection = null;
      renderAIReviewRuns();
      reviewEl('ai-review-detail').replaceChildren(reviewNode('p', `Run unavailable. It may be outside retained history or the link may be invalid (${error.message}).`, 'ai-review-error'));
            if (error.authRequired) reviewEl('ai-review-detail').append(reviewSignInLink());
    });
  }
});
