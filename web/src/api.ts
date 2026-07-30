export type DocumentStatus = 'uploaded' | 'queued' | 'processing' | 'cancelled' | 'deleting' | 'ready' | 'failed';

export type Document = {
  id: string;
  originalFilename: string;
  contentType: string;
  sizeBytes: number;
  doi?: string;
  status: DocumentStatus;
  errorMessage?: string;
  createdAt: string;
  updatedAt: string;
};

export type PaperAuthor = { name: string; affiliation?: string };

export type PaperMetadata = {
  documentId: string;
  title?: string;
  abstract?: string;
  authors: PaperAuthor[];
  tags: string[];
  sourceName?: string;
  sourceUrl?: string;
  venue?: string;
  publishedAt?: string;
  volume?: string;
  issue?: string;
  pages?: string;
  language?: string;
  metadataSource: 'unknown' | 'extraction' | 'catalog' | 'manual';
};

export type PaperMetadataUpdate = Partial<Omit<PaperMetadata, 'documentId' | 'metadataSource'>>;

export type PaperCitation = {
  citingDocumentId: string;
  citedDocumentId: string;
  rawReference: string;
  createdAt: string;
};

export type ProcessingSpanStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';
export type ProcessingSpan = {
  id: string;
  documentId: string;
  attempt: number;
  kind: string;
  scope: string;
  scopeId?: string;
  parentSpanId?: string;
  status: ProcessingSpanStatus;
  retryCount: number;
  errorCode?: string;
  errorReason?: string;
  startedAt: string;
  finishedAt?: string;
};
export type DocumentProcessingView = {
  documentId: string;
  documentStatus: DocumentStatus;
  latestAttempt: number;
  stages: ProcessingSpan[];
  referenceProgress?: {
    queued: number;
    running: number;
    completed: number;
    cancelled: number;
  };
};

export type DocumentBase = {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  updatedAt: string;
};

export type DocumentPage = {
  items: Document[];
  page: number;
  pageSize: number;
  total: number;
};

export type DocumentSearch = {
  id?: string;
  doi?: string;
  page?: number;
};

export type PDFBlockLocation = {
  pageNumber?: number;
  pageWidth?: number;
  pageHeight?: number;
  bboxX0?: number;
  bboxY0?: number;
  bboxX1?: number;
  bboxY1?: number;
  markdownCodepointStart?: number;
  markdownCodepointEnd?: number;
};

export type Block = {
  id: string;
  type: string;
  text: string;
  url?: string;
  ordinal: number;
  markdownSha256?: string;
  location?: PDFBlockLocation;
};

const baseUrl = (import.meta.env.VITE_API_URL ?? '/api/document').replace(/\/$/, '');
const paperBaseUrl = baseUrl.endsWith('/document') ? `${baseUrl.slice(0, -'/document'.length)}/paper` : '/api/paper';

async function request<T>(pathname: string, options?: RequestInit): Promise<T> {
  return requestFrom<T>(baseUrl, pathname, options);
}

