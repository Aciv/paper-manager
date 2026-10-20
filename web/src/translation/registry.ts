import { TRANSLATION_PROVIDER_DESCRIPTORS } from './providers.config.ts';
import type {
  TranslationProviderDescriptor,
  TranslationProviderID,
  TranslationProviderSetting,
} from './types.ts';

export const AUTO_TRANSLATION_NOTE = '自动选择引擎';
export const AUTO_TRANSLATION_HINT = '优先使用已就绪的 Chrome 本地模型；否则使用 Microsoft 在线翻译，失败时回退到 MyMemory。';

function validateDescriptor(descriptor: TranslationProviderDescriptor, seen: Set<string>): void {
  const id = descriptor.id?.trim();
  if (!id) throw new Error('翻译 provider 注册失败：descriptor.id 不能为空。');
  if (seen.has(id)) throw new Error(`翻译 provider 注册失败：重复的 id "${id}"。`);
  if (descriptor.kind !== 'http' && descriptor.kind !== 'script') {
    throw new Error(`翻译 provider "${id}" 的 kind 必须是 http 或 script。`);
  }
  if (!descriptor.label?.trim()) throw new Error(`翻译 provider "${id}" 缺少 label。`);
  if (!descriptor.note?.trim()) throw new Error(`翻译 provider "${id}" 缺少 note。`);
  if (!descriptor.hint?.trim()) throw new Error(`翻译 provider "${id}" 缺少 hint。`);
  if (!Array.isArray(descriptor.configFields)) {
    throw new Error(`翻译 provider "${id}" 的 configFields 必须是数组。`);
  }
  if (descriptor.autoPriority !== null && !Number.isFinite(descriptor.autoPriority)) {
    throw new Error(`翻译 provider "${id}" 的 autoPriority 必须是数字或 null。`);
  }
  if (typeof descriptor.createProvider !== 'function') {
    throw new Error(`翻译 provider "${id}" 缺少 createProvider 工厂。`);
  }
  seen.add(id);
}

export const TRANSLATION_PROVIDER_REGISTRY: Record<TranslationProviderID, TranslationProviderDescriptor> = {};
export const TRANSLATION_PROVIDER_LABELS: Record<TranslationProviderID, string> = {};

{
  const seen = new Set<string>();
  for (const descriptor of TRANSLATION_PROVIDER_DESCRIPTORS) {
    validateDescriptor(descriptor, seen);
    TRANSLATION_PROVIDER_REGISTRY[descriptor.id] = descriptor;
    TRANSLATION_PROVIDER_LABELS[descriptor.id] = descriptor.label;
  }
}

// Options and auto order derive from the config file/descriptors. Registration
// file order controls the dropdown order.
export const TRANSLATION_PROVIDER_OPTIONS: ReadonlyArray<{
  id: TranslationProviderSetting;
  label: string;
}> = [
  { id: 'auto', label: '自动（推荐）' },
  ...Object.values(TRANSLATION_PROVIDER_REGISTRY).map((provider) => ({
    id: provider.id,
    label: provider.label,
  })),
];
