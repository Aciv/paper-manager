import { PointerEvent as ReactPointerEvent, useEffect, useRef, useState } from 'react';
import { Document, Outline, Page, pdfjs } from 'react-pdf';
import type { PDFDocumentProxy, PDFPageProxy } from 'pdfjs-dist';
import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';
import './PdfReader.css';
import { api, Block, contentUrl, Document as PaperDocument, DocumentSelectionResult } from './api';
import { PDFLinkPreview, destinationPDFPoint, destinationPreviewRegion, mergeAdjacentPDFLinks, viewportRegion } from './pdfLinkPreview';
import { LatestPDFSelection, NormalizedPDFRegion, SelectionGeneration, canvasCropForRegion, normalizedRegion, regionIsLargeEnough } from './pdfSelection';

pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type SelectionMode = 'text' | 'block' | 'area';

async function collectPDFLinkPreviews(pdfDocument: PDFDocumentProxy): Promise<PDFLinkPreview[]> {
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

function SelectablePdfPage({ document, pageNumber, renderWidth, mode, selectedArea, links, onMessage, onSelection, onPreviewLink, onFollowLink }: {
  document: PaperDocument;
  pageNumber: number;
  renderWidth: number;
  mode: SelectionMode;
  selectedArea?: NormalizedPDFRegion;
  links: PDFLinkPreview[];
  onMessage(message: string): void;
  onSelection(selection: LatestPDFSelection): void;
  onPreviewLink(link: PDFLinkPreview): Promise<string>;
  onFollowLink(link: PDFLinkPreview): void;
}) {
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [loadError, setLoadError] = useState('');
  const [areaDraft, setAreaDraft] = useState<NormalizedPDFRegion>();
  const [activeLinkID, setActiveLinkID] = useState('');
  const [linkPreviewURL, setLinkPreviewURL] = useState('');
  const [linkPreviewError, setLinkPreviewError] = useState('');
  const pageRef = useRef<HTMLDivElement>(null);
  const dragStart = useRef<{ pointerId: number; x: number; y: number } | undefined>(undefined);
  const linkPreviewGeneration = useRef(0);

  useEffect(() => {
    let active = true;
    setBlocks([]);
    setLoadError('');
    void api.listDocumentBlocks(document.id, pageNumber)
      .then(result => { if (active) setBlocks(result.items); })
      .catch(error => { if (active) setLoadError(error instanceof Error ? error.message : '无法读取解析块。'); });
    return () => { active = false; };
  }, [document.id, pageNumber]);

  useEffect(() => {
    linkPreviewGeneration.current += 1;
    setActiveLinkID('');
    setLinkPreviewURL('');
    setLinkPreviewError('');
  }, [document.id, pageNumber]);

  function showLinkPreview(link: PDFLinkPreview) {
    const generation = linkPreviewGeneration.current + 1;
    linkPreviewGeneration.current = generation;
    setActiveLinkID(link.id);
    setLinkPreviewURL('');
    setLinkPreviewError('');
    void onPreviewLink(link).then(url => {
      if (linkPreviewGeneration.current === generation) setLinkPreviewURL(url);
    }).catch(error => {
      if (linkPreviewGeneration.current === generation) {
        setLinkPreviewError(error instanceof Error ? error.message : '无法生成目标区域预览。');
      }
    });
  }

  function hideLinkPreview() {
    linkPreviewGeneration.current += 1;
    setActiveLinkID('');
    setLinkPreviewURL('');
    setLinkPreviewError('');
  }

  function reportSelectedText() {
    if (mode !== 'text') return;
    const browserSelection = window.getSelection();
    const text = browserSelection?.toString().trim();
    if (!text) return;
    const pageBounds = pageRef.current?.getBoundingClientRect();
    const rangeBounds = browserSelection && browserSelection.rangeCount > 0 ? browserSelection.getRangeAt(0).getBoundingClientRect() : undefined;
    let region: NormalizedPDFRegion | undefined;
    if (pageBounds && rangeBounds && pageBounds.width > 0 && pageBounds.height > 0) {
      region = normalizedRegion(
        (rangeBounds.left - pageBounds.left) / pageBounds.width,
        (rangeBounds.top - pageBounds.top) / pageBounds.height,
        (rangeBounds.right - pageBounds.left) / pageBounds.width,
        (rangeBounds.bottom - pageBounds.top) / pageBounds.height,
      );
    }
    onSelection({ kind: 'text', documentId: document.id, text, pageNumber, region });
  }

  function pointerCoordinates(event: ReactPointerEvent<HTMLDivElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    return { bounds, x: (event.clientX - bounds.left) / bounds.width, y: (event.clientY - bounds.top) / bounds.height };
  }

  function startArea(event: ReactPointerEvent<HTMLDivElement>) {
    if (mode !== 'area' || event.button !== 0) return;
    const point = pointerCoordinates(event);
    dragStart.current = { pointerId: event.pointerId, x: point.x, y: point.y };
    event.currentTarget.setPointerCapture(event.pointerId);
    setAreaDraft(normalizedRegion(point.x, point.y, point.x, point.y));
  }

  function moveArea(event: ReactPointerEvent<HTMLDivElement>) {
    const start = dragStart.current;
    if (!start || start.pointerId !== event.pointerId) return;
    const point = pointerCoordinates(event);
    setAreaDraft(normalizedRegion(start.x, start.y, point.x, point.y));
  }

  function finishArea(event: ReactPointerEvent<HTMLDivElement>) {
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
    onSelection({ kind: 'area', documentId: document.id, pageNumber, region });
  }

  const visibleArea = areaDraft ?? selectedArea;
  return <div ref={pageRef} className="pdf-page" data-page-number={pageNumber} onMouseUp={reportSelectedText}>
    <Page pageNumber={pageNumber} width={renderWidth} renderAnnotationLayer={false} />
    <div className={`citation-link-overlay ${mode === 'text' ? 'active' : ''}`} aria-hidden={mode !== 'text'}>
      {links.map(link => {
        const region = link.sourceRegion;
        const alignRight = (region.x0 + region.x1) / 2 > 0.55;
        const active = activeLinkID === link.id;
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
            {!linkPreviewURL && !linkPreviewError && <span className="citation-link-preview-status">正在生成预览…</span>}
            {linkPreviewError && <span className="citation-link-preview-error">{linkPreviewError}</span>}
            {linkPreviewURL && <img src={linkPreviewURL} alt={`PDF 第 ${link.target.pageNumber} 页链接目标附近原文`} />}
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
          kind: 'block', documentId: document.id, blockId: block.id, pageNumber: block.location?.pageNumber,
          blockType: block.type, previewText: block.text,
        })}><span>{block.type}</span></button>;
      })}
    </div>
    <div className={`area-selection-overlay ${mode === 'area' ? 'active' : ''}`} aria-hidden={mode !== 'area'}
      onPointerDown={startArea} onPointerMove={moveArea} onPointerUp={finishArea}
      onPointerCancel={() => { dragStart.current = undefined; setAreaDraft(undefined); }}>
      {visibleArea && <div className="area-selection-box" style={{ left: `${visibleArea.x0 * 100}%`, top: `${visibleArea.y0 * 100}%`, width: `${(visibleArea.x1 - visibleArea.x0) * 100}%`, height: `${(visibleArea.y1 - visibleArea.y0) * 100}%` }} />}
    </div>
    {loadError && <p className="pdf-block-error">解析块未加载：{loadError}</p>}
  </div>;
}

