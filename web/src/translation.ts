// Frontend-only translation client.
//
// The reader sends only user-selected text. Microsoft's Edge web endpoint is
// the primary keyless route; Chrome's on-device Translator API is an optional
// offline route; MyMemory is the final fallback. No server proxy or persisted
// paper content is involved.

export const CONCRETE_TRANSLATION_LANGUAGES = [
  'zh-Hans',
  'zh-Hant',
  'en',
  'ja',
  'ko',
  'de',
  'fr',
  'es',
  'ru',
] as const;

export type ConcreteTranslationLanguage = typeof CONCRETE_TRANSLATION_LANGUAGES[number];
export type TranslationLanguage = 'auto' | ConcreteTranslationLanguage;
export type TranslationProviderID = 'microsoft' | 'chrome' | 'mymemory';
export type TranslationProviderSetting = 'auto' | TranslationProviderID;

export type TranslationSettings = {
  provider: TranslationProviderSetting;
  sourceLanguage: TranslationLanguage;
  targetLanguage: TranslationLanguage;
};

export type SelectedPDFBlock = {
  blockId: string;
  blockType: string;
  text: string;
  pageNumber?: number;
};

export type TranslationRequestSeed =
  | { kind: 'text'; text: string; pageNumber?: number }
  | { kind: 'blocks'; blocks: SelectedPDFBlock[] };

export type TranslationProviderResult = {
  text: string;
  detectedLanguage?: ConcreteTranslationLanguage;
};

// Provider contract: translated text + source language + target language in,
// translation result out. Batches, caching, fallback and rate limiting belong
// to the orchestrator, not individual providers.
export interface TranslationProvider {
  readonly id: TranslationProviderID;
  readonly label: string;
  translate(
    text: string,
    sourceLanguage: TranslationLanguage,
    targetLanguage: TranslationLanguage,
    signal?: AbortSignal,
  ): Promise<TranslationProviderResult>;
}

export type TranslateTextOptions = {
  sourceLanguage?: TranslationLanguage;
  targetLanguage?: TranslationLanguage;
  provider?: TranslationProviderSetting;
  signal?: AbortSignal;
};

export type TranslationResult = TranslationProviderResult & {
  provider?: TranslationProviderID;
  fallback?: boolean;
  skipped?: boolean;
  targetLanguage?: ConcreteTranslationLanguage;
};

export const TRANSLATION_PROVIDER_LABELS: Record<TranslationProviderID, string> = {
  microsoft: 'Microsoft 在线',
  chrome: 'Chrome 本地',
  mymemory: 'MyMemory 备用',
};

export const TRANSLATION_PROVIDER_OPTIONS: ReadonlyArray<{ id: TranslationProviderSetting; label: string }> = [
  { id: 'auto', label: '自动（推荐）' },
  { id: 'microsoft', label: TRANSLATION_PROVIDER_LABELS.microsoft },
  { id: 'chrome', label: TRANSLATION_PROVIDER_LABELS.chrome },
  { id: 'mymemory', label: TRANSLATION_PROVIDER_LABELS.mymemory },
];

export const TRANSLATION_LANGUAGE_OPTIONS: ReadonlyArray<{ id: TranslationLanguage; label: string }> = [
  { id: 'zh-Hans', label: '简体中文' },
  { id: 'zh-Hant', label: '繁體中文' },
  { id: 'en', label: 'English' },
  { id: 'ja', label: '日本語' },
  { id: 'ko', label: '한국어' },
  { id: 'de', label: 'Deutsch' },
  { id: 'fr', label: 'Français' },
  { id: 'es', label: 'Español' },
  { id: 'ru', label: 'Русский' },
];

export const TRANSLATION_SOURCE_OPTIONS: ReadonlyArray<{ id: TranslationLanguage; label: string }> = [
  { id: 'auto', label: '自动检测' },
  ...TRANSLATION_LANGUAGE_OPTIONS,
];

