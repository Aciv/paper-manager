import { useEffect, useRef, useState, type ClipboardEvent as ReactClipboardEvent } from 'react';

import { LibrarySelect } from './LibrarySelect';
import {
  TRANSLATION_LANGUAGE_OPTIONS,
  TRANSLATION_PROVIDER_LABELS,
  TRANSLATION_PROVIDER_OPTIONS,
  TRANSLATION_SOURCE_OPTIONS,
  TRANSLATION_TARGET_OPTIONS,
  chromeTranslationAvailability,
  describeTranslationError,
  detectSourceLanguage,
  isAbortError,
  loadTranslationSettings,
  normalizeTextLineBreaks,
  prepareChromeTranslation,
  resolveTargetLanguage,
  saveTranslationSettings,
  stripMarkupForTranslation,
  translateText,
  type ChromeTranslationAvailability,
  type TranslationLanguage,
  type TranslationProviderID,
  type TranslationProviderSetting,
  type TranslationRequestSeed,
  type TranslationSettings,
} from './translation';
import './TranslationPanel.css';

type TranslationRequest = TranslationRequestSeed & {
  requestId: string;
  autoTranslate: boolean;
};

type TranslationLayout = 'bilingual' | 'target';
const TRANSLATION_LAYOUT_STORAGE = 'papersManager.translation.layout.v1';

function loadTranslationLayout(): TranslationLayout {
  if (typeof window === 'undefined') return 'bilingual';
  try {
    return window.localStorage.getItem(TRANSLATION_LAYOUT_STORAGE) === 'target' ? 'target' : 'bilingual';
  } catch {
    return 'bilingual';
  }
}

type UnitStatus = 'idle' | 'dirty' | 'waiting' | 'loading' | 'done' | 'error' | 'skipped';

type TranslationUnit = {
  id: string;
  label: string;
  source: string;
  status: UnitStatus;
  translation?: string;
  provider?: TranslationProviderID;
  detectedLanguage?: TranslationLanguage;
  targetLanguage?: TranslationLanguage;
  fallback?: boolean;
  error?: string;
};

function itemsForRequest(request: TranslationRequest): Array<{ id: string; label: string; text: string }> {
  if (request.kind === 'text') {
    const text = normalizeTextLineBreaks(request.text);
    return text
      ? [{ id: 'selection', label: request.pageNumber ? `第 ${request.pageNumber} 页选中文本` : '选中文本', text }]
      : [];
  }
  return request.blocks
    .map((block, index) => ({
      id: `${block.blockId || 'block'}-${index}`,
      label: `第 ${block.pageNumber ?? '?'} 页 · ${block.blockType || 'Block'}`,
      text: normalizeTextLineBreaks(stripMarkupForTranslation(block.text)),
    }))
    .filter((item) => item.text.length > 0);
}

function manualUnit(): TranslationUnit {
  return { id: 'manual', label: '手动输入', source: '', status: 'idle' };
}

function unitsForRequest(request: TranslationRequest): TranslationUnit[] {
  const items = itemsForRequest(request);
  return items.length > 0
    ? items.map((item) => ({
      id: item.id,
      label: item.label,
      source: item.text,
      status: request.autoTranslate ? 'waiting' : 'dirty',
    }))
    : [manualUnit()];
}

function languageLabel(language: TranslationLanguage): string {
  if (language === 'auto') return '自动检测';
  return TRANSLATION_LANGUAGE_OPTIONS.find((option) => option.id === language)?.label ?? language;
}

const SOURCE_SELECT_OPTIONS = TRANSLATION_SOURCE_OPTIONS.map((option) => ({ value: option.id, label: option.label }));
const TARGET_SELECT_OPTIONS = TRANSLATION_TARGET_OPTIONS.map((option) => ({ value: option.id, label: option.label }));
const PROVIDER_SELECT_OPTIONS = TRANSLATION_PROVIDER_OPTIONS.map((option) => ({ value: option.id, label: option.label }));
const LAYOUT_SELECT_OPTIONS = [
  { value: 'bilingual', label: '对照' },
  { value: 'target', label: '仅译文' },
];

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // Permission can be denied; fall through to the selection-based path.
    }
  }
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  const copied = document.execCommand('copy');
  textarea.remove();
  if (!copied) throw new Error('copy failed');
}

