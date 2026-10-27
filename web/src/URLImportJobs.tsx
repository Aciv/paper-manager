import { useState } from 'react';
import type { Document } from './api';
import type { useURLImportJobs } from './useURLImportJobs';
import { importFinished, importSummary } from './urlImports';

export function URLImportJobs({ jobs, onOpen }: {
  jobs: ReturnType<typeof useURLImportJobs>;
  onOpen: (document: Document) => void;
}) {
  const [retryError, setRetryError] = useState('');
  const [retried, setRetried] = useState<Set<string>>(() => new Set());
  if (!jobs.handles.length && !jobs.errors.storage) return null;
  async function submit(action: () => Promise<unknown>) {
    setRetryError('');
    try { await action(); } catch (error) { setRetryError(error instanceof Error ? error.message : '提交失败'); }
  }
  return <section className="batch-results" aria-label="URL 导入任务">
    <h2>URL 导入任务</h2>
    <p className="hint">刷新后可恢复进度；新增文档逐项加入所选 Base。入库完成后，PDF 解析仍会继续。</p>
    {(jobs.errors.storage || retryError) && <p role="alert">{jobs.errors.storage || retryError}</p>}
    {jobs.handles.map((handle) => {
      const task = jobs.tasks[handle.id];
      const finished = importFinished(task);
      return <article key={handle.id}>
        <h3>{task ? importSummary(task) : '正在确认导入任务…'}</h3>
        <small>{handle.id}</small>
        {task && <progress max={task.total || 1} value={task.completed} />}
        {(jobs.errors[handle.id] || task?.error || task?.result?.error) && <p role="status">
          {jobs.errors[handle.id] || task?.error || task?.result?.error}
        </p>}
        <details open={!finished}>
          <summary>逐项结果（{task?.result?.items.length ?? 0}）</summary>
        <ul>{task?.result?.items.map((item) => <li key={item.index}>
          <span>{item.filename || item.url || `第 ${item.index + 1} 项`}</span>
          <strong>{({ accepted: '已入库', duplicate: '已存在', rejected: '已拒绝', failed: '失败' })[item.status]}</strong>
          {item.error && <small>{item.error}</small>}
          {item.importMetadata && <small>源信息：{({ applied: '已应用', skipped: '保持原值', failed: '写入失败（文件已保存）' })[item.importMetadata.status]}</small>}
          {item.document && <button type="button" onClick={() => onOpen(item.document!)}>查看文档</button>}
          {item.processingQueued === false && <span>解析尚未入队，请打开文档后重试解析。</span>}
        </li>)}</ul>
        </details>
        {!!jobs.errors[handle.id] && <button type="button" onClick={() => jobs.refresh(handle.id)}>重新查询</button>}
        {handle.pending && !task && !!jobs.errors[handle.id] && Date.now() - handle.createdAt < 24 * 60 * 60 * 1000
          && <button type="button" disabled={jobs.submitting} onClick={() => void submit(() => jobs.enqueue([], [], handle))}>确认原提交</button>}
        {finished && !!task?.retryUrls?.length && <button type="button" disabled={jobs.submitting || retried.has(handle.id)}
          onClick={() => void submit(async () => {
            const queued = await jobs.enqueue(task.retryUrls!, task.baseIds ?? (task.baseId ? [task.baseId] : []));
            if (queued) setRetried((current) => new Set([...current, handle.id]));
          })}>
          {retried.has(handle.id) ? '已提交失败项重试' : `仅重试失败项（${task.retryUrls.length}）`}
        </button>}
        {(finished || !!jobs.errors[handle.id]) && <button type="button" onClick={() => jobs.forget(handle.id)}>移除记录（不删除文档）</button>}
      </article>;
    })}
  </section>;
}
