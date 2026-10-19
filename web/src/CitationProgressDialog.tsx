import { useEffect, useMemo, useRef, useState } from 'react';

import { api, Document, DocumentProcessingView } from './api';
import { fetchAllDocumentPages } from './documentPagination';
import './CitationProgressDialog.css';

type ReferenceProgress = NonNullable<DocumentProcessingView['referenceProgress']>;
type ReferenceStage = DocumentProcessingView['stageStates'][number];

type ProgressRow = {
  document: Document;
  processing?: DocumentProcessingView;
  error?: string;
};

const EMPTY_PROGRESS: ReferenceProgress = { queued: 0, running: 0, completed: 0, failed: 0, cancelled: 0 };
const STAGE_LABELS: Record<string, string> = {
  not_started: '未开始',
  queued: '排队中',
  running: '运行中',
  succeeded: '已完成',
  failed: '失败',
  cancelled: '已取消',
  skipped: '已跳过',
};

function progressOf(row: ProgressRow): ReferenceProgress {
  return row.processing?.referenceProgress ?? EMPTY_PROGRESS;
}

function progressTotal(progress: ReferenceProgress): number {
  return progress.queued + progress.running + progress.completed + progress.failed + progress.cancelled;
}

function referenceStage(row: ProgressRow): ReferenceStage | undefined {
  return row.processing?.stageStates.find((stage) => stage.stage === 'reference_lookup');
}

function hasReferenceWork(row: ProgressRow): boolean {
  const stage = referenceStage(row);
  return progressTotal(progressOf(row)) > 0
    || stage?.status === 'failed'
    || stage?.status === 'cancelled'
    || stage?.status === 'queued'
    || stage?.status === 'running';
}

function retryable(row: ProgressRow): boolean {
  if (row.document.sourceState !== 'available') return false;
  const stage = referenceStage(row);
  if (stage?.status === 'queued' || stage?.status === 'running') return false;
  if (stage?.status === 'not_started') {
    // Manual-start mode: references await an explicit resolution run.
    return progressOf(row).queued > 0;
  }
  return stage?.status === 'failed'
    || (progressOf(row).failed > 0);
}

function documentName(documentItem: Document): string {
  if (documentItem.originalFilename) return documentItem.originalFilename;
  return documentItem.id.slice(0, 8);
}

async function fetchAllDocuments(): Promise<Document[]> {
  return fetchAllDocumentPages((pageNumber) => api.searchDocuments({ page: pageNumber }));
}

