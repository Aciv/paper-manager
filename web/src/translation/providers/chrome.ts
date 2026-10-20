import type {
  ConcreteTranslationLanguage,
  TranslationLanguage,
  TranslationProvider,
  TranslationProviderConfig,
  TranslationProviderDescriptor,
  TranslationProviderResult,
} from '../types.ts';
import {
  TranslationError,
  abortedError,
  detectSourceLanguage,
  isAbortError,
  resolveTargetLanguage,
  splitTextByCodePoints,
} from '../runtime.ts';

const CHROME_MAX_CODEPOINTS = 4000;

function chromeLanguage(language: TranslationLanguage): string {
  if (language === 'zh-Hans') return 'zh';
  return language;
}

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
  label: 'Chrome 本地',
  translate: (text, sourceLanguage, targetLanguage, signal) => (
    translateChrome(text, sourceLanguage, targetLanguage, signal)
  ),
};


const descriptor: TranslationProviderDescriptor = {
  id: 'chrome',
  label: 'Chrome 本地',
  kind: 'script',
  note: '本地模型，不上传文本',
  hint: '使用 Chrome 浏览器内置的本地翻译模型，文本不会上传；首次使用需要下载模型。',
  configFields: [],
  autoPriority: 0,
  autoEligible: async ({ sourceLanguage, targetLanguage }) => (
    chromeTranslatorApi() !== undefined
      && await chromeTranslationAvailability(sourceLanguage, targetLanguage) === 'available'
  ),
  createProvider: () => chromeTranslationProvider,
};

export default descriptor;
