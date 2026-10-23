import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import {
  APIError,
  MAX_READING_NOTE_BYTES,
  api,
  type DocumentReadingNote,
  type ReadingNoteDirectory,
  type ReadingNoteOperation,
  type ReadingNoteSectionEntry,
} from './api';
import {
  formatEvidenceMarkdown,
  parseEvidenceHref,
  parseEvidenceMarkdown,
  type EvidenceLinkTarget,
  type EvidenceSeed,
} from './evidenceLinks';
import {
  countCodepoints,
  diffSectionBody,
  insertEvidenceIntoBody,
  splitSectionBody,
} from './noteEdits';
import { parseNoteSectionMeta } from './noteSectionMeta.ts';
import { renderReadingNoteMarkdown } from './readingNotePreview';
import { ReadingNoteChapter } from './ReadingNoteChapter';
import { buildNoteTree, getReadingChapter, type NoteTreeNode } from './noteReadingTree';

export type NoteInsertionRequest = { id: string; seed: EvidenceSeed };

export type NoteEvidenceOverlay = EvidenceLinkTarget & {
  id: string;
  label: string;
  sectionId: string;
};

type SectionCacheEntry = {
  markdown: string;
  lineCount: number;
  revision: number;
};

type Props = {
  documentId?: string;
  insertion?: NoteInsertionRequest;
  onInsertionConsumed?(requestId: string): void;
  onNavigateEvidence(target: EvidenceLinkTarget): void;
  onEvidenceTargetsChange(targets: NoteEvidenceOverlay[]): void;
  onMessage(message: string): void;
};

const DEFAULT_NOTE = '# 总结\n\n# 问题\n';

// Reading-view text zoom. The preview's base font size is set from this value;
// headings and code use em so the whole section scales with it.
const NOTE_ZOOM_MIN = 0.75;
const NOTE_ZOOM_MAX = 2.5;
const NOTE_ZOOM_STEP = 0.1;
const NOTE_PREVIEW_BASE_PX = 15;
const NOTE_ZOOM_STORAGE_KEY = 'library.noteZoom';
// Section directory: a full-height splitter on the left of the content, or an
// overlay drawer when the workbench itself is too narrow for two panes.
const NOTES_SIDEBAR_DEFAULT = 240;
const NOTES_SIDEBAR_MIN = 180;
const NOTES_SIDEBAR_MAX = 420;
const NOTES_SIDEBAR_STORAGE_KEY = 'library.notesSidebarWidth';
const NOTES_DRAWER_BREAKPOINT = 560;

function clampNoteZoom(value: number): number {
  return Math.round(Math.min(NOTE_ZOOM_MAX, Math.max(NOTE_ZOOM_MIN, value)) * 100) / 100;
}

function lineCount(markdown: string): number {
  if (!markdown) return 0;
  const lines = markdown.split('\n');
  return markdown.endsWith('\n') ? lines.length - 1 : lines.length;
}

function markdownBytes(markdown: string): number {
  return new Blob([markdown]).size;
}

function exceedsNoteLimit(bytes: number): boolean {
  return bytes > MAX_READING_NOTE_BYTES;
}

function noteLimitLabel(): string {
  return `${Math.round(MAX_READING_NOTE_BYTES / (1024 * 1024))} MiB`;
}

function isNotFound(error: unknown): boolean {
  return error instanceof APIError && error.status === 404;
}

function isConflict(error: unknown): boolean {
  return error instanceof APIError && error.status === 409;
}

function isUnavailable(error: unknown): boolean {
  return error instanceof APIError && error.status === 503;
}

function sectionLabel(entry: ReadingNoteSectionEntry): string {
  return entry.level === 0 ? entry.title || '前言' : entry.title;
}

function sectionBody(markdown: string, entry: ReadingNoteSectionEntry): string {
  return splitSectionBody(markdown, entry.level).body;
}

function findNoteNodePath(nodes: NoteTreeNode[], id: string): NoteTreeNode[] | undefined {
  for (const node of nodes) {
    if (node.entry.id === id) return [node];
    const childPath = findNoteNodePath(node.children, id);
    if (childPath) return [node, ...childPath];
  }
  return undefined;
}

function newSectionMarkdown(level: number, title: string, body: string): string {
  return `${'#'.repeat(level)} ${title.trim()}\n${body}`;
}

