import type { DocumentStageStatus, PaperCitation, PaperReference } from './api';

// Both the reading library and the management console render the same
// reference rows. Keep the classification and one-line projections here so
// the two surfaces cannot disagree about what "库内"/"未解析" means.

export type CitationGroup = 'outgoing' | 'incoming';
export type CitationFilter = 'all' | 'linked' | 'pending' | 'failed';
export type CitationBadgeTone = 'linked' | 'pending' | 'failed' | 'idle';

export const CITATION_STATUS_LABELS: Record<DocumentStageStatus, string> = {
  not_started: '未解析',
  queued: '解析中',
  running: '解析中',
  succeeded: '已解析',
  failed: '解析失败',
  cancelled: '已取消',
  skipped: '已跳过',
};

export function citationLabel(citation: PaperCitation, index: number) {
  const reference = citation.rawReference?.replace(/^[\s\-–—•·]+/, '').trim();
  return reference || `第 ${index + 1} 条引用`;
}

// The catalog stores authors as a list; citation cards show a scannable
// one-line projection and lean on the detail dialog for the full list.
export function citationAuthorLine(authors: PaperReference['authors']) {
  const names = authors.map((author) => author.name).filter(Boolean);
  if (names.length === 0) return '';
  return names.length > 3 ? `${names.slice(0, 3).join('、')} 等` : names.join('、');
}

export function citationMetaLine(authors: PaperReference['authors'], venue?: string, publishedAt?: string) {
  return [citationAuthorLine(authors), venue, publishedAt?.slice(0, 4)]
    .filter((part): part is string => Boolean(part && part.trim()))
    .join(' · ');
}

export function citationBadge(citation: PaperCitation): { label: string; tone: CitationBadgeTone } {
  if (citation.citedDocumentId) return { label: '库内', tone: 'linked' };
  if (citation.resolutionStatus === 'queued' || citation.resolutionStatus === 'running') {
    return { label: '解析中', tone: 'pending' };
  }
  if (citation.resolutionStatus === 'failed') return { label: '解析失败', tone: 'failed' };
  if (citation.resolutionStatus === 'succeeded') return { label: '未匹配', tone: 'idle' };
  return { label: CITATION_STATUS_LABELS[citation.resolutionStatus] ?? '未解析', tone: 'idle' };
}

export function citationMatchesFilter(citation: PaperCitation, filter: CitationFilter) {
  if (filter === 'all') return true;
  if (filter === 'linked') return Boolean(citation.citedDocumentId);
  if (filter === 'failed') return !citation.citedDocumentId && citation.resolutionStatus === 'failed';
  return !citation.citedDocumentId && citation.resolutionStatus !== 'failed';
}

export function citationFilterCounts(citations: PaperCitation[]) {
  return citations.reduce(
    (counts, citation) => {
      if (citation.citedDocumentId) counts.linked += 1;
      else if (citation.resolutionStatus === 'failed') counts.failed += 1;
      else counts.pending += 1;
      return counts;
    },
    { linked: 0, pending: 0, failed: 0 },
  );
}
