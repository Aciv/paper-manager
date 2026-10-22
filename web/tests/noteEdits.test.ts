import assert from 'node:assert/strict';
import test from 'node:test';

import {
  countCodepoints,
  diffSectionBody,
  findNewSectionHeading,
  insertEvidenceIntoBody,
  splitSectionBody,
} from '../src/noteEdits.ts';

test('splits a heading prefix from the editable section body', () => {
  assert.deepEqual(splitSectionBody('# 标题\n正文\n', 1), { prefix: '# 标题\n', body: '正文\n' });
  assert.deepEqual(splitSectionBody('preamble', 0), { prefix: '', body: 'preamble' });
});

test('diffs a body insertion at a code-point offset after the heading', () => {
  const original = '# 标题\nalpha\nbeta\n';
  const next = '# 标题\nalpha\ninserted\nbeta\n';
  const result = diffSectionBody('section-1', original, next, 1);
  assert.equal(result.error, undefined);
  assert.deepEqual(result.operations, [{
    op: 'insertText',
    sectionId: 'section-1',
    offset: countCodepoints('# 标题\nalpha\n'),
    text: 'inserted\n',
  }]);
});

test('diffs CJK and emoji replacements using code-point offsets', () => {
  const original = '# 标题\n甲😀乙\n';
  const next = '# 标题\n甲X乙\n';
  const result = diffSectionBody('section-1', original, next, 1);
  assert.deepEqual(result.operations, [
    { op: 'deleteText', sectionId: 'section-1', offset: countCodepoints('# 标题\n甲'), length: 1 },
    { op: 'insertText', sectionId: 'section-1', offset: countCodepoints('# 标题\n甲'), text: 'X' },
  ]);
});

test('diffs deletions and returns no operations for an unchanged body', () => {
  const result = diffSectionBody('s', '# H\nkeep\ndrop\n', '# H\nkeep\n', 1);
  assert.deepEqual(result.operations, [{
    op: 'deleteText', sectionId: 's', offset: countCodepoints('# H\nkeep\n'), length: countCodepoints('drop\n'),
  }]);
  assert.deepEqual(diffSectionBody('s', '# H\nsame\n', '# H\nsame\n', 1).operations, []);
});

test('refuses to change the heading line through body edits', () => {
  const result = diffSectionBody('s', '# Old\nbody\n', '# New\nbody\n', 1);
  assert.deepEqual(result.operations, []);
  assert.match(result.error ?? '', /标题行/);
});

test('refuses to introduce another H1-H3 heading outside a fenced block', () => {
  const result = diffSectionBody('s', '# H\nbody\n', '# H\nbody\n## New\n', 1);
  assert.match(result.error ?? '', /新增 section/);
  assert.equal(findNewSectionHeading('```\n## code\n```\n'), undefined);
  assert.deepEqual(findNewSectionHeading('text\n### Added\n'), { line: 2, text: '### Added' });
});

test('inserts evidence at the textarea cursor with safe paragraph breaks', () => {
  assert.equal(insertEvidenceIntoBody('alpha\nbeta', 0, '> quote'), '> quote\nalpha\nbeta');
  assert.equal(insertEvidenceIntoBody('alpha\nbeta', countCodepoints('alpha'), '> quote'), 'alpha\n> quote\nbeta');
  assert.equal(insertEvidenceIntoBody('alpha\n', countCodepoints('alpha\n'), '> quote'), 'alpha\n> quote');
  assert.equal(insertEvidenceIntoBody('', 0, '> quote'), '> quote');
});
