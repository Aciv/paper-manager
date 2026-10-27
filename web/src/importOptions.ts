import type { DocumentImportOptions } from './api';

export type ImportDraft = {
  metadata: string;
  sourceLookup: 'inherit' | 'auto' | 'skip';
  duplicateMetadata: 'none' | 'fill_missing' | 'replace';
};
export type ImportDrafts = Record<string, ImportDraft | undefined>;

export function buildImportOptions(keys: string[], drafts: ImportDrafts, skip: boolean): DocumentImportOptions[] {
  const options = keys.map((key, index): DocumentImportOptions => {
    const draft = drafts[key];
    let metadata: DocumentImportOptions['metadata'];
    if (draft?.metadata.trim()) {
      try {
        const parsed: unknown = JSON.parse(draft.metadata);
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error();
        metadata = parsed as DocumentImportOptions['metadata'];
      } catch { throw new Error(`第 ${index + 1} 项的源信息必须是 JSON 对象。`); }
      if (new TextEncoder().encode(draft.metadata).length > 64 * 1024) {
        throw new Error(`第 ${index + 1} 项的源信息超过 64 KiB。`);
      }
    }
    return {
      metadata,
      processing: { source_lookup: draft?.sourceLookup && draft.sourceLookup !== 'inherit'
        ? draft.sourceLookup : skip ? 'skip' : 'auto' },
      duplicate_metadata: draft?.duplicateMetadata ?? 'fill_missing',
    };
  });
  if (new TextEncoder().encode(JSON.stringify(options)).length > 1024 * 1024) {
    throw new Error('本批源信息与选项合计超过 1 MiB，请拆分导入。');
  }
  return options;
}
