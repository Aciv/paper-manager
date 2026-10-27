import type { URLImportRequest, URLImportTask } from './api';

export type SavedURLImport = { id: string; createdAt: number; pending?: URLImportRequest };
export const urlImportStorageKey = 'paper_manager.url-imports.v1';

export function importFinished(task?: URLImportTask): boolean {
  return !!task && ['succeeded', 'partial', 'failed', 'stopped'].includes(task.state);
}

export function importSummary(task: URLImportTask): string {
  const label = {
    queued: '排队中', running: '下载及入库中', succeeded: '全部入库成功',
    partial: '部分失败', failed: '导入失败', stopped: '提前停止',
  }[task.state];
  const count = task.summary;
  const unconfirmed = importFinished(task) && task.completed < task.total ? `，未确认 ${task.total - task.completed}` : '';
  return `${label} · ${task.completed}/${task.total} · 新增 ${count.accepted}，已存在 ${count.duplicate}，拒绝 ${count.rejected}，失败 ${count.failed}${count.metadataFailed ? `，源信息写入失败 ${count.metadataFailed}` : ''}${unconfirmed}`;
}

// Only handles and unconfirmed submissions are cached. Redis owns progress;
// restoring a handle never automatically submits another download batch.
export function restoreURLImports(raw: string | null): SavedURLImport[] {
  try {
    const records: unknown = JSON.parse(raw ?? '[]');
    if (!Array.isArray(records)) return [];
    const seen = new Set<string>();
    return records.filter((record): record is SavedURLImport => {
      if (!record || typeof record !== 'object') return false;
      const item = record as SavedURLImport;
      if (typeof item.id !== 'string' || !/^url-import-[0-9a-f-]{36}$/i.test(item.id)
        || !Number.isFinite(item.createdAt) || seen.has(item.id)) return false;
      seen.add(item.id);
      return true;
    }).slice(0, 10);
  } catch {
    return [];
  }
}
