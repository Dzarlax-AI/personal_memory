#!/usr/bin/env node
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(require.resolve('../internal/viz/static/assets/js/ai-review.js'), 'utf8');
const start = source.indexOf('function reviewSignInLink()');
const end = source.indexOf('async function loadAIReview(', start);
assert.ok(start >= 0 && end > start);
const calls = [];
let response;
const context = vm.createContext({
  BASE: '/viz', Error,
  reviewNode: () => ({}),
  fetch: async (url, options) => { calls.push({url, options}); if (response instanceof Error) throw response; return response; },
});
vm.runInContext(source.slice(start, end), context);
(async () => {
  const request = { method: 'PUT', body: '{"expected_revision":3}', redirect: 'follow' };
  response = { type: 'opaqueredirect', status: 0, ok: false, text: () => { throw Error('Redirect body must not be read'); } };
  await assert.rejects(context.aiReviewRequest('/api/ai-review/test', request), e => e.status === 401 && e.authRequired === true);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].options.redirect, 'manual');
  assert.equal(calls[0].options.method, 'PUT');
  assert.equal(calls[0].options.body, request.body);
  response = { type: 'basic', status: 401, ok: false };
  await assert.rejects(context.aiReviewRequest('/api/ai-review/status'), e => e.authRequired === true);
  response = { type: 'basic', status: 409, ok: false, text: async () => '{"error":"assessment revision conflict"}' };
  await assert.rejects(context.aiReviewRequest('/api/ai-review/test', request), e => e.status === 409 && e.details === 'assessment revision conflict' && !e.authRequired);
  response = { type: 'basic', status: 403, ok: false, text: async () => 'assessment write unavailable' };
  await assert.rejects(context.aiReviewRequest('/api/ai-review/test', request), e => e.status === 403 && !e.authRequired);
  const networkError = new TypeError('Failed to fetch');
  response = networkError;
  const before = calls.length;
  await assert.rejects(context.aiReviewRequest('/api/ai-review/test', request), e => e === networkError);
  assert.equal(calls.length, before + 1, 'No automatic write retry');
  response = { type: 'basic', status: 200, ok: true, json: async () => ({revision:4}) };
  assert.equal((await context.aiReviewRequest('/api/ai-review/test', request)).revision, 4);
  const link = context.reviewSignInLink();
  assert.equal(link.href, '/viz/ai-review');
  assert.equal(link.target, '_blank');
  assert.equal(link.rel, 'noopener noreferrer');
  const historyNodes = [];
  context.aiReviewPageGeneration = 0;
  context.aiReviewSelectionGeneration = 0;
  context.rangeParams = () => '';
  context.reviewEl = () => ({ replaceChildren: node => historyNodes.push(node), append: node => historyNodes.push(node) });
  const historyStart = source.indexOf('async function loadAIReviewPage(');
  const historyEnd = source.indexOf('function outcomeOf(', historyStart);
  vm.runInContext(source.slice(historyStart, historyEnd), context);
  response = {type:'opaqueredirect',status:0,ok:false};
  assert.equal(await context.loadAIReviewPage(), false);
  assert.equal(historyNodes.length, 2);
  assert.equal(historyNodes[1].href, '/viz/ai-review', 'History auth error must offer sign-in');
  process.stdout.write('AI Review auth redirect, explicit recovery and error boundaries passed\n');
})().catch(error => { console.error(error); process.exitCode = 1; });
