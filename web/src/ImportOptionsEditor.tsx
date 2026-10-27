import type { ImportDraft, ImportDrafts } from './importOptions';

const emptyDraft: ImportDraft = { metadata: '', sourceLookup: 'inherit', duplicateMetadata: 'fill_missing' };

export function ImportOptionsEditor({ items, drafts, onChange, skip, onSkip, disabled }: {
  items: { key: string; label: string }[];
  drafts: ImportDrafts;
  onChange: (drafts: ImportDrafts) => void;
  skip: boolean;
  onSkip: (skip: boolean) => void;
  disabled: boolean;
}) {
  function patch(key: string, change: Partial<ImportDraft>) {
    onChange({ ...drafts, [key]: { ...emptyDraft, ...drafts[key], ...change } });
  }
  return <fieldset className="import-options" disabled={disabled}>
    <legend>源信息与自动查询</legend>
    <label className="import-option-check">
      <input type="checkbox" checked={skip} onChange={(event) => onSkip(event.target.checked)} />
      本批跳过自动源信息查询
    </label>
    <p className="hint">解析和参考文献提取照常运行。已有文档保留原处理设置，默认只补缺失源信息。</p>
    {items.map((item, index) => {
      const draft = drafts[item.key] ?? emptyDraft;
      return <details key={`${item.key}-${index}`}>
        <summary>{index + 1}. {item.label} · 单篇选项与源信息</summary>
        <label>源信息查询
          <select value={draft.sourceLookup} onChange={(event) => patch(item.key, {
            sourceLookup: event.target.value as ImportDraft['sourceLookup'],
          })}>
            <option value="inherit">跟随本批设置</option>
            <option value="auto">自动查询</option>
            <option value="skip">跳过自动查询</option>
          </select>
        </label>
        <label>源信息（可选 JSON 对象）
          <textarea className="code-input" rows={5} value={draft.metadata}
            placeholder={'{"title":"论文标题","authors":[{"name":"作者"}],"doi":"10.1234/example"}'}
            onChange={(event) => patch(item.key, { metadata: event.target.value })} />
        </label>
        <p className="hint">可提供摘要、标签、venue、sourceUrl、publishedAt 等字段；留空则自动提取。</p>
        <label>遇到已有论文时
          <select value={draft.duplicateMetadata} onChange={(event) => patch(item.key, {
            duplicateMetadata: event.target.value as ImportDraft['duplicateMetadata'],
          })}>
            <option value="fill_missing">仅补缺失字段</option>
            <option value="none">保留已有源信息</option>
            <option value="replace">覆盖本次提供的字段</option>
          </select>
        </label>
      </details>;
    })}
  </fieldset>;
}
