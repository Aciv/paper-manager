// doi.org content negotiation for one reference. The browser calls the DOI
// resolver directly (Crossref/DataCite expose the negotiated response with
// permissive CORS headers), so this module stays independent from the Go API
// client. Formats map to the Accept header documented by Crossref/DataCite:
// exchange formats use their own media types, formatted citations use
// `text/x-bibliography` with a CSL style id.

export type DoiCitationFormatID =
  | 'bibtex'
  | 'ris'
  | 'csl-json'
  | 'apa'
  | 'ieee'
  | 'mla'
  | 'chicago-author-date'
  | 'harvard'
  | 'nature'
  | 'ama'
  | 'gbt7714';

export type DoiCitationFormat = {
  id: DoiCitationFormatID;
  label: string;
  accept: string;
  kind: 'data' | 'style';
  extension: string;
  contentType: string;
};

export const DOI_CITATION_FORMATS: readonly DoiCitationFormat[] = [
  { id: 'bibtex', label: 'BibTeX', accept: 'application/x-bibtex', kind: 'data', extension: 'bib', contentType: 'application/x-bibtex' },
  { id: 'ris', label: 'RIS', accept: 'application/x-research-info-systems', kind: 'data', extension: 'ris', contentType: 'application/x-research-info-systems' },
  { id: 'csl-json', label: 'CSL-JSON', accept: 'application/vnd.citationstyles.csl+json', kind: 'data', extension: 'json', contentType: 'application/vnd.citationstyles.csl+json' },
  { id: 'apa', label: 'APA 7', accept: 'text/x-bibliography; style=apa', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'ieee', label: 'IEEE', accept: 'text/x-bibliography; style=ieee', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'mla', label: 'MLA 9', accept: 'text/x-bibliography; style=modern-language-association', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'chicago-author-date', label: 'Chicago（作者-年份）', accept: 'text/x-bibliography; style=chicago-author-date', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'harvard', label: 'Harvard', accept: 'text/x-bibliography; style=harvard-cite-them-right', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'nature', label: 'Nature', accept: 'text/x-bibliography; style=nature', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'ama', label: 'AMA', accept: 'text/x-bibliography; style=american-medical-association', kind: 'style', extension: 'txt', contentType: 'text/plain' },
  { id: 'gbt7714', label: 'GB/T 7714-2015', accept: 'text/x-bibliography; style=china-national-standard-gb-t-7714-2015-numeric', kind: 'style', extension: 'txt', contentType: 'text/plain' },
];

export const DOI_CITATION_GROUPS = [
  { label: '交换格式', kind: 'data' as const },
  { label: '格式化引用（CSL）', kind: 'style' as const },
];

export type DoiCitationTarget = {
  doi: string;
  title?: string;
  rawReference?: string;
};

// The reference DOI is usually already normalized by the Go API, but manual
// edits may paste a resolver URL. Keep the suffix untouched (DOIs are
// case-insensitive) and strip only resolver/protocol wrappers and sentence
// punctuation so the value can be used as a URL path.
const DOI_PATTERN = /10\.\d{4,9}\/\S+/u;

