#!/usr/bin/env node
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const dates = require('../internal/viz/static/assets/js/ai-review-dates.js');

if (process.argv[2] === '--tz-case') {
  const zone = process.env.TZ;
  if (zone === 'Europe/Belgrade') {
    assert.deepEqual(dates.localDateRange('2026-01-15', '2026-01-15'), {
      from: '2026-01-14T23:00:00.000Z', to: '2026-01-15T22:59:59.999999999Z',
    });
    assert.deepEqual(dates.defaultLast14Days(new Date('2026-01-14T23:30:00Z')), ['2026-01-02', '2026-01-15']);
  } else if (zone === 'America/New_York') {
    const spring = dates.localDateRange('2026-03-08', '2026-03-08');
    assert.deepEqual(spring, { from: '2026-03-08T05:00:00.000Z', to: '2026-03-09T03:59:59.999999999Z' });
    assert.equal(new Date('2026-03-09T04:00:00Z').getTime() - new Date(spring.from).getTime(), 23 * 60 * 60 * 1000);
    const fall = dates.localDateRange('2026-11-01', '2026-11-01');
    assert.deepEqual(fall, { from: '2026-11-01T04:00:00.000Z', to: '2026-11-02T04:59:59.999999999Z' });
    assert.equal(new Date('2026-11-02T05:00:00Z').getTime() - new Date(fall.from).getTime(), 25 * 60 * 60 * 1000);
    assert.deepEqual(dates.defaultLast14Days(new Date('2026-01-15T02:00:00Z')), ['2026-01-01', '2026-01-14']);
  } else {
    throw new Error(`Unexpected test timezone: ${zone}`);
  }
  assert.throws(() => dates.localDateRange('2026-02-02', '2026-02-01'), /Start date/);
  process.stdout.write(`${zone}: date range checks passed\n`);
} else {
  for (const zone of ['Europe/Belgrade', 'America/New_York']) {
    const result = spawnSync(process.execPath, [__filename, '--tz-case'], {
      encoding: 'utf8', env: { ...process.env, TZ: zone },
    });
    if (result.status !== 0) {
      process.stderr.write(result.stderr || result.stdout);
      process.exit(result.status || 1);
    }
    process.stdout.write(result.stdout);
  }
}
