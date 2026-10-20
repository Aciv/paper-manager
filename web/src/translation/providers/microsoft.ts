import type {
  ConcreteTranslationLanguage,
  TranslationLanguage,
  TranslationProvider,
  TranslationProviderConfig,
  TranslationProviderDescriptor,
  TranslationProviderResult,
} from '../types.ts';
import {
  CONCRETE_TRANSLATION_LANGUAGES,
} from '../types.ts';
import {
  TranslationError,
  ProviderGate,
  abortedError,
  delay,
  detectSourceLanguage,
  isAbortError,
  protectTranslationSymbols,
  providerHttpHeaders,
  resolveTargetLanguage,
  restoreTranslationSymbols,
  splitTextByCodePoints,
} from '../runtime.ts';

const MICROSOFT_TRANSLATE_URL = 'https://edge.microsoft.com/translate/translatetext';
const MICROSOFT_MAX_CODEPOINTS = 4000;
const MICROSOFT_CHUNK_DELAY_MS = 100;

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
  signal: AbortSignal | undefined,
  config: TranslationProviderConfig,
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
      headers: providerHttpHeaders('microsoft', config, { 'Content-Type': 'application/json' }),
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
  signal: AbortSignal | undefined,
  config: TranslationProviderConfig,
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
    const result = await translateMicrosoftChunk(chunk, sourceLanguage, target, signal, config);
    translations.push(result.text);
    detectedLanguage ??= result.detectedLanguage;
  }
  return { text: translations.join('\n'), detectedLanguage };
}

const microsoftGate = new ProviderGate(100);

export function createMicrosoftTranslationProvider(config: TranslationProviderConfig = {}): TranslationProvider {
  return {
    id: 'microsoft',
    label: 'Microsoft 在线',
    translate: (text, sourceLanguage, targetLanguage, signal) => (
      microsoftGate.run(() => translateMicrosoft(text, sourceLanguage, targetLanguage, signal, config))
    ),
  };
}

export const microsoftTranslationProvider = createMicrosoftTranslationProvider();


const descriptor: TranslationProviderDescriptor = {
  id: 'microsoft',
  label: 'Microsoft 在线',
  kind: 'http',
  note: '免密钥在线翻译',
  hint: '免密钥在线接口；选中文本会发送给 Microsoft，不需要登录或填写 API Key。',
  configFields: [],
  autoPriority: 10,
  createProvider: createMicrosoftTranslationProvider,
};

export default descriptor;
