import assert from 'node:assert/strict';
import test from 'node:test';

import { renderReadingNoteMarkdown } from '../src/readingNotePreview.ts';

test('renders headings, line breaks, lists and blockquotes', () => {
  const html = renderReadingNoteMarkdown([
    '## 结果',
    '第一行',
    '第二行',
    '',
    '- alpha',
    '- beta',
    '',
    '1. one',
    '2. two',
    '',
    '> quoted',
  ].join('\n'));
  assert.match(html, /<h2>结果<\/h2>/);
  assert.match(html, /<p>第一行<br>第二行<\/p>/);
  assert.match(html, /<ul><li>alpha<\/li><li>beta<\/li><\/ul>/);
  assert.match(html, /<ol><li>one<\/li><li>two<\/li><\/ol>/);
  assert.match(html, /<blockquote><p>quoted<\/p><\/blockquote>/);
});

test('renders fenced code and inline formatting', () => {
  const html = renderReadingNoteMarkdown([
    '**bold** and *italic* with `code` and a \\*literal\\* star',
    '',
    '```ts',
    'const value = "<tag>";',
    '```',
  ].join('\n'));
  assert.match(html, /<strong>bold<\/strong>/);
  assert.match(html, /<em>italic<\/em>/);
  assert.match(html, /<code>code<\/code>/);
  assert.match(html, /a \*literal\* star/);
  assert.match(html, /<pre><code>const value = &quot;&lt;tag&gt;&quot;;<\/code><\/pre>/);
});

test('escapes raw HTML from note content', () => {
  const html = renderReadingNoteMarkdown('<script>alert("x")</script> & <b>raw</b>');
  assert.equal(html.includes('<script>'), false);
  assert.equal(html.includes('<b>'), false);
  assert.match(html, /&lt;script&gt;alert\(&quot;x&quot;\)&lt;\/script&gt; &amp; &lt;b&gt;raw&lt;\/b&gt;/);
});

test('renders safe external links and refuses script URLs', () => {
  const html = renderReadingNoteMarkdown('[paper](https://example.com/a?b=1) [bad](javascript:alert(1))');
  assert.match(html, /<a href="https:\/\/example.com\/a\?b=1" target="_blank" rel="noopener noreferrer">paper<\/a>/);
  assert.equal(html.includes('javascript:'), false);
  assert.match(html, /bad/);
});

test('marks evidence links as panel navigation buttons', () => {
  const href = 'papers-manager://self/evidence?v=1&page=3&rect=0.1,0.2,0.3,0.4';
  const html = renderReadingNoteMarkdown(`第 3 页：[${href}](${href})`);
  assert.match(html, /<button type="button" class="reading-note-preview-evidence"/);
  assert.match(html, /data-evidence-href="papers-manager:\/\/self\/evidence\?v=1&amp;page=3&amp;rect=0.1,0.2,0.3,0.4"/);
  assert.equal(html.includes('<a href="papers-manager:'), false);
});
