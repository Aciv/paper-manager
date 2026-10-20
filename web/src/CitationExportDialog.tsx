import { useEffect, useState } from 'react';

import {
  DOI_CITATION_FORMATS,
  DOI_CITATION_GROUPS,
  citationDownloadFilename,
  describeDoiCitationError,
  fetchDoiCitation,
  normalizeDoi,
  type DoiCitationFormat,
  type DoiCitationFormatID,
  type DoiCitationTarget,
} from './doiCitation';
import './CitationExportDialog.css';

const DEFAULT_FORMAT_ID: DoiCitationFormatID = 'bibtex';

function findFormat(id: DoiCitationFormatID): DoiCitationFormat {
  return DOI_CITATION_FORMATS.find((format) => format.id === id) ?? DOI_CITATION_FORMATS[0];
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Permission can be denied; fall through to the selection-based path.
    }
  }
  // Clipboard API is only available in secure contexts; keep a fallback for a
  // plain-HTTP deployment.
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand('copy');
  textarea.remove();
  if (!copied) throw new Error('copy failed');
}

// One reference can be exported in several exchange formats and CSL citation
// styles. The dialog fetches only when opened or when the format changes; a
// successful result is cached by the DOI client so toggling formats is cheap.
export function CitationExportDialog({
  target,
  onClose,
}: {
  target: DoiCitationTarget;
  onClose: () => void;
}) {
  const doi = normalizeDoi(target.doi);
  const [formatID, setFormatID] = useState<DoiCitationFormatID>(DEFAULT_FORMAT_ID);
  const format = findFormat(formatID);
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  useEffect(() => {
    if (!doi) {
      setContent('');
      setError('');
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError('');
    setNotice('');
    setContent('');
    fetchDoiCitation(doi, format, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setContent(value);
      })
      .catch((caught) => {
        if (!controller.signal.aborted) setError(describeDoiCitationError(caught));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [doi, format]);

  async function copyContent() {
    try {
      await copyText(content);
      setNotice('已复制到剪贴板。');
    } catch {
      setNotice('复制失败，请手动选择内容复制。');
    }
  }

  function downloadContent() {
    if (!content) return;
    const filename = citationDownloadFilename(doi, format);
    const blob = new Blob([content], { type: `${format.contentType};charset=utf-8` });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
    setNotice(`已下载 ${filename}。`);
  }

  const subject = target.title?.trim() || target.rawReference?.trim().slice(0, 240) || doi || '未命名引用';

  return <div className="citation-export-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="citation-export-dialog" role="dialog" aria-modal="true" aria-labelledby="citation-export-title"
      onMouseDown={(event) => event.stopPropagation()}>
      <header>
        <div>
          <p className="eyebrow">Citation export</p>
          <h2 id="citation-export-title">获取引文格式</h2>
        </div>
        <button type="button" onClick={onClose}>关闭</button>
      </header>

      <p className="citation-export-subject" title={subject}>{subject}</p>
      <p className="citation-export-doi">{doi ? `DOI ${doi}` : '该引用没有 DOI'}</p>

      <div className="citation-export-toolbar">
        <label htmlFor="citation-export-format">格式</label>
        <select id="citation-export-format" value={formatID} disabled={!doi}
          onChange={(event) => setFormatID(event.target.value as DoiCitationFormatID)}>
          {DOI_CITATION_GROUPS.map((group) => <optgroup key={group.kind} label={group.label}>
            {DOI_CITATION_FORMATS.filter((item) => item.kind === group.kind).map((item) =>
              <option key={item.id} value={item.id}>{item.label}</option>)}
          </optgroup>)}
        </select>
        <button type="button" disabled={!content || loading} onClick={() => void copyContent()}>复制</button>
        <button type="button" disabled={!content || loading} onClick={downloadContent}>下载 .{format.extension}</button>
      </div>

      {!doi
        ? <p className="citation-export-error">该引用没有可用的 DOI，无法通过 doi.org 获取引文信息。</p>
        : <>
          {loading && <p className="citation-export-loading">正在从 doi.org 获取…</p>}
          {!loading && error && <p className="citation-export-error">{error}</p>}
          {!loading && !error && content && <pre className="citation-export-content">{content}</pre>}
        </>}

      {notice && <p className="citation-export-notice">{notice}</p>}
      <p className="citation-export-hint">
        由 doi.org 内容协商实时返回；不同 DOI 注册机构支持的格式可能不同。
        {format.kind === 'style' ? ' 格式化引用已去除 HTML 标记，以纯文本显示。' : ''}
      </p>
    </section>
  </div>;
}
