import { ChangeEvent, FormEvent, useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { api, Document, DocumentBase, DocumentBatchItemResult, DocumentBatchResult, DocumentPage, DocumentProcessingView, DocumentSearch, DocumentStageStatus, DocumentURLImport, PaperCitation, PaperMetadata, ProcessingSpan, ProcessingSpanStatus } from './api';
import { PdfReader } from './PdfReader';
import { PaperLibraryPage } from './PaperLibraryPage';
import './PaperInfo.css';
import './styles.css';
import './UploadBasePicker.css';

const acceptedFiles = '.txt,.json,.md,.markdown,.csv,.pdf,.doc,.docx,.xls,.xlsx,.ppt,.pptx';
const maxBatchItems = 20;

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

function batchItemLabel(item: DocumentBatchItemResult) {
  return item.filename || item.url || `第 ${item.index + 1} 项`;
}

function batchStatusLabel(status: DocumentBatchItemResult['status']) {
  const labels: Record<DocumentBatchItemResult['status'], string> = {
    accepted: '已受理', duplicate: '已存在', rejected: '已拒绝', failed: '失败',
  };
  return labels[status];
}

function processingStateLabel(state: ProcessingSpanStatus) {
  const labels: Record<ProcessingSpanStatus, string> = { queued: '排队中', running: '运行中', succeeded: '已完成', failed: '失败', cancelled: '已取消' };
  return labels[state];
}

function stageStateLabel(state: DocumentStageStatus) {
  const labels: Record<DocumentStageStatus, string> = { not_started: '未开始', queued: '排队中', running: '运行中', succeeded: '已完成', failed: '失败', cancelled: '已取消', skipped: '已跳过' };
  return labels[state];
}

function processingKindLabel(kind: string) {
  const labels: Record<string, string> = {
	markdown: '文档解析为 Markdown',
    pipeline: '处理流程',
    recognition: '文档识别',
    metadata_extraction: '元数据提取',
    catalog_source: '源论文查询',
    catalog_reference: '引用论文查询',
    catalog_provider: '论文库提供方查询',
    reference_download: '引用 PDF 获取',
    resolve_pdf_url: '解析 PDF 地址',
    download_pdf: '下载 PDF',
    deduplicate_document: '文档去重',
    enqueue_document_process: '提交文档处理',
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
  const [files, setFiles] = useState<File[]>([]);
  const [doi, setDoi] = useState('');
  const [title, setTitle] = useState('');
  const [tags, setTags] = useState('');
  const [remoteURL, setRemoteURL] = useState('');
  const [remoteDOI, setRemoteDOI] = useState('');
  const [remoteFilename, setRemoteFilename] = useState('');
  const [editOriginalFilename, setEditOriginalFilename] = useState('');
  const [editDoi, setEditDoi] = useState('');
  const [editTitle, setEditTitle] = useState('');
  const [editTags, setEditTags] = useState('');
  const [paperMetadata, setPaperMetadata] = useState<PaperMetadata>();
  const [processing, setProcessing] = useState<DocumentProcessingView>();
  const [citations, setCitations] = useState<PaperCitation[]>([]);
  const [citedBy, setCitedBy] = useState<PaperCitation[]>([]);
  const [citationSourceInfoOpen, setCitationSourceInfoOpen] = useState(false);
  const [citationSourceMetadata, setCitationSourceMetadata] = useState<PaperMetadata>();
  const [citationSourceDocument, setCitationSourceDocument] = useState<Document>();
  const [citationSourceRawReference, setCitationSourceRawReference] = useState('');
  const [citationSourceRawVisible, setCitationSourceRawVisible] = useState(false);
  const [citationSourceLoading, setCitationSourceLoading] = useState(false);
  const [citationSourceError, setCitationSourceError] = useState('');
  const [paperSearchTitle, setPaperSearchTitle] = useState('');
  const [paperSearchAuthor, setPaperSearchAuthor] = useState('');
  const [paperSearchTags, setPaperSearchTags] = useState('');
  const [paperTagMode, setPaperTagMode] = useState<'all' | 'any'>('all');
  const [bases, setBases] = useState<DocumentBase[]>([]);
  const [baseName, setBaseName] = useState('');
  const [baseDescription, setBaseDescription] = useState('');
  const [selectedBaseID, setSelectedBaseID] = useState('');
  const [uploadBaseIDs, setUploadBaseIDs] = useState<string[]>([]);
  const [message, setMessage] = useState('');
  const [batchResults, setBatchResults] = useState<DocumentBatchItemResult[]>([]);
  const [busy, setBusy] = useState(false);
  const processingPollDelay = useRef(2500);
  const processingWatchUntil = useRef(0);
  const processingWatchDocumentID = useRef('');
  const [processingPollRevision, setProcessingPollRevision] = useState(0);
  const citationSourceRequest = useRef(0);

  const pageCount = Math.max(1, Math.ceil(page.total / page.pageSize));
  const hasPreviousPage = page.page > 1;
  const hasNextPage = page.page < pageCount;
  const stageStates = processing?.stageStates ?? [];
  const stageState = (stage: string) => stageStates.find((item) => item.stage === stage);
  const pipelineActive = stageStates.some((stage) => stage.status === 'queued' || stage.status === 'running');
  const extractionActive = ['queued', 'running'].includes(stageState('metadata_extraction')?.status ?? 'not_started');
  const sourceCatalogActive = ['queued', 'running'].includes(stageState('source_lookup')?.status ?? 'not_started');
  const referenceCatalogActive = ['queued', 'running'].includes(stageState('reference_lookup')?.status ?? 'not_started');
  const markdownState = stageState('markdown');
	const documentStatus = processing?.sourceState === 'deleting' ? 'deleting' : processing?.sourceState === 'merged' ? 'merged' : pipelineActive ? 'processing' : markdownState?.status === 'failed' ? 'failed' : markdownState?.status === 'cancelled' ? 'cancelled' : markdownState?.status === 'succeeded' ? 'ready' : selected?.sourceState ?? 'available';
  const remoteURLs = useMemo(() => remoteURL.split(/\r?\n/).map((value) => value.trim()).filter(Boolean), [remoteURL]);

  function applyProcessingView(view: DocumentProcessingView) {
	setProcessing(view);
	setSelected((prev) => (prev ? { ...prev, sourceState: view.sourceState } : prev));
  }

  function openCitationSourceInfo(documentID: string, rawReference: string) {
    const requestID = citationSourceRequest.current + 1;
    citationSourceRequest.current = requestID;
    setCitationSourceInfoOpen(true);
    setCitationSourceMetadata(undefined);
    setCitationSourceDocument(undefined);
    setCitationSourceError('');
    setCitationSourceLoading(true);
    setCitationSourceRawReference(rawReference.trim());
    setCitationSourceRawVisible(false);

    void Promise.allSettled([
      api.getPaperMetadata(documentID),
      api.searchDocuments({ id: documentID, page: 1 }),
    ]).then(([metadataResult, documentResult]) => {
      if (citationSourceRequest.current !== requestID) return;
      if (metadataResult.status === 'fulfilled') {
        setCitationSourceMetadata(metadataResult.value.paperMetadata);
      } else {
        setCitationSourceError(metadataResult.reason instanceof Error ? metadataResult.reason.message : '无法读取引用的提取信息。');
      }
      if (documentResult.status === 'fulfilled') {
        setCitationSourceDocument(documentResult.value.items[0]);
      }
    }).finally(() => {
      if (citationSourceRequest.current === requestID) setCitationSourceLoading(false);
    });
  }

  function watchQueuedProcessing(documentID: string) {
    processingPollDelay.current = 2500;
    processingWatchUntil.current = Date.now() + 30_000;
    processingWatchDocumentID.current = documentID;
    setProcessingPollRevision((value) => value + 1);
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
    const hasActive = processing?.stageStates.some((stage) => stage.status === 'queued' || stage.status === 'running');
    const watchingQueuedTask = selected?.id === processingWatchDocumentID.current && Date.now() < processingWatchUntil.current;
    if (!selected || (!hasActive && !watchingQueuedTask)) return;
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
        setProcessingPollRevision((value) => value + 1);
      });
    }, delay);
    return () => window.clearTimeout(timer);
  }, [selected?.id, processing?.stageStates, processingPollRevision]);

  // Poll document list while a source deletion is in progress. Per-stage
  // progress is refreshed from the selected document's processing projection.
  useEffect(() => {
	const hasActive = page.items.some((item) => item.sourceState === 'deleting');
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
    if (files.length === 0) {
      setMessage('请先选择文件。');
      return;
    }
    if (files.length > maxBatchItems) {
      setMessage(`一次最多上传 ${maxBatchItems} 个文件。`);
      return;
    }
    setBusy(true);
    setBatchResults([]);
    if (files.length > 1) {
      setMessage(`正在批量上传 ${files.length} 个文件…`);
      try {
        const result = await api.uploadBatch(files);
        await presentBatchResult(result, '文件批量上传');
        setFiles([]);
        const input = window.document.getElementById('document-file') as HTMLInputElement | null;
        if (input) input.value = '';
      } catch (error) {
        setMessage(error instanceof Error ? `批量上传失败：${error.message}` : '批量上传失败。');
      } finally {
        setBusy(false);
      }
      return;
    }
    const file = files[0];
    setMessage(`正在上传 ${file.name}…`);
    try {
      const { document: uploadedDocument } = await api.upload(file, doi.trim());
      watchQueuedProcessing(uploadedDocument.id);
      let uploadedPaper: PaperMetadata | undefined;
      if (title.trim() || parseTags(tags).length > 0) {
        ({ paperMetadata: uploadedPaper } = await api.updatePaperMetadata(uploadedDocument.id, { title: title.trim(), tags: parseTags(tags) }));
      }
      const selectedUploadBases = bases.filter((base) => uploadBaseIDs.includes(base.id));
      const baseAttachmentFailures: string[] = [];
      for (const base of selectedUploadBases) {
        try {
          await api.addDocumentToBase(base.id, uploadedDocument.id);
        } catch (error) {
          const reason = error instanceof Error ? error.message : '未知错误';
          baseAttachmentFailures.push(`${base.name}（${reason}）`);
        }
      }
      setSelected(uploadedDocument);
      setPaperMetadata(uploadedPaper);
      setFiles([]);
      setDoi('');
      setTitle('');
      setTags('');
      setUploadBaseIDs([]);
      const input = window.document.getElementById('document-file') as HTMLInputElement | null;
      if (input) input.value = '';
      await load({ page: 1 });
      if (baseAttachmentFailures.length > 0) {
        setMessage(`上传成功：${documentLabel(uploadedDocument, uploadedPaper)}；已加入 ${selectedUploadBases.length - baseAttachmentFailures.length}/${selectedUploadBases.length} 个 Base，失败：${baseAttachmentFailures.join('；')}。`);
      } else if (selectedUploadBases.length > 0) {
        setMessage(`上传成功：${documentLabel(uploadedDocument, uploadedPaper)}；已加入 ${selectedUploadBases.length} 个 Base。`);
      } else {
        setMessage(`上传成功：${documentLabel(uploadedDocument, uploadedPaper)}（${uploadedDocument.id}）。`);
      }
    } catch (error) {
      setMessage(error instanceof Error ? `上传失败：${error.message}` : '上传失败。');
    } finally {
      setBusy(false);
    }
  }

  async function submitRemoteImport(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (remoteURLs.length === 0) {
      setMessage('请输入可下载的 PDF、arXiv 或 DOI URL。');
      return;
    }
    if (remoteURLs.length > maxBatchItems) {
      setMessage(`一次最多导入 ${maxBatchItems} 个 URL。`);
      return;
    }
    setBusy(true);
    setBatchResults([]);
    if (remoteURLs.length > 1) {
      setMessage(`正在批量验证并下载 ${remoteURLs.length} 个远程 PDF…`);
      try {
        const payload: DocumentURLImport[] = remoteURLs.map((url) => ({ url }));
        const result = await api.importFromURLs(payload);
        await presentBatchResult(result, 'URL 批量导入');
        setRemoteURL('');
      } catch (error) {
        setMessage(error instanceof Error ? `URL 批量导入失败：${error.message}` : 'URL 批量导入失败。');
      } finally {
        setBusy(false);
      }
      return;
    }
    setMessage('正在验证并下载远程 PDF…');
    try {
      const { document: imported } = await api.importFromURL({
        url: remoteURLs[0],
        doi: remoteDOI.trim() || undefined,
        filename: remoteFilename.trim() || undefined,
      });
      setSelected(imported);
      setRemoteURL('');
      setRemoteDOI('');
      setRemoteFilename('');
      watchQueuedProcessing(imported.id);
      setMessage(`远程 PDF 已导入并提交处理：${imported.originalFilename}（${imported.id}）。`);
      await load({ page: 1 });
    } catch (error) {
      setMessage(error instanceof Error ? `远程导入失败：${error.message}` : '远程导入失败。');
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

  async function deleteAllDocuments() {
    setBusy(true);
    try {
      const firstPage = await api.searchDocuments({ page: 1 });
      if (firstPage.total === 0) {
        setMessage('当前没有可删除的论文。');
        return;
      }

      const confirmation = window.prompt(
        `即将永久删除全部 ${firstPage.total} 篇论文、原始文件及关联数据。文档 Base 会保留。\n请输入“删除全部论文”继续：`,
      );
      if (confirmation !== '删除全部论文') {
        setMessage('已取消删除全部论文。');
        return;
      }

      // Capture a stable ID snapshot before deleting. Fetching and deleting
      // page-by-page at the same time would shrink pagination and skip rows.
      const documents = [...firstPage.items];
      const pageCount = Math.ceil(firstPage.total / firstPage.pageSize);
      for (let pageNumber = 2; pageNumber <= pageCount; pageNumber += 1) {
        const nextPage = await api.searchDocuments({ page: pageNumber });
        documents.push(...nextPage.items);
      }

      const uniqueDocuments = Array.from(new Map(documents.map((document) => [document.id, document])).values());
      const failures: string[] = [];
      let deleted = 0;
      setSelected(undefined);
      setPaperMetadata(undefined);

      for (const document of uniqueDocuments) {
        setMessage(`正在删除全部论文：${deleted + failures.length + 1} / ${uniqueDocuments.length}…`);
        try {
          await api.deleteDocument(document.id);
          deleted += 1;
        } catch (error) {
          const reason = error instanceof Error ? error.message : '未知错误';
          failures.push(`${document.originalFilename}（${reason}）`);
        }
      }

      await load({ page: 1 });
      if (failures.length > 0) {
        const preview = failures.slice(0, 3).join('；');
        const remainder = failures.length > 3 ? `；另有 ${failures.length - 3} 篇` : '';
        setMessage(`删除全部论文完成：成功 ${deleted} 篇，失败 ${failures.length} 篇。${preview}${remainder}`);
      } else {
        setMessage(`已删除全部 ${deleted} 篇论文。`);
      }
    } catch (error) {
      setMessage(error instanceof Error ? `删除全部论文失败：${error.message}` : '删除全部论文失败。');
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
    watchQueuedProcessing(selected.id);
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

  async function runPaperControl(
    operation: (documentID: string) => Promise<{ accepted: boolean }>,
    successMessage: string,
  ) {
    if (!selected) return;
    setBusy(true);
    watchQueuedProcessing(selected.id);
    try {
      await operation(selected.id);
      const { processing: view } = await api.getDocumentProcessing(selected.id);
      applyProcessingView(view);
      setMessage(successMessage);
    } catch (error) {
      setMessage(error instanceof Error ? `论文任务操作失败：${error.message}` : '论文任务操作失败。');
    } finally {
      setBusy(false);
    }
  }

  async function downloadReferencePDF(documentID: string) {
    setBusy(true);
    try {
      await api.downloadReferencePDF(documentID);
      setMessage('已提交该引用文献的 PDF 下载。');
    } catch (error) {
      setMessage(error instanceof Error ? `引用文献下载未提交：${error.message}` : '引用文献下载未提交。');
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
    const selectedFiles = Array.from(event.target.files ?? []);
    setFiles(selectedFiles);
    if (selectedFiles.length > 1) {
      setDoi('');
      setTitle('');
      setTags('');
      setUploadBaseIDs([]);
    }
  }

  function onRemoteURLsChange(event: ChangeEvent<HTMLTextAreaElement>) {
    const value = event.target.value;
    setRemoteURL(value);
    if (value.split(/\r?\n/).filter((item) => item.trim()).length > 1) {
      setRemoteDOI('');
      setRemoteFilename('');
    }
  }

  async function presentBatchResult(result: DocumentBatchResult, operation: string) {
    setBatchResults(result.items);
    const accepted = result.items.filter((item) => item.status === 'accepted' && item.document);
    const firstAccepted = accepted[0];
    const firstDocument = firstAccepted?.document ?? result.items.find((item) => item.document)?.document;
    if (firstDocument) {
      setSelected(firstDocument);
      if (firstAccepted?.document && firstAccepted.processingQueued !== false) {
        watchQueuedProcessing(firstAccepted.document.id);
      }
    }
    await load({ page: 1 });
    const counts = result.items.reduce<Record<DocumentBatchItemResult['status'], number>>(
      (current, item) => ({ ...current, [item.status]: current[item.status] + 1 }),
      { accepted: 0, duplicate: 0, rejected: 0, failed: 0 },
    );
    const stopped = result.stopped ? `；已安全停止：${result.error ?? '重复检测暂不可用'}` : '';
    setMessage(`${operation}完成：受理 ${counts.accepted}，已存在 ${counts.duplicate}，拒绝 ${counts.rejected}，失败 ${counts.failed}${stopped}。`);
  }

  function toggleUploadBase(baseID: string) {
    setUploadBaseIDs((current) => current.includes(baseID)
      ? current.filter((id) => id !== baseID)
      : [...current, baseID]);
  }

	const selectedDescription = useMemo(() => {
	if (!selected) return '选择左侧列表中的文档，可查看其 ID、DOI、状态及删除操作。';
	if (markdownState?.errorMessage) return markdownState.errorMessage;
	if (selected.contentType === 'application/pdf' && documentStatus === 'ready') return '可在下方切换文本选择和解析 Block 选择模式。';
	if (selected.contentType === 'application/pdf') return 'PDF 解析完成后可在下方阅读原文并选择解析 Block。';
	return '该文件可管理和解析；PDF 阅读器仅对 PDF 文件显示。';
	}, [selected, markdownState?.errorMessage, documentStatus]);

  return <main className="document-console">
    <aside className="document-sidebar">
      <div className="brand"><p className="eyebrow">Papers Manager</p><h1>文档测试台</h1><a className="library-back-link" href="#/library">打开论文阅读库</a></div>

      <form className="upload-form" onSubmit={submitUpload}>
        <h2>上传文档</h2>
        <label htmlFor="document-file">文件（可多选）</label>
        <input id="document-file" type="file" accept={acceptedFiles} multiple onChange={onFileChange} disabled={busy} />
        <label htmlFor="document-doi">DOI（可选）</label>
        <input id="document-doi" value={doi} maxLength={255} onChange={(event) => setDoi(event.target.value)} placeholder="10.1000/example" disabled={busy || files.length > 1} />
        <label htmlFor="document-title">论文正式标题（可选）</label>
        <input id="document-title" value={title} onChange={(event) => setTitle(event.target.value)} placeholder="未填写时使用原始文件名" disabled={busy || files.length > 1} />
        <label htmlFor="document-tags">学科标签（可选）</label>
        <input id="document-tags" value={tags} onChange={(event) => setTags(event.target.value)} placeholder="computer science, graphics" disabled={busy || files.length > 1} />
        <fieldset className="base-picker" disabled={busy || files.length > 1 || bases.length === 0}>
          <legend>加入 Document Base（可多选）</legend>
          {bases.length === 0 ? <p className="hint">暂无可选 Base；可先在下方创建。</p> : bases.map((base) => (
            <label className="base-option" htmlFor={`upload-base-${base.id}`} key={base.id}>
              <input id={`upload-base-${base.id}`} type="checkbox" checked={uploadBaseIDs.includes(base.id)} onChange={() => toggleUploadBase(base.id)} />
              <span>{base.name}</span>
            </label>
          ))}
        </fieldset>
        <button className="primary" type="submit" disabled={busy || files.length === 0}>{files.length > 1 ? `批量上传 ${files.length} 个文件` : '上传并创建记录'}</button>
        <p className="hint">支持 TXT、JSON、Markdown、CSV、PDF、Word、Excel 与 PowerPoint；单文件可设置 DOI、标题、标签和 Base。批量上传仅提交文件，完成后可逐项编辑元数据。</p>
      </form>

      <form className="upload-form" onSubmit={submitRemoteImport}>
        <h2>从 URL 导入 PDF</h2>
        <label htmlFor="remote-url">PDF、arXiv 或 DOI URL（每行一个）</label>
        <textarea id="remote-url" value={remoteURL} onChange={onRemoteURLsChange} placeholder={'https://arxiv.org/abs/1706.03762\nhttps://example.org/paper.pdf'} disabled={busy} required />
        <label htmlFor="remote-doi">显式 DOI（可选）</label>
        <input id="remote-doi" value={remoteDOI} maxLength={255} onChange={(event) => setRemoteDOI(event.target.value)} placeholder="10.1000/example" disabled={busy || remoteURLs.length > 1} />
        <label htmlFor="remote-filename">保存文件名（可选）</label>
        <input id="remote-filename" value={remoteFilename} onChange={(event) => setRemoteFilename(event.target.value)} placeholder="paper.pdf" disabled={busy || remoteURLs.length > 1} />
        <button className="primary" type="submit" disabled={busy || remoteURLs.length === 0}>{remoteURLs.length > 1 ? `批量导入 ${remoteURLs.length} 个 URL` : '验证、下载并处理'}</button>
        <p className="hint">普通出版社落地页不会被猜测为 PDF；当前支持显式 PDF URL、arXiv 页面及对应 DOI URL。多行 URL 会逐项受理，单项 DOI 和文件名仅适用于单行导入。</p>
      </form>

      {batchResults.length > 0 && <section className="batch-results" aria-live="polite">
        <h2>批量导入结果</h2>
        <ul>{batchResults.map((item) => <li key={`${item.index}-${batchItemLabel(item)}`}>
          <span>{batchItemLabel(item)}</span>
          <strong className={`batch-status batch-status-${item.status}`}>{batchStatusLabel(item.status)}</strong>
          {item.error && <small>{item.error}</small>}
        </li>)}</ul>
      </section>}

      <form className="search-form" onSubmit={submitSearch}>
        <h2>查询文档</h2>
        <label htmlFor="search-id">文档 ID</label>
        <input id="search-id" value={filters.id ?? ''} onChange={(event) => setFilters({ ...filters, id: event.target.value })} placeholder="UUID" disabled={busy} />
        <label htmlFor="search-doi">DOI</label>
        <input id="search-doi" value={filters.doi ?? ''} onChange={(event) => setFilters({ ...filters, doi: event.target.value })} placeholder="精确匹配，不区分大小写" disabled={busy} />
		<label htmlFor="search-source-state">源状态</label>
		<select id="search-source-state" value={filters.sourceState ?? ''} onChange={(event) => setFilters({ ...filters, sourceState: event.target.value as DocumentSearch['sourceState'] })} disabled={busy}>
		  <option value="">全部源状态</option>
		  <option value="missing">missing</option>
		  <option value="available">available</option>
		  <option value="deleting">deleting</option>
		  <option value="merged">merged</option>
        </select>
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
        <div className="detail-actions">
          <button type="button" onClick={() => void load()} disabled={busy}>刷新</button>
          <button className="danger" type="button" onClick={() => void deleteAllDocuments()} disabled={busy}>删除所有论文</button>
        </div>
      </header>
      <p className={`status-message ${message.includes('失败') ? 'error' : ''}`}>{message || '正在准备文档列表…'}</p>

      <div className="workspace-grid">
        <section className="document-list-panel">
          <div className="panel-heading"><h3>结果</h3><span>{page.total} 个文档</span></div>
          <div className="document-list">
            {page.items.length === 0 ? <p className="empty-list">没有匹配的文档。</p> : page.items.map((item) => <button type="button" key={item.id} className={selected?.id === item.id ? 'document-row active' : 'document-row'} onClick={() => setSelected(item)}>
			  <span className="filename">{documentLabel(item)}</span><span className={`status status-${item.sourceState}`}>{item.sourceState}</span><small>{formatBytes(item.sizeBytes)} · {item.doi || '无 DOI'}</small>
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
			  <div className="panel-heading"><h4>文档处理流程</h4><span>{processing?.sourceState ?? '尚未加载'}</span></div>
			  <div className="paper-info-stages">
				{stageStates.length ? stageStates.map((stage) => <div key={stage.stage}><strong>{processingKindLabel(stage.stage)}</strong><span className={`paper-stage paper-stage-${stage.status}`}>{stageStateLabel(stage.status)}</span><small>{stage.errorMessage || `第 ${stage.generation} 代`}</small></div>) : <p className="hint">尚未产生阶段状态。</p>}
			  </div>
			</section>
				<section className="paper-info-panel">
				  <div className="panel-heading"><h4>论文信息任务</h4><span>由四阶段状态控制</span></div>
				  <div className="paper-info-stages">
					<div><strong>元数据提取</strong><button type="button" onClick={() => void runPaperControl(api.retryPaperExtraction, '已提交元数据提取重试。')} disabled={busy || extractionActive || pipelineActive}>重试</button></div>
					<div><strong>源论文查询</strong><button type="button" onClick={() => void runPaperControl(api.retryPaperCatalog, '已提交源论文查询重试。')} disabled={busy || sourceCatalogActive || pipelineActive}>重试</button></div>
					<div><strong>引用论文查询</strong><button type="button" onClick={() => void runPaperControl(api.retryPaperReferences, '已提交引用论文查询重试。')} disabled={busy || referenceCatalogActive}>重试</button></div>
					<div><strong>引用 PDF 获取</strong><span className="paper-stage">仅在引用条目中手动触发</span></div>
				  </div>
			  {processing?.referenceProgress && <p className="hint">引用目标源查询：排队 {processing.referenceProgress.queued} · 运行 {processing.referenceProgress.running} · 完成 {processing.referenceProgress.completed} · 失败 {processing.referenceProgress.failed} · 已取消 {processing.referenceProgress.cancelled}</p>}
			  {referenceCatalogActive && <button type="button" className="warning" onClick={() => void cancelPaperInfo()} disabled={busy}>取消引用论文查询</button>}
			  <p className="hint">状态来自持久化处理 span；元数据来源来自已落库的论文信息。历史失败请查看 Asynq 死信记录。</p>
			</section>
			<section className="citation-panel">
			  <div className="panel-heading"><h4>引用关系</h4><span>{citations.length} 条参考 · 被引用 {citedBy.length} 次</span></div>
			  <div className="citation-columns">
				<div><h5>本论文引用</h5>{citations.length === 0 ? <p className="hint">尚无已提取的参考文献。</p> : <ol>{citations.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><button className="citation-source-button" type="button" onClick={() => openCitationSourceInfo(citation.citedDocumentId, citation.rawReference)}>查看第 {index + 1} 条的 Python 提取信息</button><button type="button" onClick={() => void downloadReferencePDF(citation.citedDocumentId)} disabled={busy}>下载 PDF</button><p>{citation.rawReference}</p></li>)}</ol>}</div>
				<div><h5>引用本论文</h5>{citedBy.length === 0 ? <p className="hint">尚未被库内论文引用。</p> : <ol>{citedBy.map((citation, index) => <li key={`${citation.citingDocumentId}-${citation.citedDocumentId}`}><button className="citation-source-button" type="button" onClick={() => openCitationSourceInfo(citation.citingDocumentId, citation.rawReference)}>查看第 {index + 1} 条的已落库信息</button><p>{citation.rawReference}</p></li>)}</ol>}</div>
			  </div>
			</section>
			{citationSourceInfoOpen && <div className="citation-source-backdrop" role="presentation" onMouseDown={() => setCitationSourceInfoOpen(false)}>
			  <section className="citation-source-dialog" role="dialog" aria-modal="true" aria-labelledby="citation-source-title" onMouseDown={(event) => event.stopPropagation()}>
				<header><div><p className="eyebrow">Citation extraction</p><h4 id="citation-source-title">Python 提取的引用源信息</h4></div><div className="detail-actions"><button type="button" onClick={() => setCitationSourceRawVisible((visible) => !visible)}>{citationSourceRawVisible ? '收起引用原文' : '查看引用原文'}</button><button type="button" onClick={() => setCitationSourceInfoOpen(false)}>关闭</button></div></header>
				{citationSourceRawVisible && <section className="citation-source-raw"><h5>引用原文</h5><p>{citationSourceRawReference || '未保留引用原文。'}</p></section>}
				{citationSourceLoading && <p className="hint">正在读取 Python 提取后已落库的引用信息…</p>}
				{!citationSourceLoading && citationSourceError && <p className="detail-error">读取失败：{citationSourceError}</p>}
				{!citationSourceLoading && !citationSourceError && citationSourceMetadata && <><p className="hint">字段初始由 Python 提取写入；若后续论文库查询补全过字段，当前来源会在下方标明。</p><dl>
				  <dt>当前来源</dt><dd>{citationSourceMetadata.metadataSource}</dd>
				  <dt>标题</dt><dd>{citationSourceMetadata.title || '未提取标题'}</dd>
				  <dt>作者</dt><dd>{citationSourceMetadata.authors.map((author) => author.name).filter(Boolean).join('、') || '未提取作者'}</dd>
				  <dt>发表信息</dt><dd>{[citationSourceMetadata.venue, citationSourceMetadata.publishedAt?.slice(0, 10), citationSourceMetadata.volume, citationSourceMetadata.issue, citationSourceMetadata.pages].filter(Boolean).join(' · ') || '未提取发表信息'}</dd>
				  <dt>DOI</dt><dd>{citationSourceDocument?.doi || '未提取 DOI'}</dd>
				  <dt>来源</dt><dd>{citationSourceMetadata.sourceName || '未提取来源'}{citationSourceMetadata.sourceUrl && <> · <a href={citationSourceMetadata.sourceUrl} target="_blank" rel="noreferrer">打开来源</a></>}</dd>
				</dl></>}
			  </section>
			</div>}
            <p className={documentStatus === 'failed' ? 'detail-error' : 'hint'}>{selectedDescription}</p>
			{selected.contentType === 'application/pdf' && <PdfReader document={selected} onMessage={setMessage} />}
          </>}
        </section>
      </div>
    </section>
  </main>;
}

function Root() {
  const [showLibrary, setShowLibrary] = useState(window.location.hash === '#/library');

  useEffect(() => {
    const syncRoute = () => setShowLibrary(window.location.hash === '#/library');
    window.addEventListener('hashchange', syncRoute);
    return () => window.removeEventListener('hashchange', syncRoute);
  }, []);

  return showLibrary ? <PaperLibraryPage /> : <App />;
}

createRoot(document.getElementById('root')!).render(<Root />);
