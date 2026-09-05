/**
 * PDF citation-link probe — reverse-location test zone.
 *
 * Pure browser (pdf.js), no backend needed for the geometry chain:
 *   1. read Link annotations per page (marker rects),
 *   2. resolve internal destinations (named dest via getDestination,
 *      explicit arrays parsed in place),
 *   3. convert into top-left-origin PDF points — the same space the server
 *      persists for document_blocks bboxes,
 *   4. draw the derived target point/line on the target page so you can
 *      visually confirm it lands on the right bibliography entry.
 *
 * Optional "document id" mode additionally fetches /blocks (and /citations)
 * through the Vite dev proxy and cross-checks the derived point against the
 * server's stored block bboxes (green dashed box).
 *
 * Coordinate findings baked in (verified against pymupdf ground truth):
 *   - annotation.rect  : raw PDF space, y up  ->  top-left rect = [x0, H-y1, x1, H-y0]
 *   - dest from name   : already top-left normalized (pdf.js getDestination)
 *   - dest as raw array: y up                 ->  y = H - top
 *   - dest x           : same in both spaces
 */
import * as pdfjs from 'pdfjs-dist/build/pdf.mjs';

pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString();

type Point = { x: number; y: number };
type RectTL = { x0: number; y0: number; x1: number; y1: number };
interface Entry {
  id: string;
  srcPage: number; // 0-based
  rawRect: number[];
  rectTL: RectTL;
  destName: string | null;
  destRaw: any;
  uri: string | null;
  target: (Point & { page: number }) | null; // 0-based page
  error: string | null;
  block: any | null; // matched server block, doc-id mode
}

let pdfDoc: pdfjs.PDFDocumentProxy | null = null;
let pageInfos: { width: number; height: number }[] = [];
let entries: Entry[] = [];
let blocks: any[] = [];
let pagesUI: Map<number, { holder: HTMLElement; w1: number; h1: number; box?: HTMLElement; flashes?: HTMLElement[] }> = new Map();
let activeId: string | null = null;

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const listEl = $('#list');
const pagesEl = $<HTMLDivElement>('div#pages');
const logEl = $<HTMLPreElement>('pre#log');
const statsEl = $('span#stats');
const jsonBtn = $<HTMLButtonElement>('button#jsonBtn');

function log(msg: string) {
  logEl.textContent = (logEl.textContent ? logEl.textContent + '\n' : '') + msg;
  logEl.scrollTop = logEl.scrollHeight;
}

// Surface every failure on-page instead of dying silently in the console.
window.addEventListener('error', (ev) => log(`💥 运行时错误: ${ev.message}${ev.filename ? ` @ ${ev.filename}:${ev.lineno}` : ''}`));
window.addEventListener('unhandledrejection', (ev) => {
  const r: any = ev.reason;
  log(`💥 未捕获 Promise 失败: ${r?.stack ?? r?.message ?? String(r)}`);
});
const _origConsoleError = console.error;
console.error = (...args: any[]) => {
  try {
    log(`console.error: ${args.map((a) => (a instanceof Error ? a.stack ?? a.message : typeof a === 'object' ? JSON.stringify(a) : String(a))).join(' ')}`);
  } catch { /* log panel may not exist yet */ }
  _origConsoleError(...args);
};

function setStatus(text: string, bad = false) {
  statsEl.textContent = text;
  statsEl.style.color = bad ? '#f88' : '#ffd';
}

async function loadPDF(data: ArrayBuffer, source: string) {
  pagesUI.clear();
  pagesEl.textContent = '';
  listEl.innerHTML = '<h2>引用链接(点行 → 跳到目标页)</h2>';
  entries = [];
  activeId = null;
  setStatus('解析 PDF 中…');
  log(`== load ${source} (${(data.byteLength / 1024).toFixed(0)} KiB)`);
  try {
    pdfDoc = await pdfjs.getDocument({ data, isEvalSupported: false }).promise;
  } catch (err: any) {
    setStatus('PDF 加载失败 — 见底部日志', true);
    log(`💥 getDocument 失败: ${err?.stack ?? err}`);
    return;
  }
  log(`pages: ${pdfDoc.numPages}`);
  pageInfos = [];
  for (let i = 0; i < pdfDoc.numPages; i++) {
    const page = await pdfDoc.getPage(i + 1);
    const vp = page.getViewport({ scale: 1 });
    pageInfos.push({ width: vp.width, height: vp.height });
  }
  try {
    entries = await collectEntries();
  } catch (err: any) {
    setStatus('解析注解失败 — 见底部日志', true);
    log(`💥 collectEntries 失败: ${err?.stack ?? err}`);
    return;
  }
  const internal = entries.filter((e) => e.destName || e.destRaw || e.target || e.error).length;
  setStatus(`${internal} 条内部引用链接(${entries.filter((e) => e.uri && !e.destRaw).length} 条 URI 外链)`);
  jsonBtn.disabled = false;
  renderList();
  log('✓ 完成。点击左侧行或页面上的红色标记框进行反向定位测试。');
}

