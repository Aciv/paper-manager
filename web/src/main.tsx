import { ChangeEvent, FormEvent, useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { api, Document, DocumentBase, DocumentPage, DocumentProcessingView, DocumentSearch, PaperCitation, PaperMetadata, ProcessingSpan, ProcessingSpanStatus } from './api';
import { PdfReader } from './PdfReader';
import './PaperInfo.css';
import './styles.css';

const acceptedFiles = '.txt,.json,.md,.markdown,.csv,.pdf,.doc,.docx,.xls,.xlsx,.ppt,.pptx';

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function formatDate(value: string) {
  return new Date(value).toLocaleString();
}

function documentLabel(document: Document, paper?: PaperMetadata) {
  return paper?.title || document.originalFilename;
}

function parseTags(value: string) {
  return value.split(',').map((tag) => tag.trim()).filter(Boolean);
}

function processingStateLabel(state: ProcessingSpanStatus) {
  const labels: Record<ProcessingSpanStatus, string> = { queued: '排队中', running: '运行中', succeeded: '已完成', failed: '失败', cancelled: '已取消' };
  return labels[state];
}

function processingKindLabel(kind: string) {
  const labels: Record<string, string> = {
    pipeline: '处理流程',
    recognition: '文档识别',
    metadata_extraction: '元数据提取',
    catalog_source: '源论文查询',
    catalog_reference: '引用论文查询',
    catalog_provider: '论文库提供方查询',
  };
  return labels[kind] ?? kind;
}

function isActiveSpan(stage: ProcessingSpan) {
  return stage.status === 'queued' || stage.status === 'running';
}

function App() {
  const [filters, setFilters] = useState<DocumentSearch>({ page: 1 });
  const [page, setPage] = useState<DocumentPage>({ items: [], page: 1, pageSize: 10, total: 0 });
  const [selected, setSelected] = useState<Document>();
  const [file, setFile] = useState<File>();
  const [doi, setDoi] = useState('');
  const [title, setTitle] = useState('');
  const [tags, setTags] = useState('');
  const [editOriginalFilename, setEditOriginalFilename] = useState('');
  const [editDoi, setEditDoi] = useState('');
  const [editTitle, setEditTitle] = useState('');
  const [editTags, setEditTags] = useState('');
  const [paperMetadata, setPaperMetadata] = useState<PaperMetadata>();
  const [processing, setProcessing] = useState<DocumentProcessingView>();
  const [citations, setCitations] = useState<PaperCitation[]>([]);
  const [citedBy, setCitedBy] = useState<PaperCitation[]>([]);
  const [paperSearchTitle, setPaperSearchTitle] = useState('');
  const [paperSearchAuthor, setPaperSearchAuthor] = useState('');
  const [paperSearchTags, setPaperSearchTags] = useState('');
  const [paperTagMode, setPaperTagMode] = useState<'all' | 'any'>('all');
  const [bases, setBases] = useState<DocumentBase[]>([]);
  const [baseName, setBaseName] = useState('');
  const [baseDescription, setBaseDescription] = useState('');
  const [selectedBaseID, setSelectedBaseID] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
	const processingPollDelay = useRef(2500);

  const pageCount = Math.max(1, Math.ceil(page.total / page.pageSize));
  const hasPreviousPage = page.page > 1;
  const hasNextPage = page.page < pageCount;
  const parseStages = processing?.stages.filter((stage) => stage.kind === 'recognition') ?? [];
  const pipelineStages = processing?.stages.filter((stage) => stage.kind === 'pipeline') ?? [];
  const paperInfoStages = processing?.stages.filter((stage) => stage.kind !== 'recognition' && stage.kind !== 'pipeline') ?? [];
  const paperInfoActive = paperInfoStages.some(isActiveSpan);
  const pipelineActive = pipelineStages.some(isActiveSpan);
  const documentStatus = processing?.documentStatus ?? selected?.status ?? 'uploaded';

  function applyProcessingView(view: DocumentProcessingView) {
    setProcessing(view);
    setSelected((prev) => (prev ? { ...prev, status: view.documentStatus } : prev));
  }

  async function load(nextFilters = filters) {
    setBusy(true);
    try {
      const result = await api.searchDocuments(nextFilters);
      setFilters({ ...nextFilters, page: result.page });
      setPage(result);
      setSelected((current) => result.items.find((item) => item.id === current?.id) ?? current);
      setMessage(`已加载第 ${result.page} 页，共 ${result.total} 个文档。`);
    } catch (error) {
      setMessage(error instanceof Error ? `查询失败：${error.message}` : '查询失败。');
    } finally {
      setBusy(false);
    }
  }

  async function loadBases() {
    try {
      const result = await api.listDocumentBases();
      setBases(result.items);
      setSelectedBaseID((current) => result.items.some((base) => base.id === current) ? current : (result.items[0]?.id ?? ''));
    } catch (error) {
      setMessage(error instanceof Error ? `加载文档 Base 失败：${error.message}` : '加载文档 Base 失败。');
    }
  }

  useEffect(() => { void load({ page: 1 }); void loadBases(); }, []);
  useEffect(() => {
    setEditOriginalFilename(selected?.originalFilename ?? '');
    setEditDoi(selected?.doi ?? '');
    setPaperMetadata(undefined);
    setProcessing(undefined);
    setCitations([]);
    setCitedBy([]);
		processingPollDelay.current = 2500;
    if (!selected) {
      setEditTitle('');
      setEditTags('');
      return;
    }
    void api.getPaperMetadata(selected.id).then(({ paperMetadata: metadata }) => {
      setPaperMetadata(metadata);
      setEditTitle(metadata.title ?? '');
      setEditTags(metadata.tags.join(', '));
    }).catch((error) => setMessage(error instanceof Error ? `加载论文信息失败：${error.message}` : '加载论文信息失败。'));
    void Promise.all([
      api.getDocumentProcessing(selected.id),
      api.listPaperCitations(selected.id),
      api.listPaperCitedBy(selected.id),
    ]).then(([{ processing: view }, { items: outgoing }, { items: incoming }]) => {
      applyProcessingView(view);
      setCitations(outgoing);
      setCitedBy(incoming);
    }).catch((error) => setMessage(error instanceof Error ? `加载论文任务或引用失败：${error.message}` : '加载论文任务或引用失败。'));
  }, [selected?.id]);

  useEffect(() => {
    if (!selected || !processing?.stages.some((stage) => stage.status === 'queued' || stage.status === 'running')) return;
		const visible = document.visibilityState === 'visible';
		const delay = visible ? processingPollDelay.current : Math.max(processingPollDelay.current, 8000);
		const timer = window.setTimeout(() => {
			void api.getDocumentProcessing(selected.id).then(({ processing: view }) => {
				processingPollDelay.current = Math.min(
					visible ? processingPollDelay.current * 1.5 : processingPollDelay.current * 2,
					visible ? 15000 : 60000,
				);
				applyProcessingView(view);
			}).catch(() => {
				processingPollDelay.current = Math.min(processingPollDelay.current * 2, visible ? 15000 : 60000);
			});
		}, delay);
		return () => window.clearTimeout(timer);
  }, [selected?.id, processing?.stages]);

  // Poll document list periodically while any document is in a transient state,
  // so status badges in the list stay current without a manual refresh.
  useEffect(() => {
    const hasActive = page.items.some((item) => ['uploaded', 'queued', 'processing'].includes(item.status));
    if (!hasActive) return;
    const timer = window.setTimeout(() => {
      api.searchDocuments(filters)
        .then((result) => {
          setPage(result);
          setSelected((current) => result.items.find((item) => item.id === current?.id) ?? current);
        })
        .catch(() => {});
    }, 10000);
    return () => window.clearTimeout(timer);
  }, [page.items, filters]);

  async function submitSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await load({ ...filters, page: 1 });
  }

  async function submitPaperSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    try {
      const result = await api.searchPaperMetadata({ title: paperSearchTitle.trim(), author: paperSearchAuthor.trim(), tags: parseTags(paperSearchTags), tagMode: paperTagMode, page: 1 });
      const documents = (await Promise.all(result.items.map(async (item) => {
        const page = await api.searchDocuments({ id: item.documentId, page: 1 });
        return page.items[0];
      }))).filter((item): item is Document => Boolean(item));
      setPage({ items: documents, page: result.page, pageSize: result.pageSize, total: result.total });
      const first = documents[0];
      setSelected(first);
      setPaperMetadata(first ? result.items.find((item) => item.documentId === first.id) : undefined);
      setMessage(`论文检索完成：共 ${result.total} 条匹配。`);
    } catch (error) {
      setMessage(error instanceof Error ? `论文检索失败：${error.message}` : '论文检索失败。');
    } finally {
      setBusy(false);
    }
  }

  async function submitUpload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!file) {
      setMessage('请先选择文件。');
      return;
    }
    setBusy(true);
    setMessage(`正在上传 ${file.name}…`);
    try {
      const { document: uploadedDocument } = await api.upload(file, doi.trim());
      let uploadedPaper: PaperMetadata | undefined;
      if (title.trim() || parseTags(tags).length > 0) {
        ({ paperMetadata: uploadedPaper } = await api.updatePaperMetadata(uploadedDocument.id, { title: title.trim(), tags: parseTags(tags) }));
      }
      setSelected(uploadedDocument);
      setPaperMetadata(uploadedPaper);
      setFile(undefined);
      setDoi('');
      setTitle('');
      setTags('');
      const input = window.document.getElementById('document-file') as HTMLInputElement | null;
      if (input) input.value = '';
      setMessage(`上传成功：${documentLabel(uploadedDocument, uploadedPaper)}（${uploadedDocument.id}）。`);
      await load({ page: 1 });
    } catch (error) {
      setMessage(error instanceof Error ? `上传失败：${error.message}` : '上传失败。');
    } finally {
      setBusy(false);
    }
  }

  async function deleteSelected() {
    if (!selected || !window.confirm(`删除“${documentLabel(selected, paperMetadata)}”？此操作会删除原始文件和关联数据。`)) return;
    setBusy(true);
    try {
      await api.deleteDocument(selected.id);
      setMessage(`已删除：${documentLabel(selected, paperMetadata)}。`);
      setSelected(undefined);
      await load({ ...filters, page: page.items.length === 1 && page.page > 1 ? page.page - 1 : page.page });
    } catch (error) {
      setMessage(error instanceof Error ? `删除失败：${error.message}` : '删除失败。');
    } finally {
      setBusy(false);
    }
  }

  async function cancelSelected() {
    if (!selected || !window.confirm(`取消“${documentLabel(selected, paperMetadata)}”的解析任务？`)) return;
    setBusy(true);
    try {
      const { document: cancelled } = await api.cancelDocument(selected.id);
      setSelected(cancelled);
      setMessage(`已取消解析：${documentLabel(cancelled, paperMetadata)}。`);
      await load(filters);
    } catch (error) {
      setMessage(error instanceof Error ? `取消失败：${error.message}` : '取消失败。');
    } finally {
      setBusy(false);
    }
  }

  async function retrySelected() {
    if (!selected || !window.confirm(`重新开始“${documentLabel(selected, paperMetadata)}”的解析？`)) return;
    setBusy(true);
		processingPollDelay.current = 2500;
    try {
      const { document: queued } = await api.retryDocument(selected.id);
      setSelected(queued);
      setMessage(`已提交重新解析：${documentLabel(queued, paperMetadata)}。`);
      await load(filters);
    } catch (error) {
      setMessage(error instanceof Error ? `重新解析失败：${error.message}` : '重新解析失败。');
    } finally {
      setBusy(false);
    }
  }

  async function submitMetadataUpdate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    setBusy(true);
    try {
      const documentChanged = editOriginalFilename !== selected.originalFilename || editDoi !== (selected.doi ?? '');
      const paperChanged = editTitle !== (paperMetadata?.title ?? '') || editTags !== (paperMetadata?.tags.join(', ') ?? '');
      const { document: updated } = documentChanged
        ? await api.updateDocument(selected.id, { originalFilename: editOriginalFilename, doi: editDoi })
        : { document: selected };
      let updatedPaper = paperMetadata;
      if (paperChanged) {
        ({ paperMetadata: updatedPaper } = await api.updatePaperMetadata(selected.id, { title: editTitle, tags: parseTags(editTags) }));
      }
      setSelected(updated);
      setPaperMetadata(updatedPaper);
      setMessage(`文档信息已更新：${documentLabel(updated, updatedPaper)}。`);
      await load(filters);
    } catch (error) {
      setMessage(error instanceof Error ? `更新失败：${error.message}` : '更新失败。');
    } finally {
      setBusy(false);
    }
  }

  async function retryPaperInfo() {
    if (!selected) return;
    setBusy(true);
		processingPollDelay.current = 2500;
    try {
		await api.retryDocument(selected.id);
	  const { processing: view } = await api.getDocumentProcessing(selected.id);
	  applyProcessingView(view);
		setMessage('已提交文档处理流程重试；已完成的文件识别将复用当前 Markdown。');
    } catch (error) {
      setMessage(error instanceof Error ? `论文任务重试失败：${error.message}` : '论文任务重试失败。');
    } finally {
      setBusy(false);
    }
  }

  async function cancelPaperInfo() {
    if (!selected || !window.confirm('取消当前引用论文查询任务？已解析文档、元数据提取和源论文查询不会被删除。')) return;
    setBusy(true);
    try {
      await api.cancelPaperInfo(selected.id);
      const { processing: view } = await api.getDocumentProcessing(selected.id);
      applyProcessingView(view);
      setMessage('已取消论文信息处理。');
    } catch (error) {
      setMessage(error instanceof Error ? `取消论文任务失败：${error.message}` : '取消论文任务失败。');
    } finally {
      setBusy(false);
    }
  }

  async function submitBaseCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!baseName.trim()) {
      setMessage('请输入文档 Base 名称。');
      return;
    }
    setBusy(true);
    try {
      const { documentBase } = await api.createDocumentBase({ name: baseName.trim(), description: baseDescription.trim() });
      setBaseName('');
      setBaseDescription('');
      setSelectedBaseID(documentBase.id);
      await loadBases();
      setMessage(`已创建文档 Base：${documentBase.name}。`);
    } catch (error) {
      setMessage(error instanceof Error ? `创建文档 Base 失败：${error.message}` : '创建文档 Base 失败。');
    } finally {
      setBusy(false);
    }
  }

  async function addSelectedToBase() {
    if (!selected || !selectedBaseID) {
      setMessage('请先选择文档和文档 Base。');
      return;
    }
    setBusy(true);
    try {
      await api.addDocumentToBase(selectedBaseID, selected.id);
      const base = bases.find((item) => item.id === selectedBaseID);
      setMessage(`已将“${documentLabel(selected, paperMetadata)}”加入 Base：${base?.name ?? selectedBaseID}。`);
    } catch (error) {
      setMessage(error instanceof Error ? `加入文档 Base 失败：${error.message}` : '加入文档 Base 失败。');
    } finally {
      setBusy(false);
    }
  }

  function onFileChange(event: ChangeEvent<HTMLInputElement>) {
    setFile(event.target.files?.[0]);
  }

  const selectedDescription = useMemo(() => {
	if (!selected) return '选择左侧列表中的文档，可查看其 ID、DOI、状态及删除操作。';
	if (selected.errorMessage) return selected.errorMessage;
	if (selected.contentType === 'application/pdf' && documentStatus === 'ready') return '可在下方切换文本选择和解析 Block 选择模式。';
	if (selected.contentType === 'application/pdf') return 'PDF 解析完成后可在下方阅读原文并选择解析 Block。';
	return '该文件可管理和解析；PDF 阅读器仅对 PDF 文件显示。';
  }, [selected]);

  return <main className="document-console">
    <aside className="document-sidebar">
      <div className="brand"><p className="eyebrow">Papers Manager</p><h1>文档测试台</h1></div>

      <form className="upload-form" onSubmit={submitUpload}>
        <h2>上传文档</h2>
        <label htmlFor="document-file">文件</label>
        <input id="document-file" type="file" accept={acceptedFiles} onChange={onFileChange} disabled={busy} />
        <label htmlFor="document-doi">DOI（可选）</label>
        <input id="document-doi" value={doi} maxLength={255} onChange={(event) => setDoi(event.target.value)} placeholder="10.1000/example" disabled={busy} />
        <label htmlFor="document-title">论文正式标题（可选）</label>
        <input id="document-title" value={title} onChange={(event) => setTitle(event.target.value)} placeholder="未填写时使用原始文件名" disabled={busy} />
        <label htmlFor="document-tags">学科标签（可选）</label>
        <input id="document-tags" value={tags} onChange={(event) => setTags(event.target.value)} placeholder="computer science, graphics" disabled={busy} />
        <button className="primary" type="submit" disabled={busy || !file}>上传并创建记录</button>
        <p className="hint">支持 TXT、JSON、Markdown、CSV、PDF、Word、Excel 与 PowerPoint；最大大小由服务器配置决定。</p>
      </form>

      <form className="search-form" onSubmit={submitSearch}>
        <h2>查询文档</h2>
        <label htmlFor="search-id">文档 ID</label>
        <input id="search-id" value={filters.id ?? ''} onChange={(event) => setFilters({ ...filters, id: event.target.value })} placeholder="UUID" disabled={busy} />
        <label htmlFor="search-doi">DOI</label>
        <input id="search-doi" value={filters.doi ?? ''} onChange={(event) => setFilters({ ...filters, doi: event.target.value })} placeholder="精确匹配，不区分大小写" disabled={busy} />
        <div className="form-actions"><button type="submit" disabled={busy}>查询</button><button type="button" onClick={() => { const next = { page: 1 }; setFilters(next); void load(next); }} disabled={busy}>重置</button></div>
      </form>

      <form className="search-form" onSubmit={submitPaperSearch}>
        <h2>论文检索</h2>
        <label htmlFor="paper-search-title">正式标题</label>
        <input id="paper-search-title" value={paperSearchTitle} onChange={(event) => setPaperSearchTitle(event.target.value)} placeholder="模糊匹配" disabled={busy} />
        <label htmlFor="paper-search-author">作者</label>
        <input id="paper-search-author" value={paperSearchAuthor} onChange={(event) => setPaperSearchAuthor(event.target.value)} placeholder="作者姓名模糊匹配" disabled={busy} />
        <label htmlFor="paper-search-tags">学科标签</label>
        <input id="paper-search-tags" value={paperSearchTags} onChange={(event) => setPaperSearchTags(event.target.value)} placeholder="computer-science, graphics" disabled={busy} />
        <label htmlFor="paper-tag-mode">标签匹配方式</label>
        <select id="paper-tag-mode" value={paperTagMode} onChange={(event) => setPaperTagMode(event.target.value as 'all' | 'any')} disabled={busy}>
          <option value="all">全部标签匹配</option>
          <option value="any">任一标签匹配</option>
        </select>
        <button type="submit" disabled={busy}>检索论文</button>
      </form>

      <form className="search-form" onSubmit={submitBaseCreate}>
        <h2>文档 Base</h2>
        <label htmlFor="base-name">新 Base 名称</label>
        <input id="base-name" value={baseName} maxLength={120} onChange={(event) => setBaseName(event.target.value)} placeholder="例如：计算机图形学" disabled={busy} />
        <label htmlFor="base-description">说明（可选）</label>
        <input id="base-description" value={baseDescription} onChange={(event) => setBaseDescription(event.target.value)} disabled={busy} />
        <button type="submit" disabled={busy || !baseName.trim()}>创建 Base</button>
        <label htmlFor="base-selector">目标 Base</label>
        <select id="base-selector" value={selectedBaseID} onChange={(event) => setSelectedBaseID(event.target.value)} disabled={busy || bases.length === 0}>
          {bases.length === 0 ? <option value="">尚未创建 Base</option> : bases.map((base) => <option value={base.id} key={base.id}>{base.name}</option>)}
        </select>
        <button type="button" onClick={() => void addSelectedToBase()} disabled={busy || !selected || !selectedBaseID}>将当前文档加入 Base</button>
      </form>
    </aside>

    <section className="document-workspace">
      <header className="workspace-header">
        <div><p className="eyebrow">Document API</p><h2>上传、查询、取消与删除</h2></div>
        <button type="button" onClick={() => void load()} disabled={busy}>刷新</button>
      </header>
      <p className={`status-message ${message.startsWith('上传失败') || message.startsWith('查询失败') || message.startsWith('删除失败') ? 'error' : ''}`}>{message || '正在准备文档列表…'}</p>

      <div className="workspace-grid">
        <section className="document-list-panel">
          <div className="panel-heading"><h3>结果</h3><span>{page.total} 个文档</span></div>
          <div className="document-list">
            {page.items.length === 0 ? <p className="empty-list">没有匹配的文档。</p> : page.items.map((item) => <button type="button" key={item.id} className={selected?.id === item.id ? 'document-row active' : 'document-row'} onClick={() => setSelected(item)}>
              <span className="filename">{documentLabel(item)}</span><span className={`status status-${item.status}`}>{item.status}</span><small>{formatBytes(item.sizeBytes)} · {item.doi || '无 DOI'}</small>
            </button>)}
          </div>
          <div className="pagination"><button type="button" disabled={busy || !hasPreviousPage} onClick={() => void load({ ...filters, page: page.page - 1 })}>上一页</button><span>{page.page} / {pageCount}</span><button type="button" disabled={busy || !hasNextPage} onClick={() => void load({ ...filters, page: page.page + 1 })}>下一页</button></div>
        </section>

        <section className="document-detail-panel">
          {!selected ? <div className="empty-detail"><h3>未选择文档</h3><p>{selectedDescription}</p></div> : <>
          <div className="panel-heading"><h3>文档详情</h3><div className="detail-actions">{['uploaded', 'queued', 'processing'].includes(documentStatus) && <button className="warning" type="button" onClick={() => void cancelSelected()} disabled={busy}>取消解析</button>}{['failed', 'cancelled'].includes(documentStatus) && <button type="button" onClick={() => void retrySelected()} disabled={busy || pipelineActive}>重新解析</button>}<button className="danger" type="button" onClick={() => void deleteSelected()} disabled={busy}>删除文档</button></div></div>
            <dl className="document-details">
              <dt>正式标题</dt><dd>{paperMetadata?.title || '未设置（使用原始文件名展示）'}</dd>
              <dt>原始文件名</dt><dd>{selected.originalFilename}</dd>
              <dt>文档 ID</dt><dd><code>{selected.id}</code></dd>
              <dt>DOI</dt><dd>{selected.doi || '未设置'}</dd>
              <dt>学科标签</dt><dd>{paperMetadata?.tags.length ? paperMetadata.tags.join(' · ') : '未设置'}</dd>
			  <dt>论文信息来源</dt><dd>{paperMetadata?.metadataSource ?? '加载中'}</dd>
              <dt>类型</dt><dd>{selected.contentType}</dd>
              <dt>大小</dt><dd>{formatBytes(selected.sizeBytes)}</dd>
              <dt>状态</dt><dd><span className={`status status-${documentStatus}`}>{documentStatus}</span></dd>
              <dt>创建时间</dt><dd>{formatDate(selected.createdAt)}</dd>
              <dt>更新时间</dt><dd>{formatDate(selected.updatedAt)}</dd>
            </dl>
            <form className="metadata-form" onSubmit={submitMetadataUpdate}>
              <h4>修改文档信息</h4>
              <label htmlFor="edit-filename">原始文件名</label>
              <input id="edit-filename" value={editOriginalFilename} onChange={(event) => setEditOriginalFilename(event.target.value)} disabled={busy} required />
              <label htmlFor="edit-doi">DOI</label>
              <input id="edit-doi" value={editDoi} maxLength={255} onChange={(event) => setEditDoi(event.target.value)} placeholder="留空可清除 DOI" disabled={busy} />
              <label htmlFor="edit-title">论文正式标题</label>
              <input id="edit-title" value={editTitle} onChange={(event) => setEditTitle(event.target.value)} placeholder="留空时使用原始文件名展示" disabled={busy} />
              <label htmlFor="edit-tags">学科标签</label>
              <input id="edit-tags" value={editTags} onChange={(event) => setEditTags(event.target.value)} placeholder="computer science, graphics" disabled={busy} />
              <button type="submit" disabled={busy || (editOriginalFilename === selected.originalFilename && editDoi === (selected.doi ?? '') && editTitle === (paperMetadata?.title ?? '') && editTags === (paperMetadata?.tags.join(', ') ?? ''))}>保存修改</button>
            </form>
			<section className="paper-info-panel">
			  <div className="panel-heading"><h4>文档处理流程</h4><span>{processing?.latestAttempt ? `尝试 ${processing.latestAttempt}` : '尚未开始'}</span></div>
			  {pipelineStages.length ? <div className="paper-info-stages">{pipelineStages.map((stage) => <div key={stage.id}><strong>{processingKindLabel(stage.kind)}</strong><span className={`paper-stage paper-stage-${stage.status}`}>{processingStateLabel(stage.status)}</span><small>{stage.errorReason || ''}</small></div>)}</div> : null}
			  <div className="paper-info-stages">
				{parseStages.length ? parseStages.map((stage) => <div key={stage.id} className={stage.parentSpanId ? 'processing-child' : ''}><strong>{processingKindLabel(stage.kind)}</strong><span className={`paper-stage paper-stage-${stage.status}`}>{processingStateLabel(stage.status)}</span><small>{stage.errorReason || (stage.retryCount ? `重试 ${stage.retryCount}` : '')}</small></div>) : <p className="hint">暂无持久化解析记录。</p>}
			  </div>
			</section>
				<section className="paper-info-panel">
				  <div className="panel-heading"><h4>论文信息任务</h4><span>由统一处理流程编排</span></div>
			  <div className="paper-info-stages">
				{paperInfoStages.length ? paperInfoStages.map((stage) => <div key={stage.id} className={stage.parentSpanId ? 'processing-child' : ''}><strong>{processingKindLabel(stage.kind)}</strong><span className={`paper-stage paper-stage-${stage.status}`}>{processingStateLabel(stage.status)}</span><small>{stage.errorReason || (stage.retryCount ? `重试 ${stage.retryCount}` : '')}</small></div>) : <p className="hint">尚未产生论文信息处理 span。</p>}
				<div><strong>统一处理流程</strong><span className="paper-stage">按持久流程控制</span><button type="button" onClick={() => void retryPaperInfo()} disabled={busy || paperInfoActive || pipelineActive}>重试流程</button></div>
			  </div>
			  {processing?.referenceProgress && <p className="hint">引用查询：排队 {processing.referenceProgress.queued} · 运行 {processing.referenceProgress.running} · 完成 {processing.referenceProgress.completed} · 已取消 {processing.referenceProgress.cancelled}</p>}
			  {paperInfoActive && <button type="button" className="warning" onClick={() => void cancelPaperInfo()} disabled={busy}>取消论文信息任务</button>}
			  <p className="hint">状态来自持久化处理 span；元数据来源来自已落库的论文信息。历史失败请查看 Asynq 死信记录。</p>
			</section>
			<section className="citation-panel">
			  <div className="panel-heading"><h4>引用关系</h4><span>{citations.length} 条参考 · 被引用 {citedBy.length} 次</span></div>
			  <div className="citation-columns">
				<div><h5>本论文引用</h5>{citations.length === 0 ? <p className="hint">尚无已提取的参考文献。</p> : <ol>{citations.map((citation) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><code>{citation.citedDocumentId}</code><p>{citation.rawReference}</p></li>)}</ol>}</div>
				<div><h5>引用本论文</h5>{citedBy.length === 0 ? <p className="hint">尚未被库内论文引用。</p> : <ol>{citedBy.map((citation) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><code>{citation.citingDocumentId}</code><p>{citation.rawReference}</p></li>)}</ol>}</div>
			  </div>
			</section>
            <p className={documentStatus === 'failed' ? 'detail-error' : 'hint'}>{selectedDescription}</p>
			{selected.contentType === 'application/pdf' && <PdfReader document={selected} onMessage={setMessage} />}
          </>}
        </section>
      </div>
    </section>
  </main>;
}

createRoot(document.getElementById('root')!).render(<App />);
