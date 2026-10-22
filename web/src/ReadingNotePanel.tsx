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
  const previewWheelCleanup = useRef<(() => void) | undefined>(undefined);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const noteFileInput = useRef<HTMLInputElement>(null);
  const sectionFileInput = useRef<HTMLInputElement>(null);
  const processedInsertion = useRef('');
  const noteRef = useRef<DocumentReadingNote | undefined>(note);
  noteRef.current = note;

  const activeEntry = useMemo(
    () => note?.directory.sections.find((entry) => entry.id === activeSectionId),
    [note, activeSectionId],
  );
  const activeCache = activeEntry ? sections[activeEntry.id] : undefined;
  const activeSavedBody = activeEntry && activeCache ? sectionBody(activeCache.markdown, activeEntry) : '';
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
  }, [activeSectionId, sections, onEvidenceTargetsChange]);

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
      setStatus(`已保存 revision ${result.revision}。`);
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
      setStatus(noteRef.current ? `已导入并替换为 revision ${next.revision}。` : `阅读笔记已创建（revision ${next.revision}）。`);
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
        ? `已保存 revision ${next.revision}，并拆分为 ${next.directory.sections.length} 个 section。`
        : `空白笔记已保存 revision ${next.revision}。`);
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
      setStatus(`已上传整篇 Markdown（revision ${next.revision}，${next.directory.sections.length} 个 section）。`);
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
      setStatus(`已替换 section「${sectionLabel(target)}」（revision ${result.revision}）。`);
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
      onClick={() => setViewMode('read')}>阅读</button>
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
  const headActions = <div className="reading-note-head-actions">{zoomControl}{viewTabs}</div>;

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
    <div className="reading-note-toolbar">
      <div className="reading-note-title">
        <strong>阅读注释</strong>
        {note && <span className="reading-note-revision">r{note.revision}</span>}
      </div>
      <div className="reading-note-toolbar-actions" role="group" aria-label="整篇笔记操作">
        {note && <button type="button" disabled={saving} title="上传整篇 Markdown 并替换当前笔记"
          onClick={() => noteFileInput.current?.click()}>上传 .md</button>}
        {note && <button type="button" disabled={saving} title="编辑并按 revision 替换整篇笔记"
          onClick={() => void openEditor('import')}>导入</button>}
        {note && <button type="button" disabled={saving} title="下载整篇 Markdown" onClick={() => void exportNote()}>导出</button>}
        {note && <button className="reading-note-danger" type="button" disabled={saving} title="删除整份阅读笔记"
          onClick={() => void deleteNote()}>删除</button>}
      </div>
    </div>

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
            <span>r{note.revision} · 尚未拆分 section</span>
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
        <div className="reading-note-workspace">
          <aside className={`reading-note-sidebar${directoryOpen ? '' : ' collapsed'}`} aria-label="笔记侧边栏">
            {directoryOpen ? <>
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
                  title="收起侧边栏" aria-label="收起侧边栏" onClick={toggleDirectory}>‹</button>
              </div>
              {sidebarMode === 'directory' ? <div className="reading-note-directory" role="tree" aria-label="笔记 section 目录">
                {note.directory.sections.map((entry) => <button
                  key={entry.id}
                  type="button"
                  role="treeitem"
                  aria-selected={entry.id === activeSectionId}
                  className={entry.id === activeSectionId ? 'active' : ''}
                  style={{ paddingLeft: `${5 + entry.level * 10}px` }}
                  disabled={saving || Boolean(sectionLoading)}
                  onClick={() => void selectSection(entry.id)}
                  title={sectionLabel(entry)}
                >
                  <span className="reading-note-section-title">{sectionLabel(entry)}</span>
                  {entry.id === activeSectionId && dirty && <span className="reading-note-dirty" title="有未保存修改">●</span>}
                </button>)}
              </div> : <div className="reading-note-evidence-pane">
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
                              ? <button type="button" aria-label={`定位第 ${target.page} 页`} onClick={() => onNavigateEvidence(target)}>定位</button>
                              : <span className="reading-note-diagnostic">{external ? '指向其他文档' : '无法定位'}</span>}
                            {link.diagnostics.map((diagnostic) => <span className="reading-note-diagnostic" key={diagnostic}>{diagnostic}</span>)}
                          </li>;
                        })}
                      </ol>}
                      {activeEvidence.diagnostics.map((diagnostic) => <p className="reading-note-diagnostic" key={diagnostic}>{diagnostic}</p>)}
                    </>}
              </div>}
            </> : <button className="reading-note-sidebar-rail" type="button" aria-expanded="false"
              title={`展开侧边栏（${sidebarMode === 'evidence' ? '证据' : '目录'}）`}
              aria-label={`展开侧边栏（${sidebarMode === 'evidence' ? '证据' : '目录'}）`} onClick={toggleDirectory}>
              <span aria-hidden="true">›</span>
              <span className="reading-note-sidebar-rail-label">{sidebarMode === 'evidence' ? '证据' : '目录'}</span>
            </button>}
          </aside>
          <div className="reading-note-content">
            {activeEntry && <div className="reading-note-section-head">
              <div className="reading-note-section-meta">
                <strong>{editorHeading}</strong>
                {activeSectionMeta.meta.description && <span className="reading-note-section-description">{activeSectionMeta.meta.description}</span>}
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
            {sectionLoading === activeSectionId ? <p className="hint">正在读取 section…</p>
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
        : `当前 revision ${note?.revision ?? 0}。导入会用整篇 Markdown 替换所有 section，旧 section 文件会被清理。`}</p>
      <textarea value={editorMarkdown} onChange={(event) => setEditorMarkdown(event.target.value)} readOnly={saving} spellCheck={false} />
      <footer>
        <button className="primary" type="button" disabled={saving || !editorMarkdown.trim()} onClick={() => void commitWholeNote()}>{saving ? '提交中…' : editorMode === 'create' ? '创建' : '导入并替换'}</button>
        <button type="button" disabled={saving} onClick={() => setEditorOpen(false)}>取消</button>
      </footer>
    </div>}
  </section>;
}
