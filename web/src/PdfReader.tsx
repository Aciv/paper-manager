import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent } from 'react';
import { createPortal } from 'react-dom';
import { AnnotationMode, GlobalWorkerOptions, getDocument, type PDFDocumentProxy, type PDFPageProxy } from 'pdfjs-dist';
import { EventBus, FindState, PDFFindController, PDFLinkService, PDFViewer } from 'pdfjs-dist/web/pdf_viewer.mjs';
import 'pdfjs-dist/web/pdf_viewer.css';

import './PdfReader.css';
import { api, Block, contentUrl, Document as PaperDocument, DocumentSelectionResult } from './api';
import { PDFLinkPreview, destinationPDFPoint, destinationPreviewRegion, mergeAdjacentPDFLinks, viewportRegion } from './pdfLinkPreview';
import { LatestPDFSelection, NormalizedPDFRegion, SelectionGeneration, canvasCropForRegion, normalizedRegion, regionIsLargeEnough } from './pdfSelection';
import { activeOutlineNode, annotateOutlinePages, type OutlineEntry } from './pdfOutline';

GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type SelectionMode = 'text' | 'block' | 'area';

const MODE_HINTS: Record<SelectionMode, string> = {
  text: '拖动文本层形成文本草稿。',
  block: '点击高亮区域形成 Block 草稿。',
  area: '在单页内拖动形成区域草稿；此时不会截图或请求后端。',
};

// Freehand strokes and rectangles drawn over an area selection. Points are
// normalised to the selected region, so they survive zooming and can be baked
// into the exported crop at any resolution.
type AnnotationStroke = { tool: 'pen' | 'rect'; points: Array<{ x: number; y: number }> };

const NO_STROKES: AnnotationStroke[] = [];
const ANNOTATION_COLOR = '#e5484d';

function drawAnnotationStrokes(context: CanvasRenderingContext2D, strokes: AnnotationStroke[], width: number, height: number) {
  if (strokes.length === 0) return;
  context.save();
  context.strokeStyle = ANNOTATION_COLOR;
  context.lineWidth = Math.max(2, Math.round(Math.min(width, height) / 80));
  context.lineCap = 'round';
  context.lineJoin = 'round';
  for (const stroke of strokes) {
    if (stroke.points.length === 0) continue;
    if (stroke.tool === 'pen') {
      context.beginPath();
      stroke.points.forEach((point, index) => {
        const x = point.x * width;
        const y = point.y * height;
        if (index === 0) context.moveTo(x, y);
        else context.lineTo(x, y);
      });
      context.stroke();
    } else {
      const start = stroke.points[0];
      const end = stroke.points[stroke.points.length - 1];
      context.strokeRect(
        Math.min(start.x, end.x) * width,
        Math.min(start.y, end.y) * height,
        Math.abs(end.x - start.x) * width,
        Math.abs(end.y - start.y) * height,
      );
    }
  }
  context.restore();
}

