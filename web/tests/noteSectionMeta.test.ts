import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildNoteSectionMeta,
  NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS,
  parseNoteSectionMeta,
  stripNoteSectionMeta,
} from '../src/noteSectionMeta.ts';

const MARKER = [
  '<!-- papers-manager:section-meta v=1',
  '{"description":"本节说明模型结构和训练目标。"}',
  '-->',
].join('\n');

test('parses a section description after the heading', () => {
  const markdown = `# 方法\n\n${MARKER}\n\n正文第一段。\n`;
  const parsed = parseNoteSectionMeta(markdown);
  assert.equal(parsed.hasMarker, true);
  assert.deepEqual(parsed.diagnostics, []);
  assert.equal(parsed.meta.description, '本节说明模型结构和训练目标。');
});

test('strips the metadata block without changing the section heading or body', () => {
  const markdown = `# 方法\n\n${MARKER}\n\n正文第一段。\n`;
  assert.equal(stripNoteSectionMeta(markdown), '# 方法\n\n正文第一段。\n');
});

test('accepts an inline metadata block and round-trips escaped characters', () => {
  const description = 'a --> <b> & c';
  const markdown = `# 方法\n\n${buildNoteSectionMeta(description).trim()}\n\n正文\n`;
  const parsed = parseNoteSectionMeta(markdown);
  assert.deepEqual(parsed.diagnostics, []);
  assert.equal(parsed.meta.description, description);
  assert.equal(stripNoteSectionMeta(markdown), '# 方法\n\n正文\n');
});

test('leaves a section without metadata unchanged', () => {
  const markdown = '# 方法\n\n正文\n';
  const parsed = parseNoteSectionMeta(markdown);
  assert.equal(parsed.hasMarker, false);
  assert.equal(parsed.meta.description, '');
  assert.deepEqual(parsed.diagnostics, []);
  assert.equal(stripNoteSectionMeta(markdown), markdown);
});

test('does not treat metadata-shaped body content as the leading marker', () => {
  const markdown = `# 方法\n\n正文\n\n${MARKER}\n`;
  const parsed = parseNoteSectionMeta(markdown);
  assert.equal(parsed.hasMarker, false);
  assert.equal(stripNoteSectionMeta(markdown), markdown);
});

test('reports malformed JSON and still hides a closed metadata block', () => {
  const markdown = '# 方法\n\n<!-- papers-manager:section-meta v=1\n{not json}\n-->\n\n正文\n';
  const parsed = parseNoteSectionMeta(markdown);
  assert.equal(parsed.hasMarker, true);
  assert.equal(parsed.meta.description, '');
  assert.match(parsed.diagnostics.join(' '), /JSON/);
  assert.equal(stripNoteSectionMeta(markdown), '# 方法\n\n正文\n');
});

test('reports an unclosed block without stripping content to EOF', () => {
  const markdown = '# 方法\n\n<!-- papers-manager:section-meta v=1\n{"description":"未结束"}\n正文\n';
  const parsed = parseNoteSectionMeta(markdown);
  assert.equal(parsed.hasMarker, true);
  assert.match(parsed.diagnostics.join(' '), /结束标记/);
  assert.equal(stripNoteSectionMeta(markdown), markdown);
});

test('reports an unsupported metadata version', () => {
  const markdown = '# 方法\n\n<!-- papers-manager:section-meta v=2\n{"description":"future"}\n-->\n';
  const parsed = parseNoteSectionMeta(markdown);
  assert.match(parsed.diagnostics.join(' '), /不支持的 section 元数据版本/);
  assert.equal(parsed.meta.description, '');
});

test('bounds an oversized description and reports it', () => {
  const description = 'x'.repeat(NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS + 1);
  const parsed = parseNoteSectionMeta(`# 方法\n\n${buildNoteSectionMeta(description)}\n`);
  assert.equal(Array.from(parsed.meta.description).length, NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS);
  assert.match(parsed.diagnostics.join(' '), /超过/);
});

test('preserves CRLF line endings while removing the metadata block', () => {
  const markdown = '# 方法\r\n\r\n<!-- papers-manager:section-meta v=1\r\n{"description":"摘要"}\r\n-->\r\n\r\n正文\r\n';
  assert.equal(stripNoteSectionMeta(markdown), '# 方法\r\n\r\n正文\r\n');
});