function uriCount() {
  return entries.filter((e) => e.uri && !e.destRaw).length;
}

async function collectEntries(): Promise<Entry[]> {
  const out: Entry[] = [];
  let n = 0;
  for (let i = 0; i < pdfDoc!.numPages; i++) {
    const page = await pdfDoc!.getPage(i + 1);
    const H = pageInfos[i].height;
    const anns: any[] = await page.getAnnotations();
    for (const ann of anns) {
      if (ann.subtype !== 'Link') continue;
      if (!ann.dest && !ann.url) continue;
      const id = `a${++n}`;
      const rectTL: RectTL = { x0: ann.rect[0], y0: H - ann.rect[3], x1: ann.rect[2], y1: H - ann.rect[1] };
      if (ann.dest && !(ann.url)) {
        out.push(await resolveInternal(id, i, ann, rectTL, H));
      } else if (ann.url && !ann.dest) {
        out.push({ id, srcPage: i, rawRect: ann.rect, rectTL, destName: null, destRaw: null, uri: ann.url, target: null, error: null, block: null });
      } else {
        out.push({ id, srcPage: i, rawRect: ann.rect, rectTL, destName: null, destRaw: ann.dest, uri: ann.url ?? null, target: null, error: 'both dest+url', block: null });
      }
    }
  }
  return out;
}

async function resolveInternal(id: string, srcPage: number, ann: any, rectTL: RectTL, srcH: number): Promise<Entry> {
  const dest = ann.dest;
  const isName = typeof dest === 'string';
  let resolved: any = dest;
  try {
    if (isName) resolved = await pdfDoc!.getDestination(dest);
    if (!Array.isArray(resolved)) throw new Error(`dest not resolvable: ${JSON.stringify(dest)}`);
    if (resolved.length === 0) throw new Error('empty dest');
    let pageIdx: number = -1;
    const first = resolved[0];
    if (typeof first === 'number') pageIdx = first;
    else if (first && typeof first === 'object') pageIdx = await pdfDoc!.getPageIndex(first);
    else throw new Error(`bad dest page ref: ${JSON.stringify(first)}`);
    // style name may be a string or a Name object
    const style = typeof resolved[1] === 'string' ? resolved[1] : resolved[1]?.name ?? resolved[1] ?? '';
    const args: any[] = resolved.slice(2);
    const num = (v: any) => (typeof v === 'number' ? v : v && typeof v === 'object' && typeof v.num === 'number' ? v.num : v);
    let left = 0, top = 0;
    const st = String(style).toUpperCase();
    if (st === 'XYZ') { left = num(args[0]) ?? 0; top = num(args[1]) ?? 0; }
    else if (st === 'FITH' || st === 'FITBH') { top = num(args[0]) ?? 0; }
    else if (st === 'FITV' || st === 'FITBV') { left = num(args[0]) ?? 0; top = 0; }
    else if (st === 'FIT' || st === 'FITB' || !st) { left = rectTL.x0; top = rectTL.y0; }
    const Ht = pageInfos[pageIdx]?.height;
    if (Ht == null) throw new Error(`target page ${pageIdx} out of range`);
    const yTL = isName ? top : Ht - top; // empirical: named dests pre-normalized, raw arrays y-up
    const entry: Entry = { id, srcPage, rawRect: ann.rect, rectTL, destName: isName ? dest : null, destRaw: dest, uri: null, target: { x: left, y: yTL, page: pageIdx }, error: null, block: null };
    entry.block = matchBlock({ x: left, y: yTL }, pageIdx);
    return entry;
  } catch (err: any) {
    return { id, srcPage, rawRect: ann.rect, rectTL, destName: isName ? dest : null, destRaw: dest, uri: ann.url ?? null, target: null, error: String(err?.message ?? err), block: null };
  }
}

// ---- server block cross-check (doc-id mode) ----
function matchBlock(pt: Point, pageIdx: number) {
  if (!blocks.length) return null;
  const pad = 2;
  let best: any = null, bestD = Infinity;
  for (const b of blocks) {
    const loc = b.location;
    if (!loc) continue;
    if (loc.pageNumber !== pageIdx + 1 && loc.pageNumber !== pageIdx) continue;
    const { bboxX0, bboxX1, bboxY0, bboxY1 } = loc;
    if (pt.x < bboxX0 - pad || pt.x > bboxX1 + pad || pt.y < bboxY0 - pad || pt.y > bboxY1 + pad) continue;
    const d = Math.abs(bboxY0 - pt.y);
    if (d < bestD) { bestD = d; best = b; }
  }
  return best;
}

