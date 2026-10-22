// Versioned `papers-manager://` evidence links embedded in reading-note
// Markdown. The Go backend deliberately does not parse or index these links;
// this module is the shared frontend implementation used by the note panel and
// the PDF reader.
import type { NormalizedPDFRegion } from './pdfSelection';

export type EvidenceKind = 'text' | 'block' | 'area';

export type EvidenceSeed = {
  kind: EvidenceKind;
  excerpt: string;
  pageNumber?: number;
  // A selection may carry one rectangle; `rects` is kept for callers that
  // already have a normalized list.
  region?: NormalizedPDFRegion;
  rects?: NormalizedPDFRegion[];
  blockId?: string;
  // Optional source-section hint. It is advisory: consumers always fall back
  // to page/rectangle when the hint is stale.
  section?: string;
  // Omitted means "this document". An explicit document target is supported by
  // the format but the current reader only navigates within its own document.
  documentId?: string;
};

export type EvidenceLinkTarget = {
  documentId?: string;
  page: number;
  rects: NormalizedPDFRegion[];
  blockId?: string;
  section?: string;
};

export type EvidenceLink = {
  text: string;
  href: string;
  target?: EvidenceLinkTarget;
  diagnostics: string[];
};

export type EvidenceParseResult = {
  links: EvidenceLink[];
  diagnostics: string[];
};

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const MAX_EXCERPT_CODEPOINTS = 1200;

function truncateCodepoints(value: string, limit: number): string {
  const points = Array.from(value);
  if (points.length <= limit) return value;
  return `${points.slice(0, limit).join('')}…`;
}

function validRect(rect: NormalizedPDFRegion): boolean {
  return Number.isFinite(rect.x0) && Number.isFinite(rect.y0) && Number.isFinite(rect.x1) && Number.isFinite(rect.y1)
    && rect.x0 >= 0 && rect.y0 >= 0 && rect.x1 <= 1 && rect.y1 <= 1
    && rect.x0 < rect.x1 && rect.y0 < rect.y1;
}

function normalizeRect(rect: NormalizedPDFRegion): NormalizedPDFRegion {
  const clamp = (value: number) => Math.min(1, Math.max(0, value));
  const x0 = clamp(Math.min(rect.x0, rect.x1));
  const y0 = clamp(Math.min(rect.y0, rect.y1));
  const x1 = clamp(Math.max(rect.x0, rect.x1));
  const y1 = clamp(Math.max(rect.y0, rect.y1));
  return { x0, y0, x1, y1 };
}

function rectParameter(rect: NormalizedPDFRegion): string {
  const value = (number: number) => Number(number.toFixed(5)).toString();
  return [rect.x0, rect.y0, rect.x1, rect.y1].map(value).join(',');
}

export function evidenceExcerpt(text: string): string {
  const normalized = text
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .join('\n');
  return truncateCodepoints(normalized, MAX_EXCERPT_CODEPOINTS);
}

// Build the versioned link URL. Returns undefined when the seed has no usable
// page, so callers can still insert the quoted excerpt without a broken link.
export function buildEvidenceHref(seed: EvidenceSeed): string | undefined {
  if (!seed.pageNumber || !Number.isInteger(seed.pageNumber) || seed.pageNumber < 1) return undefined;
  if (seed.documentId && !UUID_PATTERN.test(seed.documentId)) return undefined;

  const params: string[] = ['v=1', `page=${seed.pageNumber}`];
  const candidates = seed.rects ?? (seed.region ? [seed.region] : []);
  for (const candidate of candidates) {
    const rect = normalizeRect(candidate);
    if (validRect(rect)) params.push(`rect=${rectParameter(rect)}`);
  }
  if (seed.blockId?.trim()) params.push(`block=${encodeURIComponent(seed.blockId.trim())}`);
  if (seed.section?.trim()) params.push(`section=${encodeURIComponent(seed.section.trim())}`);

  const host = seed.documentId ? `document/${seed.documentId}` : 'self';
  return `papers-manager://${host}/evidence?${params.join('&')}`;
}

export function evidenceLinkLabel(seed: Pick<EvidenceSeed, 'kind' | 'pageNumber'>): string {
  const page = seed.pageNumber ? `第 ${seed.pageNumber} 页` : '原文';
  if (seed.kind === 'block') return `${page} · Block`;
  if (seed.kind === 'area') return `${page} · 区域`;
  return page;
}

// Build one compact evidence link. The selected text stays in the PDF; the
// note only records where to find it, so insertions do not duplicate the paper.
// This is ordinary note content; the backend stores it as-is and never resolves
// it.
export function formatEvidenceMarkdown(seed: EvidenceSeed): string {
  const href = buildEvidenceHref(seed);
  if (!href) return '';
  return `[${evidenceLinkLabel(seed)}](${href})`;
}

