import { ChangeEvent, useEffect, useMemo, useRef, useState } from 'react';

import { api, Document, DocumentBase, DocumentPage, PaperCitation, PaperMetadata } from './api';
import { PdfReader } from './PdfReader';
import './PaperLibraryPage.css';

function documentTitle(document: Document, metadata?: PaperMetadata) {
  return metadata?.title || document.originalFilename || '未命名论文';
}

function formatAuthors(authors: PaperMetadata['authors']) {
  return authors.map((author) => author.name).filter(Boolean).join('、') || '未识别';
}

function citationLabel(citation: PaperCitation, index: number) {
  const reference = citation.rawReference?.replace(/^[\s\-–—•·]+/, '').trim();
  return reference || `第 ${index + 1} 条引用`;
}

// A picked folder reports each file's path relative to it; the plain name is the
// fallback when a browser leaves `webkitRelativePath` empty.
function localPath(file: File): string {
  return (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
}

const LEFT_MENUS = ['catalog', 'base', 'outline'] as const;
const RIGHT_MENUS = ['info', 'citations'] as const;
type LeftMenu = typeof LEFT_MENUS[number];
type RightMenu = typeof RIGHT_MENUS[number];

const LEFT_MENU_LABELS: Record<LeftMenu, string> = { catalog: '论文目录', base: '类别', outline: 'PDF 目录' };
const RIGHT_MENU_LABELS: Record<RightMenu, string> = { info: '论文信息', citations: '引用信息' };

export function PaperLibraryPage() {
  const [leftCollapsed, setLeftCollapsed] = useState(false);
  const [rightCollapsed, setRightCollapsed] = useState(false);
  // Side column widths: draggable via the handle on each column's inner edge,
  // remembered across reloads.
  const [leftWidth, setLeftWidth] = useState(() => Math.max(180, Number(window.localStorage.getItem('library.leftWidth')) || 290));
  const [rightWidth, setRightWidth] = useState(() => Math.max(200, Number(window.localStorage.getItem('library.rightWidth')) || 320));
  const [resizing, setResizing] = useState(false);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  // Phones: touch input has no hover, so the columns become overlay sheets and
  // the PDF keeps the full width.
  const [narrow, setNarrow] = useState(() => window.matchMedia('(max-width: 820px)').matches);
  useEffect(() => {
    const query = window.matchMedia('(max-width: 820px)');
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
  const [baseID, setBaseID] = useState('');
  // Local papers are PDFs the user opens straight from a folder: they never
  // reach the backend, so their scope is tracked apart from the stored ones.
  const [localMode, setLocalMode] = useState(false);
  const [localFiles, setLocalFiles] = useState<File[]>([]);
  const [localSelected, setLocalSelected] = useState<File | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const [page, setPage] = useState<DocumentPage>({ items: [], page: 1, pageSize: 10, total: 0 });
  const [selected, setSelected] = useState<Document>();
  const [metadata, setMetadata] = useState<PaperMetadata>();
  const [citations, setCitations] = useState<PaperCitation[]>([]);
  const [citedBy, setCitedBy] = useState<PaperCitation[]>([]);
  const [citationPaperInfoOpen, setCitationPaperInfoOpen] = useState(false);
  const [citationPaperInfo, setCitationPaperInfo] = useState<PaperMetadata>();
  const [citationPaperInfoLoading, setCitationPaperInfoLoading] = useState(false);
  const [citationPaperInfoError, setCitationPaperInfoError] = useState('');
  const [citationRawReference, setCitationRawReference] = useState('');
  const [citationRawReferenceVisible, setCitationRawReferenceVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  // Only shown when there is something to report (errors, processing results);
  // routine progress stays off screen.
  const [message, setMessage] = useState('');
  const [titles, setTitles] = useState<Record<string, string>>({});
  const citationInfoRequest = useRef(0);
  const titleCache = useRef(new Map<string, string>());
  const resizeDrag = useRef<{ side: 'left' | 'right'; startX: number; startWidth: number } | null>(null);
  // Floating edge button for a collapsed column: appears only while the pointer
  // is near that edge, pinned at the position of the first approach; clicking it
  // expands the column. Collapsing itself uses the menu-bar button.
  const [leftHandle, setLeftHandle] = useState<{ x: number; y: number } | null>(null);
  const [rightHandle, setRightHandle] = useState<{ x: number; y: number } | null>(null);
  // After a click the handle goes dark until the pointer leaves the edge band.
  const handleSuppressed = useRef({ left: false, right: false });

  // Column widths: the user's drag is honoured while there is room, capped to a
  // share of the viewport, and never squeezing the reading area below a minimum.
  const MIN_READER_WIDTH = 360;
  const MIN_COLUMN_WIDTH = 180;
  const columnCap = Math.max(MIN_COLUMN_WIDTH, Math.round(viewportWidth * 0.45));
  const widthMax = (side: 'left' | 'right') => {
    const other = side === 'left' ? (leftCollapsed ? 0 : rightWidth) : (rightCollapsed ? 0 : leftWidth);
    return Math.max(MIN_COLUMN_WIDTH, Math.min(columnCap, viewportWidth - other - MIN_READER_WIDTH));
  };
  const effectiveLeftWidth = Math.max(MIN_COLUMN_WIDTH, Math.min(leftWidth, widthMax('left')));
  const effectiveRightWidth = Math.max(MIN_COLUMN_WIDTH, Math.min(rightWidth, widthMax('right')));

  const pageCount = Math.max(1, Math.ceil(page.total / page.pageSize));
  const visibleItems = useMemo(() => page.items.filter((item) => item.sourceState === 'available'), [page.items]);
  const activeBase = bases.find((base) => base.id === baseID);
  const selectedIsPDF = selected?.contentType === 'application/pdf';
  const localFolder = useMemo(() => {
    // A picked folder names each file "folder/sub/name.pdf"; without that the
    // scope just stays anonymous.
    const path = localFiles.length > 0 ? localPath(localFiles[0]) : '';
    return path.includes('/') ? path.split('/')[0] : '本地文件';
  }, [localFiles]);
  const outlineReady = localMode ? Boolean(localSelected) : selectedIsPDF;

  // The catalog shows paper titles; the search endpoint only returns files, so
  // titles come from each paper's metadata and are cached across pages.
  useEffect(() => {
    const missing = visibleItems.filter((item) => !titleCache.current.has(item.id));
    if (missing.length === 0) return;
    let active = true;
    void Promise.allSettled(missing.map((item) => api.getPaperMetadata(item.id))).then(results => {
      if (!active) return;
      const next: Record<string, string> = {};
      results.forEach((result, index) => {
        const title = result.status === 'fulfilled' ? result.value.paperMetadata?.title?.trim() : '';
        if (!title) return;
        titleCache.current.set(missing[index].id, title);
        next[missing[index].id] = title;
      });
      if (Object.keys(next).length > 0) setTitles(current => ({ ...current, ...next }));
    });
    return () => { active = false; };
  }, [visibleItems]);


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
      const next = Math.round(Math.min(max, Math.max(MIN_COLUMN_WIDTH, drag.startWidth + delta)));
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

  function loadDocuments(nextPage = 1) {
    if (localMode) return; // the local scope lists files from disk, not the API
    setLoading(true);
    const request = baseID
      ? api.listDocumentBaseDocuments(baseID, nextPage, true)
      : api.searchDocuments({ page: nextPage, excludeUnknown: true });
    void request
      .then((result) => {
        setPage(result);
        setSelected((current) => result.items.find((item) => item.id === current?.id) ?? result.items[0]);
      })
      .catch((error) => setMessage(error instanceof Error ? `加载论文失败：${error.message}` : '加载论文失败。'))
      .finally(() => setLoading(false));
  }

  useEffect(() => { loadDocuments(1); }, [baseID, localMode]);

  useEffect(() => {
    // The local scope shows files that have no backend record at all.
    if (!selected || localMode) {
      setMetadata(undefined);
      setCitations([]);
      setCitedBy([]);
      return;
    }
    let active = true;
    setMetadata(undefined);
    setCitations([]);
    setCitedBy([]);
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
    });
    return () => { active = false; };
  }, [selected?.id, localMode]);

  function openCitationPaperInfo(documentID: string, rawReference: string) {
    const requestID = citationInfoRequest.current + 1;
    citationInfoRequest.current = requestID;
    setCitationPaperInfoOpen(true);
    setCitationPaperInfo(undefined);
    setCitationPaperInfoError('');
    setCitationPaperInfoLoading(true);
    setCitationRawReference(rawReference.trim());
    setCitationRawReferenceVisible(false);
    void api.getPaperMetadata(documentID)
      .then((result) => {
        if (citationInfoRequest.current === requestID) setCitationPaperInfo(result.paperMetadata);
      })
      .catch((error) => {
        if (citationInfoRequest.current !== requestID) return;
        setCitationPaperInfoError(error instanceof Error ? error.message : '无法读取引用论文信息。');
      })
      .finally(() => {
        if (citationInfoRequest.current === requestID) setCitationPaperInfoLoading(false);
      });
  }

  const collapsedClass = `${leftCollapsed ? ' left-collapsed' : ''}${rightCollapsed ? ' right-collapsed' : ''}`;

  const leftColumnBody = <>
    <div className="library-resizer" role="separator" aria-orientation="vertical" title="拖动调整左栏宽度"
      onPointerDown={event => startResize('left', event)} />
    <div className="library-menu-bar">
      <a className="library-back-link" href="#">返回测试台</a>
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
          <span>{localMode ? localFiles.length : page.total} 篇</span>
        </p>
        <div className="library-document-list">
          {localMode
            ? <>
              <button className="library-folder-open" type="button" onClick={() => fileInput.current?.click()}>选择 PDF 文件…</button>
              <button className="library-folder-open" type="button" title="加载这个文件夹里（含子文件夹）的全部 PDF，不逐个挑选" onClick={() => folderInput.current?.click()}>{localFiles.length > 0 ? '换成整个文件夹…' : '选择整个文件夹…'}</button>
              {localFiles.length === 0
                ? <p className="hint">「选择 PDF 文件…」里可以进到任意文件夹，看到并多选其中的 PDF（Ctrl+A 全选）；全部只在浏览器里打开，不会写入后端。</p>
                : localFiles.map((item, index) => <button className={localSelected === item ? 'active' : ''} type="button" key={`${localPath(item)}-${index}`} title={localPath(item)} onClick={() => { setLocalSelected(item); if (narrow) setLeftCollapsed(true); }}>
                  <strong>{item.name}</strong>
                </button>)}
            </>
            : visibleItems.length === 0
              ? <p className="hint">此范围内没有论文。</p>
              : visibleItems.map((document) => <button className={selected?.id === document.id ? 'active' : ''} type="button" key={document.id} title={document.originalFilename} onClick={() => { setSelected(document); if (narrow) setLeftCollapsed(true); }}>
                <strong>{titles[document.id] ?? document.originalFilename}</strong>
              </button>)}
        </div>
        {!localMode && <div className="library-pagination">
          <button type="button" onClick={() => loadDocuments(page.page - 1)} disabled={loading || page.page <= 1} title="上一页">‹</button>
          <span>{page.page} / {pageCount}</span>
          <button type="button" onClick={() => loadDocuments(page.page + 1)} disabled={loading || page.page >= pageCount} title="下一页">›</button>
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
              <dt>篇数</dt><dd>{page.total}</dd>
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
    <div className="library-menu-bar library-menu-bar-end">
      <button className="library-collapse" type="button" onClick={() => setRightCollapsed(true)} aria-expanded="true" aria-controls="library-right-column" title="收起右栏">»</button>
    </div>
    <nav className="library-menu" role="tablist" aria-label="右侧功能菜单">
      {RIGHT_MENUS.map((menu) => <button className={rightMenu === menu ? 'active' : ''} type="button" role="tab" aria-selected={rightMenu === menu} key={menu} onClick={() => setRightMenu(menu)}>{RIGHT_MENU_LABELS[menu]}</button>)}
    </nav>
    <div className="library-detail">
      {rightMenu === 'info' && <section className="library-panel library-panel-info" aria-label="论文信息">
        {localMode ? <p className="hint">本地论文没有关联信息。</p> : selected ? <dl className="library-info-list">
          <dt>标题</dt><dd>{documentTitle(selected, metadata)}</dd>
          <dt>作者</dt><dd>{metadata ? formatAuthors(metadata.authors) : '加载中或未识别'}</dd>
          <dt>DOI</dt><dd>{selected.doi || '未设置'}</dd>
          <dt>来源</dt><dd>{metadata?.sourceName || '未设置'}{metadata?.sourceUrl && <> · <a href={metadata.sourceUrl} target="_blank" rel="noreferrer">打开来源</a></>}</dd>
          <dt>发表信息</dt><dd>{[metadata?.venue, metadata?.publishedAt?.slice(0, 10), metadata?.volume, metadata?.issue, metadata?.pages].filter(Boolean).join(' · ') || '未设置'}</dd>
        </dl> : <p className="hint">选择论文后显示论文信息。</p>}
      </section>}
      {rightMenu === 'citations' && <section className="library-panel library-panel-citations" aria-label="引用信息">
        {localMode ? <p className="hint">本地论文没有引用信息。</p> : <>
        <div className="library-citation-group">
          <h3>本论文引用 <span className="library-count">{citations.length}</span></h3>
          {citations.length === 0 ? <p className="hint">没有已提取的参考文献。</p> : <ol>
            {citations.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}>
              <button className="library-citation-item" type="button" onClick={() => openCitationPaperInfo(citation.citedDocumentId, citation.rawReference)}>
                <span className="library-citation-index">{index + 1}</span>
                <span className="library-citation-text">{citationLabel(citation, index)}</span>
              </button>
            </li>)}
          </ol>}
        </div>
        <div className="library-citation-group">
          <h3>库内引用本论文 <span className="library-count">{citedBy.length}</span></h3>
          {citedBy.length === 0 ? <p className="hint">暂无库内论文引用它。</p> : <ol>
            {citedBy.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}>
              <button className="library-citation-item" type="button" onClick={() => openCitationPaperInfo(citation.citingDocumentId, citation.rawReference)}>
                <span className="library-citation-index">{index + 1}</span>
                <span className="library-citation-text">{citationLabel(citation, index)}</span>
              </button>
            </li>)}
          </ol>}
        </div>
        </>}
      </section>}
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
      title="展开右栏（论文信息与引用）" aria-label="展开右栏">
      <span aria-hidden="true">«</span>
    </button>}

    <aside className={`library-column library-column-left${leftCollapsed ? ' collapsed' : ''}`} id="library-left-column">
      {leftCollapsed ? null : leftColumnBody}
    </aside>

    <section className="paper-library-main">
      {localMode
        ? localSelected
          ? <PdfReader file={localSelected} onMessage={setMessage} outlinePlacement="external" outlineTarget={leftMenu === 'outline' ? outlineSlot : null} modeRail />
          : <div className="library-empty"><p>在左侧「本地论文」里打开一个文件夹，里面的 PDF 会列在那里。</p></div>
        : !selected
          ? <div className="library-empty"><p>从左侧论文目录选择一篇论文开始阅读。</p></div>
          : selectedIsPDF
            ? <PdfReader document={selected} onMessage={setMessage} outlinePlacement="external" outlineTarget={leftMenu === 'outline' ? outlineSlot : null} modeRail />
            : <div className="library-empty"><p>该论文不是 PDF，无法在阅读库中预览。</p></div>}
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
        {!citationPaperInfoLoading && citationPaperInfoError && <p className="detail-error">读取失败：{citationPaperInfoError}</p>}
        {!citationPaperInfoLoading && !citationPaperInfoError && citationPaperInfo && <dl>
          <dt>标题</dt><dd>{citationPaperInfo.title || '未提取标题'}</dd>
          <dt>作者</dt><dd>{formatAuthors(citationPaperInfo.authors)}</dd>
          <dt>发表日期</dt><dd>{citationPaperInfo.publishedAt?.slice(0, 10) || '未提取发表日期'}</dd>
        </dl>}
      </section>
    </div>}
  </main>;
}
