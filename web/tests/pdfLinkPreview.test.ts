import assert from 'node:assert/strict';
import test from 'node:test';
import { destinationPDFPoint, destinationPreviewRegion, mergeAdjacentPDFLinks, viewportRegion } from '../src/pdfLinkPreview.ts';

test('normalizes a rectangle already converted by the pdf.js viewport', () => {
  assert.deepEqual(viewportRegion([60, 80, 120, 100], 600, 800), {
    x0: 0.1,
    y0: 0.1,
    x1: 0.2,
    y1: 0.125,
  });
});

test('extracts raw PDF coordinates from named and inline XYZ destinations', () => {
  assert.deepEqual(destinationPDFPoint([{}, { name: 'XYZ' }, 65, 477, null]), { x: 65, y: 477 });
  assert.deepEqual(destinationPDFPoint([{}, { name: 'XYZ' }, 65, 700, null]), { x: 65, y: 700 });
});

test('rejects page-only destinations without a local preview anchor', () => {
  assert.equal(destinationPDFPoint([{}, { name: 'Fit' }]), undefined);
});

test('builds a bounded full-width strip around the destination point', () => {
  assert.deepEqual(destinationPreviewRegion({ pageNumber: 3, pageWidth: 600, pageHeight: 800, x: 65, y: 400 }), {
    x0: 0.045,
    y0: 0.49,
    x1: 0.955,
    y1: 0.6775,
  });
});

test('merges neighboring words on one line when they point to the same destination', () => {
  const target = { pageNumber: 9, pageWidth: 600, pageHeight: 800, x: 70, y: 500 };
  const merged = mergeAdjacentPDFLinks([
    { id: 'second', sourcePageNumber: 1, sourceRegion: { x0: 0.21, y0: 0.2, x1: 0.3, y1: 0.22 }, target },
    { id: 'first', sourcePageNumber: 1, sourceRegion: { x0: 0.1, y0: 0.2, x1: 0.2, y1: 0.22 }, target },
  ]);
  assert.equal(merged.length, 1);
  assert.deepEqual(merged[0].sourceRegion, { x0: 0.1, y0: 0.2, x1: 0.3, y1: 0.22 });
});

test('keeps repeated links separate when they are on another line or far apart', () => {
  const target = { pageNumber: 9, pageWidth: 600, pageHeight: 800, x: 70, y: 500 };
  const merged = mergeAdjacentPDFLinks([
    { id: 'first', sourcePageNumber: 1, sourceRegion: { x0: 0.1, y0: 0.2, x1: 0.2, y1: 0.22 }, target },
    { id: 'far', sourcePageNumber: 1, sourceRegion: { x0: 0.5, y0: 0.2, x1: 0.6, y1: 0.22 }, target },
    { id: 'next-line', sourcePageNumber: 1, sourceRegion: { x0: 0.1, y0: 0.24, x1: 0.2, y1: 0.26 }, target },
  ]);
  assert.equal(merged.length, 3);
});
