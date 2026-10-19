import { FormEvent, useState } from 'react';

import { api, PaperCitation, PaperReferenceSourceUpdate } from './api';
import './ReferenceEditorDialog.css';

// One author per line; an optional affiliation follows a pipe. Keeping the
// affiliation in the text form avoids discarding data the extractor supplied.
function authorsToText(authors: PaperCitation['authors']) {
  return authors
    .map((author) => [author.name, author.affiliation].filter(Boolean).join(' | '))
    .join('\n');
}

function parseAuthors(value: string) {
  const authors: PaperCitation['authors'] = [];
  value.split(/\r?\n/).forEach((line) => {
    const trimmed = line.trim();
    if (!trimmed) return;
    const [namePart, ...affiliationParts] = trimmed.split('|');
    const name = namePart.trim();
    if (!name) throw new Error(`第 ${authors.length + 1} 位作者缺少姓名`);
    const affiliation = affiliationParts.join('|').trim();
    authors.push(affiliation ? { name, affiliation } : { name });
  });
  return authors;
}

function initialDate(value?: string) {
  return value ? value.slice(0, 10) : '';
}

export function ReferenceEditorDialog({
  reference,
  onClose,
  onSaved,
}: {
  reference: PaperCitation;
  onClose: () => void;
  onSaved: (updated: PaperCitation) => void;
}) {
  const initialDOI = reference.doi ?? '';
  const initialTitle = reference.title ?? '';
  const initialAuthors = authorsToText(reference.authors);
  const initialVenue = reference.venue ?? '';
  const initialPublishedAt = initialDate(reference.publishedAt);
  const initialVolume = reference.volume ?? '';
  const initialIssue = reference.issue ?? '';
  const initialPages = reference.pages ?? '';
  const initialSourceURL = reference.sourceUrl ?? '';

  const [doi, setDOI] = useState(initialDOI);
  const [title, setTitle] = useState(initialTitle);
  const [authors, setAuthors] = useState(initialAuthors);
  const [venue, setVenue] = useState(initialVenue);
  const [publishedAt, setPublishedAt] = useState(initialPublishedAt);
  const [volume, setVolume] = useState(initialVolume);
  const [issue, setIssue] = useState(initialIssue);
  const [pages, setPages] = useState(initialPages);
  const [sourceURL, setSourceURL] = useState(initialSourceURL);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  async function submit(event: FormEvent) {
    event.preventDefault();
    const update: PaperReferenceSourceUpdate = {};
    if (doi.trim() !== initialDOI.trim()) update.doi = doi.trim();
    if (title.trim() !== initialTitle.trim()) update.title = title.trim();
    if (authors !== initialAuthors) {
      try {
        update.authors = parseAuthors(authors);
      } catch (parseError) {
        setError(parseError instanceof Error ? parseError.message : '作者格式无效。');
        return;
      }
    }
    if (venue.trim() !== initialVenue.trim()) update.venue = venue.trim();
    if (publishedAt !== initialPublishedAt) update.publishedAt = publishedAt;
    if (volume.trim() !== initialVolume.trim()) update.volume = volume.trim();
    if (issue.trim() !== initialIssue.trim()) update.issue = issue.trim();
    if (pages.trim() !== initialPages.trim()) update.pages = pages.trim();
    if (sourceURL.trim() !== initialSourceURL.trim()) update.sourceUrl = sourceURL.trim();

    if (Object.keys(update).length === 0) {
      onClose();
      return;
    }
    setSaving(true);
    setError('');
    try {
      const { reference: updated } = await api.updatePaperReferenceSource(reference.referenceId, update);
      onSaved({ ...reference, ...updated });
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : '保存引用信息失败。');
    } finally {
      setSaving(false);
    }
  }

  return <div className="reference-editor-backdrop" role="presentation" onMouseDown={() => { if (!saving) onClose(); }}>
    <section className="reference-editor-dialog" role="dialog" aria-modal="true" aria-labelledby="reference-editor-title"
      onMouseDown={(event) => event.stopPropagation()}>
      <header>
        <div>
          <p className="eyebrow">Reference source</p>
          <h2 id="reference-editor-title">编辑引用源信息</h2>
        </div>
        <button type="button" onClick={onClose} disabled={saving}>关闭</button>
      </header>
      {reference.rawReference && <section className="reference-editor-raw">
        <h3>引用原文</h3>
        <p>{reference.rawReference}</p>
      </section>}
      <form className="reference-editor-form" onSubmit={submit}>
        <div className="reference-editor-grid">
          <div className="reference-editor-field full">
            <label htmlFor="reference-edit-doi">DOI</label>
            <input id="reference-edit-doi" value={doi} maxLength={255} onChange={(event) => setDOI(event.target.value)} placeholder="10.1000/example" disabled={saving} />
          </div>
          <div className="reference-editor-field full">
            <label htmlFor="reference-edit-title">标题</label>
            <input id="reference-edit-title" value={title} maxLength={4096} onChange={(event) => setTitle(event.target.value)} placeholder="未提取标题" disabled={saving} />
          </div>
          <div className="reference-editor-field full">
            <label htmlFor="reference-edit-authors">作者（每行一个，可用「姓名 | 单位」）</label>
            <textarea id="reference-edit-authors" value={authors} onChange={(event) => setAuthors(event.target.value)} placeholder={'Ada Lovelace | Analytical Engine Lab\nGrace Hopper'} disabled={saving} />
          </div>
          <div className="reference-editor-field">
            <label htmlFor="reference-edit-venue">期刊 / 会议</label>
            <input id="reference-edit-venue" value={venue} maxLength={1024} onChange={(event) => setVenue(event.target.value)} disabled={saving} />
          </div>
          <div className="reference-editor-field">
            <label htmlFor="reference-edit-published-at">发表日期</label>
            <input id="reference-edit-published-at" type="date" value={publishedAt} onChange={(event) => setPublishedAt(event.target.value)} disabled={saving} />
          </div>
          <div className="reference-editor-field">
            <label htmlFor="reference-edit-volume">卷</label>
            <input id="reference-edit-volume" value={volume} maxLength={128} onChange={(event) => setVolume(event.target.value)} disabled={saving} />
          </div>
          <div className="reference-editor-field">
            <label htmlFor="reference-edit-issue">期</label>
            <input id="reference-edit-issue" value={issue} maxLength={128} onChange={(event) => setIssue(event.target.value)} disabled={saving} />
          </div>
          <div className="reference-editor-field">
            <label htmlFor="reference-edit-pages">页码</label>
            <input id="reference-edit-pages" value={pages} maxLength={128} onChange={(event) => setPages(event.target.value)} disabled={saving} />
          </div>
          <div className="reference-editor-field full">
            <label htmlFor="reference-edit-source-url">来源 URL</label>
            <input id="reference-edit-source-url" value={sourceURL} maxLength={2048} onChange={(event) => setSourceURL(event.target.value)} placeholder="https://example.org/paper.pdf" disabled={saving} />
          </div>
        </div>
        <p className="reference-editor-hint">留空保存会清空对应字段。修改后该引用会标记为人工来源，后续自动抽取不会覆盖。</p>
        {error && <p className="reference-editor-error">{error}</p>}
        <div className="reference-editor-actions">
          <button type="button" onClick={onClose} disabled={saving}>取消</button>
          <button className="primary" type="submit" disabled={saving}>{saving ? '保存中…' : '保存修改'}</button>
        </div>
      </form>
    </section>
  </div>;
}