export function normalizeDoi(value?: string): string {
  let doi = (value ?? '').trim();
  if (!doi) return '';
  doi = doi.replace(/^https?:\/\/(?:dx\.)?doi\.org\//iu, '');
  doi = doi.replace(/^doi:\s*/iu, '');
  const match = doi.match(DOI_PATTERN);
  if (!match) return '';
  return match[0].replace(/[.,;:]+$/u, '');
}

export function doiResolverUrl(value: string): string {
  const doi = normalizeDoi(value);
  // Encode each path segment so suffix characters such as ?/# survive the
  // resolver path without changing the prefix/suffix separator.
  return `https://doi.org/${doi.split('/').map((segment) => encodeURIComponent(segment)).join('/')}`;
}

const NAMED_ENTITIES: Record<string, string> = {
  amp: '&',
  apos: "'",
  copy: '©',
  hellip: '…',
  ldquo: '“',
  lsquo: '‘',
  mdash: '—',
  middot: '·',
  nbsp: ' ',
  ndash: '–',
  rdquo: '”',
  reg: '®',
  rsquo: '’',
  times: '×',
  trade: '™',
};

function entityCodePoint(value: number): string {
  if (!Number.isFinite(value) || value < 0 || value > 0x10ffff) return '';
  try {
    return String.fromCodePoint(value);
  } catch {
    return '';
  }
}

// Crossref's formatted-citation response is an HTML fragment (italics,
// superscripts, entities). The dialog shows plain text, so strip markup and
// decode entities without rendering third-party HTML.
export function htmlToText(value: string): string {
  return value
    .replace(/<br\s*\/?>/giu, '\n')
    .replace(/<\/(?:p|div|li|h[1-6])>/giu, '\n')
    .replace(/<[^>]*>/gu, '')
    .replace(/&#x([0-9a-f]+);/giu, (_match, hex: string) => entityCodePoint(parseInt(hex, 16)))
    .replace(/&#(\d+);/gu, (_match, decimal: string) => entityCodePoint(parseInt(decimal, 10)))
    .replace(/&([a-z][a-z0-9]+);/giu, (match, name: string) => NAMED_ENTITIES[name.toLowerCase()] ?? match)
    .replace(/\u00a0/gu, ' ')
    .replace(/[ \t]+\n/gu, '\n')
    .replace(/\n{3,}/gu, '\n\n')
    .trim();
}

export function prepareCitationBody(body: string, format: DoiCitationFormat): string {
  const trimmed = body.trim();
  if (format.kind === 'style') return htmlToText(trimmed);
  if (format.id === 'csl-json') {
    try {
      return JSON.stringify(JSON.parse(trimmed), null, 2);
    } catch {
      return trimmed;
    }
  }
  return trimmed;
}

export function extractDoiProviderError(body: string): string {
  const trimmed = body.trim();
  if (!trimmed || trimmed.startsWith('<')) return '';
  try {
    const parsed = JSON.parse(trimmed) as { error?: unknown; message?: unknown };
    const message = typeof parsed.message === 'string'
      ? parsed.message
      : (typeof parsed.error === 'string' ? parsed.error : '');
    return htmlToText(message).slice(0, 200);
  } catch {
    if (trimmed.length <= 200 && !trimmed.includes('\n')) return trimmed;
    return '';
  }
}

export function doiCitationErrorMessage(status: number, body: string): string {
  const detail = extractDoiProviderError(body);
  if (status === 404) return 'doi.org 未找到该 DOI。';
  if (status === 406) {
    return detail ? `doi.org 不支持该引文格式：${detail}` : 'doi.org 不支持该引文格式。';
  }
  if (status === 429) return 'doi.org 请求过于频繁，请稍后再试。';
  if (status >= 500) return `doi.org 暂时不可用（HTTP ${status}），请稍后重试。`;
  return `doi.org 返回 HTTP ${status}${detail ? `：${detail}` : '。'}`;
}

export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === 'AbortError';
}

export function describeDoiCitationError(error: unknown): string {
  if (isAbortError(error)) return '';
  if (error instanceof Error && error.message) return error.message;
  return '获取引文格式失败。';
}

export function citationDownloadFilename(doi: string, format: DoiCitationFormat): string {
  const normalized = normalizeDoi(doi);
  const stem = normalized.replace(/[^a-z0-9._-]+/giu, '-').replace(/^-+|-+$/gu, '').slice(0, 80);
  return stem ? `citation-${stem}.${format.extension}` : `citation.${format.extension}`;
}

// A successful lookup is deterministic for a given DOI/format pair. Keep a
// small session cache so switching back and forth in the dialog does not
// enqueue repeated requests against doi.org's public rate limit.
const citationCache = new Map<string, string>();
const MAX_CACHE_ENTRIES = 100;

export async function fetchDoiCitation(
  doi: string,
  format: DoiCitationFormat,
  signal?: AbortSignal,
): Promise<string> {
  const normalized = normalizeDoi(doi);
  if (!normalized) throw new Error('该引用没有可用的 DOI。');
  const cacheKey = `${format.id}:${normalized.toLowerCase()}`;
  const cached = citationCache.get(cacheKey);
  if (cached !== undefined) return cached;

  let response: Response;
  try {
    response = await fetch(doiResolverUrl(normalized), {
      headers: { Accept: format.accept },
      signal,
    });
  } catch (error) {
    if (isAbortError(error)) throw error;
    throw new Error('无法访问 doi.org，请检查网络连接。');
  }

  const body = await response.text();
  if (!response.ok) throw new Error(doiCitationErrorMessage(response.status, body));
  const contentType = (response.headers.get('content-type') ?? '').toLowerCase();
  if (contentType.includes('text/html')) {
    throw new Error('doi.org 未返回引文数据；该 DOI 的注册机构可能不支持此格式。');
  }

  const prepared = prepareCitationBody(body, format);
  if (!prepared) throw new Error('doi.org 返回了空的引文内容。');
  if (citationCache.size >= MAX_CACHE_ENTRIES) {
    const oldest = citationCache.keys().next().value;
    if (oldest !== undefined) citationCache.delete(oldest);
  }
  citationCache.set(cacheKey, prepared);
  return prepared;
}
