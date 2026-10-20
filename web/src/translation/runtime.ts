import type {
  ConcreteTranslationLanguage,
  TranslationLanguage,
  TranslationProviderConfig,
  TranslationProviderID,
} from './types.ts';

export class TranslationError extends Error {
  readonly provider: TranslationProviderID;
  readonly status?: number;
  readonly retryable: boolean;

  constructor(message: string, options: { provider: TranslationProviderID; status?: number; retryable?: boolean }) {
    super(message);
    this.name = 'TranslationError';
    this.provider = options.provider;
    this.status = options.status;
    this.retryable = options.retryable ?? false;
  }
}

export function isAbortError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'name' in error
    && (error as { name?: unknown }).name === 'AbortError';
}

export function abortedError(): Error {
  const error = new Error('翻译已取消。');
  error.name = 'AbortError';
  return error;
}

export function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

export function describeTranslationError(error: unknown): string {
  if (isAbortError(error)) return '';
  if (error instanceof Error && error.message.trim()) return error.message;
  return '翻译失败，请稍后重试。';
}

export function serializeProviderConfig(config: TranslationProviderConfig = {}): string {
  return Object.keys(config).sort().map((key) => `${key}=${config[key]}`).join('\u0001');
}

// API providers share one header path: their declarative configuration can
// supply a JSON header map plus optional apiKey/apiKeyHeader/apiKeyPrefix
// fields, and every request merges that over the provider defaults.
export function providerHttpHeaders(
  providerID: TranslationProviderID,
  config: TranslationProviderConfig = {},
  defaults: Record<string, string> = {},
): Record<string, string> {
  const headers: Record<string, string> = { ...defaults };
  const rawHeaders = config.headers?.trim();
  if (rawHeaders) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(rawHeaders);
    } catch {
      throw new TranslationError('请求头配置必须是合法的 JSON 对象。', {
        provider: providerID,
        retryable: false,
      });
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      throw new TranslationError('请求头配置必须是合法的 JSON 对象。', {
        provider: providerID,
        retryable: false,
      });
    }
    for (const [key, value] of Object.entries(parsed)) {
      if (key.trim()) headers[key] = String(value);
    }
  }
  const apiKey = config.apiKey?.trim();
  if (apiKey) {
    const headerName = config.apiKeyHeader?.trim() || 'Authorization';
    const prefix = config.apiKeyPrefix?.trim();
    headers[headerName] = prefix ? `${prefix} ${apiKey}` : apiKey;
  }
  return headers;
}

// ---------------------------------------------------------------------------
// Language detection and text preparation

function countMatches(text: string, pattern: RegExp): number {
  return text.match(pattern)?.length ?? 0;
}

export function detectSourceLanguage(text: string): ConcreteTranslationLanguage {
  const cjk = countMatches(text, /[㐀-鿿]/gu);
  const kana = countMatches(text, /[぀-ヿ]/gu);
  const hangul = countMatches(text, /[가-힯]/gu);
  const cyrillic = countMatches(text, /[Ѐ-ӿ]/gu);
  const latin = countMatches(text, /[A-Za-z]/gu);
  if (kana > 0 && kana + cjk >= latin) return 'ja';
  if (hangul > 0 && hangul >= cjk) return 'ko';
  if (cyrillic > 0 && cyrillic >= latin) return 'ru';
  if (cjk > 0 && cjk >= latin) return 'zh-Hans';
  return 'en';
}

export function isChineseLanguage(language: TranslationLanguage): boolean {
  return language === 'zh-Hans' || language === 'zh-Hant';
}

export function resolveTargetLanguage(
  text: string,
  requested: TranslationLanguage,
  sourceLanguage: TranslationLanguage = 'auto',
): ConcreteTranslationLanguage {
  if (requested !== 'auto') return requested;
  const source = sourceLanguage === 'auto' ? detectSourceLanguage(text) : sourceLanguage;
  return isChineseLanguage(source) ? 'en' : 'zh-Hans';
}

// Split long input at sentence-like boundaries so a provider never receives an
// unbounded request. Chunks are translated independently and re-joined by the
// provider.
export function splitTextByCodePoints(text: string, maxCodePoints: number): string[] {
  const normalized = text.trim();
  if (!normalized) return [];
  const limit = Math.max(1, Math.floor(maxCodePoints));
  const characters = Array.from(normalized);
  if (characters.length <= limit) return [normalized];

  const chunks: string[] = [];
  let start = 0;
  while (start < characters.length) {
    let end = Math.min(characters.length, start + limit);
    if (end < characters.length) {
      const minimumBoundary = start + Math.floor(limit * 0.55);
      for (let index = end; index > minimumBoundary; index -= 1) {
        if (/[。！？.!?;；\n]/u.test(characters[index - 1])) {
          end = index;
          break;
        }
      }
    }
    const chunk = characters.slice(start, end).join('').trim();
    if (chunk) chunks.push(chunk);
    start = end;
    while (start < characters.length && /\s/u.test(characters[start])) start += 1;
  }
  return chunks;
}

