import type {
  TranslationLanguage,
  TranslationProvider,
  TranslationProviderConfig,
  TranslationProviderDescriptor,
  TranslationProviderResult,
} from '../types.ts';
import {
  TranslationError,
  ProviderGate,
  abortedError,
  delay,
  detectSourceLanguage,
  isAbortError,
  providerHttpHeaders,
  resolveTargetLanguage,
  splitTextByUtf8Bytes,
} from '../runtime.ts';

const MYMEMORY_TRANSLATE_URL = 'https://api.mymemory.translated.net/get';
const MYMEMORY_MAX_UTF8_BYTES = 450;
const MYMEMORY_CHUNK_DELAY_MS = 1000;

function myMemoryLanguage(language: TranslationLanguage): string {
  if (language === 'zh-Hans') return 'zh-CN';
  if (language === 'zh-Hant') return 'zh-TW';
  return language;
}

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
  signal: AbortSignal | undefined,
  config: TranslationProviderConfig,
): Promise<TranslationProviderResult> {
  if (signal?.aborted) throw abortedError();
  const params = new URLSearchParams({
    q: text,
    langpair: `${myMemoryLanguage(sourceLanguage)}|${myMemoryLanguage(targetLanguage)}`,
  });
  let response: Response;
  try {
    response = await fetch(`${MYMEMORY_TRANSLATE_URL}?${params.toString()}`, {
      headers: providerHttpHeaders('mymemory', config, { Accept: 'application/json' }),
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
  signal: AbortSignal | undefined,
  config: TranslationProviderConfig,
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
    const result = await translateMyMemoryChunk(chunk, source, target, signal, config);
    translations.push(result.text);
  }
  return { text: translations.join('\n'), detectedLanguage: source };
}

const myMemoryGate = new ProviderGate(1000);

export function createMyMemoryTranslationProvider(config: TranslationProviderConfig = {}): TranslationProvider {
  return {
    id: 'mymemory',
    label: 'MyMemory 备用',
    translate: (text, sourceLanguage, targetLanguage, signal) => (
      myMemoryGate.run(() => translateMyMemory(text, sourceLanguage, targetLanguage, signal, config))
    ),
  };
}

export const mymemoryTranslationProvider = createMyMemoryTranslationProvider();


const descriptor: TranslationProviderDescriptor = {
  id: 'mymemory',
  label: 'MyMemory 备用',
  kind: 'http',
  note: '备用接口，额度有限',
  hint: 'MyMemory 公共接口；单次最多 500 UTF-8 字节，匿名每日额度有限，只作为兜底。',
  configFields: [],
  autoPriority: 20,
  createProvider: createMyMemoryTranslationProvider,
};

export default descriptor;
