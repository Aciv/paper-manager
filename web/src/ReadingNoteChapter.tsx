import { useEffect, useRef, useState } from 'react';
import { api, type ReadingNoteSectionEntry } from './api';
import { parseEvidenceMarkdown } from './evidenceLinks';
import { renderReadingNoteMarkdown } from './readingNotePreview';
import type { NoteEvidenceOverlay } from './ReadingNotePanel';

type Props = {
  documentId: string;
  entries: ReadingNoteSectionEntry[];
  selectedId?: string;
  navigation: number;
  fontSize: number;
  attachZoom(element: HTMLDivElement | null): void;
  onClick(event: React.MouseEvent<HTMLDivElement>): void;
  onEvidenceTargetsChange(targets: NoteEvidenceOverlay[]): void;
  onVisibleSectionChange?(sectionId: string): void;
};

// This component is keyed by document, chapter and note revision. Requests
// cannot publish into a different paper/revision and at most three are in flight.
export function ReadingNoteChapter({ documentId, entries, selectedId, navigation, fontSize, attachZoom, onClick, onEvidenceTargetsChange, onVisibleSectionChange }: Props) {
  const [bodies, setBodies] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [retry, setRetry] = useState(0);
  const bodiesRef = useRef(bodies);
  bodiesRef.current = bodies;
  const viewport = useRef<HTMLDivElement | null>(null);
  const anchors = useRef(new Map<string, HTMLElement>());
  const pendingNavigation = useRef('');

  useEffect(() => {
    let cancelled = false;
    const abort = new AbortController();
    const pending = entries.filter((entry) => bodiesRef.current[entry.id] === undefined);
    let cursor = 0;
    setErrors({});
    async function worker() {
      while (!cancelled && cursor < pending.length) {
        const entry = pending[cursor++];
        try {
          const markdown = await api.getDocumentReadingNoteSectionContent(documentId, entry.id, abort.signal);
          if (!cancelled) setBodies((current) => ({ ...current, [entry.id]: markdown }));
        } catch (error) {
          if (!cancelled) setErrors((current) => ({ ...current, [entry.id]: error instanceof Error ? error.message : '读取失败' }));
        }
      }
    }
    void Promise.all(Array.from({ length: Math.min(3, pending.length) }, () => worker()));
    return () => { cancelled = true; abort.abort(); };
  }, [documentId, entries, retry]);

  // Wait until preceding sections settle so asynchronous growth above the
  // target does not move a newly selected subsection off-screen.
  useEffect(() => {
    const token = `${selectedId}:${navigation}`;
    if (pendingNavigation.current === token || !selectedId) return;
    const index = entries.findIndex((entry) => entry.id === selectedId);
    if (index < 0 || entries.slice(0, index + 1).some((entry) => bodies[entry.id] === undefined && !errors[entry.id])) return;
    const container = viewport.current;
    const target = anchors.current.get(selectedId);
    if (container && target) {
      container.scrollTop += target.getBoundingClientRect().top - container.getBoundingClientRect().top - 16;
      pendingNavigation.current = token;
    }
  }, [selectedId, navigation, bodies, errors, entries]);

  // Report which section currently sits under the chapter's top edge. The
  // directory uses this to follow continuous-reading scroll without changing
  // the selected section (which controls edit state and chapter membership).
  useEffect(() => {
    const container = viewport.current;
    if (!container || entries.length === 0) return;
    let reported = '';
    let frame = 0;
    const compute = () => {
      frame = 0;
      const top = container.getBoundingClientRect().top + 24;
      let next = entries[0]?.id ?? '';
      for (const entry of entries) {
        const element = anchors.current.get(entry.id);
        if (!element) continue;
        if (element.getBoundingClientRect().top <= top) next = entry.id;
        else break;
      }
      if (container.scrollHeight > container.clientHeight + 4
        && container.scrollTop + container.clientHeight >= container.scrollHeight - 4) {
        next = entries[entries.length - 1]?.id ?? next;
      }
      if (next && next !== reported) {
        reported = next;
        onVisibleSectionChange?.(next);
      }
    };
    const onScroll = () => { if (!frame) frame = requestAnimationFrame(compute); };
    container.addEventListener('scroll', onScroll, { passive: true });
    compute();
    return () => {
      container.removeEventListener('scroll', onScroll);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [entries, bodies, onVisibleSectionChange]);

  useEffect(() => {
    const targets: NoteEvidenceOverlay[] = [];
    for (const entry of entries) {
      parseEvidenceMarkdown(bodies[entry.id] ?? '').links.forEach((link, index) => {
        if (!link.target || link.target.documentId) return;
        targets.push({ ...link.target, id: `${entry.id}:${index}`, label: link.text, sectionId: entry.id });
      });
    }
    onEvidenceTargetsChange(targets);
    return () => onEvidenceTargetsChange([]);
  }, [entries, bodies, onEvidenceTargetsChange]);

  const loaded = entries.filter((entry) => bodies[entry.id] !== undefined).length;
  return <div className="reading-note-chapter reading-note-preview" role="document"
    aria-label={`${entries[0]?.title ?? ''} 连续阅读`}
    style={{ fontSize }} ref={(element) => { viewport.current = element; attachZoom(element); }} onClick={onClick}>
    {loaded < entries.length && <p className="reading-note-load-progress" role="status">已加载 {loaded} / {entries.length} 节</p>}
    {entries.map((entry) => <section key={entry.id} data-note-section={entry.id}
      className={`reading-note-chapter-section level-${entry.level}${selectedId === entry.id ? ' selected' : ''}`}
      ref={(element) => { if (element) anchors.current.set(entry.id, element); else anchors.current.delete(entry.id); }}>
      {bodies[entry.id] !== undefined
        ? <div dangerouslySetInnerHTML={{ __html: renderReadingNoteMarkdown(bodies[entry.id]) }} />
        : <><h2>{entry.title}</h2>{errors[entry.id]
          ? <p className="reading-note-error">{errors[entry.id]} <button type="button" onClick={() => setRetry((value) => value + 1)}>重试未加载章节</button></p>
          : <p className="hint">正在读取…</p>}</>}
    </section>)}
  </div>;
}
