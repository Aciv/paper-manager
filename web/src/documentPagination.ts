import type { Document, DocumentPage } from './api';

// Match the backend's MaxDocumentSearchPage. Reaching the bound is reported
// instead of silently presenting a partial library as the complete result.
export const MAX_DOCUMENT_PAGES = 10_000;

export async function fetchAllDocumentPages(
  fetchPage: (page: number) => Promise<DocumentPage>,
): Promise<Document[]> {
  const documents = new Map<string, Document>();
  for (let pageNumber = 1; pageNumber <= MAX_DOCUMENT_PAGES; pageNumber += 1) {
    const result = await fetchPage(pageNumber);
    result.items.forEach((item) => documents.set(item.id, item));
    const totalPages = Math.max(1, Math.ceil(result.total / Math.max(1, result.pageSize)));
    if (pageNumber >= totalPages || result.items.length === 0) {
      return Array.from(documents.values());
    }
    if (pageNumber === MAX_DOCUMENT_PAGES) {
      throw new Error(`论文数量超过前端可读取上限（${MAX_DOCUMENT_PAGES} 页）`);
    }
  }
  return Array.from(documents.values());
}
