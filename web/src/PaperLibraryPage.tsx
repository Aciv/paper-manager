import { ChangeEvent, lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { api, Document, DocumentBase, PaperCitation, PaperMetadata, PaperReference } from './api';
import {
  CITATION_STATUS_LABELS,
  citationBadge,
  citationDownloadErrorMessage,
  citationDownloadInFlight,
  citationFilterCounts,
  citationLabel,
  citationMatchesFilter,
  citationMetaLine,
  type CitationFilter,
  type CitationGroup,
} from './citations';
import { LibrarySelect } from './LibrarySelect';
import { ReferenceEditorDialog } from './ReferenceEditorDialog';
import { CitationExportDialog } from './CitationExportDialog';
import { TranslationPanel } from './TranslationPanel';
import type { DoiCitationTarget } from './doiCitation';
import type { TranslationRequestSeed } from './translation/index.ts';
import { fetchAllDocumentPages } from './documentPagination';
import { isDocumentRead, localFileReadingKey, subscribeReadingState, transferReadingRecord } from './readingState';
import './PaperLibraryPage.css';

// PDF.js is the heaviest dependency in the app; load it only once a paper is
// actually opened, not while the catalog or an empty scope is showing.
const PdfReader = lazy(() => import('./PdfReader').then((module) => ({ default: module.PdfReader })));

function documentTitle(document: Document, metadata?: PaperMetadata) {
  return metadata?.title || document.originalFilename || '未命名论文';
}

function formatAuthors(authors: PaperMetadata['authors']) {
  return authors.map((author) => author.name).filter(Boolean).join('、') || '未识别';
}

// A picked folder reports each file's path relative to it; the plain name is the
// fallback when a browser leaves `webkitRelativePath` empty.
function localPath(file: File): string {
  return (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
}

const LEFT_MENUS = ['catalog', 'base', 'outline'] as const;
const RIGHT_MENUS = ['info', 'citations', 'translation'] as const;
type LeftMenu = typeof LEFT_MENUS[number];
type RightMenu = typeof RIGHT_MENUS[number];

const LEFT_MENU_LABELS: Record<LeftMenu, string> = { catalog: '论文目录', base: '类别', outline: 'PDF 目录' };
const RIGHT_MENU_LABELS: Record<RightMenu, string> = { info: '论文信息', citations: '引用信息', translation: '翻译' };

type LibraryTranslationRequest = TranslationRequestSeed & {
  requestId: string;
  // false = seed the panel's source only; true = run translation immediately.
  autoTranslate: boolean;
};

function newTranslationRequestId(): string {
  return window.crypto?.randomUUID?.()
    ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

type LibraryReadFilter = 'all' | 'read' | 'unread';
type LibrarySort = 'added' | 'title' | 'year' | 'author';

function metadataYear(metadata?: PaperMetadata): string {
  return metadata?.publishedAt?.slice(0, 4) ?? '';
}

type LibraryView = {
  filtersOpen: boolean;
  authorFilter: string;
  venueFilter: string;
  tagFilter: string;
  yearFilter: string;
  readFilter: LibraryReadFilter;
  sortBy: LibrarySort;
  sortDesc: boolean;
};

const LIBRARY_VIEW_STORAGE = 'papersManager.library.view.v1';

// Persisted per device: an active filter or sort order should survive a
// reload, a trip to the console, and opening another paper in a new tab.
function readLibraryView(): LibraryView {
  const fallback: LibraryView = {
    filtersOpen: false,
    authorFilter: '',
    venueFilter: '',
    tagFilter: '',
    yearFilter: '',
    readFilter: 'all',
    sortBy: 'added',
    sortDesc: true,
  };
  try {
    const raw = window.localStorage.getItem(LIBRARY_VIEW_STORAGE);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const text = (value: unknown) => (typeof value === 'string' ? value : '');
    return {
      filtersOpen: parsed.filtersOpen === true,
      authorFilter: text(parsed.authorFilter),
      venueFilter: text(parsed.venueFilter),
      tagFilter: text(parsed.tagFilter),
      yearFilter: text(parsed.yearFilter),
      readFilter: parsed.readFilter === 'read' || parsed.readFilter === 'unread' ? parsed.readFilter : 'all',
      sortBy: parsed.sortBy === 'title' || parsed.sortBy === 'year' || parsed.sortBy === 'author' ? parsed.sortBy : 'added',
      sortDesc: typeof parsed.sortDesc === 'boolean' ? parsed.sortDesc : true,
    };
  } catch {
    return fallback;
  }
}

// The filters below run on the whole library, so the catalog loads every page
// once instead of paginating server-side. The shared loader reports an explicit
// error at the backend's page bound instead of returning a partial catalog.
async function fetchAllDocuments(baseID: string): Promise<Document[]> {
  return fetchAllDocumentPages((pageNumber) => (
    baseID
      ? api.listDocumentBaseDocuments(baseID, pageNumber, true)
      : api.searchDocuments({ page: pageNumber, excludeUnknown: true })
  ));
}

// Metadata is loaded in bounded batches instead of one request per document.
// A failed batch only disables metadata filters for that chunk's rows.
async function fetchMetadataMap(documents: Document[]): Promise<Record<string, PaperMetadata>> {
  const metadataByID: Record<string, PaperMetadata> = {};
  if (documents.length === 0) return metadataByID;
  try {
    const projections = await api.getDocumentProjections(
      documents.map((documentItem) => documentItem.id), true, false,
    );
    projections.forEach((projection) => {
      if (projection.metadata) metadataByID[projection.document.id] = projection.metadata;
    });
  } catch {
    // Missing metadata only disables the metadata-based filters.
  }
  return metadataByID;
}

export function PaperLibraryPage({ initialBaseID, initialDocumentID }: { initialBaseID?: string; initialDocumentID?: string } = {}) {
  const [leftCollapsed, setLeftCollapsed] = useState(false);
  const [rightCollapsed, setRightCollapsed] = useState(false);
  // Side column widths: draggable via the handle on each column's inner edge,
  // remembered across reloads.
  const [leftWidth, setLeftWidth] = useState(() => Math.max(180, Number(window.localStorage.getItem('library.leftWidth')) || 290));
  const [rightWidth, setRightWidth] = useState(() => Math.max(200, Number(window.localStorage.getItem('library.rightWidth')) || 320));
  const [resizing, setResizing] = useState(false);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  // Compact windows and tablets: the columns become overlay sheets so the PDF
  // keeps the full width and height instead of collapsing in a stacked flow.
  const [narrow, setNarrow] = useState(() => window.matchMedia('(max-width: 1080px)').matches);
  useEffect(() => {
    const query = window.matchMedia('(max-width: 1080px)');
    const sync = () => setNarrow(query.matches);
    query.addEventListener('change', sync);
    return () => query.removeEventListener('change', sync);
  }, []);
  useEffect(() => {
    if (narrow) { setLeftCollapsed(true); setRightCollapsed(true); }
  }, [narrow]);
  useEffect(() => {
    const onResize = () => setViewportWidth(window.innerWidth);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  const [leftMenu, setLeftMenu] = useState<LeftMenu>('catalog');
  const [rightMenu, setRightMenu] = useState<RightMenu>('info');
  const [outlineSlot, setOutlineSlot] = useState<HTMLDivElement | null>(null);
  const [bases, setBases] = useState<DocumentBase[]>([]);
  const [baseID, setBaseID] = useState(initialBaseID ?? '');
  // Local papers are PDFs the user opens straight from a folder: they never
  // reach the backend, so their scope is tracked apart from the stored ones.
  const [localMode, setLocalMode] = useState(false);
  const [localFiles, setLocalFiles] = useState<File[]>([]);
  const [localSelected, setLocalSelected] = useState<File | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  // The full scope (all pages + metadata) feeds client-side filtering/sorting.
  // This is deliberately the whole small library, not the current server page.
  const [items, setItems] = useState<Document[]>([]);
  const [metadataByID, setMetadataByID] = useState<Record<string, PaperMetadata>>({});
  const [selected, setSelected] = useState<Document>();
  const pendingDocumentID = useRef(initialDocumentID ?? '');
  const [metadata, setMetadata] = useState<PaperMetadata>();
  const [citations, setCitations] = useState<PaperCitation[]>([]);
  const [citedBy, setCitedBy] = useState<PaperCitation[]>([]);
  const [citationsLoading, setCitationsLoading] = useState(false);
  // One full-height list at a time: stacking outgoing and incoming split the
  // panel in half and left both lists nearly unreadable.
  const [citationGroup, setCitationGroup] = useState<CitationGroup>('outgoing');
  const [citationFilter, setCitationFilter] = useState<CitationFilter>('all');
  const [activeCitationIDs, setActiveCitationIDs] = useState<string[]>([]);
  const [citationActionBusy, setCitationActionBusy] = useState('');
  const [editingCitation, setEditingCitation] = useState<PaperCitation>();
  const [exportCitation, setExportCitation] = useState<DoiCitationTarget>();
  const [translationRequest, setTranslationRequest] = useState<LibraryTranslationRequest>();
  const [citationPaperInfoOpen, setCitationPaperInfoOpen] = useState(false);
  const [citationPaperInfo, setCitationPaperInfo] = useState<PaperMetadata>();
  const [citationReference, setCitationReference] = useState<PaperReference>();
  const [citationPaperInfoLoading, setCitationPaperInfoLoading] = useState(false);
  const [citationPaperInfoError, setCitationPaperInfoError] = useState('');
  const [citationRawReference, setCitationRawReference] = useState('');
  const [citationRawReferenceVisible, setCitationRawReferenceVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  // Only shown when there is something to report (errors, processing results);
  // routine progress stays off screen.
  const [message, setMessage] = useState('');
  // Client-side catalog filters and sort (the whole scope is already loaded).
  // The panel and its choices come from the per-device view saved last time.
  const [initialView] = useState(readLibraryView);
  const [filtersOpen, setFiltersOpen] = useState(initialView.filtersOpen);
  const [authorFilter, setAuthorFilter] = useState(initialView.authorFilter);
  const [venueFilter, setVenueFilter] = useState(initialView.venueFilter);
  const [tagFilter, setTagFilter] = useState(initialView.tagFilter);
  const [yearFilter, setYearFilter] = useState(initialView.yearFilter);
  const [readFilter, setReadFilter] = useState<LibraryReadFilter>(initialView.readFilter);
  const [sortBy, setSortBy] = useState<LibrarySort>(initialView.sortBy);
  const [sortDesc, setSortDesc] = useState(initialView.sortDesc);
  // Set once the current scope's items + metadata have arrived; remembered
  // filters are validated against the options they produce.
  const [catalogLoaded, setCatalogLoaded] = useState(false);
  // Read/unread writes are broadcast from the reader; the catalog recomputes.
  const [readRevision, setReadRevision] = useState(0);
  // Local PDFs can be imported with the same upload API the console uses.
  const [importingLocal, setImportingLocal] = useState(false);
  const [importedLocal, setImportedLocal] = useState<Record<string, string>>({});
  const citationInfoRequest = useRef(0);
  const citationCards = useRef(new Map<string, HTMLLIElement>());
  const loadGeneration = useRef(0);
  const resizeDrag = useRef<{ side: 'left' | 'right'; startX: number; startWidth: number } | null>(null);
  // Floating edge button for a collapsed column: appears only while the pointer
  // is near that edge, pinned at the position of the first approach; clicking it
  // expands the column. Collapsing itself uses the menu-bar button.
  const [leftHandle, setLeftHandle] = useState<{ x: number; y: number } | null>(null);
  const [rightHandle, setRightHandle] = useState<{ x: number; y: number } | null>(null);
  // After a click the handle goes dark until the pointer leaves the edge band.
  const handleSuppressed = useRef({ left: false, right: false });

  // A translation belongs to the paper it was requested from. Clear it when the
  // reader switches sources so old text cannot be mistaken for the new paper.
  useEffect(() => {
    setTranslationRequest(undefined);
  }, [selected?.id, localSelected]);

  // Selecting text/Blocks only seeds the translation panel's editable source.
  // Translation starts from the explicit 翻译 action (reader menu or panel).
  const previewTranslationSource = useCallback((request: TranslationRequestSeed) => {
    setTranslationRequest({
      ...request,
      requestId: newTranslationRequestId(),
      autoTranslate: false,
    });
  }, []);

  const requestTranslation = useCallback((request: TranslationRequestSeed) => {
    setTranslationRequest({
      ...request,
      requestId: newTranslationRequestId(),
      autoTranslate: true,
    });
    setRightMenu('translation');
    setRightCollapsed(false);
  }, []);

  // Column widths: the user's drag is honoured while there is room, capped to a
  // share of the viewport, and never squeezing the reading area below a minimum.
  const MIN_READER_WIDTH = 360;
  const MIN_COLUMN_WIDTH = 180;
  // The citation panel stacks a direction switch, filters and a card footer
  // with up to four actions. Below ~300px its labels and actions wrap into
  // vertical columns, so keep that side wider while the catalog stays compact.
  const MIN_RIGHT_COLUMN_WIDTH = 300;
  const columnMin = (side: 'left' | 'right') => side === 'right' ? MIN_RIGHT_COLUMN_WIDTH : MIN_COLUMN_WIDTH;
  const columnCap = Math.max(MIN_RIGHT_COLUMN_WIDTH, Math.round(viewportWidth * 0.45));
  const widthMax = (side: 'left' | 'right') => {
    // The other side contributes at least its own floor, even when a stale
    // stored width from before that floor existed is smaller.
    const other = side === 'left'
      ? (rightCollapsed ? 0 : Math.max(columnMin('right'), rightWidth))
      : (leftCollapsed ? 0 : Math.max(columnMin('left'), leftWidth));
    return Math.max(columnMin(side), Math.min(columnCap, viewportWidth - other - MIN_READER_WIDTH));
  };
  const effectiveLeftWidth = Math.max(columnMin('left'), Math.min(leftWidth, widthMax('left')));
  const effectiveRightWidth = Math.max(columnMin('right'), Math.min(rightWidth, widthMax('right')));

  const activeBase = bases.find((base) => base.id === baseID);
  const selectedIsPDF = selected?.contentType === 'application/pdf';
  const localFolder = useMemo(() => {
    // A picked folder names each file "folder/sub/name.pdf"; without that the
    // scope just stays anonymous.
    const path = localFiles.length > 0 ? localPath(localFiles[0]) : '';
    return path.includes('/') ? path.split('/')[0] : '本地文件';
  }, [localFiles]);
  const outlineReady = localMode ? Boolean(localSelected) : selectedIsPDF;
  const localSelectedKey = localSelected ? localPath(localSelected) : '';
  const localSelectedImportedID = localSelectedKey ? importedLocal[localSelectedKey] : undefined;

  useEffect(() => subscribeReadingState(() => setReadRevision((value) => value + 1)), []);

  useEffect(() => {
    try {
      window.localStorage.setItem(LIBRARY_VIEW_STORAGE, JSON.stringify({
        filtersOpen, authorFilter, venueFilter, tagFilter, yearFilter, readFilter, sortBy, sortDesc,
      }));
    } catch {
      // Storage may be full or disabled; filtering still works for this visit.
    }
  }, [filtersOpen, authorFilter, venueFilter, tagFilter, yearFilter, readFilter, sortBy, sortDesc]);

  // Filter options are derived from the loaded metadata, so they only offer
  // values that actually occur in this scope.
  const yearOptions = useMemo(() => Array.from(new Set(
    items.map((item) => metadataYear(metadataByID[item.id])).filter(Boolean),
  )).sort((a, b) => b.localeCompare(a)), [items, metadataByID]);
  const venueOptions = useMemo(() => Array.from(new Set(
    items.map((item) => metadataByID[item.id]?.venue?.trim()).filter((venue): venue is string => Boolean(venue)),
  )).sort((a, b) => a.localeCompare(b, 'zh-Hans-CN')), [items, metadataByID]);
  const tagOptions = useMemo(() => Array.from(new Set(
    items.flatMap((item) => metadataByID[item.id]?.tags ?? []),
  )).sort((a, b) => a.localeCompare(b, 'zh-Hans-CN')), [items, metadataByID]);
  const activeFilterCount = [
    authorFilter.trim(), venueFilter, tagFilter, yearFilter,
    readFilter === 'all' ? '' : readFilter,
  ].filter(Boolean).length;
  const sortCustomized = sortBy !== 'added' || !sortDesc;
  const viewCustomized = activeFilterCount > 0 || sortCustomized;

  // Once this scope's catalog is here, drop remembered values it no longer
  // contains (switched Base, removed paper). Author stays: it is free text.
  useEffect(() => {
    if (!catalogLoaded || localMode) return;
    if (yearFilter && !yearOptions.includes(yearFilter)) setYearFilter('');
    if (venueFilter && !venueOptions.includes(venueFilter)) setVenueFilter('');
    if (tagFilter && !tagOptions.includes(tagFilter)) setTagFilter('');
  }, [catalogLoaded, localMode, yearFilter, venueFilter, tagFilter, yearOptions, venueOptions, tagOptions]);

  const availableCount = useMemo(
    () => items.filter((item) => item.sourceState === 'available').length,
    [items],
  );

  const visibleItems = useMemo(() => {
    const authorQuery = authorFilter.trim().toLowerCase();
    const filtered = items.filter((item) => {
      if (item.sourceState !== 'available') return false;
      const itemMetadata = metadataByID[item.id];
      if (authorQuery && !(itemMetadata?.authors ?? []).some((author) => author.name.toLowerCase().includes(authorQuery))) return false;
      if (yearFilter && metadataYear(itemMetadata) !== yearFilter) return false;
      if (venueFilter && (itemMetadata?.venue ?? '').trim() !== venueFilter) return false;
      if (tagFilter && !(itemMetadata?.tags ?? []).includes(tagFilter)) return false;
      if (readFilter !== 'all' && isDocumentRead(item.id) !== (readFilter === 'read')) return false;
      return true;
    });
    const direction = sortDesc ? -1 : 1;
    const text = (left: string, right: string) => left.localeCompare(right, 'zh-Hans-CN');
    filtered.sort((left, right) => {
      const leftMetadata = metadataByID[left.id];
      const rightMetadata = metadataByID[right.id];
      if (sortBy === 'title') {
        return direction * text(documentTitle(left, leftMetadata), documentTitle(right, rightMetadata));
      }
      if (sortBy === 'year') {
        const leftYear = metadataYear(leftMetadata);
        const rightYear = metadataYear(rightMetadata);
        if (!leftYear && !rightYear) return 0;
        if (!leftYear) return 1;
        if (!rightYear) return -1;
        return direction * leftYear.localeCompare(rightYear);
      }
      if (sortBy === 'author') {
        const leftAuthor = leftMetadata?.authors[0]?.name ?? '';
        const rightAuthor = rightMetadata?.authors[0]?.name ?? '';
        if (!leftAuthor && !rightAuthor) return 0;
        if (!leftAuthor) return 1;
        if (!rightAuthor) return -1;
        return direction * text(leftAuthor, rightAuthor);
      }
      return direction * (new Date(left.createdAt).getTime() - new Date(right.createdAt).getTime());
    });
    return filtered;
    // readRevision invalidates the memo after a read flag is toggled elsewhere.
  }, [items, metadataByID, authorFilter, venueFilter, tagFilter, yearFilter, readFilter, sortBy, sortDesc, readRevision]);

  const documentsByID = useMemo(
    () => new Map(items.map((documentItem) => [documentItem.id, documentItem])),
    [items],
  );
  const citationCounts = useMemo(() => citationFilterCounts(citations), [citations]);
  const visibleCitations = useMemo(() => {
    if (activeCitationIDs.length === 0) return citations.filter(citation => citationMatchesFilter(citation, citationFilter));
    const citationsByID = new Map(citations.map(citation => [citation.referenceId, citation]));
    return activeCitationIDs
      .map(referenceId => citationsByID.get(referenceId))
      .filter((citation): citation is PaperCitation => Boolean(citation));
  }, [activeCitationIDs, citations, citationFilter]);

  // Hovering a PDF citation hotspot filters the citation panel in place. It
  // deliberately does not switch the right-column tab: the user may be reading
  // 论文信息 or 翻译, and hover should never steal that panel.
  function filterCitationsFromPDF(referenceIds: string[]) {
    setActiveCitationIDs(referenceIds);
    if (referenceIds.length === 0) return;
    setCitationGroup('outgoing');
    setCitationFilter('all');
    // Only scrolls when the citation panel is already mounted; hovering while
    // another tab is active is a pure filter update.
    window.requestAnimationFrame(() => window.requestAnimationFrame(() => {
      citationCards.current.get(referenceIds[0])?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    }));
  }

  useEffect(() => {
    setActiveCitationIDs([]);
    citationCards.current.clear();
  }, [selected?.id]);

  function resetFilters() {
    setAuthorFilter('');
    setVenueFilter('');
    setTagFilter('');
    setYearFilter('');
    setReadFilter('all');
    setSortBy('added');
    setSortDesc(true);
  }


  useEffect(() => {
    let active = true;
    void api.listDocumentBases()
      .then((result) => { if (active) setBases(result.items); })
      .catch((error) => {
        if (!active) return;
        setMessage(error instanceof Error ? `加载 Document Base 失败：${error.message}` : '加载 Document Base 失败。');
      });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!leftCollapsed && !rightCollapsed) return;
    const onMove = (event: MouseEvent) => {
      const inVerticalRange = event.clientY > 70 && event.clientY < window.innerHeight - 90;
      const settle = (side: 'left' | 'right', current: { x: number; y: number } | null, distance: number, x: number) => {
        if (!inVerticalRange || distance > 102) {
          handleSuppressed.current[side] = false;
          return null;
        }
        if (handleSuppressed.current[side]) return null;
        const show = current ? distance <= 90 : distance <= 72;
        return show ? (current ?? { x, y: event.clientY }) : null;
      };
      if (leftCollapsed) setLeftHandle(current => settle('left', current, event.clientX, 8));
      if (rightCollapsed) setRightHandle(current => settle('right', current, window.innerWidth - event.clientX, window.innerWidth - 38));
    };
    window.addEventListener('mousemove', onMove);
    return () => window.removeEventListener('mousemove', onMove);
  }, [leftCollapsed, rightCollapsed]);

  // The drag listens on the window instead of using pointer capture: a React
  // re-render during the drag would drop the capture (lostpointercapture).
  function startResize(side: 'left' | 'right', event: React.PointerEvent<HTMLDivElement>) {
    if (event.button !== 0) return;
    event.preventDefault();
    const drag = { side, startX: event.clientX, startWidth: side === 'left' ? leftWidth : rightWidth };
    resizeDrag.current = drag;
    setResizing(true);
    const max = widthMax(side);
    const onMove = (moveEvent: PointerEvent) => {
      const delta = drag.side === 'left' ? moveEvent.clientX - drag.startX : drag.startX - moveEvent.clientX;
      const next = Math.round(Math.min(max, Math.max(columnMin(drag.side), drag.startWidth + delta)));
      if (drag.side === 'left') {
        setLeftWidth(next);
        window.localStorage.setItem('library.leftWidth', String(next));
      } else {
        setRightWidth(next);
        window.localStorage.setItem('library.rightWidth', String(next));
      }
    };
    const onUp = () => {
      resizeDrag.current = null;
      setResizing(false);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  }

  function selectScopeAll() {
    setLocalMode(false);
    setBaseID('');
  }

  function selectScopeBase(id: string) {
    setLocalMode(false);
    setBaseID(id);
  }

  // Both pickers feed the same list: individual PDFs (so the dialog actually
  // shows the files) or a whole folder.
  function applyLocalPicked(selected: FileList | null) {
    const picked = Array.from(selected ?? [])
      .filter((item) => /\.pdf$/i.test(item.name) || item.type === 'application/pdf')
      .sort((a, b) => localPath(a).localeCompare(localPath(b)));
    if (picked.length === 0) return;
    setLocalFiles(picked);
    setLocalMode(true);
    setLocalSelected(picked[0]);
  }

  function onLocalInput(event: ChangeEvent<HTMLInputElement>) {
    applyLocalPicked(event.target.files);
    event.target.value = ''; // picking the same file or folder again must still fire
  }

  function loadDocuments() {
    if (localMode) return; // the local scope lists files from disk, not the API
    const generation = loadGeneration.current + 1;
    loadGeneration.current = generation;
    setLoading(true);
    setCatalogLoaded(false);
    void fetchAllDocuments(baseID)
      .then(async (documents) => {
        if (loadGeneration.current !== generation) return;
        const metadataMap = await fetchMetadataMap(documents);
        if (loadGeneration.current !== generation) return;
        setItems(documents);
        setMetadataByID(metadataMap);
        setCatalogLoaded(true);
        const pending = pendingDocumentID.current;
        if (pending) {
          pendingDocumentID.current = '';
          const target = documents.find((item) => item.id === pending);
          if (target) {
            setSelected(target);
            return;
          }
          // The paper may sit outside this Base: fetch it directly so the
          // reader still opens what the console asked for.
          try {
            const found = await api.searchDocuments({ id: pending, page: 1 });
            if (loadGeneration.current !== generation) return;
            setSelected(found.items.find((item) => item.id === pending) ?? documents[0]);
          } catch {
            setSelected(documents[0]);
          }
          return;
        }
        setSelected((current) => documents.find((item) => item.id === current?.id) ?? documents[0]);
      })
      .catch((error) => {
        if (loadGeneration.current !== generation) return;
        setMessage(error instanceof Error ? `加载论文失败：${error.message}` : '加载论文失败。');
      })
      .finally(() => {
        if (loadGeneration.current === generation) setLoading(false);
      });
  }

  useEffect(() => { loadDocuments(); }, [baseID, localMode]);

  useEffect(() => {
    // The local scope shows files that have no backend record at all.
    if (!selected || localMode) {
      setMetadata(undefined);
      setCitations([]);
      setCitedBy([]);
      setCitationsLoading(false);
      return;
    }
    let active = true;
    setMetadata(undefined);
    setCitations([]);
    setCitedBy([]);
    setCitationGroup('outgoing');
    setCitationFilter('all');
    setEditingCitation(undefined);
    setCitationsLoading(true);
    setCitationPaperInfoOpen(false);
    void Promise.allSettled([
      api.getPaperMetadata(selected.id),
      api.listPaperCitations(selected.id),
      api.listPaperCitedBy(selected.id),
    ]).then((results) => {
      if (!active) return;
      const [metadataResult, citationsResult, citedByResult] = results;
      if (metadataResult.status === 'fulfilled') setMetadata(metadataResult.value.paperMetadata);
      if (citationsResult.status === 'fulfilled') setCitations(citationsResult.value.items);
      if (citedByResult.status === 'fulfilled') setCitedBy(citedByResult.value.items);
    }).finally(() => {
      if (active) setCitationsLoading(false);
    });
    return () => { active = false; };
  }, [selected?.id, localMode]);

  // A queued reference download completes asynchronously. While one is in
  // flight, refresh the citation list so the card can flip to "库内" or show
  // the terminal failure without the user reopening the paper.
  useEffect(() => {
    if (!selected?.id || localMode) return;
    if (!citations.some((citation) => citationDownloadInFlight(citation))) return;
    let active = true;
    let timer = 0;
    const poll = () => {
      void api.listPaperCitations(selected.id)
        .then(({ items: refreshed }) => { if (active) setCitations(refreshed); })
        .catch(() => {
          // Keep the current list; finally schedules another attempt.
        })
        .finally(() => {
          if (active) timer = window.setTimeout(poll, 8000);
        });
    };
    timer = window.setTimeout(poll, 8000);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [citations, selected?.id, localMode]);

  function openCitationPaperInfo(documentID: string | undefined, rawReference: string, reference?: PaperReference) {
    const requestID = citationInfoRequest.current + 1;
    citationInfoRequest.current = requestID;
    setCitationPaperInfoOpen(true);
    setCitationPaperInfo(undefined);
    setCitationReference(reference);
    setCitationPaperInfoError('');
    setCitationRawReference(rawReference.trim());
    setCitationRawReferenceVisible(false);
    if (!documentID) {
      setCitationPaperInfoLoading(false);
      return;
    }
    setCitationPaperInfoLoading(true);
    void api.getPaperMetadata(documentID)
      .then((result) => {
        if (citationInfoRequest.current === requestID) setCitationPaperInfo(result.paperMetadata);
      })
      .catch((error) => {
        if (citationInfoRequest.current !== requestID || reference) return;
        setCitationPaperInfoError(error instanceof Error ? error.message : '无法读取引用论文信息。');
      })
      .finally(() => {
        if (citationInfoRequest.current === requestID) setCitationPaperInfoLoading(false);
      });
  }

  async function refreshCitations() {
    if (!selected || localMode) return;
    const { items: refreshed } = await api.listPaperCitations(selected.id);
    setCitations(refreshed);
  }

  async function openCitationDocument(documentID: string) {
    const inScope = documentsByID.get(documentID);
    if (inScope) {
      setSelected(inScope);
      return;
    }
    try {
      // The linked paper may sit outside the current Base or filter scope.
      const found = await api.searchDocuments({ id: documentID, page: 1 });
      const documentItem = found.items.find((item) => item.id === documentID);
      if (documentItem) setSelected(documentItem);
      else setMessage('该论文不在当前论文库中。');
    } catch (error) {
      setMessage(error instanceof Error ? `打开关联论文失败：${error.message}` : '打开关联论文失败。');
    }
  }

  async function downloadCitationPDF(citation: PaperCitation) {
    setCitationActionBusy(citation.referenceId);
    try {
      await api.downloadReferencePDF(citation.referenceId);
      setMessage('已提交该引用的 PDF 下载；完成后重新匹配或重新打开这篇论文即可看到结果。');
      await refreshCitations();
    } catch (error) {
      setMessage(citationDownloadErrorMessage(error));
    } finally {
      setCitationActionBusy('');
    }
  }

  async function rematchCitation(citation: PaperCitation) {
    setCitationActionBusy(citation.referenceId);
    try {
      const { linked } = await api.rematchPaperReference(citation.referenceId);
      setMessage(linked ? '已重新匹配到库内论文。' : '库内仍没有匹配的论文。');
      await refreshCitations();
    } catch (error) {
      setMessage(error instanceof Error ? `重新匹配引用失败：${error.message}` : '重新匹配引用失败。');
    } finally {
      setCitationActionBusy('');
    }
  }

  async function deleteCitation(citation: PaperCitation) {
    const label = citation.title?.trim() || citationLabel(citation, 0);
    if (!window.confirm(`删除这条引用？\n\n${label}\n\n不会删除任何已关联的论文文件。`)) return;
    setCitationActionBusy(citation.referenceId);
    try {
      await api.deletePaperReference(citation.referenceId);
      setCitations((current) => current.filter((item) => item.referenceId !== citation.referenceId));
      setMessage('引用已删除。');
    } catch (error) {
      setMessage(error instanceof Error ? `删除引用失败：${error.message}` : '删除引用失败。');
    } finally {
      setCitationActionBusy('');
    }
  }

  // A local PDF is uploaded through the same endpoint as the console; the
  // backend then runs the ordinary pipeline. The local scope stays selected so
  // the user can keep reading the file from disk.
  async function importLocalPaper(picked: File) {
    setImportingLocal(true);
    try {
      const { document: imported } = await api.upload(picked, '');
      // The local file's page/read state follows it into the stored document.
      const carried = transferReadingRecord(localFileReadingKey(picked), imported.id);
      let baseNote = '';
      if (baseID) {
        const targetBase = bases.find((base) => base.id === baseID);
        try {
          await api.addDocumentToBase(baseID, imported.id);
          baseNote = `，并加入 Base「${targetBase?.name ?? baseID}」`;
        } catch (error) {
          baseNote = `；加入 Base 失败：${error instanceof Error ? error.message : '未知错误'}`;
        }
      }
      setImportedLocal((current) => ({ ...current, [localPath(picked)]: imported.id }));
      setMessage(`已导入到内部论文库：${imported.originalFilename}（${imported.id}）${carried ? '，已同步本地阅读位置与已读状态' : ''}，后台解析已开始${baseNote}。`);
    } catch (error) {
      setMessage(error instanceof Error ? `导入到库失败：${error.message}` : '导入到库失败。');
    } finally {
      setImportingLocal(false);
    }
  }

  function viewImportedPaper(documentID: string) {
    pendingDocumentID.current = documentID;
    setLocalMode(false);
  }

  const collapsedClass = `${leftCollapsed ? ' left-collapsed' : ''}${rightCollapsed ? ' right-collapsed' : ''}`;

  const leftColumnBody = <>
    <div className="library-resizer" role="separator" aria-orientation="vertical" title="拖动调整左栏宽度"
      onPointerDown={event => startResize('left', event)} />
    <div className="library-menu-bar">
      <a className="library-back-link" href="#">返回论文管理端</a>
      <button className="library-collapse" type="button" onClick={() => setLeftCollapsed(true)} aria-expanded="true" aria-controls="library-left-column" title="收起左栏">«</button>
    </div>
    <nav className="library-menu" role="tablist" aria-label="左侧功能菜单">
      {LEFT_MENUS.map((menu) => <button className={leftMenu === menu ? 'active' : ''} type="button" role="tab" aria-selected={leftMenu === menu} key={menu} onClick={() => setLeftMenu(menu)}>{LEFT_MENU_LABELS[menu]}</button>)}
    </nav>
    <input ref={fileInput} className="library-folder-input" type="file" accept="application/pdf" multiple onChange={onLocalInput} />
    <input ref={folderInput} className="library-folder-input" type="file" multiple onChange={onLocalInput}
      {...({ webkitdirectory: 'true', directory: 'true' } as Record<string, string>)} />
    <div className="library-detail">
      {leftMenu === 'catalog' && <section className="library-panel library-panel-catalog" aria-label="论文目录">
        {/* The internal/local switch lives right here, above the list it scopes. */}
        <div className="library-scope-switch" role="tablist" aria-label="论文范围">
          <button className={!localMode ? 'active' : ''} type="button" role="tab" aria-selected={!localMode} onClick={selectScopeAll}>内部论文</button>
          <button className={localMode ? 'active' : ''} type="button" role="tab" aria-selected={localMode} onClick={() => setLocalMode(true)}>本地论文</button>
        </div>
        <p className="library-detail-meta">
          <span>{localMode ? `本地论文 · ${localFolder}` : activeBase ? `内部论文 · ${activeBase.name}` : '内部论文 · 全部'}</span>
          <span>{localMode ? `${localFiles.length} 篇` : `${visibleItems.length} / ${availableCount} 篇`}</span>
        </p>
        {!localMode && <div className="library-catalog-tools">
          <button className={`library-filter-toggle${filtersOpen ? ' active' : ''}`} type="button" aria-expanded={filtersOpen} onClick={() => setFiltersOpen((open) => !open)}>
            筛选与排序{activeFilterCount > 0 ? ` · ${activeFilterCount}` : ''}
          </button>
          {viewCustomized && <button className="library-filter-reset" type="button" onClick={resetFilters}>清除</button>}
        </div>}
        {!localMode && filtersOpen && <div className="library-filters">
          <input value={authorFilter} onChange={(event) => setAuthorFilter(event.target.value)} placeholder="按作者筛选（模糊）" aria-label="按作者筛选" />
          <div className="library-filter-grid">
            <LibrarySelect value={yearFilter} onChange={setYearFilter} ariaLabel="按年份筛选"
              options={[{ value: '', label: '全部年份' }, ...yearOptions.map((year) => ({ value: year, label: year }))]} />
            <LibrarySelect value={venueFilter} onChange={setVenueFilter} ariaLabel="按发表 venue 筛选"
              options={[{ value: '', label: '全部 venue' }, ...venueOptions.map((venue) => ({ value: venue, label: venue }))]} />
            <LibrarySelect value={tagFilter} onChange={setTagFilter} ariaLabel="按标签筛选"
              options={[{ value: '', label: '全部标签' }, ...tagOptions.map((tag) => ({ value: tag, label: tag }))]} />
            <LibrarySelect value={readFilter} onChange={(next) => setReadFilter(next as LibraryReadFilter)} ariaLabel="按阅读状态筛选"
              options={[{ value: 'all', label: '全部阅读状态' }, { value: 'unread', label: '未读' }, { value: 'read', label: '已读' }]} />
          </div>
          <div className="library-sort-row">
            <LibrarySelect value={sortBy} onChange={(next) => setSortBy(next as LibrarySort)} ariaLabel="排序方式"
              options={[
                { value: 'added', label: '按添加时间' },
                { value: 'title', label: '按标题' },
                { value: 'year', label: '按年份' },
                { value: 'author', label: '按第一作者' },
              ]} />
            <button type="button" title={sortDesc ? '当前降序，点击切换升序' : '当前升序，点击切换降序'} onClick={() => setSortDesc((desc) => !desc)}>{sortDesc ? '降序 ↓' : '升序 ↑'}</button>
          </div>
        </div>}
        <div className="library-document-list">
          {localMode
            ? <>
              <button className="library-folder-open" type="button" onClick={() => fileInput.current?.click()}>选择 PDF 文件…</button>
              <button className="library-folder-open" type="button" title="加载这个文件夹里（含子文件夹）的全部 PDF，不逐个挑选" onClick={() => folderInput.current?.click()}>{localFiles.length > 0 ? '换成整个文件夹…' : '选择整个文件夹…'}</button>
              {localFiles.length === 0
                ? <p className="hint">「选择 PDF 文件…」里可以进到任意文件夹，看到并多选其中的 PDF（Ctrl+A 全选）；全部只在浏览器里打开，不会写入后端。</p>
                : localFiles.map((item, index) => {
                  const read = isDocumentRead(localFileReadingKey(item));
                  return <button className={`${localSelected === item ? 'active' : ''}${read ? ' read' : ''}`} type="button" key={`${localPath(item)}-${index}`} title={localPath(item)} onClick={() => { setLocalSelected(item); if (narrow) setLeftCollapsed(true); }}>
                    <strong><span className={`library-read-dot${read ? ' read' : ''}`} aria-hidden="true" />{item.name}</strong>
                  </button>;
                })}
            </>
            : loading && items.length === 0
              ? <p className="hint">正在加载论文与元数据…</p>
              : visibleItems.length === 0
                ? <p className="hint">{items.length === 0 ? '此范围内没有论文。' : '没有符合筛选条件的论文。'}</p>
                : visibleItems.map((documentItem) => {
                  const itemMetadata = metadataByID[documentItem.id];
                  const read = isDocumentRead(documentItem.id);
                  const detail = [metadataYear(itemMetadata), itemMetadata?.venue?.trim()].filter(Boolean).join(' · ')
                    || formatAuthors(itemMetadata?.authors ?? []);
                  return <button className={`${selected?.id === documentItem.id ? 'active' : ''}${read ? ' read' : ''}`} type="button" key={documentItem.id} title={documentItem.originalFilename} onClick={() => { setSelected(documentItem); if (narrow) setLeftCollapsed(true); }}>
                    <strong><span className={`library-read-dot${read ? ' read' : ''}`} aria-hidden="true" />{documentTitle(documentItem, itemMetadata)}</strong>
                    <span>{detail}</span>
                  </button>;
                })}
        </div>
        {localMode && localSelected && <div className="library-local-import">
          <button className="primary" type="button" disabled={importingLocal || Boolean(localSelectedImportedID)}
            onClick={() => { if (localSelected) void importLocalPaper(localSelected); }}>
            {importingLocal ? '正在导入…' : localSelectedImportedID ? '已导入到库' : '导入当前论文到库'}
          </button>
          {localSelectedImportedID && <button type="button" onClick={() => viewImportedPaper(localSelectedImportedID)}>在内部论文中查看</button>}
          <p className="hint">通过上传接口写入内部论文库，解析流程与普通上传一致。</p>
        </div>}
      </section>}
      {leftMenu === 'base' && <section className="library-panel library-panel-base" aria-label="类别操作">
        <div className="library-scope">
          <h3>内部论文</h3>
          <div className="library-scope-children">
            <div className="library-base-list">
              <button className={!localMode && !baseID ? 'active' : ''} type="button" onClick={selectScopeAll} disabled={loading}>全部</button>
              {bases.map((base) => <button className={!localMode && baseID === base.id ? 'active' : ''} type="button" key={base.id} onClick={() => selectScopeBase(base.id)} disabled={loading}>{base.name}</button>)}
            </div>
            {activeBase && !localMode && <dl className="library-base-info">
              <dt>类别</dt><dd>{activeBase.name}</dd>
              <dt>说明</dt><dd>{activeBase.description || '未填写说明'}</dd>
              <dt>创建时间</dt><dd>{activeBase.createdAt ? activeBase.createdAt.slice(0, 10) : '未知'}</dd>
              <dt>篇数</dt><dd>{items.length}</dd>
            </dl>}
          </div>
        </div>
      </section>}
      {leftMenu === 'outline' && <section className="library-panel library-panel-outline" aria-label="PDF 目录">
        {outlineReady
          ? <div className="pdf-outline-slot" ref={setOutlineSlot} />
          : <p className="hint">选择一篇 PDF 论文后显示其目录。</p>}
      </section>}
    </div>
  </>;

  const rightColumnBody = <>
    <div className="library-resizer" role="separator" aria-orientation="vertical" title="拖动调整右栏宽度"
      onPointerDown={event => startResize('right', event)} />
    <div className="library-menu-bar">
      {!localMode && selected?.doi && <button className="library-export" type="button"
        title="通过 doi.org 获取当前论文的引文格式"
        onClick={() => setExportCitation({
          doi: selected.doi ?? '',
          title: documentTitle(selected, metadata),
        })}>引文格式</button>}
      <button className="library-collapse" type="button" onClick={() => setRightCollapsed(true)} aria-expanded="true" aria-controls="library-right-column" title="收起右栏">»</button>
    </div>
    <nav className="library-menu" role="tablist" aria-label="右侧功能菜单">
      {RIGHT_MENUS.map((menu) => <button className={rightMenu === menu ? 'active' : ''} type="button" role="tab" aria-selected={rightMenu === menu} key={menu} onClick={() => setRightMenu(menu)}>{RIGHT_MENU_LABELS[menu]}</button>)}
    </nav>
    <div className="library-detail">
      {rightMenu === 'info' && <section className="library-panel library-panel-info" aria-label="论文信息">
        {localMode ? <p className="hint">本地论文没有关联信息。</p> : selected ? <>
          <dl className="library-info-list">
            <dt>标题</dt><dd>{documentTitle(selected, metadata)}</dd>
            <dt>作者</dt><dd>{metadata ? formatAuthors(metadata.authors) : '加载中或未识别'}</dd>
            <dt>DOI</dt><dd>{selected.doi || '未设置'}</dd>
            <dt>来源</dt><dd>{metadata?.sourceName || '未设置'}{metadata?.sourceUrl && <> · <a href={metadata.sourceUrl} target="_blank" rel="noreferrer">打开来源</a></>}</dd>
            <dt>发表信息</dt><dd>{[metadata?.venue, metadata?.publishedAt?.slice(0, 10), metadata?.volume, metadata?.issue, metadata?.pages].filter(Boolean).join(' · ') || '未设置'}</dd>
          </dl>
        </> : <p className="hint">选择论文后显示论文信息。</p>}
      </section>}
      {rightMenu === 'citations' && <section className="library-panel library-panel-citations" aria-label="引用信息">
        {localMode ? <p className="hint">本地论文没有引用信息。</p> : <>
        <div className="library-citation-switch" role="tablist" aria-label="引用方向">
          <button className={citationGroup === 'outgoing' ? 'active' : ''} type="button" role="tab"
            aria-selected={citationGroup === 'outgoing'} onClick={() => { setCitationGroup('outgoing'); setActiveCitationIDs([]); }}>
            本论文引用 <span className="library-count">{citations.length}</span>
          </button>
          <button className={citationGroup === 'incoming' ? 'active' : ''} type="button" role="tab"
            aria-selected={citationGroup === 'incoming'} onClick={() => { setCitationGroup('incoming'); setActiveCitationIDs([]); }}>
            被引用 <span className="library-count">{citedBy.length}</span>
          </button>
        </div>
        {citationsLoading ? <p className="hint">正在读取引用信息…</p> : citationGroup === 'outgoing' ? <>
          <div className="library-citation-filters" role="group" aria-label="引用筛选">
            {([
              ['all', '全部', citations.length],
              ['linked', '库内', citationCounts.linked],
              ['pending', '未解析', citationCounts.pending],
              ['failed', '失败', citationCounts.failed],
            ] as Array<[CitationFilter, string, number]>).map(([key, label, count]) => (
              <button key={key} className={citationFilter === key ? 'active' : ''} type="button"
                aria-pressed={citationFilter === key} onClick={() => { setCitationFilter(key); setActiveCitationIDs([]); }}>
                {label} <span>{count}</span>
              </button>
            ))}
          </div>
          {activeCitationIDs.length > 0 && <div className="library-citation-candidate-filter" role="status">
            <span>索引候选引用：{activeCitationIDs.length} 条</span>
            <button type="button" onClick={() => setActiveCitationIDs([])}>清除</button>
          </div>}
          {citations.length === 0
            ? <p className="hint">没有已提取的参考文献。</p>
            : visibleCitations.length === 0
              ? <p className="hint">没有符合筛选条件的引用。</p>
              : <ol className="library-citation-list">
                {visibleCitations.map((citation, index) => {
                  const linkedDocumentID = citation.citedDocumentId;
                  const linkedMetadata = linkedDocumentID ? metadataByID[linkedDocumentID] : undefined;
                  const linkedDocument = linkedDocumentID ? documentsByID.get(linkedDocumentID) : undefined;
                  const manual = citation.metadataSource === 'manual';
                  const title = (manual ? citation.title?.trim() : '') ||
                    linkedMetadata?.title?.trim() || citation.title?.trim() || citationLabel(citation, index);
                  const authors = manual
                    ? citation.authors
                    : (linkedMetadata?.authors?.length ? linkedMetadata.authors : citation.authors);
                  const meta = citationMetaLine(
                    authors,
                    manual ? citation.venue : (linkedMetadata?.venue || citation.venue),
                    manual ? citation.publishedAt : (linkedMetadata?.publishedAt || citation.publishedAt),
                  );
                  const doi = manual ? citation.doi : (citation.doi || linkedDocument?.doi);
                  const badge = citationBadge(citation);
                  const actionBusy = citationActionBusy === citation.referenceId;
                  const downloadInFlight = citationDownloadInFlight(citation);
                  const displayOrder = citation.bibliographyOrder === undefined ? index + 1 : citation.bibliographyOrder + 1;
                  return <li className={`library-citation-card${activeCitationIDs.includes(citation.referenceId) ? ' citation-target' : ''}`}
                    ref={node => { if (node) citationCards.current.set(citation.referenceId, node); else citationCards.current.delete(citation.referenceId); }}
                    key={`${citation.citingDocumentId}-${citation.referenceId}`}>
                    <button className="library-citation-card-main" type="button"
                      onClick={() => openCitationPaperInfo(linkedDocumentID, citation.rawReference || '', citation)}>
                      <span className="library-citation-index">{displayOrder}</span>
                      <span className="library-citation-title">{title}</span>
                      {meta && <span className="library-citation-meta">{meta}</span>}
                      {doi && <span className="library-citation-doi">DOI {doi}</span>}
                    </button>
                    <div className="library-citation-card-footer">
                      <span className={`library-citation-badge badge-${badge.tone}`}>{badge.label}</span>
                      <div className="library-citation-actions">
                        {linkedDocumentID
                          ? <button type="button" onClick={() => void openCitationDocument(linkedDocumentID)}>打开论文 →</button>
                          : <>
                            <button type="button" disabled={actionBusy || downloadInFlight || !citation.sourceUrl}
                              title={downloadInFlight ? '该引用的 PDF 正在下载'
                                : citation.sourceUrl ? '提交该引用的 PDF 下载' : '该引用没有可用的来源链接'}
                              onClick={() => void downloadCitationPDF(citation)}>
                              {downloadInFlight ? '下载中…' : citation.downloadErrorMessage ? '重试下载' : '下载 PDF'}
                            </button>
                            <button type="button" disabled={actionBusy} onClick={() => void rematchCitation(citation)}>重新匹配</button>
                          </>}
                        {doi && <button type="button" disabled={actionBusy}
                          title="通过 doi.org 获取 BibTeX、RIS、CSL-JSON 或格式化引用"
                          onClick={() => setExportCitation({ doi, title, rawReference: citation.rawReference })}>引文格式</button>}
                        <button type="button" disabled={actionBusy} onClick={() => setEditingCitation(citation)}>编辑</button>
                        <button className="citation-delete" type="button" disabled={actionBusy}
                          onClick={() => void deleteCitation(citation)}>删除</button>
                      </div>
                    </div>
                  </li>;
                })}
              </ol>}
        </> : citedBy.length === 0
          ? <p className="hint">暂无库内论文引用它。</p>
          : <ol className="library-citation-list">
            {citedBy.map((citation, index) => {
              const citingMetadata = metadataByID[citation.citingDocumentId];
              const citingDocument = documentsByID.get(citation.citingDocumentId);
              const title = citingMetadata?.title?.trim() || citingDocument?.originalFilename || '库内论文';
              const meta = citationMetaLine(citingMetadata?.authors ?? [], citingMetadata?.venue, citingMetadata?.publishedAt);
              return <li className="library-citation-card" key={`${citation.citingDocumentId}-${citation.referenceId}`}>
                <button className="library-citation-card-main" type="button"
                  onClick={() => openCitationPaperInfo(citation.citingDocumentId, citation.rawReference || '')}>
                  <span className="library-citation-index">{index + 1}</span>
                  <span className="library-citation-title">{title}</span>
                  {meta && <span className="library-citation-meta">{meta}</span>}
                  {citingDocument?.doi && <span className="library-citation-doi">DOI {citingDocument.doi}</span>}
                </button>
                <div className="library-citation-card-footer">
                  <span className="library-citation-badge badge-linked">库内引用</span>
                  <div className="library-citation-actions">
                    <button type="button" onClick={() => void openCitationDocument(citation.citingDocumentId)}>打开论文 →</button>
                  </div>
                </div>
              </li>;
            })}
          </ol>}
        </>}
      </section>}
      {/* Kept mounted while another right-hand tab is active so switching to
          论文信息/引用信息 does not discard the current translations. */}
      <div className="library-panel-slot" hidden={rightMenu !== 'translation'}>
        <TranslationPanel request={translationRequest} />
      </div>
    </div>
  </>;

  return <main className={`paper-library${collapsedClass}${resizing ? ' resizing' : ''}`}
    style={{ gridTemplateColumns: narrow ? 'minmax(0, 1fr)' : `${leftCollapsed ? 0 : effectiveLeftWidth}px minmax(0, 1fr) ${rightCollapsed ? 0 : effectiveRightWidth}px` }}>
    {narrow && (!leftCollapsed || !rightCollapsed) && <div className="library-backdrop" role="presentation"
      onClick={() => { setLeftCollapsed(true); setRightCollapsed(true); }} />}
    {(narrow ? leftCollapsed : leftCollapsed && !!leftHandle) && <button className={`library-peek${narrow ? ' library-peek-left' : ''}`} style={narrow ? undefined : { left: leftHandle?.x ?? 8, top: leftHandle?.y ?? 96 }} type="button"
      onClick={() => { handleSuppressed.current.left = true; setLeftHandle(null); setLeftCollapsed(false); }}
      title="展开左栏（论文目录）" aria-label="展开左栏">
      <span aria-hidden="true">»</span>
    </button>}
    {(narrow ? rightCollapsed : rightCollapsed && !!rightHandle) && <button className={`library-peek${narrow ? ' library-peek-right' : ''}`} style={narrow ? undefined : { left: rightHandle?.x ?? 0, top: rightHandle?.y ?? 96 }} type="button"
      onClick={() => { handleSuppressed.current.right = true; setRightHandle(null); setRightCollapsed(false); }}
      title="展开右栏（论文信息、引用与翻译）" aria-label="展开右栏">
      <span aria-hidden="true">«</span>
    </button>}

    <aside className={`library-column library-column-left${leftCollapsed ? ' collapsed' : ''}`} id="library-left-column">
      {leftCollapsed ? null : leftColumnBody}
    </aside>

    <section className="paper-library-main">
      <Suspense fallback={<div className="library-empty"><p>正在加载阅读器…</p></div>}>
        {localMode
          ? localSelected
            ? <PdfReader file={localSelected} onTranslate={requestTranslation} onTranslationSource={previewTranslationSource} onMessage={setMessage} outlinePlacement="external" outlineTarget={leftMenu === 'outline' ? outlineSlot : null} modeRail />
            : <div className="library-empty"><p>在左侧「本地论文」里打开一个文件夹，里面的 PDF 会列在那里。</p></div>
          : !selected
            ? <div className="library-empty"><p>从左侧论文目录选择一篇论文开始阅读。</p></div>
            : selectedIsPDF
              ? <PdfReader document={selected} citations={citations} onCitationLinks={filterCitationsFromPDF} onTranslate={requestTranslation} onTranslationSource={previewTranslationSource} onMessage={setMessage} outlinePlacement="external" outlineTarget={leftMenu === 'outline' ? outlineSlot : null} modeRail />
              : <div className="library-empty"><p>该论文不是 PDF，无法在阅读库中预览。</p></div>}
      </Suspense>
      {message && <p className="library-message">{message}</p>}
    </section>

    <aside className={`library-column library-column-right${rightCollapsed ? ' collapsed' : ''}`} id="library-right-column">
      {rightCollapsed ? null : rightColumnBody}
    </aside>

    {citationPaperInfoOpen && <div className="citation-detail-backdrop" role="presentation" onMouseDown={() => setCitationPaperInfoOpen(false)}>
      <section className="citation-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="citation-detail-title" onMouseDown={(event) => event.stopPropagation()}>
        <header><h2 id="citation-detail-title">引用论文信息</h2><div className="citation-detail-actions"><button type="button" onClick={() => setCitationRawReferenceVisible((visible) => !visible)}>{citationRawReferenceVisible ? '收起引用原文' : '查看引用原文'}</button><button type="button" onClick={() => setCitationPaperInfoOpen(false)}>关闭</button></div></header>
        {citationRawReferenceVisible && <section className="citation-raw-reference"><h3>引用原文</h3><p>{citationRawReference || '未保留引用原文。'}</p></section>}
        {citationPaperInfoLoading && <p className="hint">正在读取提取的论文信息…</p>}
        {!citationPaperInfoLoading && citationPaperInfoError && !citationReference && <p className="detail-error">读取失败：{citationPaperInfoError}</p>}
        {!citationPaperInfoLoading && citationReference && <dl>
          <dt>标题</dt><dd>{citationReference.title || '未提取标题'}</dd>
          <dt>作者</dt><dd>{formatAuthors(citationReference.authors)}</dd>
          <dt>发表信息</dt><dd>{[citationReference.venue, citationReference.publishedAt?.slice(0, 10)].filter(Boolean).join(' · ') || '未提取发表信息'}</dd>
          <dt>DOI</dt><dd>{citationReference.doi || '未提取 DOI'}</dd>
          <dt>解析状态</dt><dd>{CITATION_STATUS_LABELS[citationReference.resolutionStatus] ?? citationReference.resolutionStatus}{citationReference.citedDocumentId ? ' · 已关联库内文档' : ''}</dd>
          <dt>PDF 下载</dt><dd>{citationReference.citedDocumentId ? '已获取文档' : citationReference.downloadErrorMessage ? `下载失败：${citationReference.downloadErrorMessage}` : citationReference.downloadRequestedAt ? '下载中' : '未请求下载'}</dd>
          {citationReference.resolutionErrorMessage && <><dt>解析错误</dt><dd>{citationReference.resolutionErrorMessage}</dd></>}
        </dl>}
        {!citationPaperInfoLoading && !citationReference && !citationPaperInfoError && citationPaperInfo && <dl>
          <dt>标题</dt><dd>{citationPaperInfo.title || '未提取标题'}</dd>
          <dt>作者</dt><dd>{formatAuthors(citationPaperInfo.authors)}</dd>
          <dt>发表日期</dt><dd>{citationPaperInfo.publishedAt?.slice(0, 10) || '未提取发表日期'}</dd>
        </dl>}
      </section>
    </div>}
    {editingCitation && <ReferenceEditorDialog
      reference={editingCitation}
      onClose={() => setEditingCitation(undefined)}
      onSaved={(updated) => {
        setCitations((current) => current.map((item) => (
          item.referenceId === updated.referenceId ? { ...item, ...updated } : item
        )));
        setEditingCitation(undefined);
        setMessage('引用信息已更新。');
      }}
    />}
    {exportCitation && <CitationExportDialog target={exportCitation} onClose={() => setExportCitation(undefined)} />}
  </main>;
}
