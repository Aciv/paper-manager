import { useEffect, useMemo, useRef, useState } from 'react';

import { api, Document, DocumentBase, DocumentPage, PaperCitation, PaperMetadata } from './api';
import { PdfReader } from './PdfReader';
import './PaperLibraryPage.css';

function documentTitle(document: Document, metadata?: PaperMetadata) {
  return metadata?.title || document.originalFilename || '未命名论文';
}

function formatAuthors(authors: PaperMetadata['authors']) {
  return authors.map((author) => author.name).filter(Boolean).join('、') || '未识别';
}

export function PaperLibraryPage() {
  const [bases, setBases] = useState<DocumentBase[]>([]);
  const [baseID, setBaseID] = useState('');
  const [page, setPage] = useState<DocumentPage>({ items: [], page: 1, pageSize: 10, total: 0 });
  const [selected, setSelected] = useState<Document>();
  const [metadata, setMetadata] = useState<PaperMetadata>();
  const [citations, setCitations] = useState<PaperCitation[]>([]);
  const [citedBy, setCitedBy] = useState<PaperCitation[]>([]);
  const [sourceInfoVisible, setSourceInfoVisible] = useState(false);
  const [citationDialogOpen, setCitationDialogOpen] = useState(false);
  const [citationPaperInfoOpen, setCitationPaperInfoOpen] = useState(false);
  const [citationPaperInfo, setCitationPaperInfo] = useState<PaperMetadata>();
  const [citationPaperInfoLoading, setCitationPaperInfoLoading] = useState(false);
  const [citationPaperInfoError, setCitationPaperInfoError] = useState('');
  const [citationRawReference, setCitationRawReference] = useState('');
  const [citationRawReferenceVisible, setCitationRawReferenceVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [message, setMessage] = useState('正在加载论文库…');
  const citationInfoRequest = useRef(0);

  const pageCount = Math.max(1, Math.ceil(page.total / page.pageSize));
  const visibleItems = useMemo(() => page.items.filter((item) => item.status !== 'unknown'), [page.items]);
  const activeBase = bases.find((base) => base.id === baseID);

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

  function loadDocuments(nextPage = 1) {
    setLoading(true);
    setMessage('正在加载论文…');
    const request = baseID
      ? api.listDocumentBaseDocuments(baseID, nextPage, true)
      : api.searchDocuments({ page: nextPage, excludeUnknown: true });
    void request
      .then((result) => {
        setPage(result);
        setSelected((current) => result.items.find((item) => item.id === current?.id) ?? result.items[0]);
        setMessage(baseID ? `已加载 Base“${activeBase?.name ?? ''}”中的 ${result.total} 篇论文。` : `已加载全部 ${result.total} 篇本地论文。`);
      })
      .catch((error) => setMessage(error instanceof Error ? `加载论文失败：${error.message}` : '加载论文失败。'))
      .finally(() => setLoading(false));
  }

  useEffect(() => { loadDocuments(1); }, [baseID]);

  useEffect(() => {
    if (!selected) {
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
  }, [selected?.id]);

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

  return <main className="paper-library">
    <aside className="paper-library-sidebar">
      <header>
        <p className="eyebrow">Papers Manager</p>
        <h1>论文阅读库</h1>
        <a className="library-back-link" href="#">返回文档测试台</a>
      </header>

      <section className="library-collections" aria-label="论文范围">
        <h2>选择 Base</h2>
        <button className={!baseID ? 'active' : ''} type="button" onClick={() => setBaseID('')} disabled={loading}>全部本地论文</button>
        {bases.map((base) => <button className={baseID === base.id ? 'active' : ''} type="button" key={base.id} onClick={() => setBaseID(base.id)} disabled={loading}>{base.name}</button>)}
        {bases.length === 0 && <p className="hint">尚未创建 Document Base。</p>}
      </section>

      <section className="library-document-list" aria-label="论文列表">
        <div className="library-section-heading"><h2>{activeBase?.name ?? '全部论文'}</h2><span>{page.total} 篇</span></div>
        {visibleItems.length === 0 ? <p className="hint">此范围内没有本地论文。</p> : visibleItems.map((document) => <button className={selected?.id === document.id ? 'active' : ''} type="button" key={document.id} onClick={() => setSelected(document)}>
          <strong>{document.originalFilename}</strong>
          <span>{document.doi || '无 DOI'}</span>
        </button>)}
        <div className="library-pagination">
          <button type="button" onClick={() => loadDocuments(page.page - 1)} disabled={loading || page.page <= 1}>上一页</button>
          <span>{page.page} / {pageCount}</span>
          <button type="button" onClick={() => loadDocuments(page.page + 1)} disabled={loading || page.page >= pageCount}>下一页</button>
        </div>
      </section>
    </aside>

    <section className="paper-library-main">
      <header className="paper-library-header">
        <div>
          <p className="eyebrow">{activeBase ? `Base · ${activeBase.name}` : '全部本地论文'}</p>
          <h2>{selected ? documentTitle(selected, metadata) : '请选择一篇论文'}</h2>
        </div>
        <div className="paper-library-tabs" role="toolbar" aria-label="论文信息操作">
          <button className={sourceInfoVisible ? 'active' : ''} type="button" onClick={() => setSourceInfoVisible((visible) => !visible)} disabled={!selected}>{sourceInfoVisible ? '收起源信息' : '查看源信息'}</button>
          <button type="button" onClick={() => setCitationDialogOpen(true)} disabled={!selected}>引用信息查看</button>
        </div>
      </header>

      <p className="library-message">{message}</p>
      {!selected ? <div className="library-empty"><p>选择左侧论文后，可阅读 PDF、查看源信息及参考文献原文。</p></div> : <>
        {sourceInfoVisible && <section className="paper-source-info" aria-label="论文源信息">
          <dl>
            <dt>标题</dt><dd>{documentTitle(selected, metadata)}</dd>
            <dt>作者</dt><dd>{metadata ? formatAuthors(metadata.authors) : '加载中或未识别'}</dd>
            <dt>DOI</dt><dd>{selected.doi || '未设置'}</dd>
            <dt>来源</dt><dd>{metadata?.sourceName || '未设置'}{metadata?.sourceUrl && <> · <a href={metadata.sourceUrl} target="_blank" rel="noreferrer">打开来源</a></>}</dd>
            <dt>发表信息</dt><dd>{[metadata?.venue, metadata?.publishedAt?.slice(0, 10), metadata?.volume, metadata?.issue, metadata?.pages].filter(Boolean).join(' · ') || '未设置'}</dd>
            <dt>摘要</dt><dd>{metadata?.abstract || '未提取摘要。'}</dd>
          </dl>
        </section>}
        {selected.contentType === 'application/pdf'
          ? <PdfReader document={selected} onMessage={setMessage} />
          : <div className="library-empty"><p>该论文不是 PDF，无法在阅读库中预览。</p></div>}
      </>}
    </section>

    {citationDialogOpen && selected && <div className="citation-dialog-backdrop" role="presentation" onMouseDown={() => setCitationDialogOpen(false)}>
      <section className="citation-dialog" role="dialog" aria-modal="true" aria-labelledby="citation-dialog-title" onMouseDown={(event) => event.stopPropagation()}>
        <header><div><p className="eyebrow">引用论文列表</p><h2 id="citation-dialog-title">{documentTitle(selected, metadata)}</h2></div><button type="button" onClick={() => setCitationDialogOpen(false)}>关闭</button></header>
        <div className="citation-dialog-content">
          <section><h3>本论文引用 · {citations.length}</h3>{citations.length === 0 ? <p className="hint">没有已提取的参考文献。</p> : <ol>{citations.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><button className="citation-info-button" type="button" onClick={() => openCitationPaperInfo(citation.citedDocumentId, citation.rawReference)}>查看第 {index + 1} 条引用的论文信息</button></li>)}</ol>}</section>
          <section><h3>库内引用本论文 · {citedBy.length}</h3>{citedBy.length === 0 ? <p className="hint">暂无库内论文引用它。</p> : <ol>{citedBy.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><button className="citation-info-button" type="button" onClick={() => openCitationPaperInfo(citation.citingDocumentId, citation.rawReference)}>查看第 {index + 1} 条引用的论文信息</button></li>)}</ol>}</section>
        </div>
      </section>
    </div>}

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
