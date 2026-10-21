import assert from 'node:assert/strict';
import test from 'node:test';

import type { BibliographySnapshot } from '../src/api.ts';
import {
  bibliographyDisplayEntries,
  bibliographyEntrySourceLabel,
  bibliographyHasWarnings,
  bibliographyJSON,
  bibliographyJSONFilename,
  bibliographySummary,
  bibliographyTaskPending,
} from '../src/bibliographyExport.ts';

const snapshot: BibliographySnapshot = {
  baseId: 'base', baseName: 'Writing', kind: 'paper', filename: 'writing.bib', bibtex: '@misc{x}',
  generated: 2, skipped: 1, failed: 0,
  entries: [
    { documentId: 'one', kind: 'paper', outcome: 'generated', source: 'doi_original', citationKey: 'x', missingFields: [], warnings: [] },
    { documentId: 'two', kind: 'paper', outcome: 'generated', source: 'local_doi_failed', missingFields: ['year'], warnings: [], doiErrorCode: 'http_404' },
    { documentId: 'three', kind: 'paper', outcome: 'skipped', missingFields: [], warnings: ['document_unavailable'] },
  ],
};

test('describes bibliography snapshot totals and sources', () => {
  assert.equal(bibliographySummary(snapshot), '生成 2 · 跳过 1 · 失败 0');
  assert.equal(bibliographyEntrySourceLabel(snapshot.entries[0]), 'DOI 原始引文');
  assert.equal(bibliographyEntrySourceLabel(snapshot.entries[1]), '本地构建 · DOI 查询失败');
  assert.equal(bibliographyEntrySourceLabel(snapshot.entries[2]), 'skipped');
});

test('detects warnings, missing fields, and DOI fallback diagnostics', () => {
  assert.equal(bibliographyHasWarnings(snapshot), true);
  assert.equal(bibliographyHasWarnings({ ...snapshot, entries: [snapshot.entries[0]] }), false);
});

test('polls only active bibliography tasks', () => {
  assert.equal(bibliographyTaskPending({ id: 'one', baseId: 'base', state: 'queued' }), true);
  assert.equal(bibliographyTaskPending({ id: 'one', baseId: 'base', state: 'running' }), true);
  assert.equal(bibliographyTaskPending({ id: 'one', baseId: 'base', state: 'succeeded', snapshot }), false);
  assert.equal(bibliographyTaskPending({ id: 'one', baseId: 'base', state: 'failed' }), false);
});

test('maps generated reports to their BibTeX blocks for navigation and source labels', () => {
  const navigable: BibliographySnapshot = {
    ...snapshot,
    bibtex: '@article{x,\n  title={Remote}\n}\n\n@misc(y,\n  title={Local}\n)',
    entries: [
      snapshot.entries[0],
      { ...snapshot.entries[1], citationKey: 'y' },
      snapshot.entries[2],
    ],
  };
  const entries = bibliographyDisplayEntries(navigable);
  assert.deepEqual(entries.map((entry) => entry.documentId), ['one', 'two']);
  assert.equal(entries[0].sourceLabel, 'DOI 原始引文');
  assert.match(entries[0].text, /^@article\{x,/u);
  assert.equal(entries[1].sourceLabel, '本地构建 · DOI 查询失败');
  assert.match(entries[1].text, /^@misc\(y,/u);
});

test('builds a structured JSON artifact without changing the BibTeX payload', () => {
  const structured: BibliographySnapshot = {
    ...snapshot,
    exportEntries: [
      { source: 'DOI', kind: 'article', bibtex: '@article{x}' },
      { source: 'auto', kind: 'article', bibtex: '@misc{y}' },
    ],
  };
  assert.equal(bibliographyJSONFilename('writing.bib'), 'writing.json');
  assert.deepEqual(JSON.parse(bibliographyJSON(structured)), {
    baseId: 'base',
    baseName: 'Writing',
    entries: structured.exportEntries,
  });
});
