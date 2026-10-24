const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const graphSource = fs.readFileSync(path.join(__dirname, 'static/assets/js/graph.js'), 'utf8');
const timelineSource = fs.readFileSync(path.join(__dirname, 'static/assets/js/timeline.js'), 'utf8');

function functionSource(source, name) {
  const marker = new RegExp(`function ${name}\\s*\\(`);
  const match = marker.exec(source);
  const start = match ? match.index : -1;
  assert.notEqual(start, -1, `${name} exists`);
  const opening = source.indexOf('{', start);
  let depth = 0;
  for (let index = opening; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1;
    if (source[index] === '}' && --depth === 0) return source.slice(start, index + 1);
  }
  throw new Error(`unclosed function ${name}`);
}

function runHideDetail({ viewName = 'memory-view', viewActive = true, openerConnected = true } = {}) {
  const focused = [];
  const view = { id: viewName, classList: { contains: name => name === 'active' && viewActive } };
  const opener = {
    isConnected: openerConnected,
    hidden: false,
    closest: selector => selector === '.view' ? view : null,
    focus: () => focused.push('opener'),
  };
  const fallback = { focus: () => focused.push('fallback') };
  const panel = {
    classList: {
      contains: name => name === 'visible',
      remove: name => assert.equal(name, 'visible'),
    },
    setAttribute: () => {},
  };
  const context = {
    panel,
    selectedFact: { id: 'fact-id' },
    detailRequest: 4,
    detailReturnFocus: opener,
    detailReturnFactID: 'fact-id',
    abortDetailRequests: () => {},
    document: {
      getElementById: id => {
        assert.equal(id, 'detail-panel');
        return panel;
      },
      querySelector: selector => {
        assert.equal(selector, '.view.active button, .view.active a, .view.active [tabindex="0"]');
        return fallback;
      },
    },
  };
  vm.runInNewContext(`${functionSource(graphSource, 'hideDetail')}; hideDetail();`, context);
  return { focused, context };
}

test('shared detail restores its opener in Memory, Graph, and Quality views', () => {
  for (const viewName of ['memory-view', 'graph-view', 'duplicates-view']) {
    const { focused, context } = runHideDetail({ viewName, viewActive: true });
    assert.deepEqual(focused, ['opener'], `${viewName} opener gets focus back`);
    assert.equal(context.detailReturnFocus, null);
  }
});

test('shared detail uses the active view fallback when its opener is hidden or detached', () => {
  assert.deepEqual(runHideDetail({ viewActive: false }).focused, ['fallback']);
  assert.deepEqual(runHideDetail({ openerConnected: false }).focused, ['fallback']);
});

test('timeline accessible list paginates at 50 events', () => {
  const items = [];
  const more = { hidden: true, textContent: '', addEventListener: (name, callback) => { more.onClick = callback; } };
  const list = { replaceChildren: () => { items.length = 0; }, appendChild: item => items.push(item) };
  const context = {
    TIMELINE_LIST_PAGE_SIZE: 50,
    timelineListShown: 50,
    timelineListNodes: [],
    document: {
      getElementById: id => id === 'timeline-list-items' ? list : more,
      createElement: () => ({ addEventListener: () => {}, textContent: '' }),
    },
    normalizeNamespace: node => node.namespace,
    factText: node => node.text,
    showDetail: () => {},
  };
  const renderSource = functionSource(timelineSource, 'renderTimelineList');
  const events = Array.from({ length: 123 }, (_, index) => ({ id: index, created_at: `2026-10-${String((index % 24) + 1).padStart(2, '0')}`, namespace: 'projects', text: `Fact ${index}` }));
  vm.runInNewContext(`${renderSource}; renderTimelineList(events, true);`, { ...context, events });
  assert.equal(items.length, 50);
  assert.equal(more.hidden, false);
  assert.match(more.textContent, /73 remaining/);

  context.timelineListNodes = events;
  context.timelineListShown = 100;
  vm.runInNewContext(`${renderSource}; renderTimelineList(timelineListNodes, false);`, context);
  assert.equal(items.length, 100);
  assert.match(more.textContent, /23 remaining/);
  context.timelineListShown = 150;
  vm.runInNewContext(`${renderSource}; renderTimelineList(timelineListNodes, false);`, context);
  assert.equal(items.length, 123);
  assert.equal(more.hidden, true);
});
