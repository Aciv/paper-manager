export type DocumentSourceState = 'available' | 'deleting' | 'merged';
export type DocumentStageStatus = 'not_started' | 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'skipped';

export type Document = {
  id: string;
  originalFilename: string;
  contentType: string;
  sizeBytes: number;
  doi?: string;
  sourceState: DocumentSourceState;
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

export type PaperReference = {
  referenceId: string;
  citingDocumentId: string;
  doi?: string;
  title?: string;
  authors: PaperAuthor[];
  venue?: string;
  publishedAt?: string;
  volume?: string;
  issue?: string;
  pages?: string;
  sourceUrl?: string;
  metadataSource: 'unknown' | 'extraction' | 'catalog' | 'manual';
  rawReference?: string;
  bibliographyOrder?: number;
  resolutionStatus: DocumentStageStatus;
  resolutionGeneration: number;
  resolutionProvider?: string;
  resolutionErrorCode?: string;
  resolutionErrorMessage?: string;
  downloadAttempt?: string;
  downloadRequestedAt?: string;
  downloadErrorCode?: string;
  downloadErrorMessage?: string;
  downloadUpdatedAt?: string;
  citedDocumentId?: string;
  createdAt: string;
  updatedAt: string;
};

// A citation is one bibliography row owned by its citing document. The API
// exposes the reference fields directly instead of nesting a second object.
export type PaperCitation = PaperReference;

// Partial manual edit of a reference row. Omitted fields stay unchanged; an
// empty string (or empty authors list) clears the field. `publishedAt` accepts
// YYYY-MM-DD or RFC3339 and an empty string clears it.
export type PaperReferenceSourceUpdate = {
  doi?: string;
  title?: string;
  authors?: PaperAuthor[];
  venue?: string;
  publishedAt?: string;
  volume?: string;
  issue?: string;
  pages?: string;
  sourceUrl?: string;
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
  sourceState: DocumentSourceState;
  canonicalDocumentId?: string;
  stageStates: Array<{
    stage: 'markdown' | 'metadata_extraction' | 'source_lookup' | 'reference_lookup';
    status: DocumentStageStatus;
    generation: number;
    updatedAt: string;
    errorCode?: string;
    errorMessage?: string;
  }>;
  stages: ProcessingSpan[];
  referenceProgress?: {
    queued: number;
    running: number;
    completed: number;
    failed: number;
    cancelled: number;
  };
};

// Batched first-paint row: the document, optional bibliography, and optional
// processing view, fetched for up to 200 IDs per request.
export type DocumentProjection = {
  document: Document;
  metadata?: PaperMetadata;
  processing?: DocumentProcessingView;
};

export type DocumentBase = {
  id: string;
  name: string;
  description: string;
  createdAt: string;
  updatedAt: string;
};

export type BibliographyEntryReport = {
  documentId: string;
  title?: string;
  kind: 'paper';
  outcome: 'generated' | 'skipped' | 'failed';
  source?: 'doi_original' | 'local_no_doi' | 'local_doi_failed';
  citationKey?: string;
  missingFields: string[];
  warnings: string[];
  doiErrorCode?: string;
};

export type BibliographySnapshot = {
  baseId: string;
  baseName: string;
  kind: 'paper';
  filename: string;
  bibtex: string;
  entries: BibliographyEntryReport[];
  exportEntries?: Array<{
    source: 'DOI' | 'auto';
    kind: 'article';
    bibtex: string;
  }>;
  generated: number;
  skipped: number;
  failed: number;
};

export type BibliographyTask = {
  id: string;
  baseId: string;
  state: 'queued' | 'running' | 'succeeded' | 'failed';
  snapshot?: BibliographySnapshot;
  errorCode?: string;
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
  sourceState?: DocumentSourceState;
  excludeUnknown?: boolean;
  page?: number;
};

export type DocumentURLImport = {
  url: string;
  doi?: string;
  filename?: string;
};

export type DocumentBatchItemStatus = 'accepted' | 'duplicate' | 'rejected' | 'failed';
export type DocumentBatchItemResult = {
  index: number;
  filename?: string;
  url?: string;
  status: DocumentBatchItemStatus;
  document?: Document;
  processingQueued?: boolean;
  retryable?: boolean;
  error?: string;
};
export type DocumentBatchResult = {
  items: DocumentBatchItemResult[];
  stopped?: boolean;
  error?: string;
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

export type NormalizedDocumentRegion = { x0: number; y0: number; x1: number; y1: number };
export type DocumentSelectionProcess =
  | { kind: 'text'; text: string; pageNumber?: number; region?: NormalizedDocumentRegion }
  | { kind: 'block'; blockId: string }
  | { kind: 'area'; pageNumber: number; region: NormalizedDocumentRegion; image: Blob };
export type DocumentSelectionResult = {
  kind: DocumentSelectionProcess['kind'];
  pageNumber?: number;
  blockId?: string;
  textRunes?: number;
  imageWidth?: number;
  imageHeight?: number;
  message: string;
};

export type DocumentOutlineSource = 'pdf_outline' | 'markdown_headings' | 'none' | 'unavailable';
export type DocumentOutlineSection = {
  ordinal: number;
  title: string;
  level: number;
  parentOrdinal: number;
  startPage: number;
  startX: number;
  startY: number;
  endPage: number;
  endX: number;
  endY: number;
  startBlockOrdinal: number;
  endBlockOrdinal: number;
  markdownCodepointStart: number;
  markdownCodepointEnd: number;
};
export type DocumentOutline = {
  schemaVersion: number;
  source: DocumentOutlineSource;
  sections: DocumentOutlineSection[];
  diagnostics?: string[];
};
export type DocumentReadingStructure = {
  schemaVersion: number;
  documentId: string;
  recognitionGeneration: number;
  fingerprint: string;
  outline: DocumentOutline;
  diagnostics?: string[];
};

// Kept in sync with MaxDocumentReadingNoteBytes on the Go side.
export const MAX_READING_NOTE_BYTES = 60 * 1024 * 1024;

export type DocumentReadingNote = {
  documentId: string;
  directory: ReadingNoteDirectory;
  sizeBytes: number;
  revision: number;
  createdAt: string;
  updatedAt: string;
};
export type ReadingNoteSectionEntry = {
  id: string;
  parentId?: string;
  order: number;
  level: number;
  title: string;
  contentRevision: number;
  sizeBytes: number;
};
export type ReadingNoteDirectory = { schemaVersion: number; sections: ReadingNoteSectionEntry[] };
export type ReadingNoteSection = {
  documentId: string;
  revision: number;
  section: ReadingNoteSectionEntry;
  markdown: string;
  lineCount: number;
};
// Text offsets and lengths are Unicode code-point positions inside one section,
// relative to the content produced by earlier operations in the same sequence.
// Replace is composed as deleteText followed by insertText. Primitive operations
// keep their op-specific fields (`text`, `markdown`); the unified `content`
// field belongs only to the agent-facing ReadingNoteEdit layer below.
export type ReadingNoteOperation =
  | { op: 'insertText'; sectionId: string; offset: number; text: string }
  | { op: 'deleteText'; sectionId: string; offset: number; length: number }
  | { op: 'addSection'; parentId?: string; afterId?: string; markdown: string }
  | { op: 'deleteSection'; sectionId: string };
export type ReadingNoteOperations = { expectedRevision: number; operations: ReadingNoteOperation[] };
// Agent-facing edits are resolved against the expectedRevision snapshot.
// Line and column are 1-based; an omitted column means line start/end.
// `content` is op-defined: insertion/replacement text, or a complete section
// Markdown fragment for replaceSection/addSection.
export type ReadingNotePosition = { line: number; column?: number };
export type ReadingNoteEdit =
  | { op: 'replaceText'; sectionId: string; oldText: string; newText: string; lineStart?: number; lineEnd?: number }
  | { op: 'replaceRange'; sectionId: string; start: ReadingNotePosition; end: ReadingNotePosition; content: string }
  | { op: 'insertAt'; sectionId: string; position: ReadingNotePosition; content: string }
  | { op: 'appendText'; sectionId: string; content: string }
  | { op: 'replaceSection'; sectionId: string; content: string }
  | { op: 'addSection'; parentId?: string; afterId?: string; content: string }
  | { op: 'deleteSection'; sectionId: string };
export type ReadingNoteEdits = { expectedRevision: number; edits: ReadingNoteEdit[] };
export type ReadingNoteEditResult = {
  documentId: string;
  revision: number;
  directory: ReadingNoteDirectory;
  changedSections: ReadingNoteSectionEntry[];
  deletedSectionIds?: string[];
};

const baseUrl = (import.meta.env.VITE_API_URL ?? '/api/document').replace(/\/$/, '');
const paperBaseUrl = baseUrl.endsWith('/document') ? `${baseUrl.slice(0, -'/document'.length)}/paper` : '/api/paper';
const referenceBaseUrl = baseUrl.endsWith('/document') ? `${baseUrl.slice(0, -'/document'.length)}/references` : '/api/references';

// APIError keeps the HTTP status available to callers that need to react to
// 404/409/503 differently, while remaining a normal Error for message-only UI.
export class APIError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'APIError';
    this.status = status;
  }
}