function parseRect(raw: string, index: number, diagnostics: string[]): NormalizedPDFRegion | undefined {
  const parts = raw.split(',').map((part) => Number(part.trim()));
  if (parts.length !== 4 || parts.some((value) => !Number.isFinite(value))) {
    diagnostics.push(`证据矩形 ${index + 1} 格式无效：${raw}`);
    return undefined;
  }
  const rect = { x0: parts[0], y0: parts[1], x1: parts[2], y1: parts[3] };
  if (!validRect(rect)) {
    diagnostics.push(`证据矩形 ${index + 1} 超出归一化页面范围：${raw}`);
    return undefined;
  }
  return rect;
}

export function parseEvidenceHref(href: string): { target?: EvidenceLinkTarget; diagnostics: string[] } {
  const diagnostics: string[] = [];
  let url: URL;
  try {
    url = new URL(href);
  } catch {
    return { diagnostics: ['证据链接不是合法 URL。'] };
  }
  if (url.protocol !== 'papers-manager:') {
    return { diagnostics: ['证据链接协议必须是 papers-manager://。'] };
  }

  let documentId: string | undefined;
  if (url.host === 'self') {
    if (url.pathname !== '/evidence') {
      diagnostics.push('self 证据链接路径必须是 /evidence。');
    }
  } else if (url.host === 'document') {
    const parts = url.pathname.split('/').filter(Boolean);
    if (parts.length !== 2 || parts[1] !== 'evidence' || !UUID_PATTERN.test(parts[0])) {
      diagnostics.push('document 证据链接必须使用 /{document-uuid}/evidence。');
    } else {
      documentId = parts[0];
    }
  } else {
    diagnostics.push(`未知的证据链接目标 host：${url.host || '空'}。`);
  }

  if (url.searchParams.get('v') !== '1') {
    diagnostics.push('证据链接版本不受支持，当前只支持 v=1。');
  }
  const pageText = url.searchParams.get('page') ?? '';
  const page = Number(pageText);
  if (!/^\d+$/.test(pageText) || !Number.isInteger(page) || page < 1) {
    diagnostics.push('证据链接缺少有效的 page。');
  }

  const rects = url.searchParams.getAll('rect')
    .map((raw, index) => parseRect(raw, index, diagnostics))
    .filter((rect): rect is NormalizedPDFRegion => Boolean(rect));

  const blockId = url.searchParams.get('block')?.trim() || undefined;
  const section = url.searchParams.get('section')?.trim() || undefined;
  if (diagnostics.length > 0) {
    return { target: undefined, diagnostics };
  }
  return { target: { documentId, page, rects, blockId, section }, diagnostics };
}

function rangesOverlap(left: { start: number; end: number }, right: { start: number; end: number }): boolean {
  return left.start < right.end && right.start < left.end;
}

// Scan Markdown for Markdown links and bare URLs using the custom scheme.
// Malformed links are reported, never silently removed from the note.
export function parseEvidenceMarkdown(markdown: string): EvidenceParseResult {
  const ordered: Array<{ position: number; link: EvidenceLink }> = [];
  const diagnostics: string[] = [];
  const occupied: Array<{ start: number; end: number }> = [];
  const markdownLink = /\[([^\]]*)\]\(\s*(papers-manager:\/\/[^)\s]+)\s*\)/g;
  for (const match of markdown.matchAll(markdownLink)) {
    const start = match.index ?? 0;
    const href = match[2];
    const parsed = parseEvidenceHref(href);
    ordered.push({
      position: start,
      link: {
        text: match[1].trim() || href,
        href,
        target: parsed.target,
        diagnostics: parsed.diagnostics,
      },
    });
    diagnostics.push(...parsed.diagnostics);
    occupied.push({ start, end: start + match[0].length });
  }

  const bare = /papers-manager:\/\/[^\s<>()]+/g;
  for (const match of markdown.matchAll(bare)) {
    const start = match.index ?? 0;
    const end = start + match[0].length;
    if (occupied.some((range) => rangesOverlap(range, { start, end }))) continue;
    const href = match[0].replace(/[.,;，。；]+$/, '');
    const parsed = parseEvidenceHref(href);
    ordered.push({
      position: start,
      link: { text: href, href, target: parsed.target, diagnostics: parsed.diagnostics },
    });
    diagnostics.push(...parsed.diagnostics);
  }
  return {
    links: ordered.sort((left, right) => left.position - right.position).map((entry) => entry.link),
    diagnostics,
  };
}
