// Reading-note editor helpers. The human editor changes one section body at a
// time; these pure functions turn that change into Plan B operations and guard
// against accidentally introducing another H1-H3 section.
import type { ReadingNoteOperation } from './api.ts';

export type SectionBody = { prefix: string; body: string };

// A section with level > 0 starts with its heading line; the preamble (level 0)
// has no heading and is edited in full.
export function splitSectionBody(markdown: string, level: number): SectionBody {
  if (level <= 0) return { prefix: '', body: markdown };
  const newline = markdown.indexOf('\n');
  if (newline < 0) return { prefix: markdown, body: '' };
  return { prefix: markdown.slice(0, newline + 1), body: markdown.slice(newline + 1) };
}

export function countCodepoints(value: string): number {
  return Array.from(value).length;
}

export function findNewSectionHeading(body: string): { line: number; text: string } | undefined {
  let inFence = false;
  const lines = body.split('\n');
  for (let index = 0; index < lines.length; index += 1) {
    const trimmed = lines[index].trim();
    if (trimmed.startsWith('```') || trimmed.startsWith('~~~')) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;
    const left = lines[index].replace(/^[ \t]+/, '');
    let level = 0;
    while (level < 3 && left[level] === '#') level += 1;
    if (level > 0 && (left[level] === ' ' || left[level] === '\t')) {
      const title = left.slice(level + 1).replace(/[# \t\r]+$/, '').trim();
      if (title) return { line: index + 1, text: lines[index] };
    }
  }
  return undefined;
}

// Convert one body edit into at most one delete and one insert. Offsets are
// Unicode code-point positions inside the whole section, so the heading prefix
// keeps its own rune count. Returns an error when the heading line changed.
export function diffSectionBody(
  sectionId: string,
  originalMarkdown: string,
  nextMarkdown: string,
  level: number,
): { operations: ReadingNoteOperation[]; error?: string } {
  const original = splitSectionBody(originalMarkdown, level);
  const next = splitSectionBody(nextMarkdown, level);
  if (original.prefix !== next.prefix) {
    return { operations: [], error: 'section 标题行不可直接修改；请删除并重建 section。' };
  }
  const nextHeading = findNewSectionHeading(next.body);
  if (nextHeading) {
    return { operations: [], error: `正文第 ${nextHeading.line} 行包含新的 H1-H3 标题；请使用「新增 section」。` };
  }
  if (original.body === next.body) return { operations: [] };

  const originalRunes = Array.from(original.body);
  const nextRunes = Array.from(next.body);
  let prefix = 0;
  while (prefix < originalRunes.length && prefix < nextRunes.length && originalRunes[prefix] === nextRunes[prefix]) {
    prefix += 1;
  }
  let suffix = 0;
  while (
    suffix < originalRunes.length - prefix
    && suffix < nextRunes.length - prefix
    && originalRunes[originalRunes.length - 1 - suffix] === nextRunes[nextRunes.length - 1 - suffix]
  ) {
    suffix += 1;
  }

  const offset = countCodepoints(original.prefix) + prefix;
  const deleteLength = originalRunes.length - prefix - suffix;
  const insertText = nextRunes.slice(prefix, nextRunes.length - suffix).join('');
  const operations: ReadingNoteOperation[] = [];
  if (deleteLength > 0) operations.push({ op: 'deleteText', sectionId, offset, length: deleteLength });
  if (insertText) operations.push({ op: 'insertText', sectionId, offset, text: insertText });
  return { operations };
}

// Insert evidence Markdown at a body-relative code-point offset. The textarea
// reports UTF-16 indices, so callers convert them with `countCodepoints` first.
export function insertEvidenceIntoBody(body: string, offset: number, evidence: string): string {
  const runes = Array.from(body);
  const safeOffset = Math.max(0, Math.min(offset, runes.length));
  const before = runes.slice(0, safeOffset).join('');
  const after = runes.slice(safeOffset).join('');
  const prefix = before && !before.endsWith('\n') ? '\n' : '';
  const suffix = after && !after.startsWith('\n') ? '\n' : '';
  return `${before}${prefix}${evidence}${suffix}${after}`;
}