async function responseErrorMessage(response: Response, contentType: string): Promise<string> {
  const body = contentType.includes('application/json')
    ? await response.json().catch(() => null) as { error?: { message?: string } } | null
    : null;
  if (body?.error?.message) return body.error.message;
  // Non-JSON failures (a proxy, an extension, a truncated response) would
  // otherwise surface as a bare statusText like "Forbidden"; keep the status
  // and a snippet of the body so the cause is visible.
  const detail = contentType.includes('application/json') ? '' : (await response.text().catch(() => '')).replace(/\s+/g, ' ').trim().slice(0, 120);
  return `HTTP ${response.status} ${response.statusText}${detail ? ` · ${detail}` : ''}`.trim();
}

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
  throw new APIError(response.status, await responseErrorMessage(response, contentType));
}

async function requestText(pathname: string): Promise<string> {
  const response = await fetch(`${baseUrl}${pathname}`);
  if (response.ok) return response.text();
  const contentType = response.headers.get('content-type') ?? '';
  throw new APIError(response.status, await responseErrorMessage(response, contentType));
}

// A blank note is persisted without sections. Older manifests may serialize
// that as null, so normalize manifest-shaped responses at the API boundary.
function normalizeReadingNoteDirectory(
  directory: ReadingNoteDirectory | null | undefined,
): ReadingNoteDirectory {
  const sections = directory?.sections;
  return {
    schemaVersion: directory?.schemaVersion ?? 1,
    sections: Array.isArray(sections) ? sections : [],
  };
}

