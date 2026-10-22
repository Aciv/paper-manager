// Versioned, optional metadata embedded in one reading-note section body.
// The Go backend deliberately stores this as ordinary Markdown and never
// parses it; the reader, Agent hosts, MCP adapters, and skill instructions are
// the consumers. Keeping the marker in the section file makes the metadata
// travel with a whole-note export without adding storage fields.

export const NOTE_SECTION_META_VERSION = 1;
export const NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS = 512;

const META_OPEN = /^<!--\s*papers-manager:section-meta\s+v=(\d+)\s*(.*)$/;
const HEADING = /^ {0,3}#{1,3}[ \t]+\S/;

export type NoteSectionMeta = {
  description: string;
};

export type ParsedNoteSectionMeta = {
  meta: NoteSectionMeta;
  hasMarker: boolean;
  diagnostics: string[];
};

type LineWithEnding = { text: string; ending: string };

type MarkerLocation = {
  start: number;
  end: number;
  version: number;
  payload: string;
  closed: boolean;
};

function splitLines(markdown: string): LineWithEnding[] {
  const lines: LineWithEnding[] = [];
  for (const match of markdown.matchAll(/[^\n]*\n|[^\n]+$/g)) {
    const raw = match[0];
    const ending = raw.endsWith('\r\n') ? '\r\n' : raw.endsWith('\n') ? '\n' : '';
    lines.push({ text: ending ? raw.slice(0, -ending.length) : raw, ending });
  }
  return lines;
}

function lineText(line: LineWithEnding | undefined): string {
  return line?.text ?? '';
}

function normalizeDescription(value: string): string {
  return value.replace(/\s+/gu, ' ').trim();
}

function codepointCount(value: string): number {
  return Array.from(value).length;
}

// The marker is accepted only in the leading metadata region: after the
// optional H1-H3 heading and blank lines, before ordinary section content.
// This keeps examples inside the body or a fenced code block from being
// mistaken for metadata.
function locateMarker(markdown: string): MarkerLocation | undefined {
  const lines = splitLines(markdown);
  let index = 0;
  while (index < lines.length && lineText(lines[index]).trim() === '') index += 1;
  if (index < lines.length && HEADING.test(lineText(lines[index]))) index += 1;
  while (index < lines.length && lineText(lines[index]).trim() === '') index += 1;
  if (index >= lines.length) return undefined;

  const first = lineText(lines[index]).trim();
  const opened = first.match(META_OPEN);
  if (!opened) return undefined;

  const version = Number(opened[1]);
  const start = index;
  let payload = opened[2] ?? '';
  let end = start;
  let closed = false;
  const inlineClose = payload.indexOf('-->');
  if (inlineClose >= 0) {
    payload = payload.slice(0, inlineClose);
    closed = true;
  } else {
    const parts: string[] = [];
    if (payload.trim()) parts.push(payload);
    for (end = start + 1; end < lines.length; end += 1) {
      const text = lineText(lines[end]);
      const close = text.indexOf('-->');
      if (close >= 0) {
        if (text.slice(0, close).trim()) parts.push(text.slice(0, close));
        closed = true;
        break;
      }
      parts.push(text);
    }
    if (!closed) {
      end = lines.length - 1;
      payload = '';
    } else {
      payload = parts.join('\n');
    }
  }
  return { start, end, version, payload, closed };
}

export function parseNoteSectionMeta(markdown: string): ParsedNoteSectionMeta {
  const location = locateMarker(markdown);
  if (!location) {
    return { meta: { description: '' }, hasMarker: false, diagnostics: [] };
  }
  const diagnostics: string[] = [];
  if (!location.closed) {
    diagnostics.push('section 元数据块没有结束标记 -->。');
  }
  if (location.version !== NOTE_SECTION_META_VERSION) {
    diagnostics.push(`不支持的 section 元数据版本：${location.version}。`);
  }
  if (!location.closed || location.version !== NOTE_SECTION_META_VERSION) {
    return { meta: { description: '' }, hasMarker: true, diagnostics };
  }
  if (!location.payload.trim()) {
    diagnostics.push('section 元数据缺少 JSON 内容。');
    return { meta: { description: '' }, hasMarker: true, diagnostics };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(location.payload);
  } catch {
    diagnostics.push('section 元数据 JSON 无效。');
    return { meta: { description: '' }, hasMarker: true, diagnostics };
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    diagnostics.push('section 元数据必须是 JSON 对象。');
    return { meta: { description: '' }, hasMarker: true, diagnostics };
  }
  const rawDescription = (parsed as Record<string, unknown>).description;
  if (rawDescription !== undefined && typeof rawDescription !== 'string') {
    diagnostics.push('section 元数据 description 必须是字符串。');
    return { meta: { description: '' }, hasMarker: true, diagnostics };
  }
  let description = normalizeDescription(rawDescription ?? '');
  if (description && codepointCount(description) > NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS) {
    diagnostics.push(`section description 超过 ${NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS} 个字符。`);
    description = Array.from(description).slice(0, NOTE_SECTION_DESCRIPTION_MAX_CODEPOINTS).join('');
  }
  return { meta: { description }, hasMarker: true, diagnostics };
}

// Remove the metadata block before rendering. Malformed but closed blocks are
// removed as well: they are metadata-shaped, so showing their raw HTML comment
// in the reading view would be noise. An unclosed block is left untouched
// because removing it could delete ordinary content until EOF.
export function stripNoteSectionMeta(markdown: string): string {
  const location = locateMarker(markdown);
  if (!location || !location.closed) return markdown;
  const lines = splitLines(markdown);
  const before = lines.slice(0, location.start);
  const after = lines.slice(location.end + 1);
  if (after.length > 0 && lineText(after[0]).trim() === ''
    && (before.length === 0 || lineText(before[before.length - 1]).trim() === '')) {
    after.shift();
  }
  return before.concat(after).map((line) => line.text + line.ending).join('');
}

export function buildNoteSectionMeta(description: string): string {
  const normalized = normalizeDescription(description);
  if (!normalized) return '';
  // JSON.stringify does not escape angle brackets. Escaping them keeps the
  // JSON inert if a consumer accidentally treats the comment body as HTML and
  // also prevents a description containing "-->" from closing the comment.
  const payload = JSON.stringify({ description: normalized })
    .replace(/</gu, '\\u003c')
    .replace(/>/gu, '\\u003e')
    .replace(/&/gu, '\\u0026');
  return `<!-- papers-manager:section-meta v=${NOTE_SECTION_META_VERSION}\n${payload}\n-->\n`;
}
