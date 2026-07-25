import { useEffect, useRef, useState } from 'react';
import { Document, Outline, Page, pdfjs } from 'react-pdf';
import 'react-pdf/dist/Page/AnnotationLayer.css';
import 'react-pdf/dist/Page/TextLayer.css';
import { api, Block, contentUrl, Document as PaperDocument } from './api';

pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type SelectionMode = 'text' | 'block';

function SelectablePdfPage({
  document,
  pageNumber,
  mode,
  onMessage,
  onBlockSelected,
}: {
  document: PaperDocument;
  pageNumber: number;
  mode: SelectionMode;
  onMessage(message: string): void;
  onBlockSelected(block: Block): void;
}) {
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [loadError, setLoadError] = useState('');

  useEffect(() => {
    let active = true;
    setBlocks([]);
    setLoadError('');
    void api.listDocumentBlocks(document.id, pageNumber)
      .then((result) => { if (active) setBlocks(result.items); })
      .catch((error) => {
        if (!active) return;
        const message = error instanceof Error ? error.message : '无法读取解析块。';
        setLoadError(message);
      });
    return () => { active = false; };
  }, [document.id, pageNumber]);

  function reportSelectedText() {
    if (mode !== 'text') return;
    const selectedText = window.getSelection()?.toString().trim();
    if (selectedText) onMessage(`已选择文本：${selectedText.slice(0, 160)}${selectedText.length > 160 ? '…' : ''}`);
  }

  return <div className="pdf-page" data-page-number={pageNumber} onMouseUp={reportSelectedText}>
    <Page pageNumber={pageNumber} width={820} renderAnnotationLayer={false} />
    <div className={`block-overlay ${mode === 'block' ? 'active' : ''}`} aria-hidden={mode !== 'block'}>
      {blocks.map((block) => {
        const location = block.location;
        if (!location || !location.pageWidth || !location.pageHeight ||
          location.bboxX0 === undefined || location.bboxY0 === undefined ||
          location.bboxX1 === undefined || location.bboxY1 === undefined) return null;
        const left = `${(location.bboxX0 / location.pageWidth) * 100}%`;
        const top = `${(location.bboxY0 / location.pageHeight) * 100}%`;
        const width = `${((location.bboxX1 - location.bboxX0) / location.pageWidth) * 100}%`;
        const height = `${((location.bboxY1 - location.bboxY0) / location.pageHeight) * 100}%`;
        return <button
          key={block.id}
          className={`pdf-block pdf-block-${block.type}`}
          style={{ left, top, width, height }}
          type="button"
          title={`${block.type}: ${block.text.slice(0, 240)}`}
          onClick={() => onBlockSelected(block)}
        ><span>{block.type}</span></button>;
      })}
    </div>
    {loadError && <p className="pdf-block-error">解析块未加载：{loadError}</p>}
  </div>;
}

export function PdfReader({ document, onMessage }: { document: PaperDocument; onMessage(message: string): void }) {
  const [pageCount, setPageCount] = useState(0);
  const [mode, setMode] = useState<SelectionMode>('text');
  const [selectedBlock, setSelectedBlock] = useState<Block>();
  const pageRefs = useRef<Record<number, HTMLDivElement | null>>({});

  function goToPage(pageNumber: number) {
    pageRefs.current[pageNumber]?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  function onBlockSelected(block: Block) {
    setSelectedBlock(block);
    const page = block.location?.pageNumber ?? '?';
    onMessage(`已选择第 ${page} 页 ${block.type} Block：${block.text.slice(0, 120)}${block.text.length > 120 ? '…' : ''}`);
  }

  return <section className="pdf-viewer-panel">
    <div className="pdf-reader-controls">
      <strong>PDF 阅读与定位</strong>
      <div className="pdf-mode-switch" role="group" aria-label="选择模式">
        <button className={mode === 'text' ? 'active' : ''} type="button" onClick={() => setMode('text')}>选择文本</button>
        <button className={mode === 'block' ? 'active' : ''} type="button" onClick={() => setMode('block')}>选择解析 Block</button>
      </div>
      <span className="hint">{mode === 'text' ? '拖动 PDF 文本层可复制选中文字。' : '点击高亮区域选择解析块。'}</span>
    </div>
    {selectedBlock && <div className="selected-block-summary">
      <strong>已选 Block · {selectedBlock.type}</strong>
      <span>第 {selectedBlock.location?.pageNumber} 页</span>
      <p>{selectedBlock.text}</p>
    </div>}
    <Document file={contentUrl(document.id)} onLoadSuccess={({ numPages }) => setPageCount(numPages)} onLoadError={error => onMessage(`PDF 加载失败：${error.message}`)}>
      <div className="pdf-reader"><aside className="outline"><h3>目录</h3><Outline onItemClick={({ pageNumber }) => goToPage(pageNumber)} onLoadError={() => onMessage('该 PDF 未提供目录。')} /><p className="hint">在阅读区上下滚动可连续阅读全部页面。</p></aside>
        <div className="pdf-content"><div className="pdf-toolbar"><span>共 {pageCount || '…'} 页</span></div>
          <div className="pdf-pages">{Array.from({ length: pageCount }, (_, index) => { const pageNumber = index + 1; return <div key={pageNumber} ref={node => { pageRefs.current[pageNumber] = node; }}><SelectablePdfPage document={document} pageNumber={pageNumber} mode={mode} onMessage={onMessage} onBlockSelected={onBlockSelected} /></div>; })}</div>
        </div>
      </div>
    </Document>
  </section>;
}
