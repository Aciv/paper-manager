import assert from 'node:assert/strict';
import test from 'node:test';

import type { DocumentPage } from '../src/api.ts';
import { fetchAllDocumentPages, MAX_DOCUMENT_PAGES } from '../src/documentPagination.ts';

function page(pageNumber: number, total: number): DocumentPage {
  return {
    items: [{
      id: `document-${pageNumber}`,
      originalFilename: `${pageNumber}.pdf`,
      contentType: 'application/pdf',
      sizeBytes: 1,
      sourceState: 'available',
      createdAt: '2026-10-19T00:00:00Z',
      updatedAt: '2026-10-19T00:00:00Z',
    }],
    page: pageNumber,
    pageSize: 1,
    total,
  };
}

test('loads beyond the former 100-page frontend cap', async () => {
  const calls: number[] = [];
  const documents = await fetchAllDocumentPages(async (pageNumber) => {
    calls.push(pageNumber);
    return page(pageNumber, 101);
  });
  assert.equal(documents.length, 101);
  assert.equal(calls.at(-1), 101);
});

test('reports an explicit error instead of silently truncating at the safety bound', async () => {
  await assert.rejects(
    fetchAllDocumentPages(async (pageNumber) => page(pageNumber, MAX_DOCUMENT_PAGES + 1)),
    /超过前端可读取上限/,
  );
});