function normalizeDocumentReadingNote(note: DocumentReadingNote): DocumentReadingNote {
  return { ...note, directory: normalizeReadingNoteDirectory(note.directory) };
}

function normalizeReadingNoteEditResult(result: ReadingNoteEditResult): ReadingNoteEditResult {
  return { ...result, directory: normalizeReadingNoteDirectory(result.directory) };
}

export const api = {
  searchDocuments: (search: DocumentSearch): Promise<DocumentPage> => {
    const query = new URLSearchParams();
    if (search.id) query.set('id', search.id);
    if (search.doi) query.set('doi', search.doi);
	if (search.sourceState) query.set('sourceState', search.sourceState);
    if (search.excludeUnknown) query.set('excludeUnknown', 'true');
    query.set('page', String(search.page ?? 1));
    return request<DocumentPage>(`/search?${query.toString()}`);
  },
  upload: (file: File, doi: string): Promise<{ document: Document }> => {
    const body = new FormData();
    body.append('file', file);
    body.append('doi', doi);
    return request<{ document: Document }>('/upload', { method: 'POST', body });
  },
  uploadBatch: (files: File[]): Promise<DocumentBatchResult> => {
    const body = new FormData();
    files.forEach((file) => body.append('files', file));
    return request<DocumentBatchResult>('/batch-upload', { method: 'POST', body });
  },
  importFromURL: (payload: DocumentURLImport): Promise<{ document: Document }> => request<{ document: Document }>(
    '/import-url',
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) },
  ),
  importFromURLs: (payload: DocumentURLImport[]): Promise<DocumentBatchResult> => request<DocumentBatchResult>(
    '/batch-import-url',
    { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ urls: payload }) },
  ),
  downloadReferencePDF: (referenceID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(
    referenceBaseUrl,
    `/${encodeURIComponent(referenceID)}/download-pdf`,
    { method: 'POST' },
  ),
  resolvePaperReference: (referenceID: string, resolvedDocumentID: string): Promise<{ reference: PaperReference }> => requestFrom<{ reference: PaperReference }>(
    referenceBaseUrl,
    `/${encodeURIComponent(referenceID)}/resolve`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ resolvedDocumentId: resolvedDocumentID }),
    },
  ),
  deletePaperReference: (referenceID: string): Promise<void> => requestFrom<void>(
    referenceBaseUrl,
    `/${encodeURIComponent(referenceID)}`,
    { method: 'DELETE' },
  ),
  updatePaperReferenceSource: (
    referenceID: string,
    update: PaperReferenceSourceUpdate,
  ): Promise<{ reference: PaperReference }> => requestFrom<{ reference: PaperReference }>(
    referenceBaseUrl,
    `/${encodeURIComponent(referenceID)}`,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(update),
    },
  ),
  rematchPaperReference: (referenceID: string): Promise<{ reference: PaperReference; linked: boolean }> => requestFrom<{ reference: PaperReference; linked: boolean }>(
    referenceBaseUrl,
    `/${encodeURIComponent(referenceID)}/rematch`,
    { method: 'POST' },
  ),
  updateDocument: (id: string, payload: { originalFilename?: string; doi?: string }): Promise<{ document: Document }> => request<{ document: Document }>(
    `/${encodeURIComponent(id)}`,
    { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) },
  ),
  cancelDocument: (id: string): Promise<{ document: Document }> => request<{ document: Document }>(`/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  retryDocument: (id: string): Promise<{ document: Document }> => request<{ document: Document }>(`/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  getDocumentProcessing: (id: string): Promise<{ processing: DocumentProcessingView }> => request<{ processing: DocumentProcessingView }>(`/${encodeURIComponent(id)}/processing`),
  getDocumentProjections: async (
    documentIDs: string[],
    includeMetadata: boolean,
    includeProcessing: boolean,
  ): Promise<DocumentProjection[]> => {
    const items: DocumentProjection[] = [];
    for (let start = 0; start < documentIDs.length; start += 200) {
      const documentIds = documentIDs.slice(start, start + 200);
      const result = await request<{ items: DocumentProjection[] }>('/projections', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ documentIds, includeMetadata, includeProcessing }),
      });
      items.push(...result.items);
    }
    return items;
  },
  deleteDocument: (id: string): Promise<void> => request<void>(`/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  listDocumentBlocks: (id: string, page?: number): Promise<{ items: Block[] }> => {
    const query = page ? `?page=${encodeURIComponent(String(page))}` : '';
    return request<{ items: Block[] }>(`/${encodeURIComponent(id)}/blocks${query}`);
  },
  getDocumentMarkdown: (id: string): Promise<string> => requestText(`/${encodeURIComponent(id)}/markdown`),
  getDocumentStructure: (id: string): Promise<DocumentReadingStructure> => request<DocumentReadingStructure>(
    `/${encodeURIComponent(id)}/structure`,
  ),
  getDocumentReadingNote: async (id: string): Promise<DocumentReadingNote> => normalizeDocumentReadingNote(
    await request<DocumentReadingNote>(`/${encodeURIComponent(id)}/reading-note`),
  ),
  getDocumentReadingNoteSection: (id: string, sectionId: string): Promise<ReadingNoteSection> => request<ReadingNoteSection>(
    `/${encodeURIComponent(id)}/reading-note/sections/${encodeURIComponent(sectionId)}`,
  ),
  getDocumentReadingNoteSectionContent: (id: string, sectionId: string): Promise<string> => requestText(
    `/${encodeURIComponent(id)}/reading-note/sections/${encodeURIComponent(sectionId)}/content`,
  ),
  uploadDocumentReadingNoteFile: async (id: string, file: File, expectedRevision: number): Promise<DocumentReadingNote> => {
    const body = new FormData();
    body.set('file', file);
    return normalizeDocumentReadingNote(await request<DocumentReadingNote>(
      `/${encodeURIComponent(id)}/reading-note?expectedRevision=${encodeURIComponent(String(expectedRevision))}`,
      { method: 'PUT', body },
    ));
  },
  uploadDocumentReadingNoteSectionFile: async (
    id: string, sectionId: string, file: File, expectedRevision: number,
  ): Promise<ReadingNoteEditResult> => {
    const body = new FormData();
    body.set('file', file);
    return normalizeReadingNoteEditResult(await request<ReadingNoteEditResult>(
      `/${encodeURIComponent(id)}/reading-note/sections/${encodeURIComponent(sectionId)}?expectedRevision=${encodeURIComponent(String(expectedRevision))}`,
      { method: 'PUT', body },
    ));
  },
  applyDocumentReadingNoteOperations: async (id: string, requestBody: ReadingNoteOperations): Promise<ReadingNoteEditResult> => normalizeReadingNoteEditResult(
    await request<ReadingNoteEditResult>(
      `/${encodeURIComponent(id)}/reading-note/operations`,
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(requestBody) },
    ),
  ),
  applyDocumentReadingNoteEdits: async (id: string, requestBody: ReadingNoteEdits): Promise<ReadingNoteEditResult> => normalizeReadingNoteEditResult(
    await request<ReadingNoteEditResult>(
      `/${encodeURIComponent(id)}/reading-note/edits`,
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(requestBody) },
    ),
  ),
  putDocumentReadingNote: async (id: string, markdown: string, expectedRevision: number): Promise<DocumentReadingNote> => normalizeDocumentReadingNote(
    await request<DocumentReadingNote>(
      `/${encodeURIComponent(id)}/reading-note?expectedRevision=${encodeURIComponent(String(expectedRevision))}`,
      { method: 'PUT', headers: { 'Content-Type': 'text/markdown; charset=utf-8' }, body: markdown },
    ),
  ),
  deleteDocumentReadingNote: (id: string, expectedRevision: number): Promise<void> => request<void>(
    `/${encodeURIComponent(id)}/reading-note?expectedRevision=${encodeURIComponent(String(expectedRevision))}`,
    { method: 'DELETE' },
  ),
  getDocumentReadingNoteExport: (id: string): Promise<string> => requestText(
    `/${encodeURIComponent(id)}/reading-note/export`,
  ),
  processDocumentSelection: (id: string, selection: DocumentSelectionProcess, signal?: AbortSignal): Promise<{ result: DocumentSelectionResult }> => {
    const body = new FormData();
    body.set('document_id', id);
    body.set('kind', selection.kind);
    if (selection.kind === 'text') {
      body.set('text', selection.text);
      if (selection.pageNumber) body.set('page_number', String(selection.pageNumber));
      if (selection.region) {
        body.set('x0', String(selection.region.x0));
        body.set('y0', String(selection.region.y0));
        body.set('x1', String(selection.region.x1));
        body.set('y1', String(selection.region.y1));
      }
    } else if (selection.kind === 'block') {
      body.set('block_id', selection.blockId);
    } else {
      body.set('page_number', String(selection.pageNumber));
      body.set('x0', String(selection.region.x0));
      body.set('y0', String(selection.region.y0));
      body.set('x1', String(selection.region.x1));
      body.set('y1', String(selection.region.y1));
      body.set('image', selection.image, `selection-page-${selection.pageNumber}.png`);
    }
    return request<{ result: DocumentSelectionResult }>(`/${encodeURIComponent(id)}/selection/process`, { method: 'POST', body, signal });
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
  retryPaperExtraction: (documentID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/info/extraction/retry`, { method: 'POST' }),
  retryPaperCatalog: (documentID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/info/catalog/retry`, { method: 'POST' }),
  retryPaperReferences: (documentID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/info/reference/retry`, { method: 'POST' }),
  cancelPaperInfo: (documentID: string): Promise<{ accepted: boolean }> => requestFrom<{ accepted: boolean }>(paperBaseUrl, `/${encodeURIComponent(documentID)}/info/cancel`, { method: 'POST' }),
  listDocumentBases: (): Promise<{ items: DocumentBase[] }> => request<{ items: DocumentBase[] }>('/bases'),
  listDocumentBaseDocuments: (baseID: string, page = 1, excludeUnknown = false): Promise<DocumentPage> => {
    const query = new URLSearchParams({ page: String(page) });
    if (excludeUnknown) query.set('excludeUnknown', 'true');
    return request<DocumentPage>(`/bases/${encodeURIComponent(baseID)}/documents?${query.toString()}`);
  },
  createDocumentBase: (payload: { name: string; description?: string }): Promise<{ documentBase: DocumentBase }> => request<{ documentBase: DocumentBase }>('/bases', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
  }),
  addDocumentToBase: (baseID: string, documentID: string): Promise<void> => request<void>(`/bases/${encodeURIComponent(baseID)}/documents/${encodeURIComponent(documentID)}`, { method: 'PUT' }),
  removeDocumentFromBase: (baseID: string, documentID: string): Promise<void> => request<void>(`/bases/${encodeURIComponent(baseID)}/documents/${encodeURIComponent(documentID)}`, { method: 'DELETE' }),
  generateBaseBibliography: (baseID: string, signal?: AbortSignal): Promise<{ task: BibliographyTask }> => request<{ task: BibliographyTask }>(
    `/bases/${encodeURIComponent(baseID)}/bibliography`,
    { method: 'POST', signal },
  ),
  getBaseBibliographyTask: (baseID: string, taskID: string, signal?: AbortSignal): Promise<{ task: BibliographyTask }> => request<{ task: BibliographyTask }>(
    `/bases/${encodeURIComponent(baseID)}/bibliography/${encodeURIComponent(taskID)}`,
    { signal },
  ),
};

// Kept for PdfReader compatibility. The current Go API intentionally exposes
// only upload/search/delete; enable the reader after a protected content route
// is introduced on the backend.
export const contentUrl = (id: string) => `${baseUrl}/${encodeURIComponent(id)}/content`;
