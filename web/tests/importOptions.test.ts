import assert from 'node:assert/strict';
import test from 'node:test';
import { buildImportOptions } from '../src/importOptions.ts';

test('batch defaults and per-paper overrides remain aligned', () => {
  const drafts = {
    a: { metadata: '{"title":"First","doi":"10.1234/a"}', sourceLookup: 'auto', duplicateMetadata: 'fill_missing' },
    b: { metadata: '{"title":"Second"}', sourceLookup: 'inherit', duplicateMetadata: 'none' },
  } as const;
  const options = buildImportOptions(['b', 'a', 'new'], drafts, true);
  assert.equal(options[0].metadata?.title, 'Second');
  assert.equal(options[0].processing?.source_lookup, 'skip');
  assert.equal(options[1].metadata?.title, 'First');
  assert.equal(options[1].processing?.source_lookup, 'auto');
  assert.equal(options[2].metadata, undefined);
  assert.equal(buildImportOptions(['new'], {}, false)[0].processing?.source_lookup, 'auto');
});

test('invalid metadata cannot start a partially accepted upload', () => {
  for (const metadata of ['[]', 'null', '1', '{bad']) {
    assert.throws(() => buildImportOptions(['a'], {
      a: { metadata, sourceLookup: 'inherit', duplicateMetadata: 'fill_missing' },
    }, false), /第 1 项.*JSON 对象/);
  }
  assert.throws(() => buildImportOptions(['a'], {
    a: { metadata: JSON.stringify({ abstract: '中'.repeat(30000) }),
      sourceLookup: 'inherit', duplicateMetadata: 'fill_missing' },
  }, false), /64 KiB/);
});
