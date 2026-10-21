import { useEffect, useMemo, useRef, useState } from 'react';

import { api, type BibliographySnapshot, type BibliographyTask, type DocumentBase } from './api';
import {
  bibliographyDisplayEntries,
  bibliographyEntrySourceLabel,
  bibliographyJSON,
  bibliographyJSONFilename,
  bibliographyTaskPending,
} from './bibliographyExport';
import './BibliographyExportDialog.css';

export function BibliographyExportDialog({ documentBase, onClose }: { documentBase: DocumentBase; onClose: () => void }) {
  const [snapshot, setSnapshot] = useState<BibliographySnapshot>();
  const [task, setTask] = useState<BibliographyTask>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [focusedDocumentID, setFocusedDocumentID] = useState('');
  const bibliographyEntryRefs = useRef(new Map<string, HTMLElement>());
  const displayEntries = useMemo(() => snapshot ? bibliographyDisplayEntries(snapshot) : [], [snapshot]);
  const displayDocumentIDs = useMemo(() => new Set(displayEntries.map((entry) => entry.documentId)), [displayEntries]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError('');
    const wait = (milliseconds: number) => new Promise<void>((resolve, reject) => {
      const onAbort = () => {
        window.clearTimeout(timer);
        reject(new DOMException('Aborted', 'AbortError'));
      };
      const timer = window.setTimeout(() => {
        controller.signal.removeEventListener('abort', onAbort);
        resolve();
      }, milliseconds);
      controller.signal.addEventListener('abort', onAbort, { once: true });
    });
    const generate = async () => {
      let current = (await api.generateBaseBibliography(documentBase.id, controller.signal)).task;
      if (!controller.signal.aborted) setTask(current);
      while (bibliographyTaskPending(current)) {
        await wait(500);
        current = (await api.getBaseBibliographyTask(documentBase.id, current.id, controller.signal)).task;
        if (!controller.signal.aborted) setTask(current);
      }
      if (current.state === 'failed') {
        throw new Error(`后台任务失败${current.errorCode ? `：${current.errorCode}` : ''}`);
      }
      if (!current.snapshot) throw new Error('后台任务完成但没有返回参考文献结果。');
      if (!controller.signal.aborted) setSnapshot(current.snapshot);
    };
    void generate()
      .catch((caught) => {
        if (!controller.signal.aborted) setError(caught instanceof Error ? caught.message : '生成参考文献失败。');
      })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [documentBase.id]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => { if (event.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  async function copyBibTeX() {
    if (!snapshot?.bibtex) return;
    try {
      await navigator.clipboard.writeText(snapshot.bibtex);
      setNotice('已复制 BibTeX。');
    } catch {
      setNotice('复制失败，请在预览中手动复制。');
    }
  }

  function downloadBlob(content: string, filename: string, contentType: string) {
    const blob = new Blob([content], { type: contentType });
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

  function downloadBibTeX() {
    if (!snapshot) return;
    downloadBlob(snapshot.bibtex, snapshot.filename, 'application/x-bibtex;charset=utf-8');
  }

  function downloadJSON() {
    if (!snapshot?.exportEntries?.length) return;
    downloadBlob(bibliographyJSON(snapshot), bibliographyJSONFilename(snapshot.filename), 'application/json;charset=utf-8');
  }

  function jumpToBibliographyEntry(documentID: string) {
    const target = bibliographyEntryRefs.current.get(documentID);
    if (!target) return;
    setFocusedDocumentID(documentID);
    target.scrollIntoView({ behavior: 'smooth', block: 'center', inline: 'nearest' });
  }

  return <div className="bibliography-export-backdrop" role="presentation" onMouseDown={onClose}>
    <section className={`bibliography-export-dialog${snapshot ? ' has-snapshot' : ''}`} role="dialog" aria-modal="true" aria-labelledby="bibliography-export-title"
      onMouseDown={(event) => event.stopPropagation()}>
      <header>
        <div><p className="eyebrow">Bibliography snapshot</p><h2 id="bibliography-export-title">生成参考文献</h2></div>
        <button type="button" onClick={onClose}>关闭</button>
      </header>
      <p className="bibliography-export-base">Document Base · {documentBase.name}</p>
      {loading && <p className="bibliography-export-loading">
        {task?.state === 'running' ? '后台任务正在从 DOI 获取引文并生成本地兜底…' : '后台任务排队中…'}
      </p>}
      {!loading && error && <p className="bibliography-export-error">{error}</p>}
      {!loading && snapshot && <>
        <div className="bibliography-export-summary">
          <span>生成 {snapshot.generated}</span><span>跳过 {snapshot.skipped}</span><span>失败 {snapshot.failed}</span>
        </div>
        <div className="bibliography-export-actions">
          <button type="button" disabled={!snapshot.bibtex} onClick={() => void copyBibTeX()}>复制</button>
          <button type="button" disabled={!snapshot.exportEntries?.length} onClick={downloadJSON}>下载 JSON</button>
          <button className="primary" type="button" disabled={!snapshot.bibtex} onClick={downloadBibTeX}>下载 .bib</button>
        </div>
        <div className="bibliography-export-body">
          <pre aria-label="BibTeX 参考文献">
            {displayEntries.length > 0
              ? displayEntries.map((entry) => <code
                key={entry.documentId}
                ref={(node) => {
                  if (node) bibliographyEntryRefs.current.set(entry.documentId, node);
                  else bibliographyEntryRefs.current.delete(entry.documentId);
                }}
                className={`bibliography-export-entry source-${entry.source}${focusedDocumentID === entry.documentId ? ' is-focused' : ''}`}
                data-source-label={entry.sourceLabel}
              >{entry.text}</code>)
              : snapshot.bibtex || '没有可导出的条目。'}
          </pre>
          <ol className="bibliography-export-report">
            {snapshot.entries.map((entry, index) => <li key={entry.documentId} className={`outcome-${entry.outcome}${focusedDocumentID === entry.documentId ? ' is-focused' : ''}`}>
              <button type="button" disabled={!displayDocumentIDs.has(entry.documentId)} onClick={() => jumpToBibliographyEntry(entry.documentId)}>
                <strong>{entry.title || `未命名论文 ${index + 1}`}</strong>
                <span>{bibliographyEntrySourceLabel(entry)}{entry.citationKey ? ` · ${entry.citationKey}` : ''}</span>
                {entry.missingFields.length > 0 && <small>缺失：{entry.missingFields.join('、')}</small>}
                {entry.doiErrorCode && <small>DOI：{entry.doiErrorCode}</small>}
                {entry.warnings.length > 0 && <small>警告：{entry.warnings.join('、')}</small>}
              </button>
            </li>)}
          </ol>
        </div>
      </>}
      {notice && <p className="bibliography-export-notice">{notice}</p>}
      <p className="bibliography-export-hint">DOI 成功时保留 doi.org 返回的完整 BibTeX；失败或无 DOI 时使用当前论文元数据构建。本次结果是下载快照，不会同步项目文件。</p>
    </section>
  </div>;
}
