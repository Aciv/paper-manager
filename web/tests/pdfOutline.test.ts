import assert from 'node:assert/strict';
import test from 'node:test';

import { activeOutlineNode, annotateOutlinePages, destinationPage, type OutlineEntry } from '../src/pdfOutline.ts';

test('reads a numeric page reference from a destination', async () => {
  const resolvers = { getDestination: async () => null, getPageIndex: async () => { throw new Error('unused'); }, numPages: 10 };
  assert.equal(await destinationPage([4, { name: 'XYZ' }, 0, 0, 0], resolvers.getPageIndex, resolvers.numPages), 5);
});

test('resolves an indirect page reference through the document', async () => {
  const seen: unknown[] = [];
  const getPageIndex = async (reference: unknown) => { seen.push(reference); return 11; };
  assert.equal(await destinationPage([{ num: 42, gen: 0 }, { name: 'Fit' }], getPageIndex, 20), 12);
  assert.deepEqual(seen, [{ num: 42, gen: 0 }]);
});

test('reports unknown pages for malformed, out-of-range or failing destinations', async () => {
  const failing = async () => { throw new Error('bad reference'); };
  assert.equal(await destinationPage(null, failing, 10), undefined);
  assert.equal(await destinationPage([], failing, 10), undefined);
  assert.equal(await destinationPage([-1], failing, 10), undefined);
  assert.equal(await destinationPage([10], failing, 10), undefined);
  assert.equal(await destinationPage([{ num: 1 }], failing, 10), undefined);
});

test('annotates nested entries, resolving named destinations only', async () => {
  const outline: OutlineEntry[] = [
    { title: '1 Introduction', dest: [0, { name: 'XYZ' }] },
    { title: '2 Method', dest: 'method-dest', items: [
      { title: '2.1 Setup', dest: [{ num: 7, gen: 0 }, { name: 'FitH' }] },
      { title: 'No destination', dest: null },
    ] },
  ];
  const resolvers = {
    getDestination: async (dest: unknown) => (dest === 'method-dest' ? [4, { name: 'Fit' }] : null),
    getPageIndex: async () => 6,
    numPages: 12,
  };
  const annotated = await annotateOutlinePages(outline, resolvers);
  assert.equal(annotated[0].page, 1);
  assert.equal(annotated[1].page, 5);
  assert.equal(annotated[1].items?.[0].page, 7);
  assert.equal(annotated[1].items?.[1].page, undefined);
  // the source tree is left alone
  assert.equal(outline[0].page, undefined);
});

test('picks the deepest entry at or before the current page', () => {
  const outline: OutlineEntry[] = [
    { title: '1 Introduction', page: 1 },
    { title: '2 Method', page: 4, items: [
      { title: '2.1 Setup', page: 4 },
      { title: '2.2 Training', page: 6 },
    ] },
    { title: '3 Results', page: 9 },
    { title: 'Appendix', items: [{ title: 'A Proofs', page: 11 }] },
  ];
  assert.equal(activeOutlineNode(outline, 1)?.title, '1 Introduction');
  assert.equal(activeOutlineNode(outline, 5)?.title, '2.1 Setup');
  assert.equal(activeOutlineNode(outline, 6)?.title, '2.2 Training');
  assert.equal(activeOutlineNode(outline, 10)?.title, '3 Results');
  assert.equal(activeOutlineNode(outline, 11)?.title, 'A Proofs');
  // entries without a resolved page never win
  assert.equal(activeOutlineNode([{ title: 'only', dest: null }], 3), undefined);
});