function clampNumber(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function selectionSummary(selection: LatestPDFSelection): string {
  if (selection.kind === 'text') return `第 ${selection.pageNumber ?? '?'} 页文本 · ${selection.text.length} 字符`;
  if (selection.kind === 'block') return `第 ${selection.pageNumber ?? '?'} 页 ${selection.blockType} Block`;
  return `第 ${selection.pageNumber} 页框选区域`;
}

function groupLinks(links: PDFLinkPreview[]): Map<number, PDFLinkPreview[]> {
  const map = new Map<number, PDFLinkPreview[]>();
  for (const link of links) {
    const list = map.get(link.sourcePageNumber);
    if (list) list.push(link);
    else map.set(link.sourcePageNumber, [link]);
  }
  return map;
}

// Link annotations become geometric hotspots; hovering one previews the
// target region as original PDF pixels (no citation matching involved).
export async function collectPDFLinkPreviews(pdfDocument: PDFDocumentProxy): Promise<PDFLinkPreview[]> {
  const pagePromises = new Map<number, Promise<PDFPageProxy>>();
  const getPage = (pageNumber: number) => {
    let page = pagePromises.get(pageNumber);
    if (!page) {
      page = pdfDocument.getPage(pageNumber);
      pagePromises.set(pageNumber, page);
    }
    return page;
  };

  const links: PDFLinkPreview[] = [];
  for (let sourcePageNumber = 1; sourcePageNumber <= pdfDocument.numPages; sourcePageNumber += 1) {
    const sourcePage = await getPage(sourcePageNumber);
    const sourceViewport = sourcePage.getViewport({ scale: 1 });
    const annotations = await sourcePage.getAnnotations();
    for (let annotationIndex = 0; annotationIndex < annotations.length; annotationIndex += 1) {
      const annotation = annotations[annotationIndex] as { id?: string; subtype?: string; rect?: unknown; dest?: unknown };
      if (annotation.subtype !== 'Link' || !annotation.dest) continue;
      if (!Array.isArray(annotation.rect) || annotation.rect.length !== 4) continue;
      const sourceRegion = viewportRegion(
        sourceViewport.convertToViewportRectangle(annotation.rect as [number, number, number, number]),
        sourceViewport.width,
        sourceViewport.height,
      );
      if (!sourceRegion) continue;
      try {
        const namedDestination = typeof annotation.dest === 'string';
        const destination = namedDestination
          ? await pdfDocument.getDestination(annotation.dest as string)
          : annotation.dest;
        if (!Array.isArray(destination) || destination.length === 0) continue;

        const pageReference = destination[0];
        const targetPageIndex = typeof pageReference === 'number'
          ? pageReference
          : await pdfDocument.getPageIndex(pageReference);
        const targetPageNumber = targetPageIndex + 1;
        if (targetPageNumber < 1 || targetPageNumber > pdfDocument.numPages) continue;
        const targetPage = await getPage(targetPageNumber);
        const targetViewport = targetPage.getViewport({ scale: 1 });
        const pdfPoint = destinationPDFPoint(destination);
        if (!pdfPoint) continue;
        const [targetX, targetY] = targetViewport.convertToViewportPoint(pdfPoint.x, pdfPoint.y);
        if (!Number.isFinite(targetX) || !Number.isFinite(targetY)) continue;

        links.push({
          id: `${sourcePageNumber}:${annotation.id ?? annotationIndex}`,
          sourcePageNumber,
          sourceRegion,
          target: {
            pageNumber: targetPageNumber,
            pageWidth: targetViewport.width,
            pageHeight: targetViewport.height,
            x: Math.min(targetViewport.width, Math.max(0, targetX)),
            y: Math.min(targetViewport.height, Math.max(0, targetY)),
          },
        });
      } catch {
        // A malformed destination disables only that link. Other native links
        // in the document can still provide trustworthy geometric previews.
      }
    }
  }
  return mergeAdjacentPDFLinks(links);
}

// One overlay layer per pdf.js page element. pdf.js owns the page DOM (and may
// re-create it), so everything the app renders on a page goes through here.
const PageOverlay = memo(function PageOverlay({ documentId, selectionTarget, pageNumber, mode, links, onSelection, onMessage, onFollowLink, previewLink, areaSelection, strokes, annotationTool, onAnnotation }: {
  // Absent for a PDF opened from disk: it has no parsed blocks, but text and
  // area selection still work — those are handled in the browser.
  documentId?: string;
  selectionTarget: string;
  pageNumber: number;
  mode: SelectionMode;
  links: PDFLinkPreview[];
  onSelection(selection: LatestPDFSelection): void;
  onMessage(message: string): void;
  onFollowLink(link: PDFLinkPreview): void;
  previewLink(link: PDFLinkPreview): Promise<string>;
  // The committed area draft for this page stays on screen so it can be
  // annotated (pen / rectangle) before the crop is saved or processed.
  areaSelection?: NormalizedPDFRegion;
  strokes: AnnotationStroke[];
  annotationTool: 'pen' | 'rect' | null;
  onAnnotation(action: { type: 'strokes'; strokes: AnnotationStroke[] } | { type: 'tool'; tool: 'pen' | 'rect' | null } | { type: 'commit' } | { type: 'cancel' }): void;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [loadError, setLoadError] = useState('');
  const [areaDraft, setAreaDraft] = useState<NormalizedPDFRegion>();
  const dragStart = useRef<{ pointerId: number; x: number; y: number } | undefined>(undefined);
  const drawStart = useRef<{ pointerId: number; points: Array<{ x: number; y: number }> } | undefined>(undefined);
  const [previewState, setPreviewState] = useState<{ linkID: string; url?: string; error?: string }>();
  const previewRequest = useRef(0);

  useEffect(() => {
    // A local file has no parsed blocks; its overlay stays empty.
    if (!documentId) {
      setBlocks([]);
      setLoadError('');
      return;
    }
    let active = true;
    setLoadError('');
    void api.listDocumentBlocks(documentId, pageNumber)
      .then(result => { if (active) setBlocks(result.items); })
      .catch(error => { if (active) setLoadError(error instanceof Error ? error.message : '无法读取解析块。'); });
    return () => { active = false; };
  }, [documentId, pageNumber]);

  // The page element is pdf.js's; text selection is reported from it because
  // this overlay stays pointer-events:none in text mode.
  useEffect(() => {
    const page = rootRef.current?.closest('.page');
    if (!page) return;
    const onMouseUp = () => {
      if (mode !== 'text') return;
      const targetId = selectionTarget;
      const browserSelection = window.getSelection();
      const text = browserSelection?.toString().trim();
      if (!text) return;
      const pageBounds = page.getBoundingClientRect();
      const rangeBounds = browserSelection && browserSelection.rangeCount > 0 ? browserSelection.getRangeAt(0).getBoundingClientRect() : undefined;
      let region: NormalizedPDFRegion | undefined;
      if (pageBounds.width > 0 && pageBounds.height > 0 && rangeBounds) {
        region = normalizedRegion(
          (rangeBounds.left - pageBounds.left) / pageBounds.width,
          (rangeBounds.top - pageBounds.top) / pageBounds.height,
          (rangeBounds.right - pageBounds.left) / pageBounds.width,
          (rangeBounds.bottom - pageBounds.top) / pageBounds.height,
        );
      }
      onSelection({ kind: 'text', documentId: selectionTarget, text, pageNumber, region });
    };
    page.addEventListener('mouseup', onMouseUp);
    return () => page.removeEventListener('mouseup', onMouseUp);
  }, [documentId, selectionTarget, mode, onSelection, pageNumber]);

  function showLinkPreview(link: PDFLinkPreview) {
    const requestID = previewRequest.current + 1;
    previewRequest.current = requestID;
    setPreviewState({ linkID: link.id });
    void previewLink(link)
      .then(url => { if (previewRequest.current === requestID) setPreviewState({ linkID: link.id, url }); })
      .catch(error => {
        if (previewRequest.current === requestID) setPreviewState({ linkID: link.id, error: error instanceof Error ? error.message : '无法生成目标区域预览。' });
      });
  }

  function hideLinkPreview() {
    previewRequest.current += 1;
    setPreviewState(undefined);
  }

  function pointerCoordinates(event: React.PointerEvent<HTMLDivElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    return { bounds, x: (event.clientX - bounds.left) / bounds.width, y: (event.clientY - bounds.top) / bounds.height };
  }

  return <div ref={rootRef} className="pdf-page-overlay">
    <div className={`citation-link-overlay ${mode === 'text' ? 'active' : ''}`} aria-hidden={mode !== 'text'}>
      {links.map(link => {
        const region = link.sourceRegion;
        const alignRight = (region.x0 + region.x1) / 2 > 0.55;
        const active = previewState?.linkID === link.id;
        return <button key={link.id} className={`citation-link-hotspot${alignRight ? ' align-right' : ''}`} style={{
          left: `${region.x0 * 100}%`, top: `${region.y0 * 100}%`,
          width: `${(region.x1 - region.x0) * 100}%`, height: `${(region.y1 - region.y0) * 100}%`,
        }} type="button" tabIndex={mode === 'text' ? 0 : -1}
          aria-label={`预览链接目标，第 ${link.target.pageNumber} 页`}
          onMouseEnter={() => showLinkPreview(link)} onMouseLeave={hideLinkPreview}
          onFocus={() => showLinkPreview(link)} onBlur={hideLinkPreview}
          onClick={() => onFollowLink(link)}>
          {active && <span className="citation-link-preview" role="tooltip">
            <strong>第 {link.target.pageNumber} 页 · PDF 原文</strong>
            {!previewState?.url && !previewState?.error && <span className="citation-link-preview-status">正在生成预览…</span>}
            {previewState?.error && <span className="citation-link-preview-error">{previewState.error}</span>}
            {previewState?.url && <img src={previewState.url} alt={`PDF 第 ${link.target.pageNumber} 页链接目标附近原文`} />}
            <small>点击跳转到原文位置</small>
          </span>}
        </button>;
      })}
    </div>
    <div className={`block-overlay ${mode === 'block' ? 'active' : ''}`} aria-hidden={mode !== 'block'}>
      {blocks.map(block => {
        const location = block.location;
        if (!location || !location.pageWidth || !location.pageHeight || location.bboxX0 === undefined || location.bboxY0 === undefined || location.bboxX1 === undefined || location.bboxY1 === undefined) return null;
        return <button key={block.id} className={`pdf-block pdf-block-${block.type}`} tabIndex={mode === 'block' ? 0 : -1} style={{
          left: `${(location.bboxX0 / location.pageWidth) * 100}%`, top: `${(location.bboxY0 / location.pageHeight) * 100}%`,
          width: `${((location.bboxX1 - location.bboxX0) / location.pageWidth) * 100}%`, height: `${((location.bboxY1 - location.bboxY0) / location.pageHeight) * 100}%`,
        }} type="button" title={`${block.type}: ${block.text.slice(0, 240)}`} onClick={() => onSelection({
          kind: 'block', documentId: selectionTarget, blockId: block.id, pageNumber: location.pageNumber,
          blockType: block.type, previewText: block.text,
        })}><span>{block.type}</span></button>;
      })}
    </div>
    <div className={`area-selection-overlay ${mode === 'area' ? 'active' : ''}`} aria-hidden={mode !== 'area'}
      onPointerDown={event => {
        if (mode !== 'area' || event.button !== 0) return;
        const point = pointerCoordinates(event);
        dragStart.current = { pointerId: event.pointerId, x: point.x, y: point.y };
        event.currentTarget.setPointerCapture(event.pointerId);
        setAreaDraft(normalizedRegion(point.x, point.y, point.x, point.y));
      }}
      onPointerMove={event => {
        const start = dragStart.current;
        if (!start || start.pointerId !== event.pointerId) return;
        const point = pointerCoordinates(event);
        setAreaDraft(normalizedRegion(start.x, start.y, point.x, point.y));
      }}
      onPointerUp={event => {
        const start = dragStart.current;
        if (!start || start.pointerId !== event.pointerId) return;
        const point = pointerCoordinates(event);
        const region = normalizedRegion(start.x, start.y, point.x, point.y);
        dragStart.current = undefined;
        if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
        setAreaDraft(undefined);
        if (!regionIsLargeEnough(region, point.bounds.width, point.bounds.height)) {
          onMessage('框选区域过小，请至少拖动 8×8 像素。');
          return;
        }
        onSelection({ kind: 'area', documentId: selectionTarget, pageNumber, region });
      }}
      onPointerCancel={() => { dragStart.current = undefined; setAreaDraft(undefined); }}>
      {areaDraft && <div className="area-selection-box" style={{ left: `${areaDraft.x0 * 100}%`, top: `${areaDraft.y0 * 100}%`, width: `${(areaDraft.x1 - areaDraft.x0) * 100}%`, height: `${(areaDraft.y1 - areaDraft.y0) * 100}%` }} />}
    </div>
    {areaSelection && <div className={`area-annotation${annotationTool ? ' drawing' : ''}`}
      style={{ left: `${areaSelection.x0 * 100}%`, top: `${areaSelection.y0 * 100}%`, width: `${(areaSelection.x1 - areaSelection.x0) * 100}%`, height: `${(areaSelection.y1 - areaSelection.y0) * 100}%` }}>
      <svg className="area-annotation-strokes" viewBox="0 0 1 1" preserveAspectRatio="none" aria-hidden="true">
        {strokes.map((stroke, index) => stroke.tool === 'pen'
          ? <polyline key={index} points={stroke.points.map(point => `${point.x},${point.y}`).join(' ')} vectorEffect="non-scaling-stroke" />
          : <rect key={index} x={Math.min(stroke.points[0]?.x ?? 0, stroke.points[stroke.points.length - 1]?.x ?? 0)}
              y={Math.min(stroke.points[0]?.y ?? 0, stroke.points[stroke.points.length - 1]?.y ?? 0)}
              width={Math.abs((stroke.points[stroke.points.length - 1]?.x ?? 0) - (stroke.points[0]?.x ?? 0))}
              height={Math.abs((stroke.points[stroke.points.length - 1]?.y ?? 0) - (stroke.points[0]?.y ?? 0))}
              vectorEffect="non-scaling-stroke" />)}
      </svg>
      <div className="area-annotation-surface"
        onPointerDown={event => {
          if (!annotationTool || event.button !== 0) return;
          const bounds = event.currentTarget.getBoundingClientRect();
          if (bounds.width < 1 || bounds.height < 1) return;
          const point = { x: clampNumber((event.clientX - bounds.left) / bounds.width, 0, 1), y: clampNumber((event.clientY - bounds.top) / bounds.height, 0, 1) };
          drawStart.current = { pointerId: event.pointerId, points: [point] };
          event.currentTarget.setPointerCapture(event.pointerId);
          onAnnotation({ type: 'strokes', strokes: [...strokes, { tool: annotationTool, points: [point] }] });
        }}
        onPointerMove={event => {
          const draw = drawStart.current;
          if (!draw || draw.pointerId !== event.pointerId || !annotationTool) return;
          const bounds = event.currentTarget.getBoundingClientRect();
          if (bounds.width < 1 || bounds.height < 1) return;
          const point = { x: clampNumber((event.clientX - bounds.left) / bounds.width, 0, 1), y: clampNumber((event.clientY - bounds.top) / bounds.height, 0, 1) };
          const points = annotationTool === 'pen' ? [...draw.points, point] : [draw.points[0], point];
          draw.points = points;
          onAnnotation({ type: 'strokes', strokes: [...strokes.slice(0, -1), { tool: annotationTool, points }] });
        }}
        onPointerUp={event => {
          const draw = drawStart.current;
          if (!draw || draw.pointerId !== event.pointerId) return;
          drawStart.current = undefined;
          if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
        }}
        onPointerCancel={() => { drawStart.current = undefined; }} />
      <div className="area-annotation-toolbar">
        <button className={annotationTool === 'pen' ? 'active' : ''} type="button" title="涂鸦" aria-pressed={annotationTool === 'pen'} onClick={() => onAnnotation({ type: 'tool', tool: annotationTool === 'pen' ? null : 'pen' })}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4.5 19.5 8 18.6 19 7.6a2 2 0 0 0-2.8-2.8L5.3 15.9z" /><path d="M14.6 6.4 17.4 9.2" /></svg>
        </button>
        <button className={annotationTool === 'rect' ? 'active' : ''} type="button" title="画框" aria-pressed={annotationTool === 'rect'} onClick={() => onAnnotation({ type: 'tool', tool: annotationTool === 'rect' ? null : 'rect' })}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="4.5" y="6.5" width="15" height="11" rx="1.5" /></svg>
        </button>
        <button className="area-annotation-ok" type="button" title="结束标注（保留选区，可继续保存/处理）" onClick={() => onAnnotation({ type: 'commit' })}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M5 12.8 9.6 17.4 19 8" /></svg>
        </button>
        <button type="button" title="取消框选与标注" onClick={() => onAnnotation({ type: 'cancel' })}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M6.5 6.5 17.5 17.5" /><path d="M17.5 6.5 6.5 17.5" /></svg>
        </button>
      </div>
    </div>}

    {loadError && <p className="pdf-block-error">解析块未加载：{loadError}</p>}
  </div>;
});

type OutlineNode = OutlineEntry;

// A real outline tree: a chevron per branch and a guide line per nesting level.
// Indentation comes from the nested list itself, so every level lines up under
// its parent's chevron instead of a per-depth pixel offset.
function OutlineTreeNode({ node, onNavigate, active }: { node: OutlineNode; onNavigate(dest: unknown): void; active?: OutlineNode }) {
  const children = node.items ?? [];
  const [open, setOpen] = useState(true);
  const rowRef = useRef<HTMLDivElement>(null);
  const isActive = active === node;
  // Keep the section being read in sight without fighting the user's scrolling.
  useEffect(() => {
    if (isActive) rowRef.current?.scrollIntoView({ block: 'nearest' });
  }, [isActive]);
  return <li className="pdf-outline-node">
    <div className={`pdf-outline-row${isActive ? ' current' : ''}`} ref={rowRef}>
      {children.length > 0
        ? <button className="pdf-outline-toggle" type="button" aria-expanded={open} title={open ? '收起' : '展开'} onClick={() => setOpen(current => !current)}>
          <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6 3.5 10.5 8 6 12.5" /></svg>
        </button>
        : <span className="pdf-outline-toggle" aria-hidden="true" />}
      {node.dest
        ? <button className="pdf-outline-link" type="button" aria-current={isActive ? 'true' : undefined} onClick={() => onNavigate(node.dest)}>{node.title}</button>
        : <span className="pdf-outline-text" aria-current={isActive ? 'true' : undefined}>{node.title}</span>}
    </div>
    {children.length > 0 && open && <ul className="pdf-outline-children">
      {children.map((child, index) => <OutlineTreeNode key={`${index}-${child.title}`} node={child} onNavigate={onNavigate} active={active} />)}
    </ul>}
  </li>;
}

function OutlineTree({ nodes, onNavigate, active }: { nodes: OutlineNode[]; onNavigate(dest: unknown): void; active?: OutlineNode }) {
  return <ul className="pdf-outline-tree">
    {nodes.map((node, index) => <OutlineTreeNode key={`${index}-${node.title}`} node={node} onNavigate={onNavigate} active={active} />)}
  </ul>;
}

export function PdfReader({ document, file, onMessage, outlinePlacement = 'internal', outlineTarget, modeRail = false }: {
  document?: PaperDocument;
  // A PDF straight from the user's disk. It has no backend record, so anything
  // keyed by a document id (parsed blocks, metadata, citations, selection
  // processing) simply does not apply and its panels stay empty.
  file?: File | null;
  onMessage(message: string): void;
  // `external` renders the outline through a portal into `outlineTarget` so a
  // host page can place it anywhere; `internal` keeps it beside the pages.
  outlinePlacement?: 'internal' | 'external';
  outlineTarget?: HTMLElement | null;
  // `true` replaces the labeled controls row with a floating icon rail.
  modeRail?: boolean;
}) {
  const outlineExternal = outlinePlacement === 'external';
  const scrollRef = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLElement>(null);
  const viewerRef = useRef<PDFViewer | undefined>(undefined);
  const linkServiceRef = useRef<PDFLinkService | undefined>(undefined);
  const pdfDocumentRef = useRef<PDFDocumentProxy | undefined>(undefined);
  const pdfLoadGeneration = useRef(0);
  const previewCache = useRef(new Map<string, string>());
  const previewPageCache = useRef(new Map<number, Promise<HTMLCanvasElement>>());
  const generations = useRef(new SelectionGeneration());
  const processController = useRef<AbortController | undefined>(undefined);

  const [mode, setMode] = useState<SelectionMode>('text');
  const [outlineCollapsed, setOutlineCollapsed] = useState(false);
  const [selection, setSelection] = useState<LatestPDFSelection>();
  const [processing, setProcessing] = useState(false);
  const [result, setResult] = useState<DocumentSelectionResult>();
  const [pageCount, setPageCount] = useState(0);
  const [zoom, setZoom] = useState(1);
  const [outline, setOutline] = useState<OutlineNode[]>([]);
  const [slots, setSlots] = useState<Array<{ pageNumber: number; node: HTMLElement }>>([]);
  const [linksByPage, setLinksByPage] = useState<Map<number, PDFLinkPreview[]>>(new Map());
  const [linkCount, setLinkCount] = useState(0);
  // One remembered reading position (link/outline/page jump overwrite it), so
  // the floating back button always returns to where the jump started.
  const [returnPoint, setReturnPoint] = useState<{ top: number; left: number } | null>(null);
  // Annotations live on the committed area draft, so they can be drawn before
  // the crop is saved (local) or processed (stored document).
  const [strokes, setStrokes] = useState<AnnotationStroke[]>(NO_STROKES);
  const [annotationTool, setAnnotationTool] = useState<'pen' | 'rect' | null>(null);
  // ✓ hides the annotation chrome without dropping the draft: the crop (strokes
  // included) can still be saved or processed from the action bar.
  const [annotationDone, setAnnotationDone] = useState(false);
  const [pageJumpOpen, setPageJumpOpen] = useState(false);
  const [pageJumpValue, setPageJumpValue] = useState('');
  const pageJumpInput = useRef<HTMLInputElement>(null);
  // In-PDF search (pdf.js PDFFindController) and the page the reader is on,
  // which the outline pane follows.
  const [currentPage, setCurrentPage] = useState(1);
  const [findOpen, setFindOpen] = useState(false);
  const [findQuery, setFindQuery] = useState('');
  const [findMatches, setFindMatches] = useState<{ current: number; total: number } | null>(null);
  const [findState, setFindState] = useState<number | null>(null);
  const findInput = useRef<HTMLInputElement>(null);
  const eventBusRef = useRef<EventBus | undefined>(undefined);
  // Ctrl+F is ours only while the reader is the surface being used; elsewhere
  // the browser's own find stays available.
  const readerActive = useRef(false);
  // The icon rail is draggable: null keeps the default docked-right column,
  // a placement means the user moved it (and picked its orientation with it).
  // Which source this reader is showing: a stored document (id-backed) or a
  // file the user opened from disk.
  const localFile = file ?? null;
  const documentId = localFile ? undefined : document?.id;
  // Changes whenever the reader's source does, which is what reloads the viewer.
  const sourceKey = localFile ? `local:${localFile.name}:${localFile.size}:${localFile.lastModified}` : documentId ?? '';
  const processable = Boolean(documentId);
  // A draft's identity: stored documents use their id, local files their key.
  const selectionTarget = documentId ?? sourceKey;

  const [railPlacement, setRailPlacement] = useState<{ x: number; y: number; horizontal: boolean; dockEnd: boolean } | null>(null);
  const [railDragging, setRailDragging] = useState(false);
  const [railCollapsed, setRailCollapsed] = useState(false);
  const railRef = useRef<HTMLDivElement>(null);
  const railDrag = useRef<{
    pointerId: number; offsetX: number; offsetY: number; startX: number; startY: number;
    lastX: number; lastY: number; width: number; height: number; panelWidth: number; panelHeight: number; moved: boolean;
  } | null>(null);
  const railSnapPending = useRef(false);
  const railClickGuard = useRef(false);

  // Ctrl+F belongs to the reader only while the reader was the last surface
  // clicked; Escape closes the bar again.
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      readerActive.current = Boolean(panelRef.current?.contains(event.target as Node));
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && findOpen) {
        closeFind();
        return;
      }
      if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== 'f') return;
      const panel = panelRef.current;
      const inside = Boolean(panel && (readerActive.current || panel.contains(window.document.activeElement)));
      if (!inside) return;
      event.preventDefault();
      setFindOpen(true);
    };
    window.document.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.document.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown);
    };
  });

  // The query is selected on open so typing replaces it.
  useEffect(() => {
    if (findOpen) findInput.current?.select();
  }, [findOpen]);

  // The prefilled page number is selected so typing simply replaces it, and a
  // click anywhere else closes the popover (Escape is handled on the input).
  useEffect(() => {
    if (!pageJumpOpen) return;
    pageJumpInput.current?.select();
    const onPointerDown = (event: PointerEvent) => {
      if ((event.target as HTMLElement | null)?.closest('.pdf-page-jump, .pdf-page-jump-toggle')) return;
      setPageJumpOpen(false);
    };
    window.document.addEventListener('pointerdown', onPointerDown);
    return () => window.document.removeEventListener('pointerdown', onPointerDown);
  }, [pageJumpOpen]);

  // One viewer per reader, reused for every document: `setDocument` is what
  // tears the previous document's page DOM down, so building a fresh viewer per
  // file (into the same container) would leave the old pages stacked in the
  // scroll area and the reader would keep showing the previous PDF.
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    let disposed = false;
    const eventBus = new EventBus();
    const linkService = new PDFLinkService({ eventBus });
    // The app draws its own link hotspots (with target previews), so pdf.js's
    // annotation layer is disabled the same way the previous reader disabled it.
    // Canvas limits: the defaults cap a page canvas at three screen areas and
    // then halve it again for the "detail canvas" trick, which repaints only the
    // band currently in the viewport — so from ~2x zoom on, most of the page is
    // upscaled mush with a sharp strip under the cursor, and the strip snaps in
    // asynchronously while scrolling. Reading sharpness matters more here than
    // that memory trick: render every page at the real device resolution and
    // bound it only by pdf.js's large-screen budget (2^25 px ~= 128 MiB).
    // pdf.js's find controller does the text scanning and paints the match
    // highlights through each page's text layer; the viewer wires it up.
    const findController = new PDFFindController({ linkService, eventBus });
    const viewer = new PDFViewer({
      container: scroll,
      eventBus,
      linkService,
      findController,
      annotationMode: AnnotationMode.DISABLE,
      capCanvasAreaFactor: -1,
      maxCanvasPixels: 2 ** 25,
    });
    linkService.setViewer(viewer);
    viewerRef.current = viewer;
    linkServiceRef.current = linkService;
    eventBusRef.current = eventBus;

    const syncSlots = () => {
      if (disposed) return;
      const next: Array<{ pageNumber: number; node: HTMLElement }> = [];
      for (let index = 0; index < viewer.pagesCount; index += 1) {
        const pageDiv = viewer.getPageView(index)?.div as HTMLElement | undefined;
        if (!pageDiv) continue;
        let slot = pageDiv.querySelector<HTMLElement>(':scope > .pdf-page-overlay-slot');
        if (!slot) {
          slot = window.document.createElement('div');
          slot.className = 'pdf-page-overlay-slot';
          pageDiv.appendChild(slot);
        }
        next.push({ pageNumber: index + 1, node: slot });
      }
      setSlots(current => (current.length === next.length && current.every((entry, i) => entry.node === next[i].node) ? current : next));
    };

    const onPagesInit = () => {
      viewer.currentScaleValue = 'page-width';
      setZoom(viewer.currentScale);
      syncSlots();
    };
    const onPageChanging = (event: { pageNumber?: number }) => {
      if (typeof event.pageNumber === 'number') setCurrentPage(event.pageNumber);
    };
    const onFindMatches = (event: { matchesCount?: { current: number; total: number } }) => {
      setFindMatches(event.matchesCount ?? null);
    };
    const onFindState = (event: { state?: number; matchesCount?: { current: number; total: number } }) => {
      setFindState(typeof event.state === 'number' ? event.state : null);
      if (typeof event.matchesCount?.current === 'number') setFindMatches(event.matchesCount);
    };
    eventBus.on('pagesinit', onPagesInit);
    eventBus.on('pagerendered', syncSlots);
    eventBus.on('pagesdestroy', syncSlots);
    eventBus.on('pagechanging', onPageChanging);
    eventBus.on('updatefindmatchescount', onFindMatches);
    eventBus.on('updatefindcontrolstate', onFindState);

    return () => {
      disposed = true;
      eventBus.off('pagesinit', onPagesInit);
      eventBus.off('pagerendered', syncSlots);
      eventBus.off('pagesdestroy', syncSlots);
      eventBus.off('pagechanging', onPageChanging);
      eventBus.off('updatefindmatchescount', onFindMatches);
      eventBus.off('updatefindcontrolstate', onFindState);
      viewerRef.current = undefined;
      linkServiceRef.current = undefined;
      eventBusRef.current = undefined;
      linkService.setDocument(null);
      setSlots([]);
      viewer.cleanup();
    };
  }, []);

  // Selecting another paper swaps the document inside that viewer; pdf.js drops
  // the previous pages (and re-fits the scale) itself.
  useEffect(() => {
    const viewer = viewerRef.current;
    if (!viewer) return;
    let disposed = false;
    const generation = pdfLoadGeneration.current + 1;
    pdfLoadGeneration.current = generation;
    // A stored document streams from the content endpoint; a local file is read
    // straight off disk.
    const loading = localFile
      ? localFile.arrayBuffer().then(data => getDocument({ data }))
      : Promise.resolve(getDocument({ url: contentUrl(documentId ?? '') }));
    const opened: { task?: ReturnType<typeof getDocument> } = {};
    void loading.then(task => {
      opened.task = task;
      if (disposed) {
        void task.destroy();
        return undefined;
      }
      return task.promise.then(pdfDocument => {
        if (disposed || pdfLoadGeneration.current !== generation) return;
        pdfDocumentRef.current = pdfDocument;
        setPageCount(pdfDocument.numPages);
        viewer.setDocument(pdfDocument);
        linkServiceRef.current?.setDocument(pdfDocument);
        void pdfDocument.getOutline().then(async nodes => {
          if (!nodes || nodes.length === 0) {
            if (!disposed && pdfLoadGeneration.current === generation) setOutline([]);
            return;
          }
          // Resolving destination → page once per load is what lets the outline
          // follow the reader without touching the document while scrolling.
          const entries = await annotateOutlinePages(nodes as OutlineEntry[], {
            getDestination: dest => pdfDocument.getDestination(dest as string),
            getPageIndex: reference => pdfDocument.getPageIndex(reference as never),
            numPages: pdfDocument.numPages,
          });
          if (!disposed && pdfLoadGeneration.current === generation) setOutline(entries);
        }).catch(() => { if (!disposed) setOutline([]); });
        void collectPDFLinkPreviews(pdfDocument).then(links => {
          if (disposed || pdfLoadGeneration.current !== generation) return;
          setLinksByPage(groupLinks(links));
          setLinkCount(links.length);
        }).catch(() => { /* annotations unavailable */ });
      });
    }).catch(error => {
      if (!disposed) onMessage(`PDF 加载失败：${error instanceof Error ? error.message : '未知错误'}`);
    });

    return () => {
      disposed = true;
      pdfDocumentRef.current = undefined;
      linkServiceRef.current?.setDocument(null);
      setSlots([]);
      setPageCount(0);
      setCurrentPage(1);
      setLinksByPage(new Map());
      setLinkCount(0);
      setOutline([]);
      setFindOpen(false);
      setFindQuery('');
      setFindMatches(null);
      setFindState(null);
      eventBusRef.current?.dispatch('findbarclose', { source: null });
      if (opened.task) void opened.task.destroy();
    };
  }, [sourceKey, localFile, documentId, onMessage]);

  // Source switch: drop any selection draft from the previous file.
  useEffect(() => {
    generations.current.replace();
    processController.current?.abort();
    setSelection(undefined);
    setResult(undefined);
    setProcessing(false);
    if (!documentId) setMode('text'); // block mode needs parsed blocks
    return () => { generations.current.replace(); processController.current?.abort(); };
  }, [sourceKey]);

  // Ctrl + wheel zoom: the gesture itself only applies a compositor transform
  // to the viewer element — no re-render, no layout, which is what makes it
  // feel smooth. When the gesture settles the scale is handed to pdf.js in one
  // synchronous step: its own origin-anchored updateScale re-renders only the
  // visible pages (old bitmaps stay until the new ones are ready) and the
  // preview transform is dropped in the same task, so the swap cannot flash.
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    let frame = 0;
    let settle = 0;
    let pending = 1;
    let accumulated = 1;
    let started = false;
    let originX = 0;
    let originY = 0;
    let anchor: { page: Element; fx: number; fy: number } | null = null;
    const pointer = { x: 0, y: 0 };
    const viewerElement = () => viewerRef.current?.viewer as HTMLElement | null | undefined;

    const apply = () => {
      frame = 0;
      const factor = pending;
      pending = 1;
      const element = viewerElement();
      if (!element || Math.abs(factor - 1) < 0.001) return;
      if (!started) {
        // Anchor once, while the element is still untransformed: the transform
        // origin for the preview, plus the page and relative point under the
        // cursor so the commit can be corrected by measurement.
        const rect = element.getBoundingClientRect();
        originX = pointer.x - rect.left;
        originY = pointer.y - rect.top;
        const page = window.document.elementFromPoint(pointer.x, pointer.y)?.closest('.page');
        const pageRect = page?.getBoundingClientRect();
        anchor = page && pageRect && pageRect.width > 0
          ? { page, fx: (pointer.x - pageRect.left) / pageRect.width, fy: (pointer.y - pageRect.top) / pageRect.height }
          : null;
        started = true;
      }
      accumulated *= factor;
      element.style.transformOrigin = `${originX}px ${originY}px`;
      element.style.willChange = 'transform';
      element.style.transform = `scale(${accumulated})`;
    };

    const settleZoom = () => {
      settle = 0;
      const viewer = viewerRef.current;
      const element = viewerElement();
      if (!viewer || !element || !started) return;
      started = false;
      const factor = accumulated;
      accumulated = 1;
      if (Math.abs(factor - 1) > 0.001) viewer.updateScale({ scaleFactor: factor });
      element.style.transform = '';
      element.style.transformOrigin = '';
      element.style.willChange = '';
      if (anchor) {
        // Put the anchored content point back under the cursor. Measured rather
        // than predicted: committing can also shift the layout (scrollbars,
        // centring), which is what made the built-in origin correction drift.
        const rect = anchor.page.getBoundingClientRect();
        if (rect.width > 0) {
          scroll.scrollLeft += rect.left + anchor.fx * rect.width - pointer.x;
          scroll.scrollTop += rect.top + anchor.fy * rect.height - pointer.y;
        }
        anchor = null;
      }
      setZoom(viewer.currentScale);
    };

    const onWheel = (event: WheelEvent) => {
      if (!event.ctrlKey) return;
      event.preventDefault();
      pointer.x = event.clientX;
      pointer.y = event.clientY;
      pending *= event.deltaY < 0 ? 1.1 : 1 / 1.1;
      if (!frame) frame = window.requestAnimationFrame(apply);
      window.clearTimeout(settle);
      settle = window.setTimeout(settleZoom, 160);
    };
    scroll.addEventListener('wheel', onWheel, { passive: false });
    return () => {
      scroll.removeEventListener('wheel', onWheel);
      window.cancelAnimationFrame(frame);
      window.clearTimeout(settle);
    };
  }, []);

  const renderedPreviewPage = useCallback((pageNumber: number): Promise<HTMLCanvasElement> => {
    const cached = previewPageCache.current.get(pageNumber);
    if (cached) return cached;
    const pdfDocument = pdfDocumentRef.current;
    if (!pdfDocument) return Promise.reject(new Error('PDF 尚未加载完成。'));
    const pending = pdfDocument.getPage(pageNumber).then(async page => {
      const viewport = page.getViewport({ scale: 1.6 });
      const canvas = window.document.createElement('canvas');
      canvas.width = Math.ceil(viewport.width);
      canvas.height = Math.ceil(viewport.height);
      const context = canvas.getContext('2d');
      if (!context) throw new Error('浏览器无法创建 PDF 预览画布。');
      await page.render({ canvas, canvasContext: context, viewport }).promise;
      return canvas;
    }).catch(error => {
      previewPageCache.current.delete(pageNumber);
      throw error;
    });
    previewPageCache.current.set(pageNumber, pending);
    return pending;
  }, []);

  const previewLink = useCallback(async (link: PDFLinkPreview): Promise<string> => {
    const target = link.target;
    const cacheKey = `${target.pageNumber}:${Math.round(target.x)}:${Math.round(target.y)}`;
    const cached = previewCache.current.get(cacheKey);
    if (cached) return cached;
    const source = await renderedPreviewPage(target.pageNumber);
    const region = destinationPreviewRegion(target);
    const crop = canvasCropForRegion(region, source.width, source.height);
    const preview = window.document.createElement('canvas');
    preview.width = Math.min(1100, crop.width);
    preview.height = Math.max(1, Math.round(crop.height * preview.width / crop.width));
    const context = preview.getContext('2d');
    if (!context) throw new Error('浏览器无法创建链接目标截图。');
    context.fillStyle = '#fff';
    context.fillRect(0, 0, preview.width, preview.height);
    context.drawImage(source, crop.sx, crop.sy, crop.width, crop.height, 0, 0, preview.width, preview.height);
    const dataURL = preview.toDataURL('image/jpeg', 0.88);
    preview.width = 0;
    preview.height = 0;
    if (previewCache.current.size >= 48) {
      const oldest = previewCache.current.keys().next().value;
      if (oldest !== undefined) previewCache.current.delete(oldest);
    }
    previewCache.current.set(cacheKey, dataURL);
    return dataURL;
  }, [renderedPreviewPage]);

  // Every jump that leaves the reader's current spot remembers it first; the
  // back button below hands that one position back.
  const rememberPosition = useCallback(() => {
    const scroll = scrollRef.current;
    if (scroll) setReturnPoint({ top: scroll.scrollTop, left: scroll.scrollLeft });
  }, []);

  const goBack = useCallback(() => {
    if (!returnPoint) return;
    const scroll = scrollRef.current;
    if (scroll) {
      scroll.scrollTop = returnPoint.top;
      scroll.scrollLeft = returnPoint.left;
    }
    setReturnPoint(null);
  }, [returnPoint]);

  const followLink = useCallback((link: PDFLinkPreview) => {
    rememberPosition();
    viewerRef.current?.scrollPageIntoView({ pageNumber: link.target.pageNumber });
  }, [rememberPosition]);

  const navigateOutline = useCallback((dest: unknown) => {
    rememberPosition();
    linkServiceRef.current?.goToDestination(dest as never);
  }, [rememberPosition]);

  const replaceSelection = useCallback((next: LatestPDFSelection) => {
    processController.current?.abort();
    generations.current.replace();
    setStrokes(NO_STROKES);
    setAnnotationTool(null);
    setAnnotationDone(false);
    setSelection(next);
    setResult(undefined);
    setProcessing(false);
  }, []);

  function clearSelection() {
    processController.current?.abort();
    generations.current.replace();
    setStrokes(NO_STROKES);
    setAnnotationTool(null);
    setAnnotationDone(false);
    setSelection(undefined);
    setResult(undefined);
    setProcessing(false);
  }

  function handleAnnotation(action: { type: 'strokes'; strokes: AnnotationStroke[] } | { type: 'tool'; tool: 'pen' | 'rect' | null } | { type: 'commit' } | { type: 'cancel' }) {
    if (action.type === 'strokes') setStrokes(action.strokes);
    else if (action.type === 'tool') setAnnotationTool(action.tool);
    else if (action.type === 'cancel') clearSelection();
    else {
      // ✓ finishes only the annotation: box, toolbar and strokes disappear from
      // the page, while the selection itself (strokes kept) stays ready to be
      // saved or processed.
      setAnnotationTool(null);
      setAnnotationDone(true);
    }
  }

  // The page canvas has to be rendered before a crop can be taken; strokes are
  // baked in here so both the saved file and the uploaded image carry them.
  function cropAreaSelection(region: NormalizedPDFRegion, pageNumber: number): HTMLCanvasElement {
    const pageView = viewerRef.current?.getPageView(pageNumber - 1);
    const source = (pageView?.div as HTMLElement | undefined)?.querySelector<HTMLCanvasElement>('.canvasWrapper canvas');
    if (!source || source.width < 1 || source.height < 1) throw new Error('当前 PDF 页面尚未完成渲染。');
    const crop = canvasCropForRegion(region, source.width, source.height);
    const temporary = window.document.createElement('canvas');
    temporary.width = crop.width;
    temporary.height = crop.height;
    const context = temporary.getContext('2d');
    if (!context) throw new Error('浏览器无法创建截图画布。');
    context.drawImage(source, crop.sx, crop.sy, crop.width, crop.height, 0, 0, crop.width, crop.height);
    drawAnnotationStrokes(context, strokes, crop.width, crop.height);
    return temporary;
  }

  function canvasToPNG(canvas: HTMLCanvasElement): Promise<Blob> {
    return new Promise<Blob>((resolve, reject) => canvas.toBlob(blob => blob ? resolve(blob) : reject(new Error('浏览器无法生成 PNG 截图。')), 'image/png'));
  }

  async function processSelection() {
    if (!selection) return;
    if (!documentId) {
      // Local file: no backend round-trip. Text goes to the clipboard, an area
      // draft is cropped out of the page canvas and saved as a PNG.
      setProcessing(true);
      try {
        if (selection.kind === 'text') {
          await navigator.clipboard.writeText(selection.text);
          onMessage(`已复制第 ${selection.pageNumber ?? '?'} 页的所选文本（${selection.text.length} 字符），未上传。`);
        } else if (selection.kind === 'area') {
          const temporary = cropAreaSelection(selection.region, selection.pageNumber);
          const blob = await canvasToPNG(temporary);
          temporary.width = 0;
          temporary.height = 0;
          const url = URL.createObjectURL(blob);
          const link = window.document.createElement('a');
          link.href = url;
          link.download = `selection-p${selection.pageNumber}.png`;
          link.click();
          window.setTimeout(() => URL.revokeObjectURL(url), 10000);
          onMessage(`已保存第 ${selection.pageNumber} 页的框选截图（${blob.size} 字节），未上传。`);
        }
      } catch (error) {
        onMessage(`本地处理选择失败：${error instanceof Error ? error.message : '未知错误'}`);
      } finally {
        setProcessing(false);
      }
      return;
    }
    const generation = generations.current.current();
    const controller = new AbortController();
    processController.current?.abort();
    processController.current = controller;
    setProcessing(true);
    setResult(undefined);
    let captureBlob: Blob | undefined;
    try {
      let response: { result: DocumentSelectionResult };
      if (selection.kind === 'text') {
        response = await api.processDocumentSelection(documentId, { kind: 'text', text: selection.text, pageNumber: selection.pageNumber, region: selection.region }, controller.signal);
      } else if (selection.kind === 'block') {
        response = await api.processDocumentSelection(documentId, { kind: 'block', blockId: selection.blockId }, controller.signal);
      } else {
        const temporary = cropAreaSelection(selection.region, selection.pageNumber);
        captureBlob = await canvasToPNG(temporary);
        temporary.width = 0;
        temporary.height = 0;
        response = await api.processDocumentSelection(documentId, { kind: 'area', pageNumber: selection.pageNumber, region: selection.region, image: captureBlob }, controller.signal);
      }
      if (!generations.current.isCurrent(generation) || controller.signal.aborted) return;
      setResult(response.result);
      onMessage('当前选择已由 Go 临时处理边界接收；未写入数据库或文件存储。');
    } catch (error) {
      if (!controller.signal.aborted && generations.current.isCurrent(generation)) onMessage(`处理当前选择失败：${error instanceof Error ? error.message : '未知错误'}`);
    } finally {
      captureBlob = undefined;
      if (generations.current.isCurrent(generation)) setProcessing(false);
    }
  }

  function selectMode(next: SelectionMode) {
    if (mode === next) return;
    setMode(next);
    onMessage(MODE_HINTS[next]);
  }

  const totalPages = pageCount || viewerRef.current?.pagesCount || 0;

  function toggleRailCollapsed() {
    if (!railCollapsed) setPageJumpOpen(false);
    setRailCollapsed(!railCollapsed);
  }

  // pdf.js reads `query` as a plain phrase and `type` as '' for a new search /
  // 'again' for next-previous; highlightAll paints every match on every page.
  function runFind(findPrevious: boolean) {
    const bus = eventBusRef.current;
    const query = findQuery.trim();
    if (!bus || !query) return;
    bus.dispatch('find', {
      source: null,
      type: 'again',
      query,
      caseSensitive: false,
      entireWord: false,
      highlightAll: true,
      findPrevious,
      phraseSearch: true,
      matchDiacritics: true,
    } as never);
  }

  function searchFind(query: string) {
    const bus = eventBusRef.current;
    if (!bus) return;
    if (!query.trim()) {
      bus.dispatch('findbarclose', { source: null });
      setFindMatches(null);
      setFindState(null);
      return;
    }
    bus.dispatch('find', {
      source: null,
      type: '',
      query: query.trim(),
      caseSensitive: false,
      entireWord: false,
      highlightAll: true,
      findPrevious: false,
      phraseSearch: true,
      matchDiacritics: true,
    } as never);
  }

  function findCountLabel(): string {
    if (!findQuery.trim()) return '';
    if (findState === FindState.PENDING) return '查找中…';
    if (findState === FindState.NOT_FOUND) return '无结果';
    if (!findMatches || findMatches.total === 0) return '';
    return `${findMatches.current}/${findMatches.total}`;
  }

  function closeFind() {
    setFindOpen(false);
    setFindQuery('');
    setFindMatches(null);
    setFindState(null);
    eventBusRef.current?.dispatch('findbarclose', { source: null });
  }

  function togglePageJump() {
    if (pageJumpOpen) {
      setPageJumpOpen(false);
      return;
    }
    // Prefilled with the page the reader is on, which doubles as a page counter.
    setPageJumpValue(String(viewerRef.current?.currentPageNumber ?? 1));
    setPageJumpOpen(true);
  }

  function jumpToPage() {
    const requested = Number.parseInt(pageJumpValue, 10);
    if (!Number.isFinite(requested) || totalPages < 1) return;
    rememberPosition();
    viewerRef.current?.scrollPageIntoView({ pageNumber: Math.min(Math.max(1, requested), totalPages) });
    setPageJumpOpen(false);
    setPageJumpValue('');
  }

  // Dragging the rail: the box follows the pointer in its current orientation,
  // and only on release does it settle — near the top or the bottom edge it
  // becomes a horizontal toolbar docked flush with that edge, anywhere else it
  // stays a vertical rail docked to the nearest side.
  //
  // The gesture is tracked with window listeners rather than
  // `setPointerCapture`: a captured pointer retargets the click that follows
  // the gesture to the capturing element, which silently kills every button
  // inside the rail after the first drag.
  const railDragEnd = useRef<(() => void) | null>(null);

  function railPointerDown(event: ReactPointerEvent<HTMLDivElement>) {
    // A new gesture starts clean: any click-swallow flag from the last drag is
    // spent by now.
    railClickGuard.current = false;
    if (event.button !== 0 || railDrag.current) return;
    if ((event.target as HTMLElement | null)?.closest('.pdf-page-jump')) return;
    const rail = railRef.current;
    const panel = panelRef.current;
    if (!rail || !panel) return;
    const railRect = rail.getBoundingClientRect();
    const panelRect = panel.getBoundingClientRect();
    const drag = {
      pointerId: event.pointerId,
      offsetX: event.clientX - railRect.left,
      offsetY: event.clientY - railRect.top,
      startX: event.clientX,
      startY: event.clientY,
      lastX: event.clientX - panelRect.left,
      lastY: event.clientY - panelRect.top,
      width: railRect.width,
      height: railRect.height,
      panelWidth: panelRect.width,
      panelHeight: panelRect.height,
      moved: false,
    };
    railDrag.current = drag;

    const onMove = (moveEvent: PointerEvent) => {
      if (moveEvent.pointerId !== drag.pointerId) return;
      if (!drag.moved && Math.hypot(moveEvent.clientX - drag.startX, moveEvent.clientY - drag.startY) < 4) return;
      const area = panelRef.current?.getBoundingClientRect();
      if (!area) return;
      if (!drag.moved) {
        drag.moved = true;
        setRailDragging(true);
      }
      drag.lastX = moveEvent.clientX - area.left;
      drag.lastY = moveEvent.clientY - area.top;
      const x = clampNumber(drag.lastX - drag.offsetX, 4, Math.max(4, area.width - drag.width - 4));
      const y = clampNumber(drag.lastY - drag.offsetY, 4, Math.max(4, area.height - drag.height - 4));
      setRailPlacement(current => ({ x, y, horizontal: current?.horizontal ?? false, dockEnd: current?.dockEnd ?? true }));
    };

    const endDrag = (upEvent: PointerEvent) => {
      if (upEvent.pointerId !== drag.pointerId) return;
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', endDrag);
      window.removeEventListener('pointercancel', endDrag);
      railDragEnd.current = null;
      railDrag.current = null;
      setRailDragging(false);
      if (!drag.moved) return;
      // The release that ends a drag also produces a click on whatever sits
      // under it; swallow that one so dragging never switches the selection
      // mode. It is dispatched in the same task, so clearing on the next tick
      // keeps the guard from ever eating a later click.
      railClickGuard.current = true;
      window.setTimeout(() => { railClickGuard.current = false; }, 0);
      const x = clampNumber(drag.lastX - drag.offsetX, 4, Math.max(4, drag.panelWidth - drag.width - 4));
      const y = clampNumber(drag.lastY - drag.offsetY, 4, Math.max(4, drag.panelHeight - drag.height - 4));
      const centerX = x + drag.width / 2;
      const centerY = y + drag.height / 2;
      const band = Math.max(56, drag.panelHeight * 0.2);
      const horizontal = centerY < band || centerY > drag.panelHeight - band;
      const dockEnd = horizontal ? centerY > drag.panelHeight / 2 : centerX > drag.panelWidth / 2;
      railSnapPending.current = true;
      setRailPlacement({ x, y, horizontal, dockEnd });
    };

    railDragEnd.current = () => endDrag({ pointerId: drag.pointerId } as PointerEvent);
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', endDrag);
    window.addEventListener('pointercancel', endDrag);
  }

  // Unmounting mid-drag must not leave window listeners behind.
  useEffect(() => () => railDragEnd.current?.(), []);

  // A placed rail re-docks when the reader itself changes size, so it can never
  // end up outside the visible area after a resize or a layout switch.
  useEffect(() => {
    const panel = panelRef.current;
    if (!railPlacement || !panel) return;
    const observer = new ResizeObserver(() => {
      railSnapPending.current = true;
      setRailPlacement(current => (current ? { ...current } : current));
    });
    observer.observe(panel);
    return () => observer.disconnect();
  }, [railPlacement !== null]);

  // Settling runs after the orientation switch, so the docked edge is computed
  // from the rail's final size (a horizontal toolbar is much wider than a rail).
  useLayoutEffect(() => {
    if (!railSnapPending.current) return;
    railSnapPending.current = false;
    const rail = railRef.current;
    const panel = panelRef.current;
    if (!rail || !panel) return;
    const railRect = rail.getBoundingClientRect();
    const panelRect = panel.getBoundingClientRect();
    const maxX = Math.max(4, panelRect.width - railRect.width - 4);
    const maxY = Math.max(4, panelRect.height - railRect.height - 4);
    setRailPlacement(current => current && {
      ...current,
      x: current.horizontal ? clampNumber(current.x, 4, maxX) : (current.dockEnd ? maxX : 12),
      y: current.horizontal ? (current.dockEnd ? maxY : 12) : clampNumber(current.y, 4, maxY),
    });
  }, [railPlacement]);

  const activeOutline = useMemo(() => activeOutlineNode(outline, currentPage), [outline, currentPage]);
  const outlineTree = useMemo(() => outline.length === 0
    ? <p className="hint">该 PDF 未提供目录。</p>
    : <OutlineTree nodes={outline} onNavigate={navigateOutline} active={activeOutline} />, [outline, navigateOutline, activeOutline]);

  return <section className="pdf-viewer-panel" ref={panelRef}>
    {!modeRail && <div className="pdf-reader-controls"><strong>PDF 阅读与定位</strong>
      <div className="pdf-mode-switch" role="group" aria-label="选择模式">
        <button className={mode === 'text' ? 'active' : ''} type="button" onClick={() => selectMode('text')}>选择文本</button>
        <button className={mode === 'block' ? 'active' : ''} type="button" onClick={() => selectMode('block')}>选择解析 Block</button>
        <button className={mode === 'area' ? 'active' : ''} type="button" onClick={() => selectMode('area')}>框选页面</button>
      </div>
      <span className="hint">{MODE_HINTS[mode]}</span>
    </div>}
    {selection && !modeRail && <div className="pdf-selection-summary"><div><strong>当前选择</strong><span>{selectionSummary(selection)}</span></div>
      {selection.kind === 'text' && <p>{selection.text}</p>}
      {selection.kind === 'block' && <p>{selection.previewText}</p>}
      {selection.kind === 'area' && <code>{JSON.stringify(selection.region)}</code>}
      <div className="pdf-selection-actions"><button className="primary" type="button" disabled={processing} onClick={() => void processSelection()}>{processing ? '处理中…' : '处理当前选择'}</button><button type="button" disabled={processing} onClick={clearSelection}>清除</button></div>
      {result && <p className="pdf-selection-result">已接收：{result.kind}{result.imageWidth ? ` · ${result.imageWidth}×${result.imageHeight}` : ''}</p>}
    </div>}
    <div className={`pdf-reader-frame${outlineExternal ? ' outline-external' : ''}${outlineCollapsed ? ' outline-collapsed' : ''}`}>
      {!outlineExternal && <aside className="outline"><h3>目录</h3>{outlineTree}<p className="hint">在阅读区上下滚动可连续阅读全部页面。</p></aside>}
      <div className="pdf-reader-body">
        {!modeRail && <div className="pdf-toolbar"><span>共 {pageCount || '…'} 页{linkCount > 0 ? ` · ${linkCount} 个可预览内部链接` : ''}</span><button type="button" aria-expanded={!outlineCollapsed} onClick={() => setOutlineCollapsed(collapsed => !collapsed)}>{outlineCollapsed ? '展开目录' : '收起目录'}</button></div>}
        <div className="pdf-viewer-host"><div className="pdf-viewer-scroll" ref={scrollRef}><div className="pdfViewer" /></div></div>
      </div>
    </div>
    {modeRail && selection && <div className="pdf-selection-bar">
      <span>{selectionSummary(selection)}</span>
      <button className="primary" type="button" disabled={processing} onClick={() => void processSelection()}>
        {processing ? '处理中…' : processable ? '处理当前选择' : selection.kind === 'text' ? '复制文本' : '保存截图'}
      </button>
      <button type="button" disabled={processing} onClick={clearSelection}>清除</button>
    </div>}
    {modeRail && <div
      className={`pdf-mode-rail${(railPlacement?.horizontal ?? false) ? ' rail-horizontal' : ''}${(railPlacement?.dockEnd ?? true) ? ' rail-dock-end' : ''}${railDragging ? ' rail-dragging' : ''}${railCollapsed ? ' rail-collapsed' : ''}`}
      ref={railRef}
      role="toolbar"
      aria-label="阅读工具"
      aria-orientation={(railPlacement?.horizontal ?? false) ? 'horizontal' : 'vertical'}
      title="拖动可移动工具栏"
      style={railPlacement ? { left: railPlacement.x, top: railPlacement.y, right: 'auto', bottom: 'auto', transform: 'none' } : undefined}
      onPointerDown={railPointerDown}
      onClickCapture={(event: ReactMouseEvent<HTMLDivElement>) => {
        if (!railClickGuard.current) return;
        event.preventDefault();
        event.stopPropagation();
      }}>
      <button className={mode === 'text' ? 'active' : ''} type="button" aria-pressed={mode === 'text'} title="选择文本" onClick={() => selectMode('text')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4.4 4.1 10.8 20 13.1 13.6 19.5 11.3z" /></svg>
      </button>
      <button className={mode === 'area' ? 'active' : ''} type="button" aria-pressed={mode === 'area'} title="框选页面" onClick={() => selectMode('area')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4 8V5.5A1.5 1.5 0 0 1 5.5 4H8" /><path d="M16 4h2.5A1.5 1.5 0 0 1 20 5.5V8" /><path d="M20 16v2.5a1.5 1.5 0 0 1-1.5 1.5H16" /><path d="M8 20H5.5A1.5 1.5 0 0 1 4 18.5V16" /></svg>
      </button>
      {processable && <button className={mode === 'block' ? 'active' : ''} type="button" aria-pressed={mode === 'block'} title="选择解析 Block" onClick={() => selectMode('block')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><rect x="4.5" y="4.5" width="15" height="15" rx="2.5" /></svg>
      </button>}
      <button className={findOpen ? 'active' : ''} type="button" aria-pressed={findOpen} title="在 PDF 中查找（Ctrl+F）" onClick={() => (findOpen ? closeFind() : setFindOpen(true))}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true"><circle cx="10.8" cy="10.8" r="5.6" /><path d="m15 15 3.8 3.8" /></svg>
      </button>
      {/* The popover is anchored to its own icon, so it opens level with it. */}
      <div className="pdf-page-jump-anchor">
        <button className={`pdf-page-jump-toggle${pageJumpOpen ? ' active' : ''}`} type="button" aria-pressed={pageJumpOpen} aria-expanded={pageJumpOpen} title="跳转到指定页" onClick={togglePageJump}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M13.6 3.6H7A1.6 1.6 0 0 0 5.4 5.2v13.6A1.6 1.6 0 0 0 7 20.4h10a1.6 1.6 0 0 0 1.6-1.6V8.6z" />
            <path d="M13.6 3.6v5h5" />
            <path d="M9 14.6h5.4m0 0-2-2m2 2-2 2" />
          </svg>
        </button>
        {pageJumpOpen && <div className="pdf-page-jump" role="dialog" aria-label="跳转到指定页">
          <input ref={pageJumpInput} className="pdf-page-jump-input" value={pageJumpValue} inputMode="numeric" aria-label="页码"
            autoFocus
            onChange={event => setPageJumpValue(event.target.value.replace(/\D/g, '').slice(0, 6))}
            onKeyDown={event => {
              if (event.key === 'Enter') { event.preventDefault(); jumpToPage(); }
              if (event.key === 'Escape') { event.preventDefault(); setPageJumpOpen(false); }
            }} />
          <span className="pdf-page-jump-total">/ {totalPages || '…'}</span>
          <button type="button" onClick={jumpToPage}>跳转</button>
        </div>}
      </div>
      {/* Folds the whole rail away, leaving this one icon to bring it back. */}
      <button className="rail-collapse-toggle" type="button" aria-expanded={!railCollapsed} title={railCollapsed ? '展开工具栏' : '收起工具栏'} onClick={toggleRailCollapsed}>
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M4.1 3.9 8.1 8l-4 4.1" />
          <path d="M7.9 3.9 11.9 8l-4 4.1" />
        </svg>
      </button>
    </div>}
    {returnPoint && <button className="pdf-back-button" type="button" title="返回到跳转前的位置" onClick={goBack}>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M19 12H5.5" /><path d="m11 6-6 6 6 6" /></svg>
      返回
    </button>}
    {findOpen && <div className="pdf-find-bar" role="search">
      <svg className="pdf-find-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" aria-hidden="true"><circle cx="10.8" cy="10.8" r="5.6" /><path d="m15 15 3.8 3.8" /></svg>
      <input ref={findInput} className="pdf-find-input" value={findQuery} placeholder="在 PDF 中查找" aria-label="在 PDF 中查找" autoFocus
        onChange={event => { setFindQuery(event.target.value); searchFind(event.target.value); }}
        onKeyDown={event => {
          if (event.key === 'Enter') { event.preventDefault(); runFind(event.shiftKey); }
          if (event.key === 'Escape') { event.preventDefault(); closeFind(); }
        }} />
      <span className="pdf-find-count" aria-live="polite">{findCountLabel()}</span>
      <button type="button" title="上一个结果（Shift+Enter）" disabled={!findQuery.trim()} onClick={() => runFind(true)}>
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3.5 10 8 5.5l4.5 4.5" /></svg>
      </button>
      <button type="button" title="下一个结果（Enter）" disabled={!findQuery.trim()} onClick={() => runFind(false)}>
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3.5 6 8 10.5 12.5 6" /></svg>
      </button>
      <button type="button" title="关闭查找（Esc）" onClick={closeFind}>
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" aria-hidden="true"><path d="M4.5 4.5l7 7" /><path d="M11.5 4.5l-7 7" /></svg>
      </button>
    </div>}
    <span className="pdf-zoom-chip" aria-hidden="true">{Math.round(zoom * 100)}%</span>
    {outlineExternal && outlineTarget && !outlineCollapsed && createPortal(<div className="pdf-outline-content">{outlineTree}</div>, outlineTarget)}
    {slots.map(entry => createPortal(
      <PageOverlay documentId={documentId} selectionTarget={selectionTarget} pageNumber={entry.pageNumber} mode={mode}
        links={linksByPage.get(entry.pageNumber) ?? []} onSelection={replaceSelection} onMessage={onMessage}
        onFollowLink={followLink} previewLink={previewLink}
        areaSelection={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? selection.region : undefined}
        strokes={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? strokes : NO_STROKES}
        annotationTool={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? annotationTool : null}
        onAnnotation={handleAnnotation} />,
      entry.node,
      entry.pageNumber,
    ))}
  </section>;
}
