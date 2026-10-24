const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

class ElementStub {
  constructor() { this.children = []; this._text = ''; this.dataset = {}; }
  addEventListener() {}
  setAttribute() {}
  replaceChildren(...children) { this.children = children; this._text = ''; }
  append(...children) { this.children.push(...children); }
  get textContent() { return this._text + this.children.map(child => child?.textContent || String(child)).join(''); }
  set textContent(value) { this._text = String(value); this.children = []; }
}

function makeHarness() {
  const elements = new Map();
  const document = {
    getElementById(id) {
      if (!elements.has(id)) elements.set(id, new ElementStub());
      return elements.get(id);
    },
    createElement() { return new ElementStub(); },
    createTextNode(text) { const node = new ElementStub(); node.textContent = text; return node; },
    querySelectorAll() { return []; },
  };
  const context = vm.createContext({
    document,
    window: { addEventListener() {}, confirm() { return true; } },
    location: { search: '', href: 'http://example.test/viz/ai-review' },
    URL,
    URLSearchParams,
    console,
  });
  const sourcePath = path.join(__dirname, 'static/assets/js/ai-review.js');
  vm.runInContext(fs.readFileSync(sourcePath, 'utf8'), context, { filename: sourcePath });
  vm.runInContext('renderAIReviewMetrics = () => {};', context);
  return { context, elements };
}

const projectID = 'a'.repeat(64);
const proposedHash = 'b'.repeat(64);
const baseHash = 'c'.repeat(64);

function run(id, startedAt) {
  return { run_id: id, started: { started_at: startedAt }, drafts: [{ sha256: id }] };
}

function project(name = 'Project') {
  return {
    project_id: projectID,
    proposed_entry_hash: proposedHash,
    base_entry_hash: baseHash,
    tag: 'project',
    namespace: 'projects',
    proposed: { name },
    current_entry_hash: '',
  };
}

function draft(proposal) { return { projects: [proposal] }; }
function assessment(verdict, savedAt, revision = 1) {
  return { project_id: projectID, saved_at: savedAt, revision, request: { proposed_entry_hash: proposedHash, verdict } };
}

function installFixture(context, runs, drafts, assessments) {
  context.__fixture = { runs, drafts, assessments };
  vm.runInContext(`
    aiReviewRuns = __fixture.runs;
    aiReviewPageGeneration = 1;
    fetchAIReviewDraft = async runID => __fixture.drafts[runID];
    fetchAIReviewAssessments = async runID => {
      const value = __fixture.assessments[runID];
      if (value?.__readError) throw new Error('read failed');
      return { assessments: value || [] };
    };
  `, context);
}

test('deduplicates by project and proposal hash and picks newest representative plus newest exact-binding assessment', async () => {
  const { context } = makeHarness();
  const older = run('older', '2026-10-20T10:00:00Z');
  const newer = run('newer', '2026-10-22T10:00:00Z');
  installFixture(context, [older, newer], { older: draft(project('Older name')), newer: draft(project('Latest name')) }, {
    older: [assessment('rejected', '2026-10-23T12:00:00Z', 3)],
    newer: [assessment('acceptable', '2026-10-22T12:00:00Z', 1)],
  });

  await vm.runInContext('rebuildAIReviewProposals(1)', context);
  const result = JSON.parse(vm.runInContext('JSON.stringify(aiReviewProposalRows.map(row => ({ runID: row.run.run_id, name: row.project.proposed.name, verdict: row.assessment.request.verdict, count: row.candidates.length })))', context));
  assert.deepEqual(result, [{ runID: 'newer', name: 'Latest name', verdict: 'rejected', count: 2 }]);
});

test('unavailable assessment reads are unknown and never counted as unreviewed', async () => {
  const { context, elements } = makeHarness();
  const only = run('only', '2026-10-22T10:00:00Z');
  installFixture(context, [only], { only: draft(project()) }, { only: { __readError: true } });

  await vm.runInContext('rebuildAIReviewProposals(1)', context);
  const counts = JSON.parse(vm.runInContext('JSON.stringify(countProposalAssessments(aiReviewProposalRows))', context));
  assert.equal(counts.unknown, 1);
  assert.equal(counts.unreviewed, 0);
  vm.runInContext('renderAIReviewRuns()', context);
  const chips = elements.get('ai-updates-summary').children.find(node => node.className === 'ai-updates-counts').children.map(node => node.textContent);
  assert.ok(chips.includes('0Unreviewed'));
  assert.ok(elements.get('ai-updates-summary').textContent.includes('Unknown · 1'));
});

test('a stale assessment response cannot replace state after a newer page generation starts', async () => {
  const { context } = makeHarness();
  const only = run('only', '2026-10-22T10:00:00Z');
  let releaseAssessment;
  const pendingAssessment = new Promise(resolve => { releaseAssessment = resolve; });
  installFixture(context, [only], { only: draft(project()) }, {});
  context.__pendingAssessment = pendingAssessment;
  vm.runInContext('fetchAIReviewAssessments = async () => __pendingAssessment; aiReviewProposalRows = [{ sentinel: true }]', context);

  const pending = vm.runInContext('rebuildAIReviewProposals(1)', context);
  await new Promise(resolve => setImmediate(resolve));
  vm.runInContext('aiReviewPageGeneration = 2', context);
  releaseAssessment({ assessments: [] });
  await pending;

  const rows = JSON.parse(vm.runInContext('JSON.stringify(aiReviewProposalRows)', context));
  assert.deepEqual(rows, [{ sentinel: true }]);
});