async function requestFrom<T>(resourceBaseUrl: string, pathname: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${resourceBaseUrl}${pathname}`, options);
  const contentType = response.headers.get('content-type') ?? '';
  if (response.ok) {
    if (response.status === 204) return undefined as T;
    if (!contentType.includes('application/json')) {
      throw new Error(`API 返回了非 JSON 响应（HTTP ${response.status}，${contentType || '未知内容类型'}）。请确认 Go API 已启动且开发代理配置正确。`);
    }
    return response.json() as Promise<T>;
  }
  const body = contentType.includes('application/json')
    ? await response.json().catch(() => null) as { error?: { message?: string } } | null
    : null;
  throw new Error(body?.error?.message ?? response.statusText);
}

export const api = {
  searchDocuments: (search: DocumentSearch): Promise<DocumentPage> => {
    const query = new URLSearchParams();
    if (search.id) query.set('id', search.id);
    if (search.doi) query.set('doi', search.doi);
    query.set('page', String(search.page ?? 1));
    return request<DocumentPage>(`/search?${query.toString()}`);
  },
  upload: (file: File, doi: string): Promise<{ document: Document }> => {
    const body = new FormData();
    body.append('file', file);
    body.append('doi', doi);
    return request<{ document: Document }>('/upload', { method: 'POST', body });
  },
  updateDocument: (id: string, payload: { originalFilename?: string; doi?: string }): Promise<{ document: Document }> => request<{ document: Document }>(
    `/${encodeURIComponent(id)}`,
    { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) },
  ),
  cancelDocument: (id: string): Promise<{ document: Document }> => request<{ document: Document }>(`/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  retryDocument: (id: string): Promise<{ document: Document }> => request<{ document: Document }>(`/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  getDocumentProcessing: (id: string): Promise<{ processing: DocumentProcessingView }> => request<{ processing: DocumentProcessingView }>(`/${encodeURIComponent(id)}/processing`),
  deleteDocument: (id: string): Promise<void> => request<void>(`/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  listDocumentBlocks: (id: string, page?: number): Promise<{ items: Block[] }> => {
    const query = page ? `?page=${encodeURIComponent(String(page))}` : '';
    return request<{ items: Block[] }>(`/${encodeURIComponent(id)}/blocks${query}`);
  },
  getPaperMetadata: (documentID: string): Promise<{ paperMetadata: PaperMetadata }> => requestFrom<{ paperMetadata: PaperMetadata }>(paperBaseUrl, `/${encodeURIComponent(documentID)}`),
  updatePaperMetadata: (documentID: string, payload: PaperMetadataUpdate): Promise<{ paperMetadata: PaperMetadata }> => requestFrom<{ paperMetadata: PaperMetadata }>(
    paperBaseUrl, `/${encodeURIComponent(documentID)}`,
    { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) },
  ),
  searchPaperMetadata: (search: { title?: string; author?: string; tags?: string[]; tagMode?: 'all' | 'any'; page?: number }): Promise<{ items: PaperMetadata[]; page: number; pageSize: number; total: number }> => {
    const query = new URLSearchParams();
    if (search.title) query.set('title', search.title);
    if (search.author) query.set('author', search.author);
    search.tags?.forEach((tag) => query.append('tag', tag));
    if (search.tagMode) query.set('tagMode', search.tagMode);
    query.set('page', String(search.page ?? 1));
    return requestFrom(paperBaseUrl, `/search?${query.toString()}`);
  },
  listPaperCitations: (documentID: string): Promise<{ items: PaperCitation[] }> => requestFrom<{ items: PaperCitation[] }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/citations`),
  listPaperCitedBy: (documentID: string): Promise<{ items: PaperCitation[] }> => requestFrom<{ items: PaperCitation[] }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/cited-by`),
  cancelPaperInfo: (documentID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/info/cancel`, { method: 'POST' }),
  listDocumentBases: (): Promise<{ items: DocumentBase[] }> => request<{ items: DocumentBase[] }>('/bases'),
  createDocumentBase: (payload: { name: string; description?: string }): Promise<{ documentBase: DocumentBase }> => request<{ documentBase: DocumentBase }>('/bases', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
  }),
  addDocumentToBase: (baseID: string, documentID: string): Promise<void> => request<void>(`/bases/${encodeURIComponent(baseID)}/documents/${encodeURIComponent(documentID)}`, { method: 'PUT' }),
  removeDocumentFromBase: (baseID: string, documentID: string): Promise<void> => request<void>(`/bases/${encodeURIComponent(baseID)}/documents/${encodeURIComponent(documentID)}`, { method: 'DELETE' }),
};

// Kept for PdfReader compatibility. The current Go API intentionally exposes
// only upload/search/delete; enable the reader after a protected content route
// is introduced on the backend.
export const contentUrl = (id: string) => `${baseUrl}/${encodeURIComponent(id)}/content`;
