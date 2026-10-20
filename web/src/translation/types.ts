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

// Provider IDs are intentionally strings: provider modules are registered from
// providers.config.ts, so adding a provider does not require editing a union.
export type TranslationProviderID = string;
export type TranslationProviderSetting = 'auto' | TranslationProviderID;

export type TranslationProviderConfig = Record<string, string>;
export type TranslationProviderConfigMap = Partial<Record<TranslationProviderID, TranslationProviderConfig>>;

export type TranslationSettings = {
  provider: TranslationProviderSetting;
  sourceLanguage: TranslationLanguage;
  targetLanguage: TranslationLanguage;
  providerConfigs: TranslationProviderConfigMap;
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
// translation result out. Capabilities, configuration, caching and fallback
// belong to the descriptor/orchestrator, not individual providers.
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
  providerConfigs?: TranslationProviderConfigMap;
  signal?: AbortSignal;
};

export type TranslationResult = TranslationProviderResult & {
  provider?: TranslationProviderID;
  fallback?: boolean;
  skipped?: boolean;
  targetLanguage?: ConcreteTranslationLanguage;
};

export type TranslationProviderKind = 'http' | 'script';

export type TranslationProviderConfigField = {
  key: string;
  label: string;
  type?: 'text' | 'password' | 'url' | 'json';
  placeholder?: string;
  description?: string;
  required?: boolean;
};

export type TranslationProviderAutoContext = {
  config: TranslationProviderConfig;
  sourceLanguage: ConcreteTranslationLanguage;
  targetLanguage: ConcreteTranslationLanguage;
};

export type TranslationProviderDescriptor = {
  readonly id: TranslationProviderID;
  readonly label: string;
  readonly kind: TranslationProviderKind;
  readonly note: string;
  readonly hint: string;
  readonly configFields: readonly TranslationProviderConfigField[];
  /** Lower numbers are tried first in automatic mode; null disables auto use. */
  readonly autoPriority: number | null;
  readonly autoEligible?: (context: TranslationProviderAutoContext) => boolean | Promise<boolean>;
  readonly createProvider: (config: TranslationProviderConfig) => TranslationProvider;
};

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
  providerConfigs: {},
};

export const TRANSLATION_SETTINGS_STORAGE = 'papersManager.translation.settings.v1';
export const MAX_TRANSLATION_CACHE_ENTRIES = 300;
export const MAX_TRANSLATION_CACHE_BYTES = 2 * 1024 * 1024;
export const TRANSLATION_CACHE_TTL_MS = 24 * 60 * 60 * 1000;
