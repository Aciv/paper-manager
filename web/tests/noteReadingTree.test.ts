import assert from 'node:assert/strict';
import test from 'node:test';
import { buildNoteTree, getReadingChapter } from '../src/noteReadingTree.ts';

function entry(id: string, level: number, order: number) {
  return { id, title: id, level, order, contentRevision: 1, sizeBytes: 10 };
}
const entries = [entry('intro', 0, 0), entry('method', 1, 10), entry('attention', 2, 20), entry('mask', 3, 30), entry('ffn', 2, 40), entry('results', 1, 50), entry('ablation', 2, 60)];

test('chapter includes parent first and every ordered descendant, excluding adjacent H1 and preamble', () => {
  assert.deepEqual(getReadingChapter([...entries].reverse(), 'mask').map((item) => item.id), ['method', 'attention', 'mask', 'ffn']);
  assert.deepEqual(getReadingChapter(entries, 'method').map((item) => item.id), ['method', 'attention', 'mask', 'ffn']);
  assert.deepEqual(getReadingChapter(entries, 'intro').map((item) => item.id), ['intro']);
  assert.deepEqual(getReadingChapter(entries, 'missing'), []);
});

test('tree preserves hierarchy and stable nested numbers', () => {
  const tree = buildNoteTree(entries);
  assert.equal(tree[1].children[0].children[0].entry.id, 'mask');
  assert.equal(tree[1].children[0].children[0].number, '2.1.1');
  assert.equal(tree[1].children[1].number, '2.2');
  assert.equal(tree[2].children[0].entry.id, 'ablation');
});

test('legacy headings without H1 remain readable and skipped levels do not create fake sections', () => {
  const legacy = [entry('root', 2, 0), entry('child', 3, 1), entry('next', 2, 2)];
  assert.deepEqual(getReadingChapter(legacy, 'child').map((item) => item.id), ['root', 'child']);
  assert.equal(buildNoteTree([entry('h1', 1, 0), entry('h3', 3, 1)])[0].children[0].entry.id, 'h3');
});
