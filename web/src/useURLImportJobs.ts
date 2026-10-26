import { useEffect, useRef, useState } from 'react';
import { APIError, api } from './api';
import type { DocumentURLImport, URLImportTask } from './api';
import { importFinished, restoreURLImports, urlImportStorageKey } from './urlImports';
import type { SavedURLImport } from './urlImports';

export function useURLImportJobs(onProgress: () => void) {
  const [handles, setHandles] = useState<SavedURLImport[]>(() => {
    try { return restoreURLImports(localStorage.getItem(urlImportStorageKey)); } catch { return []; }
  });
  const [tasks, setTasks] = useState<Record<string, URLImportTask>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const [refreshToken, setRefreshToken] = useState(0);
  const tasksRef = useRef(tasks);
  tasksRef.current = tasks;
  const progressRef = useRef(onProgress);
  progressRef.current = onProgress;
  const paused = useRef(new Set<string>());
  const handleIDs = handles.map((handle) => handle.id).join(',');

  function save(next: SavedURLImport[]) {
    setHandles(next);
    try { localStorage.setItem(urlImportStorageKey, JSON.stringify(next)); } catch {
      setErrors((current) => ({ ...current, storage: '浏览器无法保存任务句柄；请记下任务 ID，刷新后可能无法恢复查询。' }));
    }
  }

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function poll() {
      const ids = handleIDs.split(',').filter((id) => id && !paused.current.has(id) && !importFinished(tasksRef.current[id]));
      if (!ids.length) return;
      await Promise.all(ids.map(async (id) => {
        try {
          const task = await api.getURLImportTask(id, controller.signal);
          if (controller.signal.aborted) return;
          const previous = tasksRef.current[id];
          setTasks((current) => ({ ...current, [id]: task }));
          setErrors((current) => ({ ...current, [id]: '' }));
          if (task.completed !== previous?.completed || (importFinished(task) && !importFinished(previous))) {
            progressRef.current();
          }
        } catch (error) {
          if (controller.signal.aborted) return;
          const missing = error instanceof APIError && error.status === 404;
          if (missing) paused.current.add(id);
          setErrors((current) => ({ ...current, [id]: missing
            ? '任务未找到或结果已过期（完成后保留 24 小时）。请先检查论文库，勿整批重复导入。'
            : '暂时无法查询，稍后会继续；后台任务不受页面断开影响。' }));
        }
      }));
      if (!controller.signal.aborted) timer = setTimeout(() => void poll(), 2000);
    }
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [handleIDs, refreshToken]);

  async function enqueue(urls: DocumentURLImport[], baseIds: string[], existing?: SavedURLImport) {
    if (submitting) return;
    if (!existing && handles.length >= 10) throw new Error('请先移除已结束的任务记录，再提交新批次。');
    const request = existing?.pending ?? { urls, baseIds, requestId: crypto.randomUUID() };
    const handle = existing ?? { id: `url-import-${request.requestId}`, createdAt: Date.now(), pending: request };
    // Persist the stable request ID before POST, so a lost response can be
    // confirmed explicitly with the identical request rather than duplicated.
    save(existing ? handles : [handle, ...handles]);
    paused.current.delete(handle.id);
    setSubmitting(true);
    try {
      const task = await api.enqueueURLImport(request);
      paused.current.delete(handle.id);
      setTasks((current) => ({ ...current, [handle.id]: current[handle.id] ?? task }));
      setErrors((current) => ({ ...current, [handle.id]: '' }));
      setRefreshToken((value) => value + 1);
      return task;
    } catch (error) {
      paused.current.delete(handle.id);
      setRefreshToken((value) => value + 1);
      setErrors((current) => ({ ...current, [handle.id]: '提交未确认。先查询任务；必要时点击“确认原提交”，使用同一请求 ID。' }));
      throw error;
    } finally { setSubmitting(false); }
  }

  function refresh(id: string) {
    paused.current.delete(id);
    setRefreshToken((value) => value + 1);
  }

  function forget(id: string) { save(handles.filter((handle) => handle.id !== id)); }

  return { handles, tasks, errors, submitting, enqueue, refresh, forget };
}

