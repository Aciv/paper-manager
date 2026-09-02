import { PointerEvent as ReactPointerEvent, useEffect, useRef, useState } from 'react';
import { Document, Outline, Page, pdfjs } from 'react-pdf';
import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';
import './PdfReader.css';
import { api, Block, contentUrl, Document as PaperDocument, DocumentSelectionResult } from './api';
import { LatestPDFSelection, NormalizedPDFRegion, SelectionGeneration, canvasCropForRegion, normalizedRegion, regionIsLargeEnough } from './pdfSelection';

pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type SelectionMode = 'text' | 'block' | 'area';

function SelectablePdfPage({ document, pageNumber, mode, selectedArea, onMessage, onSelection }: {
  document: PaperDocument;
  pageNumber: number;
  mode: SelectionMode;
  selectedArea?: NormalizedPDFRegion;
  onMessage(message: string): void;
  onSelection(selection: LatestPDFSelection): void;
}) {
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [loadError, setLoadError] = useState('');
  const [areaDraft, setAreaDraft] = useState<NormalizedPDFRegion>();
  const pageRef = useRef<HTMLDivElement>(null);
  const dragStart = useRef<{ pointerId: number; x: number; y: number } | undefined>(undefined);

  useEffect(() => {
    let active = true;
    setBlocks([]);
    setLoadError('');
    void api.listDocumentBlocks(document.id, pageNumber)
      .then(result => { if (active) setBlocks(result.items); })
      .catch(error => { if (active) setLoadError(error instanceof Error ? error.message : '无法读取解析块。'); });
    return () => { active = false; };
  }, [document.id, pageNumber]);

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
    <Page pageNumber={pageNumber} width={820} renderAnnotationLayer={false} />
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
  const [mode, setMode] = useState<SelectionMode>('text');
  const [selection, setSelection] = useState<LatestPDFSelection>();
  const [processing, setProcessing] = useState(false);
  const [result, setResult] = useState<DocumentSelectionResult>();
  const pageRefs = useRef<Record<number, HTMLDivElement | null>>({});
  const generations = useRef(new SelectionGeneration());
  const processController = useRef<AbortController | undefined>(undefined);

  useEffect(() => {
    generations.current.replace();
    processController.current?.abort();
    setSelection(undefined);
    setResult(undefined);
    setProcessing(false);
    return () => { generations.current.replace(); processController.current?.abort(); };
  }, [document.id]);

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
    <Document file={contentUrl(document.id)} onLoadSuccess={({ numPages }) => setPageCount(numPages)} onLoadError={error => onMessage(`PDF 加载失败：${error.message}`)}>
      <div className="pdf-reader"><aside className="outline"><h3>目录</h3><Outline onItemClick={({ pageNumber }) => goToPage(pageNumber)} onLoadError={() => onMessage('该 PDF 未提供目录。')} /><p className="hint">在阅读区上下滚动可连续阅读全部页面。</p></aside>
        <div className="pdf-content"><div className="pdf-toolbar"><span>共 {pageCount || '…'} 页</span></div><div className="pdf-pages">{Array.from({ length: pageCount }, (_, index) => {
          const pageNumber = index + 1;
          const selectedArea = selection?.kind === 'area' && selection.pageNumber === pageNumber ? selection.region : undefined;
          return <div key={pageNumber} ref={node => { pageRefs.current[pageNumber] = node; }}><SelectablePdfPage document={document} pageNumber={pageNumber} mode={mode} selectedArea={selectedArea} onMessage={onMessage} onSelection={replaceSelection} /></div>;
        })}</div></div>
      </div>
    </Document>
  </section>;
}
