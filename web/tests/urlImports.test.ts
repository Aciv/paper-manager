import assert from 'node:assert/strict';
import test from 'node:test';
import { importFinished, importSummary, restoreURLImports } from '../src/urlImports.ts';
import type { URLImportTask } from '../src/api.ts';

test('restored handles keep the original request for explicit confirmation and reject malformed storage', () => {
  const requestId = '12345678-1234-1234-1234-123456789012';
  const record = { id: `url-import-${requestId}`, createdAt: 123, pending: {
    requestId, urls: [{ url: 'https://example.org/a.pdf', doi: '10.1000/a' }], baseIds: ['base-a'],
  } };
  const restored = restoreURLImports(JSON.stringify([record, record, { id: '../bad', createdAt: 1 }]));
  assert.deepEqual(restored, [record]);
  assert.equal(restored[0].pending?.requestId, requestId);
  assert.deepEqual(restoreURLImports('{invalid'), []);
  assert.deepEqual(restoreURLImports('{}'), []);
});

test('partial and stopped imports are terminal and never presented as all successful', () => {
  const task: URLImportTask = { id: 'task', total: 3, completed: 3, state: 'partial',
    summary: { accepted: 1, duplicate: 0, rejected: 1, failed: 1 } };
  assert.equal(importFinished(task), true);
  assert.match(importSummary(task), /部分失败.*新增 1.*拒绝 1.*失败 1/);
  assert.doesNotMatch(importSummary(task), /全部入库成功/);
  assert.match(importSummary({ ...task, state: 'stopped' }), /提前停止/);
  assert.equal(importFinished({ ...task, state: 'running' }), false);
  assert.equal(importFinished({ ...task, state: 'queued' }), false);
});