export function ReadingNotePanel({
  documentId,
  insertion,
  onInsertionConsumed,
  onNavigateEvidence,
  onEvidenceTargetsChange,
  onMessage,
}: Props) {
  const [note, setNote] = useState<DocumentReadingNote>();
  const [manifestLoading, setManifestLoading] = useState(false);
  const [manifestError, setManifestError] = useState('');
  const [sections, setSections] = useState<Record<string, SectionCacheEntry>>({});
  const [activeSectionId, setActiveSectionId] = useState<string>();
  const [draft, setDraft] = useState('');
  const [savedMarkdown, setSavedMarkdown] = useState('');
  const [sectionLoading, setSectionLoading] = useState('');
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState('');
  const [editorOpen, setEditorOpen] = useState(false);
  const [editorMode, setEditorMode] = useState<'create' | 'import'>('create');
  const [editorMarkdown, setEditorMarkdown] = useState(DEFAULT_NOTE);
  const [addOpen, setAddOpen] = useState(false);
  const [addMode, setAddMode] = useState<'child' | 'after'>();
  const [addTitle, setAddTitle] = useState('');
  const [addBody, setAddBody] = useState('');
  const [blankDraft, setBlankDraft] = useState('');
  const [pendingEvidence, setPendingEvidence] = useState<EvidenceSeed>();
  const [viewMode, setViewMode] = useState<'read' | 'edit'>('read');
  const [foldedSections, setFoldedSections] = useState<Set<string>>(new Set());
  const [readingNavigation, setReadingNavigation] = useState(0);
  const [directoryOpen, setDirectoryOpen] = useState(() => {
    try {
      return window.localStorage.getItem('library.notesDirectory') !== 'closed';
    } catch {
      return true;
    }
  });
  const [sidebarMode, setSidebarMode] = useState<'directory' | 'evidence'>(() => {
    try {
      return window.localStorage.getItem('library.notesSidebarMode') === 'evidence' ? 'evidence' : 'directory';
    } catch {
      return 'directory';
    }
  });
  const [previewZoom, setPreviewZoom] = useState(() => {
    try {
      const stored = Number(window.localStorage.getItem(NOTE_ZOOM_STORAGE_KEY));
      return stored >= NOTE_ZOOM_MIN && stored <= NOTE_ZOOM_MAX ? clampNoteZoom(stored) : 1;
    } catch {
      return 1;
    }
  });
  const [workspaceWidth, setWorkspaceWidth] = useState(0);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [notesSidebarWidth, setNotesSidebarWidth] = useState(() => {
    try {
      const stored = Number(window.localStorage.getItem(NOTES_SIDEBAR_STORAGE_KEY));
      return stored >= NOTES_SIDEBAR_MIN && stored <= NOTES_SIDEBAR_MAX ? stored : NOTES_SIDEBAR_DEFAULT;
    } catch {
      return NOTES_SIDEBAR_DEFAULT;
    }
  });
  // The section currently under the top edge of the continuous chapter reader.
  // It drives the breadcrumb and directory highlight without changing selection.
  const [visibleSectionId, setVisibleSectionId] = useState<string>();
  const [actionsOpen, setActionsOpen] = useState(false);
  const previewWheelCleanup = useRef<(() => void) | undefined>(undefined);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const noteFileInput = useRef<HTMLInputElement>(null);
  const sectionFileInput = useRef<HTMLInputElement>(null);
  const workspaceRef = useRef<HTMLDivElement | null>(null);
  const directoryRef = useRef<HTMLElement | null>(null);
  const directoryRows = useRef(new Map<string, HTMLDivElement>());
  const actionsRef = useRef<HTMLDivElement | null>(null);
  const sidebarResize = useRef<{ startX: number; startWidth: number; max: number } | null>(null);
  const processedInsertion = useRef('');
  const noteRef = useRef<DocumentReadingNote | undefined>(note);
  noteRef.current = note;

  const activeEntry = useMemo(
    () => note?.directory.sections.find((entry) => entry.id === activeSectionId),
    [note, activeSectionId],
  );
  const activeCache = activeEntry ? sections[activeEntry.id] : undefined;
  const directoryTree = useMemo(() => buildNoteTree(note?.directory.sections ?? []), [note?.directory]);
  const chapterRootId = getReadingChapter(note?.directory.sections ?? [], activeSectionId)[0]?.id;
  const readingChapter = useMemo(
    () => getReadingChapter(note?.directory.sections ?? [], chapterRootId),
    [note?.directory, chapterRootId],
  );
  const workspaceNarrow = workspaceWidth > 0 && workspaceWidth < NOTES_DRAWER_BREAKPOINT;
  const notesSidebarMax = workspaceWidth > 0
    ? Math.max(NOTES_SIDEBAR_MIN, Math.min(NOTES_SIDEBAR_MAX, workspaceWidth - 260))
    : NOTES_SIDEBAR_MAX;
  const effectiveNotesSidebarWidth = Math.min(notesSidebarWidth, notesSidebarMax);
  // In read view the directory follows the continuous chapter reader; in edit
  // view it follows the explicitly selected section.
  const currentSectionId = viewMode === 'read' ? (visibleSectionId ?? activeSectionId) : activeSectionId;
  const currentEntry = note?.directory.sections.find((entry) => entry.id === currentSectionId);
  const chapterRoots = directoryTree.filter((node) => node.entry.level > 0);
  const chapterIndex = chapterRoots.findIndex((node) => node.entry.id === chapterRootId);
  const chapterPosition = chapterIndex >= 0 && chapterRoots.length > 1
    ? `第 ${chapterIndex + 1} / ${chapterRoots.length} 章`
    : `共 ${readingChapter.length} 节`;
  const foldableSections = useMemo(() => {
    const foldable = new Set<string>();
    const walk = (nodes: NoteTreeNode[]) => nodes.forEach((node) => {
      if (node.children.length > 0) foldable.add(node.entry.id);
      walk(node.children);
    });
    walk(directoryTree);
    return foldable;
  }, [directoryTree]);
  const onVisibleSectionChange = useCallback((sectionId: string) => setVisibleSectionId(sectionId), []);
  const activeSavedBody = activeEntry && activeCache ? sectionBody(activeCache.markdown, activeEntry) : '';

  // Follow the workbench width so directory/content switch between side-by-side
  // and an overlay drawer instead of stacking into a top/bottom split.
  useEffect(() => {
    const element = workspaceRef.current;
    if (!element) {
      setWorkspaceWidth(0);
      return;
    }
    const observer = new ResizeObserver((entries) => {
      setWorkspaceWidth(entries[0]?.contentRect.width ?? element.clientWidth);
    });
    observer.observe(element);
    setWorkspaceWidth(element.clientWidth);
    return () => observer.disconnect();
  }, [note?.revision, note?.directory.sections.length]);

  useEffect(() => {
    if (!workspaceNarrow) setDrawerOpen(false);
  }, [workspaceNarrow]);

  // Reset the breadcrumb/highlight when the selected section or chapter changes.
  useEffect(() => {
    setVisibleSectionId(activeSectionId);
  }, [activeSectionId, chapterRootId, documentId]);

  // Reading can land on a folded grandchild; open its ancestor path so the
  // highlighted directory row is actually visible.
  useEffect(() => {
    if (viewMode !== 'read' || !currentSectionId) return;
    const path = findNoteNodePath(directoryTree, currentSectionId);
    if (!path || path.length < 2) return;
    setFoldedSections((current) => {
      let next: Set<string> | undefined;
      for (const node of path.slice(0, -1)) {
        if (!current.has(node.entry.id)) continue;
        next ??= new Set(current);
        next.delete(node.entry.id);
      }
      return next ?? current;
    });
  }, [currentSectionId, viewMode, directoryTree]);

  // Chapter-first accordion: the active chapter opens, the other chapters fold.
  // The key is document + chapter, so saving a section does not reinitialize
  // the tree or discard the reader's manual expansions.
  const appliedChapterFold = useRef('');
  useEffect(() => {
    if (!note || !chapterRootId) return;
    const key = `${documentId ?? ''}:${chapterRootId}`;
    if (appliedChapterFold.current === key) return;
    appliedChapterFold.current = key;
    const next = new Set(foldableSections);
    next.delete(chapterRootId);
    for (const node of directoryTree) {
      if (node.entry.id !== chapterRootId && node.children.length > 0) next.add(node.entry.id);
    }
    setFoldedSections(next);
  }, [note, documentId, chapterRootId, foldableSections, directoryTree]);

  // Keep the highlighted directory row visible while the chapter body scrolls.
  useEffect(() => {
    const container = directoryRef.current;
    const row = currentSectionId ? directoryRows.current.get(currentSectionId) : undefined;
    if (!container || !row) return;
    const containerRect = container.getBoundingClientRect();
    const rowRect = row.getBoundingClientRect();
    if (rowRect.top < containerRect.top) container.scrollTop += rowRect.top - containerRect.top - 4;
    else if (rowRect.bottom > containerRect.bottom) container.scrollTop += rowRect.bottom - containerRect.bottom + 4;
  }, [currentSectionId, sidebarMode, directoryOpen, drawerOpen, foldedSections]);

  useEffect(() => {
    if (!actionsOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!actionsRef.current?.contains(event.target as Node)) setActionsOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setActionsOpen(false);
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [actionsOpen]);

  const dirty = Boolean(activeEntry && activeCache && draft !== activeSavedBody);
  const activeEvidence = useMemo(
    () => parseEvidenceMarkdown(activeCache?.markdown ?? ''),
    [activeCache?.markdown],
  );
  // The description lives in the section Markdown. The directory stays a
  // plain title list; only the active section header shows its description.
  // Use the live draft so editing the marker updates the header before saving.
  const activeSectionMeta = useMemo(() => {
    if (!activeEntry || !activeCache) return parseNoteSectionMeta('');
    const prefix = splitSectionBody(activeCache.markdown, activeEntry.level).prefix;
    return parseNoteSectionMeta(prefix + draft);
  }, [activeEntry, activeCache, draft]);
  // Insertions only move forward: a child goes after the parent's existing
  // children, and the next sibling goes after the current section's subtree.
  // Preamble (level 0) cannot be a parent; its "next" action appends a new H1.
  const canAddChild = Boolean(activeEntry && activeEntry.level > 0 && activeEntry.level < 3);
  const canAddAfter = Boolean(activeEntry);
  const activeLabel = activeEntry ? sectionLabel(activeEntry) : '所选 section';
  const addLevel = addMode === 'child' && activeEntry
    ? activeEntry.level + 1
    : activeEntry && activeEntry.level > 0 ? activeEntry.level : 1;
  const addDescription = addMode === 'child'
    ? `作为「${activeLabel}」的子节 · H${addLevel}`
    : addMode === 'after'
      ? activeEntry && activeEntry.level > 0
        ? `在「${activeLabel}」之后插入同级 · H${addLevel}`
        : '在笔记末尾插入 H1'
      : '';

  const loadSection = useCallback(async (sectionId: string): Promise<SectionCacheEntry | undefined> => {
    if (!documentId) return undefined;
    const existing = sections[sectionId];
    if (existing) return existing;
    setSectionLoading(sectionId);
    try {
      const markdown = await api.getDocumentReadingNoteSectionContent(documentId, sectionId);
      const entry: SectionCacheEntry = {
        markdown,
        lineCount: lineCount(markdown),
        revision: noteRef.current?.revision ?? 0,
      };
      setSections((current) => ({ ...current, [sectionId]: entry }));
      return entry;
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '读取 section 失败。');
      return undefined;
    } finally {
      setSectionLoading('');
    }
  }, [documentId, sections]);

  // Load the manifest for the selected paper. A missing note is an ordinary
  // empty state; transport/API failures stay visible.
  useEffect(() => {
    setManifestLoading(false);
    setNote(undefined);
    setManifestError('');
    setStatus('');
    setSections({});
    setActiveSectionId(undefined);
    setDraft('');
    setSavedMarkdown('');
    setBlankDraft('');
    setEditorOpen(false);
    setAddOpen(false);
    setPendingEvidence(undefined);
    setViewMode('read');
    setFoldedSections(new Set());
    if (!documentId) return;
    let active = true;
    setManifestLoading(true);
    void api.getDocumentReadingNote(documentId)
      .then((next) => {
        if (!active) return;
        setNote(next);
        setActiveSectionId(next.directory.sections[0]?.id);
        setViewMode(next.directory.sections.length === 0 ? 'edit' : 'read');
      })
      .catch((error) => {
        if (!active) return;
        if (!isNotFound(error)) setManifestError(error instanceof Error ? error.message : '读取阅读笔记失败。');
      })
      .finally(() => { if (active) setManifestLoading(false); });
    return () => { active = false; };
  }, [documentId]);

  // Save with Ctrl/Cmd+S from the note editor or the whole-note dialog. The
  // browser's own Save Page dialog is suppressed only when the note panel has
  // an actionable save target.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey || event.repeat ||
        event.key.toLowerCase() !== 's') {
        return;
      }
      if (!noteRef.current && !editorOpen) return;
      if (!editorOpen && viewMode !== 'edit') return;
      event.preventDefault();
      if (saving) return;
      if (editorOpen) {
        void commitWholeNote();
        return;
      }
      if (noteRef.current?.directory.sections.length === 0) {
        if (blankDraft.trim()) void saveBlankNote();
        return;
      }
      void saveActive();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [blankDraft, commitWholeNote, editorOpen, saveActive, saveBlankNote, saving, viewMode]);

  // Load only the active section body. Draft state always mirrors the saved
  // body after a section switch; unsaved changes are saved before switching.
  useEffect(() => {
    if (!activeEntry) {
      setDraft('');
      setSavedMarkdown('');
      return;
    }
    const cached = sections[activeEntry.id];
    if (cached) {
      setSavedMarkdown(cached.markdown);
      setDraft(sectionBody(cached.markdown, activeEntry));
      return;
    }
    let active = true;
    setSectionLoading(activeEntry.id);
    void api.getDocumentReadingNoteSectionContent(documentId!, activeEntry.id)
      .then((markdown) => {
        if (!active) return;
        const entry: SectionCacheEntry = {
          markdown,
          lineCount: lineCount(markdown),
          revision: noteRef.current?.revision ?? 0,
        };
        setSections((current) => ({ ...current, [activeEntry.id]: entry }));
        setSavedMarkdown(markdown);
        setDraft(sectionBody(markdown, activeEntry));
      })
      .catch((error) => {
        if (!active) return;
        setStatus(error instanceof Error ? error.message : '读取 section 失败。');
      })
      .finally(() => { if (active) setSectionLoading(''); });
    return () => { active = false; };
  }, [activeEntry, documentId, sections]);

  // Only the selected section contributes PDF overlays. Cached bodies for
  // other sections stay available for fast switching but never mark the PDF
  // until the user selects them. The reader still draws only visible pages.
  useEffect(() => {
    if (viewMode === 'read' && readingChapter.length > 0) return;
    const sectionId = activeSectionId;
    const active = sectionId ? sections[sectionId] : undefined;
    if (!sectionId || !active) {
      onEvidenceTargetsChange([]);
      return;
    }
    const targets: NoteEvidenceOverlay[] = [];
    const parsed = parseEvidenceMarkdown(active.markdown);
    parsed.links.forEach((link, index) => {
      if (!link.target || link.target.documentId) return;
      targets.push({
        ...link.target,
        id: `${sectionId}:${index}`,
        label: link.text,
        sectionId,
      });
    });
    onEvidenceTargetsChange(targets);
    return () => onEvidenceTargetsChange([]);
  }, [activeSectionId, sections, onEvidenceTargetsChange, viewMode, readingChapter]);

  // Remember the reading scale per device. The inline font size below is the
  // only thing that changes, so the note reflows instead of being stretched.
  useEffect(() => {
    try {
      window.localStorage.setItem(NOTE_ZOOM_STORAGE_KEY, String(previewZoom));
    } catch {
      // A blocked localStorage only loses the remembered zoom.
    }
  }, [previewZoom]);

  const adjustPreviewZoom = useCallback((direction: number) => {
    setPreviewZoom((current) => clampNoteZoom(current + direction * NOTE_ZOOM_STEP));
  }, []);
  const resetPreviewZoom = useCallback(() => setPreviewZoom(1), []);

  // Ctrl + wheel is bound natively with passive: false so the browser's page
  // zoom does not fire while the pointer is over the Markdown preview. The ref
  // callback re-binds when the preview swaps between the blank and section
  // views.
  const attachPreviewZoom = useCallback((element: HTMLDivElement | null) => {
    previewWheelCleanup.current?.();
    previewWheelCleanup.current = undefined;
    if (!element) return;
    const onWheel = (event: WheelEvent) => {
      if (!event.ctrlKey || event.deltaY === 0) return;
      event.preventDefault();
      adjustPreviewZoom(event.deltaY < 0 ? 1 : -1);
    };
    element.addEventListener('wheel', onWheel, { passive: false });
    previewWheelCleanup.current = () => element.removeEventListener('wheel', onWheel);
  }, [adjustPreviewZoom]);
  useEffect(() => () => {
    previewWheelCleanup.current?.();
    previewWheelCleanup.current = undefined;
  }, []);

  const reloadActiveSection = useCallback(async (sectionId?: string) => {
    if (!documentId) return;
    try {
      const next = await api.getDocumentReadingNote(documentId);
      setNote(next);
      const wanted = sectionId ?? activeSectionId;
      const entry = next.directory.sections.find((item) => item.id === wanted) ?? next.directory.sections[0];
      setActiveSectionId(entry?.id);
      setSections({});
      if (!entry) {
        setDraft('');
        setSavedMarkdown('');
      }
    } catch (error) {
      if (isNotFound(error)) {
        setNote(undefined);
        setSections({});
        setActiveSectionId(undefined);
      } else {
        setStatus(error instanceof Error ? error.message : '重新读取阅读笔记失败。');
      }
    }
  }, [activeSectionId, documentId]);

  async function handleEditError(error: unknown, sectionId?: string): Promise<void> {
    if (isConflict(error)) {
      setStatus('阅读笔记已被其他编辑更新，已重新加载最新内容。');
      await reloadActiveSection(sectionId);
      return;
    }
    if (isUnavailable(error)) {
      setStatus('整篇笔记正在导入，暂不可编辑；请稍后重试。');
      return;
    }
    if (isNotFound(error)) {
      setStatus('阅读笔记已不存在，已重新加载。');
      await reloadActiveSection();
      return;
    }
    setStatus(error instanceof Error ? error.message : '保存阅读笔记失败。');
  }

  function toggleDirectory() {
    setDirectoryOpen((open) => {
      const next = !open;
      try {
        window.localStorage.setItem('library.notesDirectory', next ? 'open' : 'closed');
      } catch {
        // A blocked localStorage only loses the remembered sidebar state.
      }
      return next;
    });
  }

  function chooseSidebarMode(mode: 'directory' | 'evidence') {
    setSidebarMode(mode);
    try {
      window.localStorage.setItem('library.notesSidebarMode', mode);
    } catch {
      // A blocked localStorage only loses the remembered sidebar mode.
    }
  }

  function expandCurrentChapter() {
    setFoldedSections((current) => {
      const next = new Set(current);
      const open = (nodes: NoteTreeNode[]) => nodes.forEach((node) => {
        next.delete(node.entry.id);
        open(node.children);
      });
      const chapter = directoryTree.find((node) => node.entry.id === chapterRootId);
      if (chapter) open([chapter]);
      else next.clear();
      return next;
    });
  }

  function foldAllSections() {
    setFoldedSections(new Set(foldableSections));
  }

  function selectFromDirectory(sectionId: string) {
    void selectSection(sectionId).finally(() => {
      if (workspaceNarrow) setDrawerOpen(false);
    });
  }

  function resetSidebarWidth() {
    setNotesSidebarWidth(NOTES_SIDEBAR_DEFAULT);
    try { window.localStorage.setItem(NOTES_SIDEBAR_STORAGE_KEY, String(NOTES_SIDEBAR_DEFAULT)); } catch { /* blocked storage */ }
  }

  function startSidebarResize(event: React.PointerEvent<HTMLDivElement>) {
    if (event.button !== 0) return;
    const element = workspaceRef.current;
    if (!element) return;
    event.preventDefault();
    const max = Math.max(NOTES_SIDEBAR_MIN, Math.min(NOTES_SIDEBAR_MAX, element.clientWidth - 260));
    sidebarResize.current = {
      startX: event.clientX,
      startWidth: Math.min(notesSidebarWidth, max),
      max,
    };
    const onMove = (moveEvent: PointerEvent) => {
      const drag = sidebarResize.current;
      if (!drag) return;
      const next = Math.round(Math.min(drag.max, Math.max(NOTES_SIDEBAR_MIN, drag.startWidth + moveEvent.clientX - drag.startX)));
      setNotesSidebarWidth(next);
      try { window.localStorage.setItem(NOTES_SIDEBAR_STORAGE_KEY, String(next)); } catch { /* blocked storage */ }
    };
    const onUp = () => {
      sidebarResize.current = null;
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
      window.removeEventListener('pointercancel', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    window.addEventListener('pointercancel', onUp);
  }

  // The read view emits evidence links as buttons (never anchors), so a click
  // can resolve the versioned URL and jump the PDF without leaving the note.
  function handlePreviewClick(event: React.MouseEvent<HTMLDivElement>) {
    const element = (event.target as HTMLElement).closest<HTMLElement>('[data-evidence-href]');
    if (!element) return;
    event.preventDefault();
    const parsed = parseEvidenceHref(element.dataset.evidenceHref ?? '');
    if (!parsed.target) {
      setStatus(parsed.diagnostics.join(' ') || '无法解析证据链接。');
      return;
    }
    if (parsed.target.documentId && parsed.target.documentId !== documentId) {
      setStatus('该证据链接指向其他文档，无法在当前阅读器中定位。');
      return;
    }
    onNavigateEvidence(parsed.target);
  }

  async function persistSection(
    sectionId: string,
    originalMarkdown: string,
    nextMarkdown: string,
    level: number,
  ): Promise<boolean> {
    const currentNote = noteRef.current;
    if (!currentNote || !documentId) return false;
    const diff = diffSectionBody(sectionId, originalMarkdown, nextMarkdown, level);
    if (diff.error) {
      setStatus(diff.error);
      onMessage(diff.error);
      return false;
    }
    if (diff.operations.length === 0) {
      setSavedMarkdown(nextMarkdown);
      setDraft(splitSectionBody(nextMarkdown, level).body);
      return true;
    }
    setSaving(true);
    setStatus('');
    try {
      const result = await api.applyDocumentReadingNoteOperations(documentId, {
        expectedRevision: currentNote.revision,
        operations: diff.operations,
      });
      setNote((current) => current ? { ...current, revision: result.revision, directory: result.directory } : current);
      setSections((current) => {
        const next = { ...current };
        for (const deleted of result.deletedSectionIds ?? []) delete next[deleted];
        if (next[sectionId]) {
          next[sectionId] = {
            markdown: nextMarkdown,
            lineCount: lineCount(nextMarkdown),
            revision: result.revision,
          };
        }
        return next;
      });
      if (sectionId === activeSectionId || !activeSectionId) {
        setActiveSectionId(sectionId);
        setSavedMarkdown(nextMarkdown);
        setDraft(splitSectionBody(nextMarkdown, level).body);
      }
      setStatus('已保存。');
      return true;
    } catch (error) {
      await handleEditError(error, sectionId);
      return false;
    } finally {
      setSaving(false);
    }
  }

  async function saveActive(): Promise<boolean> {
    if (!activeEntry || !activeCache) return true;
    if (!dirty) return true;
    const prefix = splitSectionBody(activeCache.markdown, activeEntry.level).prefix;
    return persistSection(activeEntry.id, activeCache.markdown, prefix + draft, activeEntry.level);
  }

  async function selectSection(sectionId: string) {
    setReadingNavigation((value) => value + 1);
    if (sectionId === activeSectionId || saving) return;
    if (dirty && !(await saveActive())) return;
    setStatus('');
    setAddOpen(false);
    setActiveSectionId(sectionId);
  }

  function openAddForm(mode: 'child' | 'after') {
    if (!activeEntry) {
      setStatus('请先选择一个 section 作为插入位置。');
      return;
    }
    if (mode === 'child' && !canAddChild) {
      setStatus(activeEntry.level === 0 ? '前言不能作为父节。' : 'H3 已是最低级别，不能再增加子节。');
      return;
    }
    setAddMode(mode);
    setAddTitle('');
    setAddBody('');
    setAddOpen(true);
  }

  async function applyEditResult(
    directory: ReadingNoteDirectory,
    revision: number,
    sectionId: string,
    nextMarkdown: string,
    level: number,
    deleted: string[],
  ) {
    setNote((current) => current ? { ...current, revision, directory } : current);
    setSections((current) => {
      const next = { ...current };
      for (const id of deleted) delete next[id];
      next[sectionId] = { markdown: nextMarkdown, lineCount: lineCount(nextMarkdown), revision };
      return next;
    });
    setActiveSectionId(sectionId);
    setSavedMarkdown(nextMarkdown);
    setDraft(splitSectionBody(nextMarkdown, level).body);
  }

  async function addSection() {
    const currentNote = noteRef.current;
    if (!currentNote || !documentId) return;
    const title = addTitle.trim();
    if (!title) {
      setStatus('请填写 section 标题。');
      return;
    }
    const target = activeEntry;
    if (!target || !addMode) return;
    const level = addMode === 'child'
      ? target.level + 1
      : target.level > 0 ? target.level : 1;
    const markdown = newSectionMarkdown(level, title, addBody);
    const operation: ReadingNoteOperation = { op: 'addSection', markdown };
    if (addMode === 'child') {
      operation.parentId = target.id;
    } else {
      operation.afterId = target.id;
      if (target.parentId) operation.parentId = target.parentId;
    }
    setSaving(true);
    setStatus('');
    try {
      const result = await api.applyDocumentReadingNoteOperations(documentId, {
        expectedRevision: currentNote.revision,
        operations: [operation],
      });
      const changed = result.changedSections[0];
      if (!changed) throw new Error('服务端未返回新增的 section。');
      await applyEditResult(result.directory, result.revision, changed.id, markdown, changed.level, []);
      setAddOpen(false);
      setAddMode(undefined);
      setAddTitle('');
      setAddBody('');
      setStatus(`已新增 section「${changed.title}」。`);
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function deleteActiveSection() {
    const currentNote = noteRef.current;
    if (!currentNote || !documentId || !activeEntry) return;
    if (currentNote.directory.sections.length <= 1) {
      setStatus('笔记至少需要一个 section；如不再需要请删除整份笔记。');
      return;
    }
    if (!window.confirm(`删除 section「${sectionLabel(activeEntry)}」及其子 section？`)) return;
    setSaving(true);
    setStatus('');
    try {
      const result = await api.applyDocumentReadingNoteOperations(documentId, {
        expectedRevision: currentNote.revision,
        operations: [{ op: 'deleteSection', sectionId: activeEntry.id }],
      });
      const deleted = [...(result.deletedSectionIds ?? [])];
      setNote((current) => current ? { ...current, revision: result.revision, directory: result.directory } : current);
      setSections((current) => {
        const next = { ...current };
        for (const id of deleted) delete next[id];
        return next;
      });
      const nextEntry = result.directory.sections[0];
      setActiveSectionId(nextEntry?.id);
      setDraft('');
      setSavedMarkdown('');
      setStatus(`已删除 ${deleted.length || 1} 个 section。`);
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function openEditor(mode: 'create' | 'import') {
    setEditorMode(mode);
    setStatus('');
    if (mode === 'create') {
      const evidence = pendingEvidence ? formatEvidenceMarkdown(pendingEvidence) : '';
      setEditorMarkdown(evidence ? `# 总结\n\n${evidence}\n\n# 问题\n` : DEFAULT_NOTE);
      setEditorOpen(true);
      return;
    }
    if (!documentId) return;
    try {
      const markdown = await api.getDocumentReadingNoteExport(documentId);
      setEditorMarkdown(markdown);
      setEditorOpen(true);
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '读取整篇笔记失败。');
    }
  }

  async function commitWholeNote() {
    if (!documentId) return;
    if (!editorMarkdown.trim()) {
      setStatus('笔记 Markdown 不能为空。');
      return;
    }
    if (exceedsNoteLimit(markdownBytes(editorMarkdown))) {
      setStatus(`笔记超过 ${noteLimitLabel()} 上限，未保存。`);
      return;
    }
    setSaving(true);
    setStatus('');
    try {
      const next = await api.putDocumentReadingNote(
        documentId, editorMarkdown, noteRef.current?.revision ?? 0,
      );
      setNote(next);
      setSections({});
      setActiveSectionId(next.directory.sections[0]?.id);
      setDraft('');
      setSavedMarkdown('');
      setEditorOpen(false);
      setPendingEvidence(undefined);
      setViewMode(editorMode === 'create' ? 'edit' : 'read');
      setStatus(noteRef.current ? '已导入并替换整篇笔记。' : '阅读笔记已创建。');
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function deleteNote() {
    const currentNote = noteRef.current;
    if (!currentNote || !documentId) return;
    if (!window.confirm('删除整份阅读笔记及其全部 section？该操作不可撤销。')) return;
    setSaving(true);
    setStatus('');
    try {
      await api.deleteDocumentReadingNote(documentId, currentNote.revision);
      setNote(undefined);
      setSections({});
      setActiveSectionId(undefined);
      setDraft('');
      setSavedMarkdown('');
      setViewMode('read');
      setStatus('阅读笔记已删除。');
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function createBlankNote() {
    if (!documentId || noteRef.current) return;
    setSaving(true);
    setStatus('');
    try {
      const next = await api.putDocumentReadingNote(documentId, '', 0);
      setNote(next);
      setSections({});
      setActiveSectionId(undefined);
      setBlankDraft('');
      setViewMode('edit');
      setStatus('已创建空白阅读笔记，可直接输入 Markdown。');
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function saveBlankNote() {
    const currentNote = noteRef.current;
    if (!documentId || !currentNote) return;
    if (exceedsNoteLimit(markdownBytes(blankDraft))) {
      setStatus(`笔记超过 ${noteLimitLabel()} 上限，未保存。`);
      return;
    }
    setSaving(true);
    setStatus('');
    try {
      const next = await api.putDocumentReadingNote(documentId, blankDraft, currentNote.revision);
      setNote(next);
      setSections({});
      setActiveSectionId(next.directory.sections[0]?.id);
      setBlankDraft('');
      setViewMode('edit');
      setStatus(next.directory.sections.length > 0
        ? `已保存，并拆分为 ${next.directory.sections.length} 个 section。`
        : '空白笔记已保存。');
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function uploadWholeNote(file: File) {
    if (!documentId) return;
    if (exceedsNoteLimit(file.size)) {
      setStatus(`文件超过 ${noteLimitLabel()} 上限，未上传。`);
      return;
    }
    const currentNote = noteRef.current;
    if (currentNote && !window.confirm('上传会替换整篇阅读笔记，继续？')) return;
    setSaving(true);
    setStatus('');
    try {
      const next = await api.uploadDocumentReadingNoteFile(documentId, file, currentNote?.revision ?? 0);
      setNote(next);
      setSections({});
      setActiveSectionId(next.directory.sections[0]?.id);
      setBlankDraft('');
      setViewMode('read');
      setStatus(`已上传整篇 Markdown（${next.directory.sections.length} 个 section）。`);
    } catch (error) {
      await handleEditError(error);
    } finally {
      setSaving(false);
    }
  }

  async function uploadSectionNote(file: File) {
    const currentNote = noteRef.current;
    if (!documentId || !currentNote || !activeEntry) return;
    if (exceedsNoteLimit(file.size)) {
      setStatus(`文件超过 ${noteLimitLabel()} 上限，未上传。`);
      return;
    }
    if (!window.confirm(`上传会替换 section「${sectionLabel(activeEntry)}」，继续？`)) return;
    setSaving(true);
    setStatus('');
    const target = activeEntry;
    let uploaded = false;
    try {
      const result = await api.uploadDocumentReadingNoteSectionFile(documentId, target.id, file, currentNote.revision);
      uploaded = true;
      // Upload through the file stream, then read the stored section once for
      // the editor. Do not pre-read the whole File into a second JS string.
      const markdown = await api.getDocumentReadingNoteSectionContent(documentId, target.id);
      setNote((current) => current ? { ...current, revision: result.revision, directory: result.directory } : current);
      setSections((current) => ({
        ...current,
        [target.id]: { markdown, lineCount: lineCount(markdown), revision: result.revision },
      }));
      setSavedMarkdown(markdown);
      setDraft(splitSectionBody(markdown, target.level).body);
      setStatus(`已替换 section「${sectionLabel(target)}」。`);
    } catch (error) {
      if (uploaded) {
        setStatus(error instanceof Error ? `已上传，但读取最新 section 失败：${error.message}` : '已上传，但读取最新 section 失败。');
        await reloadActiveSection(target.id);
      } else {
        await handleEditError(error, target.id);
      }
    } finally {
      setSaving(false);
    }
  }

  async function exportNote() {
    if (!documentId) return;
    try {
      const markdown = await api.getDocumentReadingNoteExport(documentId);
      const url = URL.createObjectURL(new Blob([markdown], { type: 'text/markdown;charset=utf-8' }));
      const link = window.document.createElement('a');
      link.href = url;
      link.download = `reading-note-${documentId}.md`;
      link.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 10000);
      setStatus('已导出整篇 Markdown。');
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '导出阅读笔记失败。');
    }
  }

  const insertSeed = useCallback(async (seed: EvidenceSeed) => {
    const currentNote = noteRef.current;
    if (!currentNote || !documentId) return;
    const sectionId = activeSectionId ?? currentNote.directory.sections[0]?.id;
    if (!sectionId) {
      setPendingEvidence(seed);
      setStatus('请先创建阅读笔记，再插入证据。');
      return;
    }
    const entry = currentNote.directory.sections.find((item) => item.id === sectionId);
    if (!entry) return;
    const cached = sections[sectionId] ?? await loadSection(sectionId);
    if (!cached) return;

    const baseMarkdown = sectionId === activeSectionId && savedMarkdown === cached.markdown
      ? splitSectionBody(cached.markdown, entry.level).prefix + draft
      : cached.markdown;
    const split = splitSectionBody(baseMarkdown, entry.level);
    let offset = countCodepoints(split.body);
    const textarea = textareaRef.current;
    if (sectionId === activeSectionId && textarea && window.document.activeElement === textarea) {
      offset = countCodepoints(split.body.slice(0, textarea.selectionStart));
    }
    const evidence = formatEvidenceMarkdown(seed);
    if (!evidence) {
      setStatus('当前选择没有可写入的证据内容。');
      return;
    }
    const nextMarkdown = split.prefix + insertEvidenceIntoBody(split.body, offset, evidence);
    if (await persistSection(sectionId, cached.markdown, nextMarkdown, entry.level)) {
      setStatus(`已插入第 ${seed.pageNumber ?? '?'} 页证据链接到「${sectionLabel(entry)}」。`);
    }
  }, [activeSectionId, documentId, draft, loadSection, savedMarkdown, sections]);

  // Evidence insertion arrives from the reader. Wait for the manifest first;
  // when no note exists, offer creation instead of creating one silently.
  useEffect(() => {
    if (!insertion || processedInsertion.current === insertion.id) return;
    if (!documentId) {
      processedInsertion.current = insertion.id;
      setStatus('本地文件没有阅读笔记。');
      onInsertionConsumed?.(insertion.id);
      return;
    }
    if (manifestLoading) return;
    if (manifestError) {
      processedInsertion.current = insertion.id;
      setStatus(manifestError);
      onInsertionConsumed?.(insertion.id);
      return;
    }
    if (!note) {
      processedInsertion.current = insertion.id;
      setPendingEvidence(insertion.seed);
      setStatus('还没有阅读笔记。请创建笔记后插入证据。');
      onInsertionConsumed?.(insertion.id);
      return;
    }
    processedInsertion.current = insertion.id;
    void insertSeed(insertion.seed).finally(() => onInsertionConsumed?.(insertion.id));
  }, [documentId, insertSeed, insertion, manifestError, manifestLoading, note, onInsertionConsumed]);

  const editorHeading = activeEntry ? sectionLabel(activeEntry) : '';
  const viewTabs = <div className="reading-note-view-tabs" role="group" aria-label="阅读注释视图">
    <button type="button" aria-pressed={viewMode === 'read'}
      className={viewMode === 'read' ? 'active' : ''}
      title="只读预览，证据链接可直接定位 PDF"
      onClick={() => { void (async () => { if (!dirty || await saveActive()) setViewMode('read'); })(); }}>阅读</button>
    <button type="button" aria-pressed={viewMode === 'edit'}
      className={viewMode === 'edit' ? 'active' : ''}
      title="编辑 Markdown"
      onClick={() => setViewMode('edit')}>编辑{dirty && <span className="reading-note-dirty" title="有未保存修改">●</span>}</button>
  </div>;
  const previewStyle = { fontSize: `${NOTE_PREVIEW_BASE_PX * previewZoom}px` };
  const zoomControl = viewMode === 'read'
    ? <button type="button" className="reading-note-zoom"
      title="Ctrl + 滚轮调整阅读字号；点击恢复 100%"
      aria-label={`阅读字号 ${Math.round(previewZoom * 100)}%，点击恢复 100%`}
      onClick={resetPreviewZoom}>{Math.round(previewZoom * 100)}%</button>
    : null;
  const drawerToggle = workspaceNarrow
    ? <button type="button" className="reading-note-drawer-toggle" aria-expanded={drawerOpen}
      title={drawerOpen ? '关闭目录与证据' : '打开目录与证据'}
      onClick={() => setDrawerOpen((open) => !open)}>目录</button>
    : null;
  // Whole-note actions are rare compared with reading/editing, so they live in
  // a compact overflow menu instead of a permanent toolbar row.
  const noteActions = note ? <div className="reading-note-overflow" ref={actionsRef}>
    <button type="button" className="reading-note-overflow-trigger" aria-label="更多笔记操作"
      aria-haspopup="menu" aria-expanded={actionsOpen}
      onClick={() => setActionsOpen((open) => !open)}>⋯</button>
    {actionsOpen && <div className="reading-note-overflow-menu" role="menu" aria-label="整篇笔记操作">
      <button type="button" role="menuitem" disabled={saving}
        onClick={() => { setActionsOpen(false); noteFileInput.current?.click(); }}>上传整篇 .md</button>
      <button type="button" role="menuitem" disabled={saving}
        onClick={() => { setActionsOpen(false); void openEditor('import'); }}>导入并替换</button>
      <button type="button" role="menuitem" disabled={saving}
        onClick={() => { setActionsOpen(false); void exportNote(); }}>导出</button>
      <button type="button" role="menuitem" className="reading-note-overflow-danger" disabled={saving}
        onClick={() => { setActionsOpen(false); void deleteNote(); }}>删除笔记</button>
    </div>}
  </div> : null;
  const headActions = <div className="reading-note-head-actions">{drawerToggle}{zoomControl}{viewTabs}{noteActions}</div>;

  function renderDirectory(nodes: NoteTreeNode[], depth = 0) {
    return <ul className="reading-note-tree-list">{nodes.map(({ entry, number, children }) => {
      const folded = foldedSections.has(entry.id);
      const chapter = depth === 0;
      const highlighted = entry.id === currentSectionId;
      return <li key={entry.id} className={`reading-note-tree-node level-${entry.level}${chapter ? ' chapter' : ''}`}>
        <div className={`reading-note-tree-row${highlighted ? ' active' : ''}${entry.id === chapterRootId ? ' current-chapter' : ''}`}
          ref={(node) => { if (node) directoryRows.current.set(entry.id, node); else directoryRows.current.delete(entry.id); }}>
          {children.length > 0 ? <button type="button" className="reading-note-tree-toggle"
            aria-label={`${folded ? '展开' : '折叠'}${sectionLabel(entry)}`} aria-expanded={!folded}
            onClick={() => setFoldedSections((current) => {
              const next = new Set(current);
              if (next.has(entry.id)) next.delete(entry.id); else next.add(entry.id);
              return next;
            })}>
            <svg className={`reading-note-tree-chevron${folded ? '' : ' expanded'}`} viewBox="0 0 12 12" aria-hidden="true">
              <path d="M4.25 2.5 7.75 6l-3.5 3.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button> : <span className="reading-note-tree-spacer" />}
          <button type="button" className="reading-note-tree-select" aria-current={highlighted ? 'location' : undefined}
            disabled={saving} onClick={() => selectFromDirectory(entry.id)} title={sectionLabel(entry)}>
            {!chapter && <span className="reading-note-tree-number">{number}</span>}
            <span className="reading-note-section-title">{sectionLabel(entry)}</span>
            {entry.id === activeSectionId && dirty && <span className="reading-note-dirty">●</span>}
          </button>
        </div>
        {children.length > 0 && !folded && renderDirectory(children, depth + 1)}
      </li>;
    })}</ul>;
  }

  function renderSidebar(mode: 'open' | 'collapsed' | 'drawer') {
    if (mode === 'collapsed') {
      return <aside className="reading-note-sidebar collapsed" aria-label="笔记侧边栏">
        <button className="reading-note-sidebar-rail" type="button" aria-expanded="false"
          title={`展开侧边栏（${sidebarMode === 'evidence' ? '证据' : '目录'}）`}
          aria-label={`展开侧边栏（${sidebarMode === 'evidence' ? '证据' : '目录'}）`} onClick={toggleDirectory}>
          <span aria-hidden="true">›</span>
          <span className="reading-note-sidebar-rail-label">{sidebarMode === 'evidence' ? '证据' : '目录'}</span>
        </button>
      </aside>;
    }
    return <aside className={`reading-note-sidebar${mode === 'drawer' ? ' drawer' : ''}`} aria-label="笔记侧边栏"
      style={mode === 'open' ? { width: effectiveNotesSidebarWidth } : undefined}>
      <div className="reading-note-sidebar-head">
        <div className="reading-note-sidebar-tabs" role="group" aria-label="侧边栏内容">
          <button type="button" aria-pressed={sidebarMode === 'directory'}
            className={sidebarMode === 'directory' ? 'active' : ''}
            title="笔记 section 目录"
            onClick={() => chooseSidebarMode('directory')}>目录</button>
          <button type="button" aria-pressed={sidebarMode === 'evidence'}
            className={sidebarMode === 'evidence' ? 'active' : ''}
            title="当前 section 的证据定位"
            onClick={() => chooseSidebarMode('evidence')}>
            证据{activeEvidence.links.length > 0 ? ` ${activeEvidence.links.length}` : ''}
          </button>
        </div>
        <button className="reading-note-sidebar-collapse" type="button" aria-expanded="true"
          title={mode === 'drawer' ? '关闭目录' : '收起侧边栏'} aria-label={mode === 'drawer' ? '关闭目录' : '收起侧边栏'}
          onClick={mode === 'drawer' ? () => setDrawerOpen(false) : toggleDirectory}>{mode === 'drawer' ? '×' : '‹'}</button>
      </div>
      {sidebarMode === 'directory' ? <>
        <div className="reading-note-tree-actions">
          <span>{note?.directory.sections.length ?? 0} 节</span>
          <button type="button" onClick={expandCurrentChapter}>展开本章</button>
          <button type="button" onClick={foldAllSections}>折叠全部</button>
        </div>
        <nav className="reading-note-directory" aria-label="笔记 section 目录" ref={directoryRef}>{renderDirectory(directoryTree)}</nav>
      </> : <div className="reading-note-evidence-pane">
        {!activeEntry
          ? <p className="hint">选择一个 section 查看证据链接。</p>
          : activeEvidence.links.length === 0 && activeEvidence.diagnostics.length === 0
            ? <p className="hint">当前 section 没有证据链接。在 PDF 中选择内容后点「插入证据」。</p>
            : <>
              {activeEvidence.links.length > 0 && <ol className="reading-note-evidence-list">
                {activeEvidence.links.map((link, index) => {
                  const target = link.target;
                  const external = target?.documentId && target.documentId !== documentId;
                  return <li key={`${link.href}-${index}`}>
                    <span title={link.text}>{target ? `第 ${target.page} 页` : '无效链接'}</span>
                    {target && !external
                      ? <button type="button" aria-label={`定位第 ${target.page} 页`}
                        onClick={() => { onNavigateEvidence(target); if (workspaceNarrow) setDrawerOpen(false); }}>定位</button>
                      : <span className="reading-note-diagnostic">{external ? '指向其他文档' : '无法定位'}</span>}
                    {link.diagnostics.map((diagnostic) => <span className="reading-note-diagnostic" key={diagnostic}>{diagnostic}</span>)}
                  </li>;
                })}
              </ol>}
              {activeEvidence.diagnostics.map((diagnostic) => <p className="reading-note-diagnostic" key={diagnostic}>{diagnostic}</p>)}
            </>}
      </div>}
    </aside>;
  }

  return <section className="library-panel library-panel-notes" aria-label="阅读注释">
    <input ref={noteFileInput} type="file" accept=".md,text/markdown,text/plain" hidden
      onChange={(event) => {
        const file = event.target.files?.[0];
        event.target.value = '';
        if (file) void uploadWholeNote(file);
      }} />
    <input ref={sectionFileInput} type="file" accept=".md,text/markdown,text/plain" hidden
      onChange={(event) => {
        const file = event.target.files?.[0];
        event.target.value = '';
        if (file) void uploadSectionNote(file);
      }} />
    {!documentId ? <p className="hint">本地打开的 PDF 没有阅读笔记；先导入到论文库后即可使用。</p>
      : manifestLoading ? <p className="hint">正在读取阅读笔记…</p>
      : manifestError ? <p className="reading-note-error">{manifestError}</p>
      : !note ? <div className="reading-note-empty">
        <div className="reading-note-empty-head">
          <strong>开始阅读注释</strong>
          <p>为这篇论文建立一份 Markdown 阅读笔记，阅读与编辑分开。</p>
        </div>
        {pendingEvidence && <div className="reading-note-pending">
          <strong>待插入证据</strong>
          <p>{pendingEvidence.excerpt.trim().slice(0, 240) || `第 ${pendingEvidence.pageNumber ?? '?'} 页选择`}</p>
          <span>创建笔记后只在「总结」section 插入第 {pendingEvidence.pageNumber ?? '?'} 页证据链接，不复制原文。</span>
        </div>}
        <div className="reading-note-start-actions" role="group" aria-label="创建阅读笔记">
          <button className="reading-note-start-option primary" type="button" disabled={saving}
            aria-label={pendingEvidence ? '创建笔记并插入证据' : '创建笔记'}
            onClick={() => void openEditor('create')}>
            <span className="reading-note-start-label">{pendingEvidence ? '创建笔记并插入证据' : '创建笔记'}</span>
            <span className="reading-note-start-desc">使用「总结 / 问题」模板开始</span>
          </button>
          <button className="reading-note-start-option" type="button" disabled={saving}
            aria-label="空白开始" onClick={() => void createBlankNote()}>
            <span className="reading-note-start-label">空白开始</span>
            <span className="reading-note-start-desc">不创建 section，直接输入 Markdown</span>
          </button>
          <button className="reading-note-start-option" type="button" disabled={saving}
            aria-label="上传 .md" onClick={() => noteFileInput.current?.click()}>
            <span className="reading-note-start-label">上传 .md</span>
            <span className="reading-note-start-desc">导入整篇文件并自动拆分 section</span>
          </button>
        </div>
        {pendingEvidence && <button className="reading-note-cancel" type="button" disabled={saving}
          onClick={() => setPendingEvidence(undefined)}>取消待插入</button>}
      </div>
      : note.directory.sections.length === 0 ? <div className="reading-note-blank">
        <div className="reading-note-blank-head">
          <div className="reading-note-section-meta">
            <strong>空白笔记</strong>
            <span>尚未拆分 section</span>
          </div>
          {headActions}
        </div>
        {viewMode === 'read'
          ? blankDraft.trim()
            ? <div className="reading-note-preview" role="document" aria-label="空白笔记阅读视图"
              ref={attachPreviewZoom}
              style={previewStyle}
              onClick={handlePreviewClick}
              dangerouslySetInnerHTML={{ __html: renderReadingNoteMarkdown(blankDraft) }} />
            : <div className="reading-note-preview-empty">空白笔记还没有内容。切换到「编辑」开始写 Markdown。</div>
          : <>
            <p className="hint">直接输入 Markdown；保存后会按 H1-H3 拆分 section，没有标题的内容会成为前言。</p>
            <textarea className="reading-note-body" aria-label="空白阅读笔记 Markdown"
              value={blankDraft} onChange={(event) => setBlankDraft(event.target.value)} spellCheck={false} readOnly={saving} />
            <div className="reading-note-editor-actions">
              <button className="primary" type="button" disabled={saving || !blankDraft.trim()} onClick={() => void saveBlankNote()}>
                {saving ? '保存中…' : '保存'}
              </button>
              <button type="button" disabled={saving} onClick={() => noteFileInput.current?.click()}>上传整篇 .md</button>
            </div>
          </>}
      </div>
      : <>
        <div className={`reading-note-workspace${workspaceNarrow ? ' narrow' : ''}`} ref={workspaceRef}>
          {workspaceNarrow
            ? drawerOpen && <>
              <div className="reading-note-drawer-backdrop" role="presentation" onClick={() => setDrawerOpen(false)} />
              {renderSidebar('drawer')}
            </>
            : <>
              {renderSidebar(directoryOpen ? 'open' : 'collapsed')}
              {directoryOpen && <div className="reading-note-splitter" role="separator" aria-orientation="vertical"
                title="拖动调整目录宽度，双击恢复默认"
                onPointerDown={startSidebarResize} onDoubleClick={resetSidebarWidth} />}
            </>}
          <div className="reading-note-content">
            {activeEntry && <div className="reading-note-section-head">
              <div className="reading-note-section-meta">
                <strong title={viewMode === 'read' ? (readingChapter[0] ? sectionLabel(readingChapter[0]) : '') : editorHeading}>
                  {viewMode === 'read' ? (readingChapter[0] ? sectionLabel(readingChapter[0]) : '') : editorHeading}
                </strong>
                {viewMode === 'read'
                  ? currentEntry && currentEntry.id !== chapterRootId
                    ? <span className="reading-note-breadcrumb">{sectionLabel(currentEntry)}</span>
                    : <span className="reading-note-breadcrumb">{chapterPosition}</span>
                  : activeSectionMeta.meta.description && <span className="reading-note-section-description">{activeSectionMeta.meta.description}</span>}
              </div>
              {headActions}
            </div>}
            {viewMode === 'edit' && activeEntry && <div className="reading-note-section-actions" role="group" aria-label="新增 section">
              <button type="button" disabled={saving || !canAddChild}
                title={activeEntry.level === 0 ? '前言不能作为父节' : activeEntry.level >= 3 ? 'H3 已是最低级别' : '在当前 section 下新增子节'}
                onClick={() => openAddForm('child')}>增加子节</button>
              <button type="button" disabled={saving || !canAddAfter}
                title={activeEntry.level === 0 ? '在笔记末尾插入 H1' : '在当前 section 之后插入同级 section'}
                onClick={() => openAddForm('after')}>插入下一节</button>
            </div>}
            {viewMode === 'edit' && addOpen && addMode && <div className="reading-note-inline-form">
              <p className="reading-note-add-description">{addDescription}</p>
              <input value={addTitle} onChange={(event) => setAddTitle(event.target.value)} placeholder="section 标题" disabled={saving} />
              <textarea value={addBody} onChange={(event) => setAddBody(event.target.value)} placeholder="正文（可选）" disabled={saving} rows={3} />
              <div>
                <button className="primary" type="button" disabled={saving || !addTitle.trim()} onClick={() => void addSection()}>新增</button>
                <button type="button" disabled={saving} onClick={() => setAddOpen(false)}>取消</button>
              </div>
            </div>}
            {viewMode === 'read' && documentId && readingChapter.length > 0
              ? <ReadingNoteChapter key={`${documentId}:${chapterRootId}:${note.revision}`}
                documentId={documentId} entries={readingChapter} selectedId={activeSectionId} navigation={readingNavigation}
                fontSize={NOTE_PREVIEW_BASE_PX * previewZoom} attachZoom={attachPreviewZoom}
                onClick={handlePreviewClick} onEvidenceTargetsChange={onEvidenceTargetsChange}
                onVisibleSectionChange={onVisibleSectionChange} />
              : sectionLoading === activeSectionId ? <p className="hint">正在读取 section…</p>
              : activeEntry ? <>
                {viewMode === 'edit' ? <>
                  <textarea
                    ref={textareaRef}
                    className="reading-note-body"
                    aria-label={`${editorHeading} 正文`}
                    value={draft}
                    readOnly={saving}
                    onChange={(event) => setDraft(event.target.value)}
                    spellCheck={false}
                  />
                  <div className="reading-note-editor-actions">
                    <button className="primary" type="button" disabled={saving || !dirty} onClick={() => void saveActive()}>{saving ? '保存中…' : '保存'}</button>
                    <button type="button" disabled={saving} onClick={() => sectionFileInput.current?.click()}>上传本节 .md</button>
                    <button className="reading-note-danger" type="button" disabled={saving} onClick={() => void deleteActiveSection()}>删除 section</button>
                    {dirty && <span className="reading-note-dirty-label">未保存</span>}
                  </div>
                </> : draft.trim()
                  ? <div className="reading-note-preview" role="document" aria-label={`${editorHeading} 阅读视图`}
                    ref={attachPreviewZoom}
                    style={previewStyle}
                    onClick={handlePreviewClick}
                    dangerouslySetInnerHTML={{ __html: renderReadingNoteMarkdown(draft) }} />
                  : <div className="reading-note-preview-empty">这个 section 还没有正文。切换到「编辑」开始写。</div>}
              </> : <p className="hint">选择一个 section 开始阅读。</p>}
          </div>
        </div>
      </>}
    {status && <p className="reading-note-status" role="status">{status}</p>}

    {editorOpen && <div className="reading-note-dialog" role="dialog" aria-modal="true" aria-label={editorMode === 'create' ? '创建阅读笔记' : '导入阅读笔记'}>
      <header>
        <strong>{editorMode === 'create' ? '创建阅读笔记' : '导入并替换整篇笔记'}</strong>
        <button type="button" disabled={saving} onClick={() => setEditorOpen(false)}>关闭</button>
      </header>
      <p className="hint">{editorMode === 'create'
        ? 'Markdown 会按 H1-H3 拆成 section；之后只编辑单个 section。'
        : '导入会用整篇 Markdown 替换所有 section，旧 section 文件会被清理。'}</p>
      <textarea value={editorMarkdown} onChange={(event) => setEditorMarkdown(event.target.value)} readOnly={saving} spellCheck={false} />
      <footer>
        <button className="primary" type="button" disabled={saving || !editorMarkdown.trim()} onClick={() => void commitWholeNote()}>{saving ? '提交中…' : editorMode === 'create' ? '创建' : '导入并替换'}</button>
        <button type="button" disabled={saving} onClick={() => setEditorOpen(false)}>取消</button>
      </footer>
    </div>}
  </section>;
}