function selectionSummary(selection: LatestPDFSelection): string {
  if (selection.kind === 'text') return `第 ${selection.pageNumber ?? '?'} 页文本 · ${selection.text.length} 字符`;
  if (selection.kind === 'block') return `第 ${selection.pageNumber ?? '?'} 页 ${selection.blockType} Block`;
  return `第 ${selection.pageNumber} 页框选区域`;
}

export function PdfReader({ document, onMessage }: { document: PaperDocument; onMessage(message: string): void }) {
  const [pageCount, setPageCount] = useState(0);
  const [pageRenderWidth, setPageRenderWidth] = useState(820);
  const [mode, setMode] = useState<SelectionMode>('text');
  const [outlineCollapsed, setOutlineCollapsed] = useState(false);
  const [selection, setSelection] = useState<LatestPDFSelection>();
  const [processing, setProcessing] = useState(false);
  const [result, setResult] = useState<DocumentSelectionResult>();
  const [pdfLinks, setPDFLinks] = useState<PDFLinkPreview[]>([]);
  const pageRefs = useRef<Record<number, HTMLDivElement | null>>({});
  const pdfContentRef = useRef<HTMLDivElement>(null);
  const generations = useRef(new SelectionGeneration());
  const processController = useRef<AbortController | undefined>(undefined);
  const pdfDocumentRef = useRef<PDFDocumentProxy | undefined>(undefined);
  const pdfLoadGeneration = useRef(0);
  const previewCache = useRef(new Map<string, string>());
  const previewPageCache = useRef(new Map<number, Promise<HTMLCanvasElement>>());

  useEffect(() => {
    generations.current.replace();
    processController.current?.abort();
    setSelection(undefined);
    setResult(undefined);
    setProcessing(false);
    setPDFLinks([]);
    pdfDocumentRef.current = undefined;
    pdfLoadGeneration.current += 1;
    previewCache.current.clear();
    previewPageCache.current.clear();
    return () => { generations.current.replace(); processController.current?.abort(); };
  }, [document.id]);

  useEffect(() => {
    const content = pdfContentRef.current;
    if (!content) return;
    let frame = 0;
    const updateWidth = (width: number) => {
      window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => {
        const next = Math.floor(Math.min(1080, Math.max(300, width - 8)));
        setPageRenderWidth(current => Math.abs(current - next) >= 8 ? next : current);
      });
    };
    const observer = new ResizeObserver(entries => updateWidth(entries[0]?.contentRect.width ?? content.clientWidth));
    observer.observe(content);
    updateWidth(content.clientWidth);
    return () => {
      window.cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [outlineCollapsed, pageCount]);

  function handleDocumentLoad(pdfDocument: PDFDocumentProxy) {
    const generation = pdfLoadGeneration.current + 1;
    pdfLoadGeneration.current = generation;
    pdfDocumentRef.current = pdfDocument;
    setPageCount(pdfDocument.numPages);
    setPDFLinks([]);
    void collectPDFLinkPreviews(pdfDocument).then(links => {
      if (pdfLoadGeneration.current === generation) setPDFLinks(links);
    }).catch(error => {
      if (pdfLoadGeneration.current === generation) {
        onMessage(`PDF 内部链接预览不可用：${error instanceof Error ? error.message : '无法读取链接注解。'}`);
      }
    });
  }

  function replaceSelection(next: LatestPDFSelection) {
    processController.current?.abort();
    generations.current.replace();
    setSelection(next);
    setResult(undefined);
    setProcessing(false);
    onMessage(`已暂存${selectionSummary(next)}；点击“处理当前选择”后才会发送到后端。`);
  }

  function clearSelection() {
    processController.current?.abort();
    generations.current.replace();
    setSelection(undefined);
    setResult(undefined);
    setProcessing(false);
  }

  async function processSelection() {
    if (!selection) return;
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
        response = await api.processDocumentSelection(document.id, { kind: 'text', text: selection.text, pageNumber: selection.pageNumber, region: selection.region }, controller.signal);
      } else if (selection.kind === 'block') {
        response = await api.processDocumentSelection(document.id, { kind: 'block', blockId: selection.blockId }, controller.signal);
      } else {
        const source = pageRefs.current[selection.pageNumber]?.querySelector<HTMLCanvasElement>('.react-pdf__Page canvas');
        if (!source || source.width < 1 || source.height < 1) throw new Error('当前 PDF 页面尚未完成渲染。');
        const crop = canvasCropForRegion(selection.region, source.width, source.height);
        const temporary = window.document.createElement('canvas');
        temporary.width = crop.width;
        temporary.height = crop.height;
        const context = temporary.getContext('2d');
        if (!context) throw new Error('浏览器无法创建截图画布。');
        context.drawImage(source, crop.sx, crop.sy, crop.width, crop.height, 0, 0, crop.width, crop.height);
        captureBlob = await new Promise<Blob>((resolve, reject) => temporary.toBlob(blob => blob ? resolve(blob) : reject(new Error('浏览器无法生成 PNG 截图。')), 'image/png'));
        temporary.width = 0;
        temporary.height = 0;
        response = await api.processDocumentSelection(document.id, { kind: 'area', pageNumber: selection.pageNumber, region: selection.region, image: captureBlob }, controller.signal);
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

  function goToPage(pageNumber: number) { pageRefs.current[pageNumber]?.scrollIntoView({ behavior: 'smooth', block: 'start' }); }

  function renderedPreviewPage(pageNumber: number): Promise<HTMLCanvasElement> {
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
  }

  async function previewLink(link: PDFLinkPreview): Promise<string> {
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
  }

  function followLink(link: PDFLinkPreview) {
    window.requestAnimationFrame(() => {
      const pageWrapper = pageRefs.current[link.target.pageNumber];
      const page = pageWrapper?.querySelector<HTMLElement>('.pdf-page');
      const scroller = pageWrapper?.closest<HTMLElement>('.pdf-content');
      if (!pageWrapper || !page || !scroller) {
        pageWrapper?.scrollIntoView({ behavior: 'smooth', block: 'center' });
        return;
      }
      const pageBounds = page.getBoundingClientRect();
      const scrollerBounds = scroller.getBoundingClientRect();
      const targetOffset = link.target.y / link.target.pageHeight * pageBounds.height;
      const targetTop = scroller.scrollTop + pageBounds.top - scrollerBounds.top + targetOffset;
      scroller.scrollTo({ top: Math.max(0, targetTop - scroller.clientHeight / 2), behavior: 'smooth' });
    });
  }

  return <section className="pdf-viewer-panel">
    <div className="pdf-reader-controls"><strong>PDF 阅读与定位</strong>
      <div className="pdf-mode-switch" role="group" aria-label="选择模式">
        <button className={mode === 'text' ? 'active' : ''} type="button" onClick={() => setMode('text')}>选择文本</button>
        <button className={mode === 'block' ? 'active' : ''} type="button" onClick={() => setMode('block')}>选择解析 Block</button>
        <button className={mode === 'area' ? 'active' : ''} type="button" onClick={() => setMode('area')}>框选页面</button>
      </div>
      <span className="hint">{mode === 'text' ? '拖动文本层形成文本草稿。' : mode === 'block' ? '点击高亮区域形成 Block 草稿。' : '在单页内拖动形成区域草稿；此时不会截图或请求后端。'}</span>
    </div>
    {selection && <div className="pdf-selection-summary"><div><strong>当前选择</strong><span>{selectionSummary(selection)}</span></div>
      {selection.kind === 'text' && <p>{selection.text}</p>}
      {selection.kind === 'block' && <p>{selection.previewText}</p>}
      {selection.kind === 'area' && <code>{JSON.stringify(selection.region)}</code>}
      <div className="pdf-selection-actions"><button className="primary" type="button" disabled={processing} onClick={() => void processSelection()}>{processing ? '处理中…' : '处理当前选择'}</button><button type="button" disabled={processing} onClick={clearSelection}>清除</button></div>
      {result && <p className="pdf-selection-result">已接收：{result.kind}{result.imageWidth ? ` · ${result.imageWidth}×${result.imageHeight}` : ''}</p>}
    </div>}
    <Document file={contentUrl(document.id)} onLoadSuccess={handleDocumentLoad} onLoadError={error => onMessage(`PDF 加载失败：${error.message}`)}>
      <div className={`pdf-reader${outlineCollapsed ? ' outline-collapsed' : ''}`}><aside className="outline"><h3>目录</h3><Outline onItemClick={({ pageNumber }) => goToPage(pageNumber)} onLoadError={() => onMessage('该 PDF 未提供目录。')} /><p className="hint">在阅读区上下滚动可连续阅读全部页面。</p></aside>
        <div ref={pdfContentRef} className="pdf-content"><div className="pdf-toolbar"><span>共 {pageCount || '…'} 页{pdfLinks.length > 0 ? ` · ${pdfLinks.length} 个可预览内部链接` : ''}</span><button type="button" aria-expanded={!outlineCollapsed} onClick={() => setOutlineCollapsed(collapsed => !collapsed)}>{outlineCollapsed ? '展开目录' : '收起目录'}</button></div><div className="pdf-pages">{Array.from({ length: pageCount }, (_, index) => {
          const pageNumber = index + 1;
          const selectedArea = selection?.kind === 'area' && selection.pageNumber === pageNumber ? selection.region : undefined;
          return <div key={pageNumber} ref={node => { pageRefs.current[pageNumber] = node; }}><SelectablePdfPage document={document} pageNumber={pageNumber} renderWidth={pageRenderWidth} mode={mode} selectedArea={selectedArea}
            links={pdfLinks.filter(link => link.sourcePageNumber === pageNumber)}
            onMessage={onMessage} onSelection={replaceSelection} onPreviewLink={previewLink} onFollowLink={followLink} /></div>;
        })}</div></div>
      </div>
    </Document>
  </section>;
}
