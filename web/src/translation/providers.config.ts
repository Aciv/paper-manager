import type { TranslationProviderDescriptor } from './types.ts';
import microsoft from './providers/microsoft.ts';
import chrome from './providers/chrome.ts';
import mymemory from './providers/mymemory.ts';

// Single provider registration/control file.
//
// Browser/Vite modules must be statically reachable, so each provider is a
// module imported here and default-exporting a TranslationProviderDescriptor.
// To add a provider:
//   1. add providers/<name>.ts with a default descriptor export
//   2. import it here
//   3. add an entry below (enabled + order)
// No central ID union, label table, options table or registry switch is needed.
export type TranslationProviderModuleRegistration = {
  /** Documentation only; the actual module is the static import above. */
  file: string;
  module: TranslationProviderDescriptor;
  enabled?: boolean;
};

export const TRANSLATION_PROVIDER_MODULES: readonly TranslationProviderModuleRegistration[] = [
  { file: './providers/microsoft.ts', module: microsoft, enabled: true },
  { file: './providers/chrome.ts', module: chrome, enabled: true },
  { file: './providers/mymemory.ts', module: mymemory, enabled: true },
];

export const TRANSLATION_PROVIDER_DESCRIPTORS: readonly TranslationProviderDescriptor[] =
  TRANSLATION_PROVIDER_MODULES
    .filter((entry) => entry.enabled !== false)
    .map((entry) => entry.module);