// ---- rendering ----
function renderList() {
  listEl.innerHTML = '<h2>引用链接(点行 → 跳到目标页)</h2>';
  const internals = entries.filter((e) => e.destRaw || e.destName || e.error);
  for (const e of internals) {
    const row = document.createElement('div');
    row.className = 'row';
    const tgt = e.target ? `p${e.target.page + 1} @ (${e.target.x.toFixed(1)}, ${e.target.y.toFixed(1)})` : '无目标点';
    row.innerHTML = `<span class="arrow">p${e.srcPage + 1}</span> marker[${e.rectTL.x0.toFixed(0)},${e.rectTL.y0.toFixed(0)}] → <span class="dest">${tgt}</span> ${e.destName ? `<span class="meta">${e.destName}</span>` : ''} ${e.error ? `<span class="meta" style="color:#c00">${e.error}</span>` : ''}`;
    if (e.block) row.innerHTML += `<div class="block">✓ block ${e.block.ordinal} "${(e.block.text ?? '').slice(0, 90)}"</div>`;
    else if (blocks.length) row.innerHTML += `<div class="block" style="color:#b80">✗ 无块包含该点</div>`;
    row.onclick = () => jumpTo(e);
    (e as any).rowEl = row;
    listEl.appendChild(row);
  }
  if (!internals.length) listEl.insertAdjacentHTML('beforeend', '<div style="padding:8px;color:#888">未发现带目标点的内部链接。</div>');
}

async function getPageUI(pageIdx: number) {
  if (pagesUI.has(pageIdx)) return pagesUI.get(pageIdx)!;
  const page = await pdfDoc!.getPage(pageIdx + 1);
  const { width: w1, height: h1 } = pageInfos[pageIdx];
  const ui: any = { w1, h1 };
  const pw = document.createElement('div');
  pw.className = 'pw';
  const head = document.createElement('div');
  head.className = 'phead';
  head.innerHTML = `<span>第 ${pageIdx + 1} 页 (${w1.toFixed(0)}×${h1.toFixed(0)} pt)</span>`;
  pw.appendChild(head);
  const holder = document.createElement('div');
  holder.className = 'canvas-holder';
  pw.appendChild(holder);
  const canvas = document.createElement('canvas');
  holder.appendChild(canvas);
  pagesEl.appendChild(pw);

  const cssW = 800;
  const scale = cssW / w1;
  const dpr = window.devicePixelRatio || 1;
  const vp = page.getViewport({ scale: scale * dpr });
  canvas.width = Math.ceil(vp.width);
  canvas.height = Math.ceil(vp.height);
  canvas.style.width = '100%';
  await page.render({ canvasContext: canvas.getContext('2d')!, viewport: vp }).promise;

  const toPct = (v: number, tot: number) => ((v / tot) * 100).toFixed(3) + '%';
  // marker overlays
  for (const e of entries) {
    if (e.srcPage !== pageIdx) continue;
    const d = document.createElement('div');
    d.className = 'ann' + (e.id === activeId ? ' active' : '');
    d.style.left = toPct(e.rectTL.x0, w1);
    d.style.top = toPct(e.rectTL.y0, h1);
    d.style.width = toPct(e.rectTL.x1 - e.rectTL.x0, w1);
    d.style.height = toPct(e.rectTL.y1 - e.rectTL.y0, h1);
    d.title = e.destName ?? `marker p${e.srcPage + 1} → ${e.target ? `p${e.target.page + 1} @(${e.target.x.toFixed(1)},${e.target.y.toFixed(1)})` : 'no target'}`;
    d.onclick = () => jumpTo(e);
    (e as any).annEl = d;
    holder.appendChild(d);
  }
  ui.holder = holder;
  pagesUI.set(pageIdx, ui);
  return ui;
}

function clearTargetBox(ui: any) {
  if (ui.box) ui.box.remove();
  ui.box = undefined;
}