export const TRANSLATION_TARGET_OPTIONS: ReadonlyArray<{ id: TranslationLanguage; label: string }> = [
  { id: 'auto', label: '自动' },
  ...TRANSLATION_LANGUAGE_OPTIONS,
];

export const DEFAULT_TRANSLATION_SETTINGS: TranslationSettings = {
  provider: 'auto',
  sourceLanguage: 'auto',
  targetLanguage: 'auto',
};

export const TRANSLATION_SETTINGS_STORAGE = 'papersManager.translation.settings.v1';
export const MAX_TRANSLATION_CACHE_ENTRIES = 300;
export const MAX_TRANSLATION_CACHE_BYTES = 2 * 1024 * 1024;
export const TRANSLATION_CACHE_TTL_MS = 24 * 60 * 60 * 1000;

const MICROSOFT_TRANSLATE_URL = 'https://edge.microsoft.com/translate/translatetext';
const MICROSOFT_MAX_CODEPOINTS = 4000;
const MICROSOFT_CHUNK_DELAY_MS = 100;
const MYMEMORY_TRANSLATE_URL = 'https://api.mymemory.translated.net/get';
const MYMEMORY_MAX_UTF8_BYTES = 450;
const MYMEMORY_CHUNK_DELAY_MS = 1000;
const CHROME_MAX_CODEPOINTS = 4000;

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