async function mapLimit<T, R>(items: T[], limit: number, mapper: (item: T, index: number) => Promise<R>): Promise<R[]> {
  const results = new Array<R>(items.length);
  let cursor = 0;
  const worker = async () => {
    while (cursor < items.length) {
      const index = cursor;
      cursor += 1;
      results[index] = await mapper(items[index], index);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

export function CitationProgressDialog({ open, onClose }: { open: boolean; onClose(): void }) {
  const [rows, setRows] = useState<ProgressRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [showAll, setShowAll] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const requestGeneration = useRef(0);

  async function load() {
    const generation = requestGeneration.current + 1;
    requestGeneration.current = generation;
    setLoading(true);
    setError('');
    try {
      const documents = await fetchAllDocuments();
      // One batched projection per 200 documents instead of N /processing
      // requests. The dialog only needs the processing view.
      const projections = await api.getDocumentProjections(
        documents.map((documentItem) => documentItem.id), false, true,
      );
      const processingByID = new Map(projections.map((projection) => [projection.document.id, projection.processing]));
      const nextRows: ProgressRow[] = documents.map((documentItem) => ({
        document: documentItem,
        processing: processingByID.get(documentItem.id),
      }));
      if (requestGeneration.current !== generation) return;
      setRows(nextRows);
      setSelected(new Set(nextRows.filter(retryable).map((row) => row.document.id)));
    } catch (caught) {
      if (requestGeneration.current !== generation) return;
      setError(caught instanceof Error ? `加载引用进度失败：${caught.message}` : '加载引用进度失败。');
    } finally {
      if (requestGeneration.current === generation) setLoading(false);
    }
  }

  useEffect(() => {
    if (!open) {
      requestGeneration.current += 1;
      return;
    }
    setNotice('');
    void load();
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [open, onClose]);

  const summary = useMemo(() => {
    return rows.reduce((current, row) => {
      const progress = progressOf(row);
      return {
        withReferences: current.withReferences + (progressTotal(progress) > 0 ? 1 : 0),
        queued: current.queued + progress.queued,
        running: current.running + progress.running,
        completed: current.completed + progress.completed,
        failed: current.failed + progress.failed,
        cancelled: current.cancelled + progress.cancelled,
        failedDocuments: current.failedDocuments + (retryable(row) ? 1 : 0),
      };
    }, { withReferences: 0, queued: 0, running: 0, completed: 0, failed: 0, cancelled: 0, failedDocuments: 0 });
  }, [rows]);

  const visibleRows = useMemo(() => {
    const filtered = showAll ? rows : rows.filter(hasReferenceWork);
    const priority = (row: ProgressRow) => {
      if (retryable(row)) return 0;
      const stage = referenceStage(row);
      if (stage?.status === 'queued' || stage?.status === 'running') return 1;
      return 2;
    };
    return [...filtered].sort((left, right) => {
      const byPriority = priority(left) - priority(right);
      if (byPriority !== 0) return byPriority;
      const byProgress = progressTotal(progressOf(right)) - progressTotal(progressOf(left));
      if (byProgress !== 0) return byProgress;
      return documentName(left.document).localeCompare(documentName(right.document), 'zh-Hans-CN');
    });
  }, [rows, showAll]);

  function toggleRow(documentID: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(documentID)) next.delete(documentID);
      else next.add(documentID);
      return next;
    });
  }

  async function retryRows(documentIDs: string[]) {
    if (documentIDs.length === 0) return;
    setBusy(true);
    setNotice('');
    setError('');
    const results = await mapLimit(documentIDs, 3, async (documentID) => {
      try {
        await api.retryPaperReferences(documentID);
        return true;
      } catch {
        return false;
      }
    });
    const accepted = results.filter(Boolean).length;
    const failed = results.length - accepted;
    setBusy(false);
    setNotice(`已提交 ${accepted} 个文档的引用查询重试${failed > 0 ? `，${failed} 个未能提交（可能正在运行或源文件不可用）` : ''}。`);
    await load();
  }

  if (!open) return null;

  return <div className="citation-progress-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="citation-progress-dialog" role="dialog" aria-modal="true" aria-labelledby="citation-progress-title" onMouseDown={(event) => event.stopPropagation()}>
      <header className="citation-progress-header">
        <div>
          <p className="eyebrow">Reference pipeline</p>
          <h2 id="citation-progress-title">引用解析进度</h2>
          <p className="hint">批量读取文档的 processing 投影并汇总 referenceProgress；批量重试会重新派发引用查询。</p>
        </div>
        <div className="detail-actions">
          <button type="button" onClick={() => void load()} disabled={loading || busy}>{loading ? '刷新中…' : '刷新'}</button>
          <button type="button" onClick={onClose}>关闭</button>
        </div>
      </header>

      <div className="citation-progress-summary">
        <span><strong>{rows.length}</strong> 篇文档</span>
        <span><strong>{summary.withReferences}</strong> 篇有引用进度</span>
        <span className="summary-queued"><strong>{summary.queued}</strong> 排队</span>
        <span className="summary-running"><strong>{summary.running}</strong> 运行</span>
        <span className="summary-completed"><strong>{summary.completed}</strong> 完成</span>
        <span className="summary-failed"><strong>{summary.failed}</strong> 失败</span>
        <span className="summary-cancelled"><strong>{summary.cancelled}</strong> 取消</span>
      </div>

      <div className="citation-progress-toolbar">
        <label><input type="checkbox" checked={showAll} onChange={(event) => setShowAll(event.target.checked)} /> 显示无引用进度的文档</label>
        <span className="citation-progress-selected">{summary.failedDocuments > 0 ? `${summary.failedDocuments} 篇可重试 · ` : ''}已选 {selected.size} 个</span>
        <button className="primary" type="button" disabled={busy || selected.size === 0} onClick={() => void retryRows(Array.from(selected))}>
          {busy ? '提交中…' : `批量解析 / 重试 ${selected.size} 个`}
        </button>
      </div>

      {notice && <p className="citation-progress-notice">{notice}</p>}
      {error && <p className="detail-error">{error}</p>}

      {loading && rows.length === 0
        ? <p className="hint citation-progress-loading">正在遍历文档并读取处理状态…</p>
        : visibleRows.length === 0
          ? <p className="hint citation-progress-loading">{rows.length === 0 ? '没有可扫描的文档。' : '没有引用解析进度；勾选上方选项可显示全部文档。'}</p>
          : <ul className="citation-progress-list">
            {visibleRows.map((row) => {
              const progress = progressOf(row);
              const total = progressTotal(progress);
              const stage = referenceStage(row);
              const canRetry = retryable(row);
              return <li className="citation-progress-row" key={row.document.id}>
                <label className="citation-progress-check" title={canRetry ? '选中以批量重试' : '该文档当前不可重试'}>
                  <input type="checkbox" checked={selected.has(row.document.id)} disabled={!canRetry || busy} onChange={() => toggleRow(row.document.id)} />
                </label>
                <div className="citation-progress-main">
                  <div className="citation-progress-title">
                    <strong>{documentName(row.document)}</strong>
                    <span className={`citation-progress-stage stage-${stage?.status ?? 'not_started'}`}>{STAGE_LABELS[stage?.status ?? 'not_started']}</span>
                  </div>
                  <div className="citation-progress-bar" aria-label={`排队 ${progress.queued}，运行 ${progress.running}，完成 ${progress.completed}，失败 ${progress.failed}，取消 ${progress.cancelled}`}>
                    {total === 0
                      ? <i className="seg-empty" />
                      : <>
                        <i className="seg-completed" style={{ width: `${(progress.completed / total) * 100}%` }} />
                        <i className="seg-running" style={{ width: `${(progress.running / total) * 100}%` }} />
                        <i className="seg-queued" style={{ width: `${(progress.queued / total) * 100}%` }} />
                        <i className="seg-failed" style={{ width: `${(progress.failed / total) * 100}%` }} />
                        <i className="seg-cancelled" style={{ width: `${(progress.cancelled / total) * 100}%` }} />
                      </>}
                  </div>
                  <span className="citation-progress-counts">
                    排队 {progress.queued} · 运行 {progress.running} · 完成 {progress.completed} · 失败 {progress.failed} · 取消 {progress.cancelled}
                    {stage?.errorMessage && <> · <em>{stage.errorMessage}</em></>}
                    {row.error && <> · <em>{row.error}</em></>}
                  </span>
                </div>
                {canRetry
                  ? <button className="citation-progress-retry" type="button" disabled={busy} onClick={() => void retryRows([row.document.id])}>重试</button>
                  : <span className="citation-progress-no-retry" title="当前没有失败项可重试">—</span>}
              </li>;
            })}
          </ul>}
    </section>
  </div>;
}