function CopyIcon() {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <rect x="8.5" y="8.5" width="10.5" height="10.5" rx="2" />
    <path d="M5.5 15.5V6a1.5 1.5 0 0 1 1.5-1.5h9.5" />
  </svg>;
}

function SwapIcon() {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M4 8h13l-3-3" />
    <path d="M20 16H7l3 3" />
  </svg>;
}

function TranslateIcon() {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M5 12h13" />
    <path d="m13 6 6 6-6 6" />
  </svg>;
}

// Bing-style translator surface: a language bar, editable source panes and
// matched translation panes. PDF selections seed the source, but the source is
// always editable and a blank manual unit is available without a selection.
export function TranslationPanel({ request }: { request?: TranslationRequest }) {
  const [settings, setSettings] = useState<TranslationSettings>(() => loadTranslationSettings());
  const [layout, setLayout] = useState<TranslationLayout>(() => loadTranslationLayout());
  const [units, setUnits] = useState<TranslationUnit[]>(() => [manualUnit()]);
  const unitsRef = useRef<TranslationUnit[]>(units);
  const [notice, setNotice] = useState('');
  const [translateNonce, setTranslateNonce] = useState(0);
  const [chromeCheckNonce, setChromeCheckNonce] = useState(0);
  const [chromeAvailability, setChromeAvailability] = useState<ChromeTranslationAvailability | 'checking'>('checking');
  const [chromeProgress, setChromeProgress] = useState(0);
  const abortRef = useRef<AbortController | undefined>(undefined);
  // True only after an explicit 翻译 action. Selection seeding and source edits
  // leave it false, so incidental re-renders or language changes cannot start a
  // translation behind the user's back.
  const autoTranslateArmedRef = useRef(false);

  const firstText = units.find((unit) => unit.source.trim())?.source.trim() ?? '';
  const chromeSource = settings.sourceLanguage === 'auto'
    ? (firstText ? detectSourceLanguage(firstText) : 'en')
    : settings.sourceLanguage;
  const chromeTarget = resolveTargetLanguage(firstText, settings.targetLanguage, settings.sourceLanguage);

  // Keep the latest units synchronously so a click/effect can read edits that
  // were committed in this same event loop turn.
  function commitUnits(update: TranslationUnit[] | ((current: TranslationUnit[]) => TranslationUnit[])) {
    const next = typeof update === 'function' ? update(unitsRef.current) : update;
    unitsRef.current = next;
    setUnits(next);
  }

  function updateSettings(patch: Partial<TranslationSettings>) {
    setSettings((current) => {
      const next = { ...current, ...patch };
      saveTranslationSettings(next);
      return next;
    });
    setNotice('');
  }

  function updateLayout(next: TranslationLayout) {
    setLayout(next);
    try {
      window.localStorage.setItem(TRANSLATION_LAYOUT_STORAGE, next);
    } catch {
      // A disabled storage must not prevent switching the view.
    }
  }

  function swapLanguages() {
    const target = resolveTargetLanguage(firstText, settings.targetLanguage, settings.sourceLanguage);
    const source = settings.sourceLanguage === 'auto'
      ? (firstText ? detectSourceLanguage(firstText) : 'en')
      : settings.sourceLanguage;
    updateSettings({ sourceLanguage: target, targetLanguage: source });
  }

  function showNotice(message: string) {
    setNotice(message);
    window.setTimeout(() => setNotice((current) => current === message ? '' : current), 3000);
  }

  // A PDF selection replaces the editable units. A source-only seed shows the
  // text and waits for an explicit 翻译 action; the reader's context-menu
  // 翻译 action and the panel button carry autoTranslate/translateNonce.
  useEffect(() => {
    abortRef.current?.abort();
    autoTranslateArmedRef.current = Boolean(request?.autoTranslate);
    commitUnits(request ? unitsForRequest(request) : [manualUnit()]);
    setNotice('');
    if (request?.autoTranslate) {
      setChromeCheckNonce((value) => value + 1);
      setTranslateNonce((value) => value + 1);
    }
  }, [request?.requestId]);

  // Check Chrome only on provider/request/language changes or an explicit
  // translate action. Typing alone must not repeatedly probe or auto-translate.
  useEffect(() => {
    if (settings.provider !== 'chrome') {
      setChromeAvailability('checking');
      return;
    }
    const probeText = unitsRef.current.find((unit) => unit.source.trim())?.source.trim() ?? '';
    if (!probeText) {
      setChromeAvailability('checking');
      return;
    }
    let cancelled = false;
    setChromeAvailability('checking');
    const source = settings.sourceLanguage === 'auto' ? detectSourceLanguage(probeText) : settings.sourceLanguage;
    const target = resolveTargetLanguage(probeText, settings.targetLanguage, settings.sourceLanguage);
    void chromeTranslationAvailability(source, target).then((availability) => {
      if (!cancelled) setChromeAvailability(availability);
    });
    return () => { cancelled = true; };
  }, [settings.provider, settings.sourceLanguage, settings.targetLanguage, chromeCheckNonce]);

  // A model download started in another tab can finish while this panel is
  // open; poll only while Chrome reports that a download is in progress.
  useEffect(() => {
    if (settings.provider !== 'chrome' || chromeAvailability !== 'downloading') return;
    const timer = window.setInterval(() => {
      const probeText = unitsRef.current.find((unit) => unit.source.trim())?.source.trim() ?? '';
      if (!probeText) return;
      const source = settings.sourceLanguage === 'auto' ? detectSourceLanguage(probeText) : settings.sourceLanguage;
      const target = resolveTargetLanguage(probeText, settings.targetLanguage, settings.sourceLanguage);
      void chromeTranslationAvailability(source, target).then((availability) => {
        if (availability === 'downloading') return;
        setChromeAvailability(availability);
        if (availability === 'available') setTranslateNonce((value) => value + 1);
      });
    }, 2000);
    return () => window.clearInterval(timer);
  }, [settings.provider, chromeAvailability, settings.sourceLanguage, settings.targetLanguage]);

  // Translation runs only on explicit triggers: a new PDF selection, a
  // provider/language change, a Chrome availability change, or the 翻译 button.
  // Source edits merely mark the unit dirty until the user asks to translate.
  useEffect(() => {
    if (!autoTranslateArmedRef.current) return;
    abortRef.current?.abort();
    const currentUnits = unitsRef.current
      .filter((unit) => unit.source.trim())
      .map((unit) => ({ id: unit.id, source: unit.source.trim() }));
    if (currentUnits.length === 0) return;

    if (settings.provider === 'chrome' && chromeAvailability !== 'available') {
      commitUnits((current) => current.map((unit) => (
        unit.source.trim() ? { ...unit, status: 'waiting' as const } : unit
      )));
      return;
    }

    const controller = new AbortController();
    abortRef.current = controller;
    commitUnits((current) => current.map((unit) => (
      unit.source.trim() ? { ...unit, status: 'waiting' as const } : unit
    )));

    void (async () => {
      if (settings.provider === 'chrome') {
        const availability = await chromeTranslationAvailability(chromeSource, chromeTarget);
        if (controller.signal.aborted) return;
        if (availability !== 'available') {
          setChromeAvailability(availability);
          commitUnits((current) => current.map((unit) => (
            unit.source.trim() ? { ...unit, status: 'waiting' as const } : unit
          )));
          return;
        }
      }

      for (const item of currentUnits) {
        if (controller.signal.aborted) return;
        commitUnits((current) => current.map((unit) => (
          unit.id === item.id ? { ...unit, status: 'loading' as const } : unit
        )));
        try {
          const result = await translateText(item.source, {
            sourceLanguage: settings.sourceLanguage,
            targetLanguage: settings.targetLanguage,
            provider: settings.provider,
            signal: controller.signal,
          });
          if (controller.signal.aborted) return;
          commitUnits((current) => current.map((unit) => (
            unit.id === item.id && unit.source.trim() === item.source
              ? {
                ...unit,
                status: result.skipped ? 'skipped' as const : 'done' as const,
                translation: result.text,
                provider: result.provider,
                detectedLanguage: result.detectedLanguage,
                targetLanguage: result.targetLanguage,
                fallback: result.fallback,
                error: undefined,
              }
              : unit
          )));
        } catch (error) {
          if (controller.signal.aborted || isAbortError(error)) return;
          commitUnits((current) => current.map((unit) => (
            unit.id === item.id && unit.source.trim() === item.source
              ? { ...unit, status: 'error' as const, error: describeTranslationError(error) }
              : unit
          )));
        }
      }
    })();

    return () => controller.abort();
  }, [translateNonce, settings.provider, settings.sourceLanguage, settings.targetLanguage, chromeAvailability]);

  function translateNow() {
    autoTranslateArmedRef.current = true;
    if (settings.provider === 'chrome' && chromeAvailability !== 'available') {
      setChromeCheckNonce((value) => value + 1);
    }
    setTranslateNonce((value) => value + 1);
  }

  // Pasting PDF text from the clipboard cleans its visual line wraps too,
  // while typing keeps whatever line breaks the user intentionally enters.
  function pasteNormalizedSource(event: ReactClipboardEvent<HTMLTextAreaElement>, id: string) {
    const pasted = event.clipboardData.getData('text/plain');
    if (!pasted) return;
    const normalized = normalizeTextLineBreaks(pasted);
    if (normalized === pasted) return;
    event.preventDefault();
    const textarea = event.currentTarget;
    const start = textarea.selectionStart ?? textarea.value.length;
    const end = textarea.selectionEnd ?? start;
    const next = `${textarea.value.slice(0, start)}${normalized}${textarea.value.slice(end)}`;
    updateUnitSource(id, next);
    window.requestAnimationFrame(() => {
      const caret = start + normalized.length;
      textarea.setSelectionRange(caret, caret);
    });
  }

  function updateUnitSource(id: string, source: string) {
    autoTranslateArmedRef.current = false;
    abortRef.current?.abort();
    setNotice('');
    commitUnits((current) => current.map((unit) => {
      if (unit.id === id) {
        return {
          ...unit,
          source,
          status: source.trim() ? 'dirty' as const : 'idle' as const,
          translation: undefined,
          provider: undefined,
          detectedLanguage: undefined,
          targetLanguage: undefined,
          fallback: undefined,
          error: undefined,
        };
      }
      if (unit.status === 'loading' || unit.status === 'waiting') {
        return { ...unit, status: 'dirty' as const };
      }
      return unit;
    }));
  }

  function clearAll() {
    abortRef.current?.abort();
    autoTranslateArmedRef.current = false;
    commitUnits([manualUnit()]);
    setNotice('已清空，可手动输入文本。');
  }

  async function downloadChromeModel() {
    if (!firstText) return;
    setNotice('');
    setChromeAvailability('downloading');
    setChromeProgress(0);
    try {
      await prepareChromeTranslation(chromeSource, chromeTarget, (loaded) => setChromeProgress(loaded));
      setChromeAvailability('available');
      setTranslateNonce((value) => value + 1);
      showNotice('Chrome 本地翻译模型已就绪。');
    } catch (error) {
      setChromeAvailability('downloadable');
      showNotice(describeTranslationError(error));
    }
  }

  async function copyValue(text: string, label: string) {
    try {
      await copyText(text);
      showNotice(`${label}已复制。`);
    } catch {
      showNotice('复制失败，请手动选择内容复制。');
    }
  }

  const sourceUnits = units.filter((unit) => unit.source.trim());
  const translatedUnits = units.filter((unit) => unit.status === 'done' && unit.translation?.trim());
  const allTranslations = translatedUnits
    .map((unit) => translatedUnits.length > 1
      ? `${unit.label}\n${unit.translation}`
      : unit.translation)
    .join('\n\n');
  const sourceCharacters = units.reduce((total, unit) => total + unit.source.length, 0);
  const busy = units.some((unit) => unit.status === 'loading' || unit.status === 'waiting');
  const chromeBlocked = settings.provider === 'chrome'
    && (chromeAvailability === 'downloadable' || chromeAvailability === 'downloading'
      || chromeAvailability === 'unavailable' || chromeAvailability === 'unsupported');

  const engineNote = settings.provider === 'auto'
    ? '自动选择引擎'
    : settings.provider === 'microsoft'
      ? '免密钥在线翻译'
      : settings.provider === 'chrome'
        ? '本地模型，不上传文本'
        : '备用接口，额度有限';
  const providerHint = settings.provider === 'auto'
    ? '优先使用已就绪的 Chrome 本地模型；否则使用 Microsoft 在线翻译，失败时回退到 MyMemory。'
    : settings.provider === 'microsoft'
      ? '免密钥在线接口；选中文本会发送给 Microsoft，不需要登录或填写 API Key。'
      : settings.provider === 'chrome'
        ? '使用 Chrome 浏览器内置的本地翻译模型，文本不会上传；首次使用需要下载模型。'
        : 'MyMemory 公共接口；单次最多 500 UTF-8 字节，匿名每日额度有限，只作为兜底。';

  return <section className={`library-panel library-panel-translation${layout === 'target' ? ' layout-target' : ''}`} aria-label="翻译">
    <div className="translation-language-bar">
      <div className="translation-language-select">
        <LibrarySelect value={settings.sourceLanguage} options={SOURCE_SELECT_OPTIONS}
          ariaLabel="源语言"
          onChange={(value) => updateSettings({ sourceLanguage: value as TranslationLanguage })} />
      </div>
      <button className="translation-swap" type="button" title="交换源语言和目标语言" aria-label="交换源语言和目标语言"
        onClick={swapLanguages}>
        <SwapIcon />
      </button>
      <div className="translation-language-select">
        <LibrarySelect value={settings.targetLanguage} options={TARGET_SELECT_OPTIONS}
          ariaLabel="目标语言"
          onChange={(value) => updateSettings({ targetLanguage: value as TranslationLanguage })} />
      </div>
    </div>

    <div className="translation-engine-row">
      <div className="translation-engine" title={providerHint}>
        <span>引擎</span>
        <LibrarySelect value={settings.provider} options={PROVIDER_SELECT_OPTIONS}
          ariaLabel="翻译引擎"
          onChange={(value) => updateSettings({ provider: value as TranslationProviderSetting })} />
      </div>
      <span className="translation-engine-note" title={providerHint}>{engineNote}</span>
      <div className="translation-layout" title="切换原文/译文显示方式">
        <span>显示</span>
        <LibrarySelect value={layout} options={LAYOUT_SELECT_OPTIONS}
          ariaLabel="显示方式"
          onChange={(value) => updateLayout(value as TranslationLayout)} />
      </div>
    </div>

    {settings.provider === 'chrome' && <div className={`translation-chrome-status ${chromeAvailability}`}>
      {sourceUnits.length === 0
        ? <span>输入文本后会自动检查 Chrome 本地模型。</span>
        : <>
          {chromeAvailability === 'checking' && <span>正在检查 Chrome 本地翻译…</span>}
          {chromeAvailability === 'available' && <span>Chrome 本地模型已就绪，译文不会上传。</span>}
          {chromeAvailability === 'downloadable' && <>
            <span>首次使用需要下载 Chrome 语言模型，只需下载一次。</span>
            <button type="button" onClick={() => void downloadChromeModel()}>下载并启用本地翻译</button>
          </>}
          {chromeAvailability === 'downloading' && <>
            <span>正在下载本地模型… {Math.round(chromeProgress * 100)}%</span>
            <div className="translation-progress" role="progressbar" aria-valuemin={0} aria-valuemax={100}
              aria-valuenow={Math.round(chromeProgress * 100)}>
              <span style={{ width: `${Math.max(0, Math.min(100, chromeProgress * 100))}%` }} />
            </div>
          </>}
          {chromeAvailability === 'unavailable' && <span>当前设备或语言对不支持 Chrome 本地翻译，请选择其他服务。</span>}
          {chromeAvailability === 'unsupported' && <span>当前浏览器没有 Chrome 内置翻译 API，请选择其他服务。</span>}
        </>}
    </div>}

    <div className={`translation-units${units.length === 1 ? ' single-unit' : ''}`}>
      {units.map((unit) => <article className="translation-unit" key={unit.id}>
        <header className="translation-unit-header">
          <span className="translation-unit-label">{unit.label}</span>
          {unit.provider && <span className="translation-badge">
            {TRANSLATION_PROVIDER_LABELS[unit.provider]}{unit.fallback ? ' · 自动回退' : ''}
          </span>}
        </header>
        <div className="translation-unit-panes">
          {layout === 'bilingual' && <section className="translation-pane translation-pane-source">
            <header>
              <span>{unit.detectedLanguage ? `原文 · ${languageLabel(unit.detectedLanguage)}` : '原文'}</span>
              <span className="translation-char-count">{unit.source.length} 字符</span>
              {unit.source.trim() && <button className="translation-icon-button" type="button" title="复制原文" aria-label="复制原文"
                onClick={() => void copyValue(unit.source, '原文')}>
                <CopyIcon />
              </button>}
            </header>
            <div className="translation-pane-body translation-source-body">
              <textarea className="translation-source-input" value={unit.source}
                placeholder="输入或粘贴要翻译的文本…" spellCheck={false}
                aria-label={`${unit.label} 原文`}
                onPaste={(event) => pasteNormalizedSource(event, unit.id)}
                onChange={(event) => updateUnitSource(unit.id, event.target.value)} />
            </div>
          </section>}
          <section className="translation-pane translation-pane-target">
            <header>
              <span>{unit.targetLanguage ? `译文 · ${languageLabel(unit.targetLanguage)}` : '译文'}</span>
              {unit.translation && <span className="translation-char-count">{unit.translation.length} 字符</span>}
              {unit.status === 'done' && unit.translation && <button className="translation-icon-button"
                type="button" title="复制译文" aria-label="复制译文"
                onClick={() => void copyValue(unit.translation ?? '', '译文')}>
                <CopyIcon />
              </button>}
            </header>
            <div className="translation-pane-body">
              {unit.status === 'idle' && !unit.source.trim() && <p className="translation-placeholder">
                {layout === 'target' ? '切换到「对照」输入原文。' : '输入或粘贴文本，然后点击「翻译」。'}
              </p>}
              {(unit.status === 'dirty' || unit.status === 'idle') && unit.source.trim() && <p className="translation-draft-hint">
                点击底部「翻译」获取译文。
              </p>}
              {unit.status === 'waiting' && <p className="translation-loading">
                {settings.provider === 'chrome' && chromeAvailability !== 'available'
                  ? '等待 Chrome 本地模型…'
                  : '等待翻译…'}
              </p>}
              {unit.status === 'loading' && <p className="translation-loading">
                <span className="translation-spinner" aria-hidden="true" />翻译中…
              </p>}
              {unit.status === 'error' && <p className="translation-error">{unit.error || '翻译失败。'}</p>}
              {unit.status === 'skipped' && <p className="translation-skipped">与目标语言相同，未翻译。</p>}
              {unit.status === 'done' && unit.translation && <p className="translation-result">{unit.translation}</p>}
            </div>
          </section>
        </div>
      </article>)}
    </div>

    <div className="translation-footer">
      <span className="translation-summary">
        {sourceUnits.length > 1 ? `${sourceUnits.length} 段 · ${sourceCharacters} 字符` : `${sourceCharacters} 字符`}
      </span>
      <button type="button" onClick={clearAll}>清空</button>
      {translatedUnits.length > 1 && <button type="button" disabled={!allTranslations}
        onClick={() => void copyValue(allTranslations, '全部译文')}>复制全部</button>}
      <button type="button" className="primary" disabled={sourceUnits.length === 0 || busy || chromeBlocked}
        title="翻译当前原文" onClick={translateNow}>
        <TranslateIcon />
        翻译
      </button>
    </div>

    {notice && <p className="translation-notice" role="status">{notice}</p>}
    <p className="translation-privacy">
      在线翻译会把文本发送给 Microsoft 或 MyMemory；论文内容不会写入本地存储。敏感文本请使用 Chrome 本地翻译。
    </p>
  </section>;
}