function abortedError(): Error {
  const error = new Error('翻译已取消。');
  error.name = 'AbortError';
  return error;
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

export function describeTranslationError(error: unknown): string {
  if (isAbortError(error)) return '';
  if (error instanceof Error && error.message.trim()) return error.message;
  return '翻译失败，请稍后重试。';
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

function normalizeDetectedLanguage(value: unknown): ConcreteTranslationLanguage | undefined {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim().toLowerCase();
  if (!normalized) return undefined;
  if (normalized === 'zh' || normalized.startsWith('zh-hans') || normalized.startsWith('zh-cn')) return 'zh-Hans';
  if (normalized.startsWith('zh-hant') || normalized.startsWith('zh-tw') || normalized.startsWith('zh-hk')) return 'zh-Hant';
  const base = normalized.split('-')[0];
  return (CONCRETE_TRANSLATION_LANGUAGES as readonly string[]).includes(base)
    ? base as ConcreteTranslationLanguage
    : undefined;
}

function microsoftLanguage(language: TranslationLanguage): string {
  if (language === 'auto') return '';
  return language;
}

function chromeLanguage(language: TranslationLanguage): string {
  if (language === 'zh-Hans') return 'zh';
  return language;
}

function myMemoryLanguage(language: TranslationLanguage): string {
  if (language === 'zh-Hans') return 'zh-CN';
  if (language === 'zh-Hant') return 'zh-TW';
  return language;
}

// ---------------------------------------------------------------------------
// Microsoft Edge web endpoint

class ProviderGate {
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

function microsoftTranslationFrom(value: unknown): { text: string; detectedLanguage?: ConcreteTranslationLanguage } | undefined {
  if (!Array.isArray(value) || value.length === 0) return undefined;
  const item = value[0] as {
    translations?: unknown;
    detectedLanguage?: { language?: unknown };
  } | undefined;
  if (!item || !Array.isArray(item.translations) || item.translations.length === 0) return undefined;
  const translation = item.translations[0] as { text?: unknown };
  if (typeof translation.text !== 'string' || !translation.text.trim()) return undefined;
  return {
    text: translation.text,
    detectedLanguage: normalizeDetectedLanguage(item.detectedLanguage?.language),
  };
}

async function translateMicrosoftChunk(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal?: AbortSignal,
): Promise<TranslationProviderResult> {
  if (signal?.aborted) throw abortedError();
  const params = new URLSearchParams();
  params.set('from', sourceLanguage === 'auto' ? '' : microsoftLanguage(sourceLanguage));
  params.set('to', microsoftLanguage(targetLanguage));
  params.set('isEnterpriseClient', 'false');

  let response: Response;
  try {
    response = await fetch(`${MICROSOFT_TRANSLATE_URL}?${params.toString()}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify([protectTranslationSymbols(text)]),
      signal,
      credentials: 'omit',
      cache: 'no-store',
    });
  } catch (error) {
    if (isAbortError(error)) throw error;
    throw new TranslationError('无法连接 Microsoft 翻译服务，请检查网络。', {
      provider: 'microsoft',
      retryable: true,
    });
  }

  if (!response.ok) {
    if (response.status === 429) {
      throw new TranslationError('Microsoft 翻译请求过于频繁，请稍后重试。', {
        provider: 'microsoft',
        status: 429,
        retryable: true,
      });
    }
    if (response.status >= 500) {
      throw new TranslationError(`Microsoft 翻译服务暂时不可用（HTTP ${response.status}）。`, {
        provider: 'microsoft',
        status: response.status,
        retryable: true,
      });
    }
    throw new TranslationError(`Microsoft 翻译请求被拒绝（HTTP ${response.status}）。`, {
      provider: 'microsoft',
      status: response.status,
      retryable: false,
    });
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new TranslationError('Microsoft 翻译返回了无法解析的内容。', {
      provider: 'microsoft',
      retryable: true,
    });
  }
  const parsed = microsoftTranslationFrom(payload);
  if (!parsed) {
    throw new TranslationError('Microsoft 翻译返回了空的翻译结果。', {
      provider: 'microsoft',
      retryable: true,
    });
  }
  return { text: restoreTranslationSymbols(parsed.text), detectedLanguage: parsed.detectedLanguage };
}

async function translateMicrosoft(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal?: AbortSignal,
): Promise<TranslationProviderResult> {
  const normalized = text.trim();
  if (!normalized) return { text: '' };
  const target = resolveTargetLanguage(normalized, targetLanguage, sourceLanguage);
  if (sourceLanguage !== 'auto' && sourceLanguage === target) {
    return { text: normalized, detectedLanguage: sourceLanguage };
  }
  const chunks = splitTextByCodePoints(normalized, MICROSOFT_MAX_CODEPOINTS);
  const translations: string[] = [];
  let detectedLanguage: ConcreteTranslationLanguage | undefined;
  for (const [index, chunk] of chunks.entries()) {
    if (signal?.aborted) throw abortedError();
    if (index > 0) await delay(MICROSOFT_CHUNK_DELAY_MS);
    const result = await translateMicrosoftChunk(chunk, sourceLanguage, target, signal);
    translations.push(result.text);
    detectedLanguage ??= result.detectedLanguage;
  }
  return { text: translations.join('\n'), detectedLanguage };
}

const microsoftGate = new ProviderGate(100);

export const microsoftTranslationProvider: TranslationProvider = {
  id: 'microsoft',
  label: TRANSLATION_PROVIDER_LABELS.microsoft,
  translate: (text, sourceLanguage, targetLanguage, signal) => (
    microsoftGate.run(() => translateMicrosoft(text, sourceLanguage, targetLanguage, signal))
  ),
};

// ---------------------------------------------------------------------------
// MyMemory public API

function myMemoryTranslationFrom(value: unknown): { text: string; status: number; quotaFinished: boolean } | undefined {
  if (typeof value !== 'object' || value === null) return undefined;
  const record = value as {
    responseData?: { translatedText?: unknown };
    responseStatus?: unknown;
    quotaFinished?: unknown;
  };
  const text = record.responseData?.translatedText;
  if (typeof text !== 'string') return undefined;
  return {
    text,
    status: Number(record.responseStatus),
    quotaFinished: record.quotaFinished === true,
  };
}

async function translateMyMemoryChunk(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal?: AbortSignal,
): Promise<TranslationProviderResult> {
  if (signal?.aborted) throw abortedError();
  const params = new URLSearchParams({
    q: text,
    langpair: `${myMemoryLanguage(sourceLanguage)}|${myMemoryLanguage(targetLanguage)}`,
  });
  let response: Response;
  try {
    response = await fetch(`${MYMEMORY_TRANSLATE_URL}?${params.toString()}`, {
      signal,
      credentials: 'omit',
      cache: 'no-store',
    });
  } catch (error) {
    if (isAbortError(error)) throw error;
    throw new TranslationError('无法连接 MyMemory 翻译服务，请检查网络。', {
      provider: 'mymemory',
      retryable: true,
    });
  }
  if (!response.ok) {
    throw new TranslationError(`MyMemory 翻译请求失败（HTTP ${response.status}）。`, {
      provider: 'mymemory',
      status: response.status,
      retryable: response.status === 429 || response.status >= 500,
    });
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new TranslationError('MyMemory 翻译返回了无法解析的内容。', {
      provider: 'mymemory',
      retryable: true,
    });
  }
  const parsed = myMemoryTranslationFrom(payload);
  if (!parsed) {
    throw new TranslationError('MyMemory 翻译返回了空的翻译结果。', {
      provider: 'mymemory',
      retryable: true,
    });
  }
  if (parsed.quotaFinished) {
    throw new TranslationError('MyMemory 今日免费额度已用完，请切换翻译服务。', {
      provider: 'mymemory',
      retryable: false,
    });
  }
  if (parsed.status !== 200 || !parsed.text.trim()) {
    throw new TranslationError(
      parsed.text.includes('QUERY LENGTH LIMIT EXCEEDED')
        ? 'MyMemory 单次请求超长，请缩短选中文本。'
        : 'MyMemory 没有返回可用的翻译结果。',
      { provider: 'mymemory', retryable: false },
    );
  }
  return { text: parsed.text };
}

async function translateMyMemory(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal?: AbortSignal,
): Promise<TranslationProviderResult> {
  const normalized = text.trim();
  if (!normalized) return { text: '' };
  const source = sourceLanguage === 'auto' ? detectSourceLanguage(normalized) : sourceLanguage;
  const target = resolveTargetLanguage(normalized, targetLanguage, source);
  if (source === target) return { text: normalized, detectedLanguage: source };
  const chunks = splitTextByUtf8Bytes(normalized, MYMEMORY_MAX_UTF8_BYTES);
  const translations: string[] = [];
  for (const [index, chunk] of chunks.entries()) {
    if (signal?.aborted) throw abortedError();
    if (index > 0) await delay(MYMEMORY_CHUNK_DELAY_MS);
    const result = await translateMyMemoryChunk(chunk, source, target, signal);
    translations.push(result.text);
  }
  return { text: translations.join('\n'), detectedLanguage: source };
}

const myMemoryGate = new ProviderGate(1000);

export const mymemoryTranslationProvider: TranslationProvider = {
  id: 'mymemory',
  label: TRANSLATION_PROVIDER_LABELS.mymemory,
  translate: (text, sourceLanguage, targetLanguage, signal) => (
    myMemoryGate.run(() => translateMyMemory(text, sourceLanguage, targetLanguage, signal))
  ),
};

// ---------------------------------------------------------------------------
// Chrome built-in Translator API

export type ChromeTranslationAvailability =
  | 'available'
  | 'downloadable'
  | 'downloading'
  | 'unavailable'
  | 'unsupported';

type ChromeTranslator = {
  translate(text: string): Promise<string>;
};

type ChromeTranslatorApi = {
  availability(options: { sourceLanguage: string; targetLanguage: string }): Promise<string>;
  create(options: {
    sourceLanguage: string;
    targetLanguage: string;
    monitor?: (monitor: EventTarget) => void;
  }): Promise<ChromeTranslator>;
};

function chromeTranslatorApi(): ChromeTranslatorApi | undefined {
  return (globalThis as unknown as { Translator?: ChromeTranslatorApi }).Translator;
}

const chromeTranslatorCache = new Map<string, Promise<ChromeTranslator>>();

export async function chromeTranslationAvailability(
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
): Promise<ChromeTranslationAvailability> {
  const api = chromeTranslatorApi();
  if (!api) return 'unsupported';
  const source = sourceLanguage === 'auto' ? 'en' : sourceLanguage;
  const target = targetLanguage === 'auto' ? 'zh-Hans' : targetLanguage;
  try {
    const availability = await api.availability({
      sourceLanguage: chromeLanguage(source),
      targetLanguage: chromeLanguage(target),
    });
    if (availability === 'available' || availability === 'downloadable'
      || availability === 'downloading' || availability === 'unavailable') {
      return availability;
    }
    return 'unsupported';
  } catch {
    return 'unsupported';
  }
}

function getChromeTranslator(
  sourceLanguage: ConcreteTranslationLanguage,
  targetLanguage: ConcreteTranslationLanguage,
  onProgress?: (loaded: number) => void,
): Promise<ChromeTranslator> {
  const api = chromeTranslatorApi();
  if (!api) {
    throw new TranslationError('当前浏览器不支持 Chrome 本地翻译。', {
      provider: 'chrome',
      retryable: false,
    });
  }
  const key = `${chromeLanguage(sourceLanguage)}|${chromeLanguage(targetLanguage)}`;
  const existing = chromeTranslatorCache.get(key);
  if (existing) return existing;
  const created = api.create({
    sourceLanguage: chromeLanguage(sourceLanguage),
    targetLanguage: chromeLanguage(targetLanguage),
    monitor: (monitor) => {
      monitor.addEventListener('downloadprogress', (event) => {
        const loaded = (event as ProgressEvent).loaded;
        if (typeof loaded === 'number') onProgress?.(loaded);
      });
    },
  }).catch((error) => {
    chromeTranslatorCache.delete(key);
    throw error;
  });
  chromeTranslatorCache.set(key, created);
  return created;
}

export async function prepareChromeTranslation(
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  onProgress?: (loaded: number) => void,
): Promise<void> {
  const source = sourceLanguage === 'auto' ? 'en' : sourceLanguage;
  const target = targetLanguage === 'auto' ? 'zh-Hans' : targetLanguage;
  await getChromeTranslator(source, target, onProgress);
}

async function translateChrome(
  text: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  signal?: AbortSignal,
): Promise<TranslationProviderResult> {
  const normalized = text.trim();
  if (!normalized) return { text: '' };
  const source = sourceLanguage === 'auto' ? detectSourceLanguage(normalized) : sourceLanguage;
  const target = resolveTargetLanguage(normalized, targetLanguage, source);
  if (source === target) return { text: normalized, detectedLanguage: source };
  try {
    const translator = await getChromeTranslator(source, target);
    const chunks = splitTextByCodePoints(normalized, CHROME_MAX_CODEPOINTS);
    const translations: string[] = [];
    for (const chunk of chunks) {
      if (signal?.aborted) throw abortedError();
      translations.push(await translator.translate(chunk));
    }
    return { text: translations.join('\n'), detectedLanguage: source };
  } catch (error) {
    if (error instanceof TranslationError) throw error;
    if (isAbortError(error)) throw error;
    if (typeof error === 'object' && error !== null && 'name' in error
      && (error as { name?: unknown }).name === 'NotAllowedError') {
      throw new TranslationError('Chrome 本地模型尚未下载，请点击“下载并启用本地翻译”。', {
        provider: 'chrome',
        retryable: false,
      });
    }
    throw new TranslationError(
      `Chrome 本地翻译失败：${error instanceof Error ? error.message : '未知错误'}`,
      { provider: 'chrome', retryable: true },
    );
  }
}

export const chromeTranslationProvider: TranslationProvider = {
  id: 'chrome',
  label: TRANSLATION_PROVIDER_LABELS.chrome,
  translate: (text, sourceLanguage, targetLanguage, signal) => (
    translateChrome(text, sourceLanguage, targetLanguage, signal)
  ),
};

export const TRANSLATION_PROVIDERS: Record<TranslationProviderID, TranslationProvider> = {
  microsoft: microsoftTranslationProvider,
  chrome: chromeTranslationProvider,
  mymemory: mymemoryTranslationProvider,
};

// ---------------------------------------------------------------------------
// Cache

type TranslationCacheEntry = {
  value: TranslationProviderResult;
  bytes: number;
  expiresAt: number;
};

function utf8Size(value: string): number {
  return new TextEncoder().encode(value).length;
}

export class TranslationCache {
  private readonly entries = new Map<string, TranslationCacheEntry>();
  private readonly options: {
    maxEntries?: number;
    maxBytes?: number;
    ttlMs?: number;
  };

  constructor(options: {
    maxEntries?: number;
    maxBytes?: number;
    ttlMs?: number;
  } = {}) {
    this.options = options;
  }

  get(key: string): TranslationProviderResult | undefined {
    const entry = this.entries.get(key);
    if (!entry) return undefined;
    if (entry.expiresAt <= Date.now()) {
      this.entries.delete(key);
      return undefined;
    }
    // Refresh insertion order: this is an LRU cache, not FIFO.
    this.entries.delete(key);
    this.entries.set(key, entry);
    return entry.value;
  }

  set(key: string, value: TranslationProviderResult): void {
    const bytes = utf8Size(`${key}\u0000${value.text}`);
    const maxBytes = this.options.maxBytes ?? MAX_TRANSLATION_CACHE_BYTES;
    if (bytes > maxBytes) return;
    this.entries.delete(key);
    this.entries.set(key, {
      value,
      bytes,
      expiresAt: Date.now() + (this.options.ttlMs ?? TRANSLATION_CACHE_TTL_MS),
    });
    this.evict();
  }

  clear(): void {
    this.entries.clear();
  }

  get size(): number {
    return this.entries.size;
  }

  private evict(): void {
    const now = Date.now();
    for (const [key, entry] of this.entries) {
      if (entry.expiresAt <= now) this.entries.delete(key);
    }
    const maxEntries = this.options.maxEntries ?? MAX_TRANSLATION_CACHE_ENTRIES;
    const maxBytes = this.options.maxBytes ?? MAX_TRANSLATION_CACHE_BYTES;
    let totalBytes = 0;
    for (const entry of this.entries.values()) totalBytes += entry.bytes;
    while (this.entries.size > maxEntries || totalBytes > maxBytes) {
      const oldestKey = this.entries.keys().next().value;
      if (oldestKey === undefined) break;
      const oldest = this.entries.get(oldestKey);
      this.entries.delete(oldestKey);
      totalBytes -= oldest?.bytes ?? 0;
    }
  }
}

const translationCache = new TranslationCache();

export function clearTranslationCache(): void {
  translationCache.clear();
}

function translationCacheKey(
  provider: TranslationProviderID,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  text: string,
): string {
  return `${provider}\u0000${sourceLanguage}\u0000${targetLanguage}\u0000${text}`;
}

// ---------------------------------------------------------------------------
// Orchestration

async function autoProviderOrder(
  sourceLanguage: ConcreteTranslationLanguage,
  targetLanguage: ConcreteTranslationLanguage,
): Promise<TranslationProviderID[]> {
  const order: TranslationProviderID[] = [];
  if (chromeTranslatorApi()) {
    const availability = await chromeTranslationAvailability(sourceLanguage, targetLanguage);
    // Never start a model download implicitly in automatic mode.
    if (availability === 'available') order.push('chrome');
  }
  order.push('microsoft', 'mymemory');
  return order;
}

export async function translateText(text: string, options: TranslateTextOptions = {}): Promise<TranslationResult> {
  const normalized = text.trim();
  if (!normalized) return { text: '', skipped: true };
  const sourceSetting = options.sourceLanguage ?? 'auto';
  const targetRequested = options.targetLanguage ?? 'auto';
  const providerSetting = options.provider ?? 'auto';
  const detectedLanguage = sourceSetting === 'auto' ? detectSourceLanguage(normalized) : sourceSetting;
  const targetLanguage = resolveTargetLanguage(normalized, targetRequested, sourceSetting);
  if (targetRequested !== 'auto' && detectedLanguage === targetLanguage) {
    return { text: normalized, skipped: true, detectedLanguage, targetLanguage };
  }

  const order = providerSetting === 'auto'
    ? await autoProviderOrder(detectedLanguage, targetLanguage)
    : [providerSetting];
  let lastError: unknown;

  for (const [index, providerID] of order.entries()) {
    if (options.signal?.aborted) throw abortedError();
    const provider = TRANSLATION_PROVIDERS[providerID];
    const cacheSource = sourceSetting === 'auto' ? detectedLanguage : sourceSetting;
    const key = translationCacheKey(providerID, cacheSource, targetLanguage, normalized);
    const cached = translationCache.get(key);
    if (cached) {
      return { ...cached, provider: providerID, fallback: index > 0, targetLanguage };
    }
    try {
      const result = await provider.translate(normalized, sourceSetting, targetLanguage, options.signal);
      if (!result.text.trim()) {
        throw new TranslationError('翻译服务返回了空内容。', {
          provider: providerID,
          retryable: true,
        });
      }
      translationCache.set(key, result);
      return { ...result, provider: providerID, fallback: index > 0, targetLanguage };
    } catch (error) {
      if (isAbortError(error)) throw error;
      lastError = error;
      if (providerSetting !== 'auto') throw error;
      // Automatic mode tries the next provider; the last error is surfaced
      // only after every provider has failed.
    }
  }

  if (lastError instanceof Error) throw lastError;
  throw new TranslationError('没有可用的翻译服务。', {
    provider: 'microsoft',
    retryable: false,
  });
}

// ---------------------------------------------------------------------------
// Device-local settings

function isProviderSetting(value: unknown): value is TranslationProviderSetting {
  return typeof value === 'string' && TRANSLATION_PROVIDER_OPTIONS.some((option) => option.id === value);
}

function isTranslationLanguage(value: unknown): value is TranslationLanguage {
  return typeof value === 'string'
    && (value === 'auto' || TRANSLATION_LANGUAGE_OPTIONS.some((option) => option.id === value));
}

export function loadTranslationSettings(): TranslationSettings {
  if (typeof window === 'undefined') return { ...DEFAULT_TRANSLATION_SETTINGS };
  try {
    const raw: unknown = JSON.parse(window.localStorage.getItem(TRANSLATION_SETTINGS_STORAGE) ?? 'null');
    if (typeof raw !== 'object' || raw === null) return { ...DEFAULT_TRANSLATION_SETTINGS };
    const record = raw as { provider?: unknown; sourceLanguage?: unknown; targetLanguage?: unknown };
    return {
      provider: isProviderSetting(record.provider) ? record.provider : DEFAULT_TRANSLATION_SETTINGS.provider,
      sourceLanguage: isTranslationLanguage(record.sourceLanguage)
        ? record.sourceLanguage
        : DEFAULT_TRANSLATION_SETTINGS.sourceLanguage,
      targetLanguage: isTranslationLanguage(record.targetLanguage)
        ? record.targetLanguage
        : DEFAULT_TRANSLATION_SETTINGS.targetLanguage,
    };
  } catch {
    return { ...DEFAULT_TRANSLATION_SETTINGS };
  }
}

export function saveTranslationSettings(settings: TranslationSettings): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(TRANSLATION_SETTINGS_STORAGE, JSON.stringify(settings));
  } catch {
    // A full or disabled storage must not break translation itself.
  }
}
