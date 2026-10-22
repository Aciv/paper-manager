import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent, type PointerEvent as ReactPointerEvent } from 'react';
import { createPortal } from 'react-dom';
import { AnnotationMode, GlobalWorkerOptions, Util, getDocument, type PDFDocumentProxy, type PDFPageProxy } from 'pdfjs-dist';
import { EventBus, FindState, PDFFindController, PDFLinkService, PDFViewer, ScrollMode, SpreadMode } from 'pdfjs-dist/web/pdf_viewer.mjs';
import 'pdfjs-dist/web/pdf_viewer.css';

import './PdfReader.css';
import {
  api,
  Block,
  contentUrl,
  Document as PaperDocument,
  type DocumentOutlineSource,
  type DocumentReadingStructure,
  type PaperCitation,
} from './api';
import { matchCitationTargets } from './citationLinkMatcher';
import type { EvidenceLinkTarget, EvidenceSeed } from './evidenceLinks';
import { PDFLinkPreview, destinationPDFPoint, destinationPreviewRegion, mergeAdjacentPDFLinks, viewportRegion } from './pdfLinkPreview';
import { LatestPDFSelection, NormalizedPDFRegion, canvasCropForRegion, normalizedRegion, regionIsLargeEnough } from './pdfSelection';
import { type SelectedPDFBlock, type TranslationRequestSeed } from './translation/index.ts';
import type { TranslationPreview } from './TranslationPanel';
import { activeOutlineNode, annotateOutlinePages, outlineFromSections, type OutlineEntry } from './pdfOutline';
import { isDocumentRead, localFileReadingKey, readingRecord, saveReadingPage, setDocumentRead, subscribeReadingState } from './readingState';

GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type SelectionMode = 'text' | 'block' | 'area';

export type ReaderEvidenceOverlay = EvidenceLinkTarget & {
  id: string;
  label?: string;
  sectionId?: string;
};

const MODE_HINTS: Record<SelectionMode, string> = {
  text: '拖动文本层形成文本草稿；右键打开翻译与证据菜单。',
  block: '点击高亮区域形成 Block 草稿；Shift/Ctrl+点击可多选翻译；右键打开菜单。',
  area: '在单页内拖动形成区域草稿；右键打开翻译与证据菜单。',
};

// Freehand strokes and rectangles drawn over an area selection. Points are
// normalised to the selected region, so they survive zooming and can be baked
// into the exported crop at any resolution.
type AnnotationStroke = { tool: 'pen' | 'rect'; points: Array<{ x: number; y: number }> };

const NO_STROKES: AnnotationStroke[] = [];
const ANNOTATION_COLOR = '#e5484d';

// Reader presentation preferences are device-local, shared by every document.
type PDFViewMode = 'single' | 'continuous' | 'double';
const VIEW_MODE_STORAGE = 'papersManager.pdfViewMode';
const NIGHT_MODE_STORAGE = 'papersManager.pdfNightMode';

function initialViewMode(): PDFViewMode {
  const stored = window.localStorage.getItem(VIEW_MODE_STORAGE);
  return stored === 'single' || stored === 'double' ? stored : 'continuous';
}

function initialNightMode(): boolean {
  return window.localStorage.getItem(NIGHT_MODE_STORAGE) === 'true';
}

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

function evidenceTargetKey(target: EvidenceLinkTarget): string {
  const rects = target.rects.map((rect) => `${rect.x0},${rect.y0},${rect.x1},${rect.y1}`).join('|');
  return `${target.page}:${target.blockId ?? ''}:${rects}`;
}

function blockRegion(block: Block): NormalizedPDFRegion | undefined {
  const location = block.location;
  if (!location || !location.pageWidth || !location.pageHeight
    || location.bboxX0 === undefined || location.bboxY0 === undefined
    || location.bboxX1 === undefined || location.bboxY1 === undefined) return undefined;
  return normalizedRegion(
    location.bboxX0 / location.pageWidth,
    location.bboxY0 / location.pageHeight,
    location.bboxX1 / location.pageWidth,
    location.bboxY1 / location.pageHeight,
  );
}

function evidenceRegions(target: ReaderEvidenceOverlay, blocks: Block[]): NormalizedPDFRegion[] {
  if (target.rects.length > 0) return target.rects;
  if (!target.blockId) return [];
  const block = blocks.find((candidate) => candidate.id === target.blockId);
  const region = block ? blockRegion(block) : undefined;
  return region ? [region] : [];
}

function outlineSourceLabel(source?: DocumentOutlineSource): string {
  switch (source) {
    case 'pdf_outline': return 'PDF 原生目录';
    case 'markdown_headings': return 'Markdown 标题回退';
    case 'none': return '未识别目录';
    case 'unavailable': return '旧数据无目录';
    default: return '';
  }
}

// The API keeps low-level diagnostic codes for agents/debugging. The reader
// shows only actionable explanations and hides normal fallback details.
function outlineDiagnosticMessage(code: string): string | undefined {
  switch (code) {
    case 'pdf_outline_missing':
    case 'outline_non_monotonic_mapping_dropped':
    case 'legacy_missing_outline':
      return undefined;
    case 'recognize_document_to_create_outline':
      return '文档尚未完成识别，暂时无法生成目录。';
    case 'pdf_outline_item_limit':
    case 'markdown_outline_item_limit':
      return '目录项过多，列表已截断。';
    case 'pdf_outline_depth_clamped':
      return '目录层级过深，显示层级已压缩。';
    case 'outline_response_budget_dropped':
    case 'outline_selection_failed':
      return '目录生成不完整，部分内容已省略。';
    case 'pdf_outline_read_failed':
    case 'pdf_outline_invalid_item':
    case 'pdf_outline_invalid_destination':
    case 'pdf_outline_wholly_unusable':
    case 'markdown_headings_missing':
    case 'outline_unavailable':
      return '未找到完整目录。';
    default:
      return undefined;
  }
}

type PositionedPDFText = { text: string; x: number; y: number };