function jumpTo(e: Entry) {
  activeId = e.id;
  for (const x of entries) x.rowEl?.classList.remove('active');
  for (const x of entries) x.annEl?.classList.remove('active');
  e.rowEl?.classList.add('active');
  e.annEl?.classList.add('active');
  log(`→ p${e.srcPage + 1} marker → ${e.target ? `p${e.target.page + 1} 目标点(${e.target.x.toFixed(2)}, ${e.target.y.toFixed(2)})` : '无定位目标'} ${e.destName ?? ''} ${e.error ? `ERR ${e.error}` : ''}`);
  if (!e.target) {
    if (e.destName) log('  named dest 解析失败或无 /XYZ 型坐标,仅记录');
    return;
  }
  void getPageUI(e.target.page).then((ui) => {
    clearTargetBox(ui);
    const d = document.createElement('div');
    d.className = 'target flash';
    const { w1, h1 } = ui;
    const pct = (v: number, t: number) => ((v / t) * 100).toFixed(3) + '%';
    // entry first line: from target point down ~ font height, to end of text width guess
    d.style.left = pct(Math.max(e.target!.x - 1, 0), w1);
    d.style.top = pct(Math.max(e.target!.y - 2, 0), h1);
    d.style.width = pct(Math.min(w1 - Math.max(e.target!.x - 1, 0), 320), w1);
    d.style.height = pct(13, h1);
    ui.holder.appendChild(d);
    ui.box = d;
    // server block overlay (authoritative bbox from DB) if matched
    if (e.block && e.block.location) {
      const loc = e.block.location;
      const b = document.createElement('div');
      b.className = 'blockrect';
      b.style.left = pct(loc.bboxX0, w1);
      b.style.top = pct(loc.bboxY0, h1);
      b.style.width = pct(loc.bboxX1 - loc.bboxX0, w1);
      b.style.height = pct(loc.bboxY1 - loc.bboxY0, h1);
      b.title = `server block ordinal=${e.block.ordinal}`;
      ui.holder.appendChild(b);
      (ui as any).blockrect = b;
    }
    const pw = ui.holder.closest('.pw')!;
    pw.scrollIntoView({ behavior: 'smooth', block: 'center' });
  });
}

// ---- loaders ----
async function fromFile(file: File) {
  try {
    blocks = [];
    setStatus(`读取 ${file.name}…`);
    const buf = await file.arrayBuffer();
    await loadPDF(buf, file.name);
  } catch (err: any) {
    setStatus('读取文件失败 — 见底部日志', true);
    log(`💥 fromFile 失败: ${err?.stack ?? err}`);
  }
}

async function fromDocId(id: string) {
  try {
    log(`== fetch doc ${id}`);
    setStatus('拉取 /api 文档…');
    const contentRes = await fetch(`/api/document/${id}/content`);
    if (!contentRes.ok) throw new Error(`content HTTP ${contentRes.status}`);
    const blocksRes = await fetch(`/api/document/${id}/blocks`);
    if (blocksRes.ok) {
      const bj = await blocksRes.json();
      blocks = bj.items ?? [];
      log(`blocks: ${blocks.length}`);
    } else {
      blocks = [];
      log(`blocks HTTP ${blocksRes.status} — 跳过对拍`);
    }
    const buf = await contentRes.arrayBuffer();
    await loadPDF(buf, `doc ${id}`);
  } catch (err: any) {
    setStatus('doc 加载失败 — 见底部日志', true);
    log(`💥 fromDocId 失败: ${err?.stack ?? err}`);
  }
}

const fileInput = $<HTMLInputElement>('input#file');
$('button#fileBtn').onclick = () => fileInput.click(); // 打开系统文件选择器
fileInput.onchange = () => {
  const f = fileInput.files?.[0];
  if (f) fromFile(f);
};
async function loadUrl(url: string, label: string) {
  try {
    setStatus(`加载 ${label}…`);
    const res = await fetch(url);
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    await loadPDF(await res.arrayBuffer(), label);
  } catch (err: any) {
    setStatus(`${label} 加载失败 — 见底部日志`, true);
    log(`💥 loadUrl(${url}) 失败: ${err?.stack ?? err}`);
  }
}
$('button#sampleBtn').onclick = () => loadUrl('/sample_links.pdf', '内置样例 sample_links.pdf');
$('button#realBtn').onclick = () => loadUrl('/done_example.pdf', '真实论文 .example/DOne…pdf');
// 打开页面即自动硬加载真实论文,零点击、零文件选择
void loadUrl('/done_example.pdf', '真实论文 .example/DOne…pdf');
// 拖拽上传(Windows 资源管理器可直接把 PDF 拖进页面,绕开 WSL 路径选择问题)
for (const el of [pagesEl, document.body]) {
  el.addEventListener('dragover', (ev) => ev.preventDefault());
  el.addEventListener('drop', (ev) => {
    ev.preventDefault();
    const f = ev.dataTransfer?.files?.[0];
    if (f) fromFile(f);
  });
}
$('button#docBtn').onclick = () => {
  const id = $<HTMLInputElement>('input#docid').value.trim();
  if (id) fromDocId(id);
};
jsonBtn.onclick = () => {
  const dump = entries.map((e) => ({
    srcPage: e.srcPage + 1,
    marker: e.rectTL,
    destName: e.destName,
    uri: e.uri,
    target: e.target ? { page: e.target.page + 1, x: +e.target.x.toFixed(2), y: +e.target.y.toFixed(2) } : null,
    blockOrdinal: e.block?.ordinal ?? null,
    error: e.error,
  }));
  navigator.clipboard.writeText(JSON.stringify(dump, null, 1)).then(() => log(`已复制 ${dump.length} 条 JSON`));
};
