import {
  DEFAULT_TRANSLATION_SETTINGS,
  MAX_TRANSLATION_CACHE_BYTES,
  MAX_TRANSLATION_CACHE_ENTRIES,
  TRANSLATION_CACHE_TTL_MS,
  TRANSLATION_LANGUAGE_OPTIONS,
  TRANSLATION_SETTINGS_STORAGE,
  type ConcreteTranslationLanguage,
  type TranslateTextOptions,
  type TranslationLanguage,
  type TranslationProviderConfig,
  type TranslationProviderConfigField,
  type TranslationProviderConfigMap,
  type TranslationProviderDescriptor,
  type TranslationProviderID,
  type TranslationProviderResult,
  type TranslationProviderSetting,
  type TranslationResult,
  type TranslationSettings,
} from './types.ts';
import {
  TranslationError,
  abortedError,
  detectSourceLanguage,
  isAbortError,
  resolveTargetLanguage,
  serializeProviderConfig,
} from './runtime.ts';
import {
  TRANSLATION_PROVIDER_LABELS,
  TRANSLATION_PROVIDER_OPTIONS,
  TRANSLATION_PROVIDER_REGISTRY,
} from './registry.ts';

export * from './types.ts';
export * from './runtime.ts';
export * from './registry.ts';
export * from './providers/microsoft.ts';
export * from './providers/chrome.ts';
export * from './providers/mymemory.ts';

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
  configKey: string,
  sourceLanguage: TranslationLanguage,
  targetLanguage: TranslationLanguage,
  text: string,
): string {
  return `${provider}\u0000${configKey}\u0000${sourceLanguage}\u0000${targetLanguage}\u0000${text}`;
}


function missingRequiredConfigFields(
  descriptor: TranslationProviderDescriptor,
  config: TranslationProviderConfig,
): TranslationProviderConfigField[] {
  return descriptor.configFields.filter((field) => (
    field.required && !(config[field.key] ?? '').trim()
  ));
}

async function autoProviderOrder(
  sourceLanguage: ConcreteTranslationLanguage,
  targetLanguage: ConcreteTranslationLanguage,
  providerConfigs: TranslationProviderConfigMap,
): Promise<TranslationProviderID[]> {
  const descriptors = Object.values(TRANSLATION_PROVIDER_REGISTRY)
    .filter((descriptor) => descriptor.autoPriority !== null)
    .sort((left, right) => (left.autoPriority ?? Infinity) - (right.autoPriority ?? Infinity));
  const order: TranslationProviderID[] = [];
  for (const descriptor of descriptors) {
    const config = providerConfigs[descriptor.id] ?? {};
    if (missingRequiredConfigFields(descriptor, config).length > 0) continue;
    if (descriptor.autoEligible
      && !(await descriptor.autoEligible({ config, sourceLanguage, targetLanguage }))) continue;
    order.push(descriptor.id);
  }
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

  const providerConfigs = options.providerConfigs ?? {};
  const order = providerSetting === 'auto'
    ? await autoProviderOrder(detectedLanguage, targetLanguage, providerConfigs)
    : [providerSetting];
  let lastError: unknown;

  for (const [index, providerID] of order.entries()) {
    if (options.signal?.aborted) throw abortedError();
    const descriptor = TRANSLATION_PROVIDER_REGISTRY[providerID];
    const config = providerConfigs[providerID] ?? {};
    if (providerSetting !== 'auto') {
      const missing = missingRequiredConfigFields(descriptor, config);
      if (missing.length > 0) {
        throw new TranslationError(
          `请先配置：${missing.map((field) => field.label).join('、')}。`,
          { provider: providerID, retryable: false },
        );
      }
    }
    const configKey = serializeProviderConfig(config);
    const cacheSource = sourceSetting === 'auto' ? detectedLanguage : sourceSetting;
    const key = translationCacheKey(providerID, configKey, cacheSource, targetLanguage, normalized);
    const cached = translationCache.get(key);
    if (cached) {
      return { ...cached, provider: providerID, fallback: index > 0, targetLanguage };
    }
    try {
      const provider = descriptor.createProvider(config);
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


function isProviderSetting(value: unknown): value is TranslationProviderSetting {
  return typeof value === 'string' && TRANSLATION_PROVIDER_OPTIONS.some((option) => option.id === value);
}

function isTranslationLanguage(value: unknown): value is TranslationLanguage {
  return typeof value === 'string'
    && (value === 'auto' || TRANSLATION_LANGUAGE_OPTIONS.some((option) => option.id === value));
}

function normalizeProviderConfigs(value: unknown): TranslationProviderConfigMap {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return {};
  const configs: TranslationProviderConfigMap = {};
  for (const [id, raw] of Object.entries(value as Record<string, unknown>)) {
    if (!(id in TRANSLATION_PROVIDER_LABELS)) continue;
    if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) continue;
    const config: TranslationProviderConfig = {};
    for (const [key, item] of Object.entries(raw as Record<string, unknown>)) {
      if (typeof item === 'string') config[key] = item;
    }
    configs[id as TranslationProviderID] = config;
  }
  return configs;
}

export function loadTranslationSettings(): TranslationSettings {
  if (typeof window === 'undefined') return { ...DEFAULT_TRANSLATION_SETTINGS };
  try {
    const raw: unknown = JSON.parse(window.localStorage.getItem(TRANSLATION_SETTINGS_STORAGE) ?? 'null');
    if (typeof raw !== 'object' || raw === null) return { ...DEFAULT_TRANSLATION_SETTINGS };
    const record = raw as {
      provider?: unknown;
      sourceLanguage?: unknown;
      targetLanguage?: unknown;
      providerConfigs?: unknown;
    };
    return {
      provider: isProviderSetting(record.provider) ? record.provider : DEFAULT_TRANSLATION_SETTINGS.provider,
      sourceLanguage: isTranslationLanguage(record.sourceLanguage)
        ? record.sourceLanguage
        : DEFAULT_TRANSLATION_SETTINGS.sourceLanguage,
      targetLanguage: isTranslationLanguage(record.targetLanguage)
        ? record.targetLanguage
        : DEFAULT_TRANSLATION_SETTINGS.targetLanguage,
      providerConfigs: normalizeProviderConfigs(record.providerConfigs),
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
