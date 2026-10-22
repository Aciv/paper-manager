// Safe, dependency-free Markdown rendering for the reading-note "阅读" view.
// The renderer emits a small allowlist of HTML elements; every piece of note
// text is escaped, so raw HTML in a note can never become markup. Evidence
// links stay buttons instead of anchors so the panel can navigate the PDF.
import { stripNoteSectionMeta } from './noteSectionMeta.ts';

const HTML_ESCAPES: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
};

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => HTML_ESCAPES[character]);
}

function safeExternalHref(href: string): string | undefined {
  try {
    const url = new URL(href);
    if (url.protocol === 'http:' || url.protocol === 'https:' || url.protocol === 'mailto:') {
      return url.href;
    }
  } catch {
    return undefined;
  }
  return undefined;
}

function renderLink(label: string, href: string, depth: number): string {
  const text = renderInline(label, depth + 1) || escapeHtml(href);
  if (href.startsWith('papers-manager://')) {
    return `<button type="button" class="reading-note-preview-evidence" data-evidence-href="${escapeHtml(href)}">${text}</button>`;
  }
  const external = safeExternalHref(href);
  if (!external) return text;
  return `<a href="${escapeHtml(external)}" target="_blank" rel="noopener noreferrer">${text}</a>`;
}

function isWordCharacter(value: string | undefined): boolean {
  return Boolean(value && /[\p{L}\p{N}]/u.test(value));
}

// Inline formatting is deliberately conservative: code spans, strong/em,
// Markdown links and images. Unknown or unsafe syntax stays literal text.
function renderInline(text: string, depth = 0): string {
  if (depth > 8) return escapeHtml(text);
  let output = '';
  let plain = '';
  const flush = () => {
    if (!plain) return;
    output += escapeHtml(plain);
    plain = '';
  };

  let index = 0;
  while (index < text.length) {
    const character = text[index];

    if (character === '\\' && index + 1 < text.length) {
      plain += text[index + 1];
      index += 2;
      continue;
    }

    if (character === '`') {
      let run = 1;
      while (text[index + run] === '`') run += 1;
      const fence = '`'.repeat(run);
      const close = text.indexOf(fence, index + run);
      if (close >= 0) {
        flush();
        output += `<code>${escapeHtml(text.slice(index + run, close))}</code>`;
        index = close + run;
        continue;
      }
      plain += fence;
      index += run;
      continue;
    }

    if (character === '\n') {
      flush();
      output += '<br>';
      index += 1;
      continue;
    }

    if ((character === '*' || character === '_')
      && !(character === '_' && isWordCharacter(text[index - 1]))) {
      const marker = text.startsWith(character + character, index) ? character + character : character;
      const close = text.indexOf(marker, index + marker.length);
      if (close > index + marker.length) {
        flush();
        const tag = marker.length === 2 ? 'strong' : 'em';
        output += `<${tag}>${renderInline(text.slice(index + marker.length, close), depth + 1)}</${tag}>`;
        index = close + marker.length;
        continue;
      }
    }

    if ((character === '[' || (character === '!' && text[index + 1] === '['))) {
      const isImage = character === '!';
      const labelStart = index + (isImage ? 2 : 1);
      const labelEnd = text.indexOf(']', labelStart);
      if (labelEnd > labelStart && text[labelEnd + 1] === '(') {
        const hrefEnd = text.indexOf(')', labelEnd + 2);
        if (hrefEnd > labelEnd + 2) {
          const label = text.slice(labelStart, labelEnd);
          const href = text.slice(labelEnd + 2, hrefEnd).trim();
          flush();
          output += isImage ? escapeHtml(label || '图片') : renderLink(label, href, depth);
          index = hrefEnd + 1;
          continue;
        }
      }
    }

    plain += character;
    index += 1;
  }

  flush();
  return output;
}

function isBlockStart(lines: string[], index: number): boolean {
  const line = lines[index] ?? '';
  return /^\s*(`{3,}|~{3,})/.test(line)
    || /^\s{0,3}#{1,6}\s+/.test(line)
    || /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)
    || /^\s{0,3}>/.test(line)
    || /^\s*(?:[-*+]|\d+[.)])\s+/.test(line);
}

function renderBlocks(lines: string[], depth = 0): string {
  if (depth > 6) return `<p>${renderInline(lines.join('\n'))}</p>`;
  const output: string[] = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) {
      index += 1;
      continue;
    }

    const fence = line.match(/^\s*(`{3,}|~{3,})/);
    if (fence) {
      const marker = fence[1];
      const markerCharacter = marker[0];
      const minimum = marker.length;
      const code: string[] = [];
      index += 1;
      while (index < lines.length) {
        const closing = lines[index].match(/^\s*(`{3,}|~{3,})\s*$/);
        if (closing && closing[1][0] === markerCharacter && closing[1].length >= minimum) {
          index += 1;
          break;
        }
        code.push(lines[index]);
        index += 1;
      }
      output.push(`<pre><code>${escapeHtml(code.join('\n'))}</code></pre>`);
      continue;
    }

    const heading = line.match(/^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/);
    if (heading) {
      const level = heading[1].length;
      output.push(`<h${level}>${renderInline(heading[2])}</h${level}>`);
      index += 1;
      continue;
    }

    if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
      output.push('<hr>');
      index += 1;
      continue;
    }

    if (/^\s{0,3}>/.test(line)) {
      const quoted: string[] = [];
      while (index < lines.length && /^\s{0,3}>/.test(lines[index])) {
        quoted.push(lines[index].replace(/^\s{0,3}>\s?/, ''));
        index += 1;
      }
      output.push(`<blockquote>${renderBlocks(quoted, depth + 1)}</blockquote>`);
      continue;
    }

    const unordered = line.match(/^\s*[-*+]\s+(.*)$/);
    if (unordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const item = lines[index].match(/^\s*[-*+]\s+(.*)$/);
        if (!item) break;
        items.push(item[1]);
        index += 1;
      }
      output.push(`<ul>${items.map((item) => `<li>${renderInline(item)}</li>`).join('')}</ul>`);
      continue;
    }

    const ordered = line.match(/^\s*(\d+)[.)]\s+(.*)$/);
    if (ordered) {
      const start = Number(ordered[1]);
      const items: string[] = [];
      while (index < lines.length) {
        const item = lines[index].match(/^\s*\d+[.)]\s+(.*)$/);
        if (!item) break;
        items.push(item[1]);
        index += 1;
      }
      const startAttribute = Number.isInteger(start) && start !== 1 ? ` start="${start}"` : '';
      output.push(`<ol${startAttribute}>${items.map((item) => `<li>${renderInline(item)}</li>`).join('')}</ol>`);
      continue;
    }

    const paragraph: string[] = [];
    while (index < lines.length && lines[index].trim() && !isBlockStart(lines, index)) {
      paragraph.push(lines[index]);
      index += 1;
    }
    output.push(`<p>${renderInline(paragraph.join('\n'))}</p>`);
  }

  return output.join('');
}

export function renderReadingNoteMarkdown(markdown: string): string {
  const body = stripNoteSectionMeta(markdown).replace(/\r\n?/g, '\n');
  return renderBlocks(body.split('\n'));
}
