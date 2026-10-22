import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildEvidenceHref,
  evidenceExcerpt,
  formatEvidenceMarkdown,
  parseEvidenceHref,
  parseEvidenceMarkdown,
} from '../src/evidenceLinks.ts';

const documentID = '123e4567-e89b-12d3-a456-426614174000';

test('builds and parses a self evidence link with page, rect, block and section hints', () => {
  const href = buildEvidenceHref({
    kind: 'block',
    excerpt: 'block text',
    pageNumber: 2,
    region: { x0: 0.1, y0: 0.2, x1: 0.3, y1: 0.4 },
    blockId: 'block-7',
    section: '2 Method',
  });
  assert.equal(href, 'papers-manager://self/evidence?v=1&page=2&rect=0.1,0.2,0.3,0.4&block=block-7&section=2%20Method');
  const parsed = parseEvidenceHref(href!);
  assert.deepEqual(parsed.diagnostics, []);
  assert.deepEqual(parsed.target, {
    documentId: undefined,
    page: 2,
    rects: [{ x0: 0.1, y0: 0.2, x1: 0.3, y1: 0.4 }],
    blockId: 'block-7',
    section: '2 Method',
  });
});

test('supports explicit document targets and repeated rectangles', () => {
  const href = buildEvidenceHref({
    kind: 'text',
    excerpt: 'quote',
    documentId: documentID,
    pageNumber: 5,
    rects: [{ x0: 0.1, y0: 0.1, x1: 0.4, y1: 0.2 }, { x0: 0.1, y0: 0.3, x1: 0.6, y1: 0.4 }],
  });
  assert.match(href!, new RegExp(`^papers-manager://document/${documentID}/evidence\\?`));
  const parsed = parseEvidenceHref(href!);
  assert.equal(parsed.target?.documentId, documentID);
  assert.equal(parsed.target?.rects.length, 2);
});

test('keeps a page-only link usable when no rectangle was selected', () => {
  const href = buildEvidenceHref({ kind: 'text', excerpt: '', pageNumber: 3 });
  assert.equal(href, 'papers-manager://self/evidence?v=1&page=3');
  const parsed = parseEvidenceHref(href!);
  assert.equal(parsed.target?.page, 3);
  assert.deepEqual(parsed.target?.rects, []);
});

test('reports unsupported versions, invalid pages and malformed rectangles', () => {
  assert.match(parseEvidenceHref('papers-manager://self/evidence?v=2&page=1').diagnostics.join('\n'), /版本/);
  assert.match(parseEvidenceHref('papers-manager://self/evidence?v=1&page=0').diagnostics.join('\n'), /page/);
  assert.match(parseEvidenceHref('papers-manager://self/evidence?v=1&page=1&rect=0.4,0.2,0.1,0.3').diagnostics.join('\n'), /矩形/);
  assert.match(parseEvidenceHref('papers-manager://self/evidence?v=1&page=1&rect=1,2,3').diagnostics.join('\n'), /格式无效/);
});

test('rejects unknown hosts and malformed document paths', () => {
  assert.match(parseEvidenceHref('papers-manager://elsewhere/evidence?v=1&page=1').diagnostics.join('\n'), /host/);
  assert.match(parseEvidenceHref('papers-manager://document/not-a-uuid/evidence?v=1&page=1').diagnostics.join('\n'), /document/);
});

test('parses markdown links and bare URLs, and reports malformed links', () => {
  const markdown = [
    '> 结论',
    '[第 2 页](papers-manager://self/evidence?v=1&page=2&rect=0.1,0.2,0.3,0.4)',
    '<papers-manager://self/evidence?v=1&page=4>',
    '[坏链接](papers-manager://self/evidence?v=1&page=zero)',
  ].join('\n');
  const parsed = parseEvidenceMarkdown(markdown);
  assert.equal(parsed.links.length, 3);
  assert.equal(parsed.links[0].target?.page, 2);
  assert.equal(parsed.links[1].target?.page, 4);
  assert.equal(parsed.links[2].target, undefined);
  assert.match(parsed.diagnostics.join('\n'), /page/);
});

test('formats a link-only insertion and never copies the excerpt', () => {
  const markdown = formatEvidenceMarkdown({
    kind: 'area',
    excerpt: '第一行\n第二行',
    pageNumber: 7,
    region: { x0: 0.2, y0: 0.2, x1: 0.5, y1: 0.5 },
  });
  assert.equal(markdown, '[第 7 页 · 区域](papers-manager://self/evidence?v=1&page=7&rect=0.2,0.2,0.5,0.5)');
  assert.equal(markdown.includes('第一行'), false);
  assert.equal(formatEvidenceMarkdown({ kind: 'text', excerpt: '无页码', pageNumber: undefined }), '');
});

test('normalizes excerpts and bounds their size', () => {
  assert.equal(evidenceExcerpt('  a\r\n\r\n  b  '), 'a\nb');
  const excerpt = evidenceExcerpt('中'.repeat(1300));
  assert.equal(Array.from(excerpt).length, 1201);
  assert.ok(excerpt.endsWith('…'));
});