export function splitTextByUtf8Bytes(text: string, maxBytes: number): string[] {
  const normalized = text.trim();
  if (!normalized) return [];
  const limit = Math.max(1, Math.floor(maxBytes));
  const characters = Array.from(normalized);
  const encoder = new TextEncoder();
  const chunks: string[] = [];
  let start = 0;
  while (start < characters.length) {
    let bytes = 0;
    let end = start;
    while (end < characters.length) {
      const size = encoder.encode(characters[end]).length;
      if (bytes + size > limit && end > start) break;
      bytes += size;
      end += 1;
    }
    if (end < characters.length) {
      const minimumBoundary = start + Math.max(1, Math.floor((end - start) * 0.55));
      for (let index = end; index > minimumBoundary; index -= 1) {
        if (/[\s。！？.!?;；,，]/u.test(characters[index - 1])) {
          end = index;
          break;
        }
      }
    }
    const chunk = characters.slice(start, end).join('').trim();
    if (chunk) chunks.push(chunk);
    start = end;
    while (start < characters.length && /\s/u.test(characters[start])) start += 1;
  }
  return chunks;
}

// The Microsoft endpoint runs an HTML tag aligner over plain text. Full-width
// comparisons and ampersands survive translation without being interpreted as
// markup; they are restored after the response arrives.
export function protectTranslationSymbols(text: string): string {
  return text
    .replaceAll('<=', '≤')
    .replaceAll('>=', '≥')
    .replaceAll('!=', '≠')
    .replaceAll('<', '＜')
    .replaceAll('>', '＞')
    .replaceAll('&', '＆');
}

export function restoreTranslationSymbols(text: string): string {
  return text
    .replaceAll('≤', '<=')
    .replaceAll('≥', '>=')
    .replaceAll('≠', '!=')
    .replaceAll('＜', '<')
    .replaceAll('＞', '>')
    .replaceAll('＆', '&');
}

const CJK_CHARACTER = /[㐀-鿿぀-ヿ가-힯]/u;
const LIST_ITEM_START = /^(?:[•·▪◦‣*\-–—]\s+|\d{1,3}[.)]\s+|[（(]\d{1,3}[)）]\s+|[①-⑳]\s*)/u;

function isCJKCharacter(value: string | undefined): boolean {
  return Boolean(value && CJK_CHARACTER.test(value));
}

function joinWrappedLines(paragraph: string): string {
  const lines = paragraph.split('\n').map((line) => line.trim()).filter(Boolean);
  let result = '';
  for (const line of lines) {
    if (!result) {
      result = line;
      continue;
    }
    // Keep real list items on separate lines; only visual line wraps are
    // rejoined inside the paragraph.
    if (LIST_ITEM_START.test(line)) {
      result += `\n${line}`;
      continue;
    }
    // Join a word broken across lines, e.g. "trans-\nlation".
    if (/[-‐‑]$/u.test(result) && /^[A-Za-z]/u.test(line)) {
      result = `${result.slice(0, -1)}${line}`;
      continue;
    }
    const previous = Array.from(result).at(-1);
    const next = Array.from(line)[0];
    // CJK text has no inter-word spaces; Latin text needs one when a visual
    // line break is removed.
    result += isCJKCharacter(previous) || isCJKCharacter(next) ? line : ` ${line}`;
  }
  return result.replace(/[ \t]+/g, ' ').trim();
}

// PDF text-layer selections contain one newline per visual line. Join those
// soft wraps while preserving genuine paragraph breaks (blank lines), list
// items, and CJK/Latin spacing rules. Manual input is never normalized.
export function normalizeTextLineBreaks(text: string): string {
  const normalized = text
    .replace(/\r\n?/g, '\n')
    .replace(/\u00a0/g, ' ')
    .replace(/[\u200b\ufeff]/gu, '');
  return normalized
    .split(/\n[ \t]*\n+/)
    .map(joinWrappedLines)
    .filter(Boolean)
    .join('\n\n')
    .trim();
}

// Parsed Blocks are Markdown fragments with occasional inline HTML (<sup>,
// <br>). The web translation endpoints expect plain text; strip formatting
// without turning ordinary comparisons like "a < b" into a tag match.
export function stripMarkupForTranslation(text: string): string {
  return text
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/<\/(?:p|div|li|h[1-6])>/gi, '\n')
    .replace(/<[A-Za-z/][^>]*>/g, '')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/__([^_]+)__/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/&nbsp;/gi, ' ')
    .replace(/&amp;/gi, '&')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&quot;/gi, '"')
    .replace(/&#39;/g, "'")
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

export class ProviderGate {
  private tail: Promise<unknown> = Promise.resolve();
  private lastFinished = 0;
  private readonly minimumIntervalMs: number;

  constructor(minimumIntervalMs: number) {
    this.minimumIntervalMs = minimumIntervalMs;
  }

  run<T>(task: () => Promise<T>): Promise<T> {
    const result = this.tail.then(async () => {
      const wait = this.minimumIntervalMs - (Date.now() - this.lastFinished);
      if (wait > 0) await delay(wait);
      try {
        return await task();
      } finally {
        this.lastFinished = Date.now();
      }
    });
    this.tail = result.then(() => undefined, () => undefined);
    return result;
  }
}

