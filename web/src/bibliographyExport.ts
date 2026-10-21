import type { BibliographyEntryReport, BibliographySnapshot } from './api';
import type { BibliographyTask } from './api';

const SOURCE_LABELS: Record<string, string> = {
  doi_original: 'DOI 原始引文',
  local_no_doi: '本地构建 · 无 DOI',
  local_doi_failed: '本地构建 · DOI 查询失败',
};

export function bibliographyEntrySourceLabel(entry: BibliographyEntryReport): string {
  return SOURCE_LABELS[entry.source ?? ''] ?? entry.outcome;
}

export function bibliographySummary(snapshot: BibliographySnapshot): string {
  return `生成 ${snapshot.generated} · 跳过 ${snapshot.skipped} · 失败 ${snapshot.failed}`;
}

export function bibliographyHasWarnings(snapshot: BibliographySnapshot): boolean {
  return snapshot.entries.some((entry) => entry.warnings.length > 0 || entry.missingFields.length > 0 || Boolean(entry.doiErrorCode));
}

export function bibliographyTaskPending(task: BibliographyTask): boolean {
  return task.state === 'queued' || task.state === 'running';
}

export function bibliographyJSONFilename(filename: string): string {
  const stem = filename.replace(/\.bib$/iu, '') || 'bibliography';
  return `${stem}.json`;
}

export function bibliographyJSON(snapshot: BibliographySnapshot): string {
  return `${JSON.stringify({
    baseId: snapshot.baseId,
    baseName: snapshot.baseName,
    entries: snapshot.exportEntries ?? [],
  }, null, 2)}\n`;
}

export type BibliographyDisplayEntry = {
  documentId: string;
  source: BibliographyEntryReport['source'];
  sourceLabel: string;
  text: string;
};

export function bibliographyDisplayEntries(snapshot: BibliographySnapshot): BibliographyDisplayEntry[] {
  const located: Array<{ report: BibliographyEntryReport; start: number }> = [];
  let cursor = 0;
  for (const report of snapshot.entries) {
    if (report.outcome !== 'generated' || !report.citationKey) continue;
    const bracePosition = snapshot.bibtex.indexOf(`{${report.citationKey},`, cursor);
    const parenthesisPosition = snapshot.bibtex.indexOf(`(${report.citationKey},`, cursor);
    const keyPosition = [bracePosition, parenthesisPosition]
      .filter((position) => position >= cursor)
      .reduce((earliest, position) => Math.min(earliest, position), Number.POSITIVE_INFINITY);
    if (!Number.isFinite(keyPosition)) continue;
    const start = snapshot.bibtex.lastIndexOf('@', keyPosition);
    if (start < cursor) continue;
    located.push({ report, start });
    cursor = keyPosition + report.citationKey.length + 2;
  }
  return located.map(({ report, start }, index) => ({
    documentId: report.documentId,
    source: report.source,
    sourceLabel: bibliographyEntrySourceLabel(report),
    text: snapshot.bibtex.slice(start, located[index + 1]?.start ?? snapshot.bibtex.length).trimEnd(),
  }));
}