async function targetTextByLink(page: PDFPageProxy, links: PDFLinkPreview[]): Promise<Map<string, string>> {
  const viewport = page.getViewport({ scale: 1 });
  const content = await page.getTextContent();
  const positioned: PositionedPDFText[] = [];
  for (const item of content.items) {
    if (!(item && typeof item === 'object' && 'str' in item && 'transform' in item)) continue;
    if (typeof item.str !== 'string' || !Array.isArray(item.transform) || item.transform.length < 6) continue;
    const transformed = Util.transform(viewport.transform, item.transform);
    if (!Number.isFinite(transformed[4]) || !Number.isFinite(transformed[5]) || !item.str.trim()) continue;
    positioned.push({ text: item.str.trim(), x: transformed[4], y: transformed[5] });
  }

  // Reference pages are often two-column. Cluster native destination x values
  // into column starts; vertical boundaries from the other column must never
  // truncate or contaminate this link's text window.
  const sortedXs = [...new Set(links.map(link => link.target.x))].sort((left, right) => left - right);
  const columns: Array<{ center: number; values: number[] }> = [];
  for (const x of sortedXs) {
    const current = columns.at(-1);
    if (current && x - current.center <= viewport.width * 0.12) {
      current.values.push(x);
      current.center = current.values.reduce((sum, value) => sum + value, 0) / current.values.length;
    } else {
      columns.push({ center: x, values: [x] });
    }
  }
  const columnIndexFor = (x: number) => {
    let best = 0;
    for (let index = 1; index < columns.length; index += 1) {
      if (Math.abs(columns[index].center - x) < Math.abs(columns[best].center - x)) best = index;
    }
    return best;
  };
  const destinationYsByColumn = new Map<number, number[]>();
  for (const link of links) {
    const column = columnIndexFor(link.target.x);
    const ys = destinationYsByColumn.get(column) ?? [];
    if (!ys.includes(link.target.y)) ys.push(link.target.y);
    destinationYsByColumn.set(column, ys);
  }
  for (const ys of destinationYsByColumn.values()) ys.sort((left, right) => left - right);
  const result = new Map<string, string>();
  for (const link of links) {
    // Destinations normally point at the entry's first baseline. A bounded
    // window collects enough title/author/identifier text for matching without
    // reconstructing the whole bibliography or blocking the click path.
    const column = columnIndexFor(link.target.x);
    const columnYs = destinationYsByColumn.get(column) ?? [];
    const yIndex = columnYs.indexOf(link.target.y);
    const nextDestinationY = yIndex < 0 ? undefined : columnYs[yIndex + 1];
    const columnStart = column === 0 ? 0 : columns[column].center - 12;
    const columnEnd = column + 1 < columns.length ? columns[column + 1].center - 8 : viewport.width;
    const top = Math.max(0, link.target.y - 18);
    const defaultBottom = link.target.y + Math.min(180, viewport.height * 0.23);
    const bottom = Math.min(viewport.height, defaultBottom,
      nextDestinationY === undefined || nextDestinationY <= link.target.y + 2 ? defaultBottom : nextDestinationY - 2);
    const nearby = positioned
      .filter(item => item.x >= columnStart && item.x < columnEnd && item.y >= top && item.y <= bottom)
      .sort((left, right) => Math.abs(left.y - right.y) <= 3 ? left.x - right.x : left.y - right.y);
    result.set(link.id, nearby.map(item => item.text).join(' '));
  }
  return result;
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
const PageOverlay = memo(function PageOverlay({ documentId, selectionTarget, pageNumber, mode, links, citationMatches, selectedBlockId, pickedBlockIds, evidence, focusedEvidenceKey, onSelection, onBlockToggle, onBlockContextMenu, onMessage, onPreviewCitations, onFollowLink, previewLink, areaSelection, strokes, annotationTool, onAnnotation }: {
  // Absent for a PDF opened from disk: it has no parsed blocks, but text and
  // area selection still work — those are handled in the browser.
  documentId?: string;
  selectionTarget: string;
  pageNumber: number;
  mode: SelectionMode;
  links: PDFLinkPreview[];
  citationMatches: Map<string, PaperCitation[]>;
  selectedBlockId?: string;
  pickedBlockIds?: ReadonlySet<string>;
  evidence: ReaderEvidenceOverlay[];
  focusedEvidenceKey?: string;
  onSelection(selection: LatestPDFSelection): void;
  onBlockToggle?(block: SelectedPDFBlock): void;
  onBlockContextMenu?(block: SelectedPDFBlock, clientX: number, clientY: number): void;
  onMessage(message: string): void;
  onPreviewCitations(referenceIds: string[]): void;
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
    const onMouseUp = (event: Event) => {
      if (event instanceof MouseEvent && event.button !== 0) return;
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
    const matchedCitations = citationMatches.get(link.id) ?? [];
    onPreviewCitations(matchedCitations.map(citation => citation.referenceId));
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
        const matchedCitations = citationMatches.get(link.id) ?? [];
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
            {matchedCitations.length > 0 && <span className="citation-link-preview-matches">
              <b>区域内匹配到 {matchedCitations.length} 条引用</b>
              {matchedCitations.map(citation => <span key={citation.referenceId}>
                {citation.rawReference?.trim() || citation.title?.trim() || '未命名引用'}
              </span>)}
            </span>}
            <small>{matchedCitations.length > 0 ? '已在右侧定位引用 · 点击跳转到原文位置' : '点击跳转到原文位置'}</small>
          </span>}
        </button>;
      })}
    </div>
    <div className={`block-overlay ${mode === 'block' ? 'active' : ''}`} aria-hidden={mode !== 'block'}>
      {blocks.map(block => {
        const location = block.location;
        if (!location || !location.pageWidth || !location.pageHeight || location.bboxX0 === undefined || location.bboxY0 === undefined || location.bboxX1 === undefined || location.bboxY1 === undefined) return null;
        const picked = pickedBlockIds?.has(block.id) ?? false;
        const selectedBlock = selectedBlockId === block.id;
        const region = blockRegion(block);
        const blockPick: SelectedPDFBlock = {
          blockId: block.id,
          blockType: block.type,
          text: block.text,
          pageNumber: location.pageNumber,
          region,
        };
        return <button key={block.id} className={`pdf-block pdf-block-${block.type}${selectedBlock ? ' selected' : ''}${picked ? ' picked' : ''}`} tabIndex={mode === 'block' ? 0 : -1} style={{
          left: `${(location.bboxX0 / location.pageWidth) * 100}%`, top: `${(location.bboxY0 / location.pageHeight) * 100}%`,
          width: `${((location.bboxX1 - location.bboxX0) / location.pageWidth) * 100}%`, height: `${((location.bboxY1 - location.bboxY0) / location.pageHeight) * 100}%`,
        }} type="button" title={`${block.type}: ${block.text.slice(0, 240)}`} onClick={(event) => {
          if ((event.shiftKey || event.ctrlKey || event.metaKey) && onBlockToggle) {
            event.preventDefault();
            onBlockToggle(blockPick);
            return;
          }
          onSelection({
            kind: 'block', documentId: selectionTarget, blockId: block.id, pageNumber: location.pageNumber,
            blockType: block.type, previewText: block.text, region,
          });
        }} onContextMenu={(event) => {
          if (!onBlockContextMenu) return;
          event.preventDefault();
          event.stopPropagation();
          onBlockContextMenu(blockPick, event.clientX, event.clientY);
        }}><span>{block.type}</span></button>;
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

    {evidence.length > 0 && <div className="pdf-evidence-overlay" aria-hidden="true">
      {evidence.map((target) => {
        const focused = focusedEvidenceKey === evidenceTargetKey(target);
        return evidenceRegions(target, blocks).map((region, index) => <div
          key={`${target.id}-${index}`}
          className={`pdf-evidence-region${focused ? ' focused' : ''}`}
          title={target.label ?? `第 ${target.page} 页证据`}
          style={{
            left: `${region.x0 * 100}%`, top: `${region.y0 * 100}%`,
            width: `${(region.x1 - region.x0) * 100}%`, height: `${(region.y1 - region.y0) * 100}%`,
          }}
        />);
      })}
    </div>}

    {loadError && <p className="pdf-block-error">解析块未加载：{loadError}</p>}
  </div>;
});

type OutlineNode = OutlineEntry;

// A real outline tree: a chevron per branch and a guide line per nesting level.
// Indentation comes from the nested list itself, so every level lines up under
// its parent's chevron instead of a per-depth pixel offset.
function OutlineTreeNode({ node, onNavigate, active }: { node: OutlineNode; onNavigate(node: OutlineNode): void; active?: OutlineNode }) {
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
      {node.dest || node.page
        ? <button className="pdf-outline-link" type="button" aria-current={isActive ? 'true' : undefined} onClick={() => onNavigate(node)}>{node.title}</button>
        : <span className="pdf-outline-text" aria-current={isActive ? 'true' : undefined}>{node.title}</span>}
    </div>
    {children.length > 0 && open && <ul className="pdf-outline-children">
      {children.map((child, index) => <OutlineTreeNode key={`${index}-${child.title}`} node={child} onNavigate={onNavigate} active={active} />)}
    </ul>}
  </li>;
}

function OutlineTree({ nodes, onNavigate, active }: { nodes: OutlineNode[]; onNavigate(node: OutlineNode): void; active?: OutlineNode }) {
  return <ul className="pdf-outline-tree">
    {nodes.map((node, index) => <OutlineTreeNode key={`${index}-${node.title}`} node={node} onNavigate={onNavigate} active={active} />)}
  </ul>;
}

export function PdfReader({ document, file, citations = [], onCitationLinks, onTranslate, onTranslationSource, translationPreview, onMessage, onInsertEvidence, evidenceTargets = [], evidenceJump, outlinePlacement = 'internal', outlineTarget, modeRail = false }: {
  document?: PaperDocument;
  // A PDF straight from the user's disk. It has no backend record, so anything
  // keyed by a document id (parsed blocks, metadata, citations, selection
  // processing) simply does not apply and its panels stay empty.
  file?: File | null;
  // Stored-paper citations are already in the host page's browser memory.
  // Native link matching never performs a hover-time or click-time API request.
  citations?: PaperCitation[];
  onCitationLinks?(referenceIds: string[]): void;
  // Translation is a frontend-only action; the reader forwards the exact text
  // or blocks the user selected and never uploads the whole PDF.
  onTranslate?(request: TranslationRequestSeed): void;
  // Selection changes seed the panel's editable source without translating;
  // only onTranslate (the reader's 翻译 action) starts a translation.
  onTranslationSource?(request: TranslationRequestSeed): void;
  // Latest result mirrored from the translation panel. The reader only shows it
  // when translation was started from its right-click menu.
  translationPreview?: TranslationPreview;
  onMessage(message: string): void;
  // Replaces the old no-op backend selection process: the reader forwards a
  // selection seed and the host inserts readable evidence Markdown into the
  // active reading-note section.
  onInsertEvidence?(seed: EvidenceSeed): void;
  // Note evidence links loaded by the note panel. Only self-document targets
  // are drawn, and only on the pages the viewer has actually rendered.
  evidenceTargets?: ReaderEvidenceOverlay[];
  // A note-panel "定位" action. Every jump is page/rectangle based; block IDs
  // are hints that degrade to the rectangle when the block is gone.
  evidenceJump?: { id: string; target: EvidenceLinkTarget } | null;
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
  const previewPageCache = useRef(new Map<string, Promise<HTMLCanvasElement>>());

  const [mode, setMode] = useState<SelectionMode>('text');
  const [outlineCollapsed, setOutlineCollapsed] = useState(false);
  const [selection, setSelection] = useState<LatestPDFSelection>();
  // Shift/Ctrl-clicked Blocks form a translation-only multi-selection; a plain
  // click keeps the existing single-Block processing behavior.
  const [pickedBlocks, setPickedBlocks] = useState<SelectedPDFBlock[]>([]);
  const pickedBlocksRef = useRef<SelectedPDFBlock[]>([]);
  const pickedBlockIds = useMemo(() => new Set(pickedBlocks.map((block) => block.blockId)), [pickedBlocks]);
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number }>();
  const [translationPopup, setTranslationPopup] = useState<{ x: number; y: number }>();
  const contextMenuRef = useRef<HTMLDivElement>(null);
  const [processing, setProcessing] = useState(false);
  const [pageCount, setPageCount] = useState(0);
  const [zoom, setZoom] = useState(1);
  // The persisted backend outline is authoritative when it has sections; the
  // PDF.js outline stays as a fallback for local files and structure failures.
  const [nativeOutline, setNativeOutline] = useState<OutlineNode[]>([]);
  const [sourceStructure, setSourceStructure] = useState<DocumentReadingStructure>();
  const [focusedEvidenceKey, setFocusedEvidenceKey] = useState<string>();
  const [slots, setSlots] = useState<Array<{ pageNumber: number; node: HTMLElement }>>([]);
  const [linksByPage, setLinksByPage] = useState<Map<number, PDFLinkPreview[]>>(new Map());
  const [links, setLinks] = useState<PDFLinkPreview[]>([]);
  const [citationMatchesByLink, setCitationMatchesByLink] = useState<Map<string, PaperCitation[]>>(new Map());
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
  // Where this paper's saved reading position and read flag live.
  const readingKey = localFile ? localFileReadingKey(localFile) : documentId ?? '';

  // Presentation preferences: single page / continuous / two-page spread, and
  // the inverted night canvas. Kept per device, not per document.
  const [viewMode, setViewMode] = useState<PDFViewMode>(initialViewMode);
  const [nightMode, setNightMode] = useState(initialNightMode);
  // Digits typed on the keyboard open a transient "jump to page" prompt.
  const [typedPage, setTypedPage] = useState('');
  const [readState, setReadState] = useState(false);
  const viewModeRef = useRef(viewMode);
  viewModeRef.current = viewMode;
  const readingKeyRef = useRef(readingKey);
  readingKeyRef.current = readingKey;
  const latestPageRef = useRef(1);
  // An explicit "unread" on the last page wins over the automatic flag until
  // the reader leaves that page (or opens another document).
  const suppressAutoRead = useRef(false);
  const positionSaveTimer = useRef(0);
  const typedPageTimer = useRef(0);
  // While a saved position is being restored, pagechanging events report the
  // transient page 1 first; saving that would overwrite the stored position.
  const restoreGuard = useRef(false);

  const [railPlacement, setRailPlacement] = useState<{ x: number; y: number; horizontal: boolean; dockEnd: boolean } | null>(null);
  const [railDragging, setRailDragging] = useState(false);
  const [railCollapsed, setRailCollapsed] = useState(false);
  // View / night / read controls fold behind one icon so the rail stays short.
  const [viewMenuOpen, setViewMenuOpen] = useState(false);
  const railRef = useRef<HTMLDivElement>(null);
  const railDrag = useRef<{
    pointerId: number; offsetX: number; offsetY: number; startX: number; startY: number;
    lastX: number; lastY: number; width: number; height: number; panelWidth: number; panelHeight: number; moved: boolean;
  } | null>(null);
  const railSnapPending = useRef(false);
  const railClickGuard = useRef(false);

  // Keyboard shortcuts belong to the reader only while it is the surface being
  // used. They never fire inside form controls, so typing in the find bar, the
  // page-jump popover or a metadata form is unaffected.
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      readerActive.current = Boolean(panelRef.current?.contains(event.target as Node));
    };
    // Scrolling with the wheel is using the reader too; without this, shortcuts
    // only worked after a click on the page.
    const onWheel = (event: WheelEvent) => {
      if (panelRef.current?.contains(event.target as Node)) readerActive.current = true;
    };
    const onFocusIn = (event: FocusEvent) => {
      if (panelRef.current?.contains(event.target as Node)) readerActive.current = true;
    };
    const clearTypedPage = () => {
      window.clearTimeout(typedPageTimer.current);
      setTypedPage('');
    };
    const appendTypedDigit = (digit: string) => {
      const next = `${typedPage}${digit}`.replace(/^0+(?=\d)/, '').slice(0, 6);
      setTypedPage(next);
      window.clearTimeout(typedPageTimer.current);
      // A pause commits nothing; the prompt simply fades away.
      typedPageTimer.current = window.setTimeout(() => setTypedPage(''), 1400);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      const panel = panelRef.current;
      const inside = Boolean(panel && (readerActive.current || panel.contains(window.document.activeElement)));
      if (!inside) return;
      const target = event.target;
      if (target instanceof Element && target.closest('input, textarea, select, [contenteditable="true"]')) return;

      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'f') {
        event.preventDefault();
        setFindOpen(true);
        window.requestAnimationFrame(() => findInput.current?.select());
        return;
      }
      if (event.ctrlKey || event.metaKey || event.altKey) return;

      if (event.key === 'Escape') {
        if (findOpen) { closeFind(); return; }
        if (pageJumpOpen) { setPageJumpOpen(false); return; }
        if (viewMenuOpen) { setViewMenuOpen(false); return; }
        if (typedPage) { clearTypedPage(); return; }
        if (translationPopup) { setTranslationPopup(undefined); return; }
        if (selection) clearSelection();
        return;
      }

      // Type a page number, then Enter. The overlay shows what will be jumped to.
      if (/^[0-9]$/.test(event.key)) {
        event.preventDefault();
        appendTypedDigit(event.key);
        return;
      }
      if (event.key === 'Backspace' && typedPage) {
        event.preventDefault();
        const next = typedPage.slice(0, -1);
        setTypedPage(next);
        if (!next) window.clearTimeout(typedPageTimer.current);
        else {
          window.clearTimeout(typedPageTimer.current);
          typedPageTimer.current = window.setTimeout(() => setTypedPage(''), 1400);
        }
        return;
      }
      if ((event.key === 'Enter' || event.key === 'NumpadEnter') && typedPage) {
        event.preventDefault();
        const requested = Number.parseInt(typedPage, 10);
        clearTypedPage();
        jumpToPageNumber(requested);
        return;
      }

      const viewer = viewerRef.current;
      if (!viewer || viewer.pagesCount === 0) return;
      if (event.key === 'ArrowRight' || event.key === 'PageDown') {
        event.preventDefault();
        viewer.nextPage();
        return;
      }
      if (event.key === 'ArrowLeft' || event.key === 'PageUp') {
        event.preventDefault();
        viewer.previousPage();
        return;
      }
      if (event.key === 'Home') {
        event.preventDefault();
        jumpToPageNumber(1);
        return;
      }
      if (event.key === 'End') {
        event.preventDefault();
        jumpToPageNumber(viewer.pagesCount);
        return;
      }
      if (event.key === '+' || event.key === '=') {
        event.preventDefault();
        viewer.increaseScale();
        setZoom(viewer.currentScale);
        return;
      }
      if (event.key === '-' || event.key === '_') {
        event.preventDefault();
        viewer.decreaseScale();
        setZoom(viewer.currentScale);
      }
    };
    window.document.addEventListener('pointerdown', onPointerDown, true);
    window.document.addEventListener('wheel', onWheel, { capture: true, passive: true });
    window.document.addEventListener('focusin', onFocusIn, true);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.document.removeEventListener('pointerdown', onPointerDown, true);
      window.document.removeEventListener('wheel', onWheel, { capture: true });
      window.document.removeEventListener('focusin', onFocusIn, true);
      window.removeEventListener('keydown', onKeyDown);
    };
  });

  // Read/unread is shared with the catalog through the storage module.
  useEffect(() => {
    const sync = () => setReadState(isDocumentRead(readingKey));
    sync();
    return subscribeReadingState(sync);
  }, [readingKey]);

  // Switching the presentation mode applies to the live viewer and keeps the
  // reader on the page it was already showing. Re-applying the page after the
  // asynchronous re-scale is required: the scale update scrolls back to
  // whatever page the viewer considered current before the mode switch.
  useEffect(() => {
    window.localStorage.setItem(VIEW_MODE_STORAGE, viewMode);
    const viewer = viewerRef.current;
    if (!viewer || viewer.pagesCount === 0) return;
    const page = Math.min(Math.max(1, latestPageRef.current || viewer.currentPageNumber || 1), viewer.pagesCount);
    viewer.scrollMode = viewMode === 'continuous' ? ScrollMode.VERTICAL : ScrollMode.PAGE;
    viewer.spreadMode = viewMode === 'double' ? SpreadMode.ODD : SpreadMode.NONE;
    viewer.currentPageNumber = page;
    viewer.currentScaleValue = viewMode === 'continuous' ? 'page-width' : 'page-fit';
    setZoom(viewer.currentScale);
    const timer = window.setTimeout(() => {
      if (viewerRef.current === viewer) viewer.currentPageNumber = page;
    }, 220);
    return () => window.clearTimeout(timer);
  }, [viewMode]);

  useEffect(() => {
    window.localStorage.setItem(NIGHT_MODE_STORAGE, String(nightMode));
  }, [nightMode]);

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

  // The view panel is a side popover: clicking anywhere else dismisses it.
  useEffect(() => {
    if (!viewMenuOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if ((event.target as HTMLElement | null)?.closest('.pdf-view-anchor')) return;
      setViewMenuOpen(false);
    };
    window.document.addEventListener('pointerdown', onPointerDown);
    return () => window.document.removeEventListener('pointerdown', onPointerDown);
  }, [viewMenuOpen]);

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
    viewer.scrollMode = viewModeRef.current === 'continuous' ? ScrollMode.VERTICAL : ScrollMode.PAGE;
    viewer.spreadMode = viewModeRef.current === 'double' ? SpreadMode.ODD : SpreadMode.NONE;
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
      // Continuous reading fills the width; a single page or a spread fits the
      // whole page into the viewport.
      viewer.currentScaleValue = viewModeRef.current === 'continuous' ? 'page-width' : 'page-fit';
      setZoom(viewer.currentScale);
      syncSlots();
    };
    const onPageChanging = (event: { pageNumber?: number }) => {
      if (typeof event.pageNumber !== 'number') return;
      setCurrentPage(event.pageNumber);
      latestPageRef.current = event.pageNumber;
      // Reaching the last page counts as having read the paper; the catalog
      // updates through the same broadcast as a manual toggle.
      if (event.pageNumber < viewer.pagesCount) suppressAutoRead.current = false;
      if (event.pageNumber >= viewer.pagesCount && !suppressAutoRead.current && !isDocumentRead(readingKeyRef.current)) {
        setDocumentRead(readingKeyRef.current, true, event.pageNumber);
      }
      // Debounced: scrolling a long document must not write on every page tick.
      if (!restoreGuard.current) {
        window.clearTimeout(positionSaveTimer.current);
        positionSaveTimer.current = window.setTimeout(
          () => saveReadingPage(readingKeyRef.current, latestPageRef.current),
          900,
        );
      }
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
    // PdfReader stays mounted while the selected paper changes. Drop retained
    // canvases/data URLs here, and also namespace keys by source below so an
    // older asynchronous crop can never satisfy a request for the new paper.
    previewCache.current.clear();
    previewPageCache.current.clear();
    const viewer = viewerRef.current;
    if (!viewer) return;
    let disposed = false;
    const generation = pdfLoadGeneration.current + 1;
    pdfLoadGeneration.current = generation;
    // Until the saved position has been applied, ignore the transient first
    // pagechanging events of the new document.
    restoreGuard.current = true;
    latestPageRef.current = 1;
    suppressAutoRead.current = false;
    window.clearTimeout(positionSaveTimer.current);
    const savedPage = readingRecord(readingKey)?.page ?? 0;
    const bus = eventBusRef.current;
    const onRestorePages = () => {
      // Pages are initialised here; defer one frame-ish so the target page view
      // exists, then jump to the remembered page.
      window.setTimeout(() => {
        if (disposed || pdfLoadGeneration.current !== generation) return;
        try {
          if (savedPage > 1 && savedPage <= viewer.pagesCount) {
            viewer.scrollPageIntoView({ pageNumber: savedPage });
            latestPageRef.current = savedPage;
            setCurrentPage(savedPage);
          }
        } finally {
          restoreGuard.current = false;
        }
      }, 120);
    };
    bus?.on('pagesinit', onRestorePages);
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
            if (!disposed && pdfLoadGeneration.current === generation) setNativeOutline([]);
            return;
          }
          // Resolving destination → page once per load is what lets the outline
          // follow the reader without touching the document while scrolling.
          const entries = await annotateOutlinePages(nodes as OutlineEntry[], {
            getDestination: dest => pdfDocument.getDestination(dest as string),
            getPageIndex: reference => pdfDocument.getPageIndex(reference as never),
            numPages: pdfDocument.numPages,
          });
          if (!disposed && pdfLoadGeneration.current === generation) setNativeOutline(entries);
        }).catch(() => { if (!disposed) setNativeOutline([]); });
        void collectPDFLinkPreviews(pdfDocument).then(links => {
          if (disposed || pdfLoadGeneration.current !== generation) return;
          setLinks(links);
          setLinksByPage(groupLinks(links));
          setLinkCount(links.length);
        }).catch(() => { /* annotations unavailable */ });
      });
    }).catch(error => {
      restoreGuard.current = false;
      if (!disposed) onMessage(`PDF 加载失败：${error instanceof Error ? error.message : '未知错误'}`);
    });

    return () => {
      disposed = true;
      bus?.off('pagesinit', onRestorePages);
      window.clearTimeout(positionSaveTimer.current);
      // Flush the latest page of the source being left.
      if (!restoreGuard.current) saveReadingPage(readingKey, latestPageRef.current);
      restoreGuard.current = false;
      pdfDocumentRef.current = undefined;
      linkServiceRef.current?.setDocument(null);
      setSlots([]);
      setPageCount(0);
      setCurrentPage(1);
      setLinks([]);
      setLinksByPage(new Map());
      setLinkCount(0);
      setNativeOutline([]);
      setSourceStructure(undefined);
      setFindOpen(false);
      setFindQuery('');
      setFindMatches(null);
      setFindState(null);
      eventBusRef.current?.dispatch('findbarclose', { source: null });
      if (opened.task) void opened.task.destroy();
    };
  }, [sourceKey, localFile, documentId, readingKey, onMessage]);

  // Persisted source outline from the recognition generation. It carries the
  // native/fallback source label and page mappings; when it has no sections the
  // reader keeps the PDF.js outline loaded above.
  useEffect(() => {
    setSourceStructure(undefined);
    if (!documentId) return;
    let active = true;
    void api.getDocumentStructure(documentId)
      .then((structure) => { if (active) setSourceStructure(structure); })
      .catch(() => { /* structure is a disposable read model; PDF.js stays the fallback */ });
    return () => { active = false; };
  }, [documentId, sourceKey]);

  // Target text extraction and citation scoring happen as soon as both the PDF
  // links and the already-loaded citation list are available. Hover handling is
  // therefore a single in-memory Map lookup plus the host's UI update.
  useEffect(() => {
    setCitationMatchesByLink(new Map());
    const pdfDocument = pdfDocumentRef.current;
    if (!pdfDocument || links.length === 0 || citations.length === 0 || !documentId) return;
    let cancelled = false;
    const linksByTargetPage = new Map<number, PDFLinkPreview[]>();
    for (const link of links) {
      const pageLinks = linksByTargetPage.get(link.target.pageNumber);
      if (pageLinks) pageLinks.push(link);
      else linksByTargetPage.set(link.target.pageNumber, [link]);
    }
    // Pages with several destinations are most likely bibliography pages, so
    // make the common first hover ready before sparse outline/figure targets.
    const targetPages = [...linksByTargetPage.entries()].sort((left, right) => right[1].length - left[1].length);
    void (async () => {
      const matches = new Map<string, PaperCitation[]>();
      const citationsByID = new Map(citations.map(citation => [citation.referenceId, citation]));
      for (const [pageNumber, pageLinks] of targetPages) {
        if (cancelled) return;
        try {
          const page = await pdfDocument.getPage(pageNumber);
          const targetTexts = await targetTextByLink(page, pageLinks);
          for (const link of pageLinks) {
            const matched = matchCitationTargets(targetTexts.get(link.id) ?? '', citations)
              .map(match => citationsByID.get(match.referenceId))
              .filter((citation): citation is PaperCitation => Boolean(citation));
            if (matched.length > 0) matches.set(link.id, matched);
          }
          if (!cancelled) {
            setCitationMatchesByLink(new Map(matches));
          }
        } catch {
          // One malformed/textless target page falls back to the ordinary PDF
          // jump and must not prevent other pages from being precomputed.
        }
      }
    })();
    return () => { cancelled = true; };
  }, [citations, documentId, links, sourceKey]);

  // Source switch: drop any selection draft from the previous file.
  useEffect(() => {
    setSelection(undefined);
    resetPickedBlocks();
    setContextMenu(undefined);
    setTranslationPopup(undefined);
    setFocusedEvidenceKey(undefined);
    setProcessing(false);
    if (!documentId) setMode('text'); // block mode needs parsed blocks
  }, [sourceKey]);

  // The selection action surface is a right-click menu. Close it on an outside
  // press, Escape, scrolling or resizing so it can never float over new content.
  useEffect(() => {
    if (!contextMenu) return;
    const onPointerDown = (event: PointerEvent) => {
      if (contextMenuRef.current?.contains(event.target as Node)) return;
      setContextMenu(undefined);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setContextMenu(undefined);
    };
    const close = () => setContextMenu(undefined);
    window.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
    };
  }, [contextMenu]);

  // The compact translation result behaves like a page-anchored popover:
  // dismiss it on an outside press, Escape, page scroll, or resize.
  useEffect(() => {
    if (!translationPopup) return;
    const onPointerDown = (event: PointerEvent) => {
      if (event.target instanceof Element && event.target.closest('.pdf-translation-popover')) return;
      setTranslationPopup(undefined);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setTranslationPopup(undefined);
    };
    const close = (event: Event) => {
      if (event.target instanceof Element && event.target.closest('.pdf-translation-popover')) return;
      setTranslationPopup(undefined);
    };
    window.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('scroll', close, true);
    window.addEventListener('resize', close);
    return () => {
      window.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('scroll', close, true);
      window.removeEventListener('resize', close);
    };
  }, [translationPopup]);

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
    const cacheKey = `${sourceKey}:${pageNumber}`;
    const cached = previewPageCache.current.get(cacheKey);
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
      previewPageCache.current.delete(cacheKey);
      throw error;
    });
    previewPageCache.current.set(cacheKey, pending);
    return pending;
  }, [sourceKey]);

  const previewLink = useCallback(async (link: PDFLinkPreview): Promise<string> => {
    const target = link.target;
    const cacheKey = `${sourceKey}:${target.pageNumber}:${Math.round(target.x)}:${Math.round(target.y)}`;
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
  }, [renderedPreviewPage, sourceKey]);

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

  const previewCitations = useCallback((referenceIds: string[]) => {
    onCitationLinks?.(referenceIds);
  }, [onCitationLinks]);

  const navigateOutline = useCallback((node: OutlineNode) => {
    rememberPosition();
    // Native PDF outlines keep their exact destination; persisted outlines
    // carry only a start page.
    if (node.dest) {
      linkServiceRef.current?.goToDestination(node.dest as never);
      return;
    }
    if (node.page) {
      viewerRef.current?.scrollPageIntoView({ pageNumber: node.page });
      setCurrentPage(node.page);
    }
  }, [rememberPosition]);

  const evidenceByPage = useMemo(() => {
    const map = new Map<number, ReaderEvidenceOverlay[]>();
    for (const target of evidenceTargets) {
      if (target.documentId && target.documentId !== documentId) continue;
      const list = map.get(target.page);
      if (list) list.push(target);
      else map.set(target.page, [target]);
    }
    return map;
  }, [documentId, evidenceTargets]);

  // Note-panel navigation: jump to the evidence page and pulse its rectangle.
  // Block IDs are only hints; the link's page/rectangle is always the fallback.
  useEffect(() => {
    if (!evidenceJump) return;
    const { target } = evidenceJump;
    if (target.documentId && target.documentId !== documentId) {
      onMessage('该证据指向其他文档，当前阅读器无法跳转。');
      return;
    }
    if (pageCount > 0 && target.page > pageCount) {
      onMessage(`证据指向第 ${target.page} 页，但当前 PDF 只有 ${pageCount} 页；请重新解析或更新笔记。`);
      return;
    }
    rememberPosition();
    viewerRef.current?.scrollPageIntoView({ pageNumber: target.page });
    setCurrentPage(target.page);
    setFocusedEvidenceKey(evidenceTargetKey(target));
    if (target.rects.length === 0 && !target.blockId) {
      onMessage(`该证据只有页码提示，已跳到第 ${target.page} 页。`);
    } else if (target.rects.length === 0 && target.blockId) {
      onMessage(`已按第 ${target.page} 页定位；Block hint 仅作参考，区域以当前解析结果为准。`);
    }
    const timer = window.setTimeout(() => setFocusedEvidenceKey(undefined), 3200);
    return () => window.clearTimeout(timer);
  }, [documentId, evidenceJump?.id, onMessage, rememberPosition]);

  function resetPickedBlocks() {
    pickedBlocksRef.current = [];
    setPickedBlocks([]);
  }

  const toggleBlockPick = useCallback((block: SelectedPDFBlock) => {
    setContextMenu(undefined);
    const current = pickedBlocksRef.current;
    const next = current.some((item) => item.blockId === block.blockId)
      ? current.filter((item) => item.blockId !== block.blockId)
      : [...current, block];
    pickedBlocksRef.current = next;
    setPickedBlocks(next);
    if (next.length > 0) onTranslationSource?.({ kind: 'blocks', blocks: next });
  }, [onTranslationSource]);

  async function copySelectedText() {
    if (selection?.kind !== 'text') return;
    try {
      await navigator.clipboard.writeText(selection.text);
      onMessage(`已复制所选文本（${selection.text.length} 字符）。`);
    } catch {
      onMessage('复制失败，请手动选择文本复制。');
    }
  }

  function contextMenuPosition(clientX: number, clientY: number) {
    const rect = panelRef.current?.getBoundingClientRect();
    const menuWidth = 200;
    const menuHeight = 220;
    const left = rect ? Math.max(6, Math.min(clientX - rect.left, rect.width - menuWidth - 6)) : clientX;
    const top = rect ? Math.max(6, Math.min(clientY - rect.top, rect.height - menuHeight - 6)) : clientY;
    return { x: left, y: top };
  }

  function openSelectionContextMenu(event: ReactMouseEvent<HTMLDivElement>) {
    if (!selection && pickedBlocks.length === 0) return;
    event.preventDefault();
    event.stopPropagation();
    setTranslationPopup(undefined);
    setContextMenu(contextMenuPosition(event.clientX, event.clientY));
  }

  function runContextAction(action: () => void) {
    setContextMenu(undefined);
    action();
  }

  function selectionTranslationRequest(): TranslationRequestSeed | undefined {
    if (pickedBlocks.length > 0) return { kind: 'blocks', blocks: pickedBlocks };
    if (!selection) return undefined;
    if (selection.kind === 'text') {
      return { kind: 'text', text: selection.text, pageNumber: selection.pageNumber };
    }
    if (selection.kind === 'block') {
      return {
        kind: 'blocks',
        blocks: [{
          blockId: selection.blockId,
          blockType: selection.blockType,
          text: selection.previewText,
          pageNumber: selection.pageNumber,
        }],
      };
    }
    return undefined;
  }

  function openTranslationPopup() {
    const anchor = contextMenu ?? { x: 16, y: 16 };
    const rect = panelRef.current?.getBoundingClientRect();
    if (!rect) {
      setTranslationPopup(anchor);
      return;
    }
    // Keep the compact popover inside the reading surface.
    setTranslationPopup({
      x: Math.max(8, Math.min(anchor.x, Math.max(8, rect.width - 368))),
      y: Math.max(8, Math.min(anchor.y, Math.max(8, rect.height - 248))),
    });
  }

  function translateSelection(showPopup = false) {
    if (!onTranslate) return;
    const request = selectionTranslationRequest();
    if (!request) return;
    if (request.kind === 'text' && !request.text.trim()) return;
    if (request.kind === 'blocks' && request.blocks.every((block) => !block.text.trim())) return;
    if (showPopup) openTranslationPopup();
    onTranslate(request);
  }

  const replaceSelection = useCallback((next: LatestPDFSelection) => {
    setStrokes(NO_STROKES);
    setAnnotationTool(null);
    setAnnotationDone(false);
    setFocusedEvidenceKey(undefined);
    resetPickedBlocks();
    setContextMenu(undefined);
    setTranslationPopup(undefined);
    setSelection(next);
    setProcessing(false);
    // Selecting is only source seeding; the panel must not translate until the
    // user explicitly chooses 翻译 from the context menu or the panel.
    if (next.kind === 'text') {
      onTranslationSource?.({ kind: 'text', text: next.text, pageNumber: next.pageNumber });
    } else if (next.kind === 'block') {
      onTranslationSource?.({
        kind: 'blocks',
        blocks: [{
          blockId: next.blockId,
          blockType: next.blockType,
          text: next.previewText,
          pageNumber: next.pageNumber,
          region: next.region,
        }],
      });
    }
  }, [onTranslationSource]);

  // Block mode also supports right-click as the primary selection gesture:
  // right-clicking an unpicked Block selects it (clearing stale picks) and opens
  // the action menu. Right-clicking a picked Block keeps the multi-selection.
  function openBlockContextMenu(block: SelectedPDFBlock, clientX: number, clientY: number) {
    setTranslationPopup(undefined);
    if (!pickedBlocks.some((item) => item.blockId === block.blockId)) {
      replaceSelection({
        kind: 'block',
        documentId: selectionTarget,
        blockId: block.blockId,
        pageNumber: block.pageNumber,
        blockType: block.blockType,
        previewText: block.text,
        region: block.region,
      });
    }
    setContextMenu(contextMenuPosition(clientX, clientY));
  }

  function clearSelection() {
    setStrokes(NO_STROKES);
    setAnnotationTool(null);
    setAnnotationDone(false);
    resetPickedBlocks();
    setContextMenu(undefined);
    setTranslationPopup(undefined);
    setSelection(undefined);
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

  async function handleSelectionAction() {
    if (!selection) return;
    if (documentId && onInsertEvidence) {
      // Stored document: hand the selection to the reading-note panel. The
      // panel builds the Markdown link, chooses the active section, and commits
      // through the revision-fenced operations endpoint.
      const seed: EvidenceSeed = selection.kind === 'text'
        ? {
          kind: 'text', excerpt: selection.text, pageNumber: selection.pageNumber,
          region: selection.region,
        }
        : selection.kind === 'block'
          ? {
            kind: 'block', excerpt: selection.previewText, pageNumber: selection.pageNumber,
            region: selection.region, blockId: selection.blockId,
          }
          : { kind: 'area', excerpt: '', pageNumber: selection.pageNumber, region: selection.region };
      onInsertEvidence(seed);
      return;
    }
    if (documentId) {
      onMessage('阅读注释不可用，无法插入证据。');
      return;
    }
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
  }

  function selectMode(next: SelectionMode) {
    if (mode === next) return;
    if (next !== 'block') resetPickedBlocks();
    setContextMenu(undefined);
    setMode(next);
    onMessage(MODE_HINTS[next]);
  }

  const totalPages = pageCount || viewerRef.current?.pagesCount || 0;

  function toggleRailCollapsed() {
    if (!railCollapsed) {
      setPageJumpOpen(false);
      setViewMenuOpen(false);
    }
    setRailCollapsed(!railCollapsed);
  }

  function toggleReadState() {
    if (!readingKey) return;
    // Turning the flag off while sitting on the last page must not be undone
    // by the next pagechanging tick.
    suppressAutoRead.current = readState;
    setDocumentRead(readingKey, !readState, latestPageRef.current);
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
    setViewMenuOpen(false);
    // Prefilled with the page the reader is on, which doubles as a page counter.
    setPageJumpValue(String(viewerRef.current?.currentPageNumber ?? 1));
    setPageJumpOpen(true);
  }

  function jumpToPageNumber(requested: number) {
    if (!Number.isFinite(requested) || totalPages < 1) return;
    rememberPosition();
    const page = Math.min(Math.max(1, Math.round(requested)), totalPages);
    latestPageRef.current = page;
    viewerRef.current?.scrollPageIntoView({ pageNumber: page });
  }

  function jumpToPage() {
    const requested = Number.parseInt(pageJumpValue, 10);
    if (!Number.isFinite(requested) || totalPages < 1) return;
    jumpToPageNumber(requested);
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
    if ((event.target as HTMLElement | null)?.closest('.pdf-page-jump, .pdf-view-popover')) return;
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
      // Decide the orientation from where the pointer was released, not from
      // the rail's centre. The vertical rail is ~290px tall, so on shorter
      // viewports its centre can never enter the top/bottom band even when the
      // pointer is dragged all the way to the edge.
      const releaseX = clampNumber(drag.lastX, 0, drag.panelWidth);
      const releaseY = clampNumber(drag.lastY, 0, drag.panelHeight);
      const band = Math.max(56, drag.panelHeight * 0.2);
      const horizontal = releaseY < band || releaseY > drag.panelHeight - band;
      const dockEnd = horizontal ? releaseY > drag.panelHeight / 2 : releaseX > drag.panelWidth / 2;
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

  const sourceOutline = useMemo(
    () => outlineFromSections(sourceStructure?.outline.sections ?? []),
    [sourceStructure],
  );
  const outline = sourceOutline.length > 0 ? sourceOutline : nativeOutline;
  const outlineSource = sourceOutline.length > 0
    ? sourceStructure?.outline.source
    : nativeOutline.length > 0 ? 'pdf_outline' : sourceStructure?.outline.source;
  const outlineDiagnostics = Array.from(new Set(
    [...(sourceStructure?.diagnostics ?? []), ...(sourceStructure?.outline.diagnostics ?? [])]
      .map(outlineDiagnosticMessage)
      .filter((message): message is string => Boolean(message)),
  ));
  const activeOutline = useMemo(() => activeOutlineNode(outline, currentPage), [outline, currentPage]);
  const outlineTree = useMemo(() => <>
    {outlineSource && <p className="pdf-outline-source">目录来源：{outlineSourceLabel(outlineSource)}</p>}
    {outline.length === 0
      ? <p className="hint">{outlineSource === 'none' ? '未识别论文目录。' : '该 PDF 未提供目录。'}</p>
      : <OutlineTree nodes={outline} onNavigate={navigateOutline} active={activeOutline} />}
    {outlineDiagnostics.map((diagnostic) => <p className="pdf-outline-diagnostic" key={diagnostic}>{diagnostic}</p>)}
  </>, [activeOutline, navigateOutline, outline, outlineDiagnostics, outlineSource]);

  return <section className={`pdf-viewer-panel${nightMode ? ' pdf-night' : ''}`} ref={panelRef}>
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
      <div className="pdf-selection-actions">
        <button className="primary" type="button" disabled={processing} onClick={() => void handleSelectionAction()}>
          {processing ? '处理中…' : documentId && onInsertEvidence ? '插入证据' : selection.kind === 'text' ? '复制文本' : '保存截图'}
        </button>
        {onTranslate && selection.kind !== 'area' && <button type="button" disabled={processing} onClick={() => translateSelection()}>翻译</button>}
        <button type="button" disabled={processing} onClick={clearSelection}>清除</button>
      </div>
    </div>}
    <div className={`pdf-reader-frame${outlineExternal ? ' outline-external' : ''}${outlineCollapsed ? ' outline-collapsed' : ''}`}>
      {!outlineExternal && <aside className="outline"><h3>目录</h3>{outlineTree}<p className="hint">在阅读区上下滚动可连续阅读全部页面。</p></aside>}
      <div className="pdf-reader-body">
        {!modeRail && <div className="pdf-toolbar"><span>共 {pageCount || '…'} 页{linkCount > 0 ? ` · ${linkCount} 个可预览内部链接` : ''}</span><button type="button" aria-expanded={!outlineCollapsed} onClick={() => setOutlineCollapsed(collapsed => !collapsed)}>{outlineCollapsed ? '展开目录' : '收起目录'}</button></div>}
        <div className="pdf-viewer-host" onContextMenu={openSelectionContextMenu}><div className="pdf-viewer-scroll" ref={scrollRef}><div className="pdfViewer" /></div></div>
      </div>
    </div>
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
      <button className={mode === 'text' ? 'active' : ''} type="button" aria-pressed={mode === 'text'} title="选择文本（选中后右键操作）" onClick={() => selectMode('text')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4.4 4.1 10.8 20 13.1 13.6 19.5 11.3z" /></svg>
      </button>
      <button className={mode === 'area' ? 'active' : ''} type="button" aria-pressed={mode === 'area'} title="框选页面（选中后右键操作）" onClick={() => selectMode('area')}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4 8V5.5A1.5 1.5 0 0 1 5.5 4H8" /><path d="M16 4h2.5A1.5 1.5 0 0 1 20 5.5V8" /><path d="M20 16v2.5a1.5 1.5 0 0 1-1.5 1.5H16" /><path d="M8 20H5.5A1.5 1.5 0 0 1 4 18.5V16" /></svg>
      </button>
      {processable && <button className={mode === 'block' ? 'active' : ''} type="button" aria-pressed={mode === 'block'} title="选择解析 Block（Shift/Ctrl 多选，右键操作）" onClick={() => selectMode('block')}>
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
      {/* Presentation, night mode and read state fold behind one icon and open
          as a side panel, like the page-number popover. */}
      <div className="pdf-view-anchor">
        <button className={viewMenuOpen ? 'active' : ''} type="button" aria-pressed={viewMenuOpen} aria-expanded={viewMenuOpen}
          title={viewMenuOpen ? '收起视图与阅读状态' : '视图与阅读状态'} onClick={() => { setPageJumpOpen(false); setViewMenuOpen((open) => !open); }}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true">
            <rect x="4" y="4" width="6.5" height="6.5" rx="1.2" /><rect x="13.5" y="4" width="6.5" height="6.5" rx="1.2" />
            <rect x="4" y="13.5" width="6.5" height="6.5" rx="1.2" /><rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.2" />
          </svg>
        </button>
        {viewMenuOpen && <div className="pdf-view-popover" role="group" aria-label="视图与阅读状态">
          <button className={viewMode === 'single' ? 'active' : ''} type="button" aria-pressed={viewMode === 'single'} title="单页视图" onClick={() => setViewMode('single')}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true"><rect x="6.5" y="3.5" width="11" height="17" rx="1.4" /><path d="M9.2 7h5.6M9.2 10h5.6" strokeLinecap="round" /></svg>
          </button>
          <button className={viewMode === 'continuous' ? 'active' : ''} type="button" aria-pressed={viewMode === 'continuous'} title="连续视图" onClick={() => setViewMode('continuous')}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true"><rect x="6" y="3.5" width="12" height="6.5" rx="1" /><rect x="6" y="14" width="12" height="6.5" rx="1" /></svg>
          </button>
          <button className={viewMode === 'double' ? 'active' : ''} type="button" aria-pressed={viewMode === 'double'} title="双页视图" onClick={() => setViewMode('double')}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true"><rect x="3.5" y="4.5" width="7.6" height="15" rx="1.2" /><rect x="12.9" y="4.5" width="7.6" height="15" rx="1.2" /></svg>
          </button>
          <button className={nightMode ? 'active' : ''} type="button" aria-pressed={nightMode} title={nightMode ? '关闭夜间反色' : '夜间反色'} onClick={() => setNightMode(value => !value)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M20.2 13.4A8.2 8.2 0 1 1 10.6 3.8a6.6 6.6 0 0 0 9.6 9.6z" /></svg>
          </button>
          {/* Manual read state; the catalog filters and marks the same record. */}
          <button className={readState ? 'active' : ''} type="button" aria-pressed={readState} title={readState ? '标记为未读' : '标记为已读'} onClick={toggleReadState}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M4.5 5.5A2.5 2.5 0 0 1 7 3h12.5v15.5H7a2.5 2.5 0 0 0-2.5 2.5z" /><path d="M4.5 5.5V21" /><path d="m8.4 10.7 2.2 2.2 4.4-4.4" /></svg>
          </button>
        </div>}
      </div>
      {/* Folds the whole rail away, leaving this one icon to bring it back.
          Eye/eye-off keeps it distinct from the left/right column chevrons. */}
      <button className="rail-collapse-toggle" type="button" aria-expanded={!railCollapsed} title={railCollapsed ? '展开工具栏' : '收起工具栏'} onClick={toggleRailCollapsed}>
        {railCollapsed
          ? <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M1 12s4-7 11-7 11 7 11 7-4 7-11 7S1 12 1 12z" />
            <circle cx="12" cy="12" r="3" />
          </svg>
          : <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94" />
            <path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19" />
            <path d="m1 1 22 22" />
            <path d="M14.12 14.12a3 3 0 1 1-4.24-4.24" />
          </svg>}
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
    {typedPage && <span className="pdf-typed-page" aria-live="polite">跳到第 {typedPage} 页 · Enter</span>}
    {outlineExternal && outlineTarget && !outlineCollapsed && createPortal(<div className="pdf-outline-content">{outlineTree}</div>, outlineTarget)}
    {slots.map(entry => createPortal(
      <PageOverlay documentId={documentId} selectionTarget={selectionTarget} pageNumber={entry.pageNumber} mode={mode}
        links={linksByPage.get(entry.pageNumber) ?? []} citationMatches={citationMatchesByLink}
        selectedBlockId={selection?.kind === 'block' ? selection.blockId : undefined}
        pickedBlockIds={pickedBlockIds} evidence={evidenceByPage.get(entry.pageNumber) ?? []}
        focusedEvidenceKey={focusedEvidenceKey} onSelection={replaceSelection} onBlockToggle={toggleBlockPick}
        onBlockContextMenu={openBlockContextMenu} onMessage={onMessage}
        onPreviewCitations={previewCitations}
        onFollowLink={followLink} previewLink={previewLink}
        areaSelection={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? selection.region : undefined}
        strokes={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? strokes : NO_STROKES}
        annotationTool={!annotationDone && selection?.kind === 'area' && selection.pageNumber === entry.pageNumber ? annotationTool : null}
        onAnnotation={handleAnnotation} />,
      entry.node,
      entry.pageNumber,
    ))}
    {contextMenu && <div ref={contextMenuRef} className="pdf-context-menu" role="menu" aria-label="选择操作"
      style={{ left: contextMenu.x, top: contextMenu.y }} onContextMenu={(event) => event.preventDefault()}>
      <p className="pdf-context-menu-title">
        {pickedBlocks.length > 0
          ? `已选 ${pickedBlocks.length} 个 Block`
          : selection ? selectionSummary(selection) : ''}
      </p>
      {pickedBlocks.length > 0 ? <>
        {onTranslate && <button type="button" role="menuitem" onClick={() => runContextAction(() => translateSelection(true))}>
          翻译 {pickedBlocks.length} 段
        </button>}
        <button type="button" role="menuitem" onClick={() => runContextAction(() => resetPickedBlocks())}>清除多选</button>
      </> : selection && <>
        {onTranslate && selection.kind !== 'area' && <button type="button" role="menuitem" disabled={processing}
          onClick={() => runContextAction(() => translateSelection(true))}>翻译</button>}
        <button type="button" role="menuitem" disabled={processing}
          onClick={() => runContextAction(() => void handleSelectionAction())}>
          {processing ? '处理中…' : documentId && onInsertEvidence ? '插入证据' : selection.kind === 'text' ? '复制文本' : '保存截图'}
        </button>
        {processable && selection.kind === 'text' && <button type="button" role="menuitem" disabled={processing}
          onClick={() => runContextAction(() => void copySelectedText())}>复制文本</button>}
        <div className="pdf-context-menu-separator" role="separator" />
        <button className="pdf-context-menu-danger" type="button" role="menuitem" disabled={processing}
          onClick={() => runContextAction(clearSelection)}>清除选择</button>
      </>}
    </div>}
    {translationPopup && <div className="pdf-translation-popover" role="dialog" aria-label="翻译结果"
      style={{ left: translationPopup.x, top: translationPopup.y }}>
      <div className="pdf-translation-popover-head">
        <strong>翻译结果</strong>
        <button className="pdf-translation-popover-close" type="button" title="关闭"
          aria-label="关闭翻译结果" onClick={() => setTranslationPopup(undefined)}>
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden="true">
            <path d="m4.5 4.5 7 7" />
            <path d="m11.5 4.5-7 7" />
          </svg>
        </button>
      </div>
      {translationPreview?.translation?.trim()
        ? <p className="pdf-translation-popover-body" aria-live="polite">{translationPreview.translation}</p>
        : <p className="pdf-translation-popover-status" aria-live="polite">
          {translationPreview?.status === 'error'
            ? translationPreview.error ?? '翻译失败。'
            : translationPreview?.status === 'done'
              ? '没有可显示的译文。'
              : translationPreview?.status === 'waiting'
                ? '正在等待翻译引擎…'
                : '正在翻译…'}
        </p>}
      {translationPreview?.translation?.trim() && translationPreview.error
        && <p className="pdf-translation-popover-error">{translationPreview.error}</p>}
      {translationPreview?.fallback && <small>已自动选用备用翻译引擎。</small>}
    </div>}
  </section>;
}
