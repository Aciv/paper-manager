import assert from 'node:assert/strict';
import test from 'node:test';

import {
  TRANSLATION_PROVIDER_OPTIONS,
  TRANSLATION_SOURCE_OPTIONS,
  TRANSLATION_TARGET_OPTIONS,
  TranslationCache,
  chromeTranslationAvailability,
  chromeTranslationProvider,
  clearTranslationCache,
  describeTranslationError,
  detectSourceLanguage,
  microsoftTranslationProvider,
  mymemoryTranslationProvider,
  normalizeTextLineBreaks,
  protectTranslationSymbols,
  resolveTargetLanguage,
  restoreTranslationSymbols,
  splitTextByCodePoints,
  splitTextByUtf8Bytes,
  stripMarkupForTranslation,
  translateText,
} from '../src/translation.ts';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

function restoreFetch(original: typeof fetch) {
  globalThis.fetch = original;
}

test('exposes the provider contract and selectable providers', () => {
  assert.deepEqual(
    TRANSLATION_PROVIDER_OPTIONS.map((option) => option.id),
    ['auto', 'microsoft', 'chrome', 'mymemory'],
  );
  assert.equal(typeof microsoftTranslationProvider.translate, 'function');
  assert.equal(typeof mymemoryTranslationProvider.translate, 'function');
  assert.equal(TRANSLATION_SOURCE_OPTIONS[0]?.label, '自动检测');
  assert.equal(TRANSLATION_TARGET_OPTIONS[0]?.label, '自动');
});

test('detects common source scripts and resolves automatic targets', () => {
  assert.equal(detectSourceLanguage('Hello world'), 'en');
  assert.equal(detectSourceLanguage('这是一篇论文'), 'zh-Hans');
  assert.equal(detectSourceLanguage('こんにちは世界'), 'ja');
  assert.equal(detectSourceLanguage('안녕하세요'), 'ko');
  assert.equal(detectSourceLanguage('Привет мир'), 'ru');
  assert.equal(resolveTargetLanguage('Hello world', 'auto'), 'zh-Hans');
  assert.equal(resolveTargetLanguage('这是一篇论文', 'auto'), 'en');
  assert.equal(resolveTargetLanguage('Hello world', 'auto', 'zh-Hans'), 'en');
  assert.equal(resolveTargetLanguage('这是一篇论文', 'auto', 'en'), 'zh-Hans');
  assert.equal(resolveTargetLanguage('Hello world', 'ja'), 'ja');
});

test('strips block Markdown and inline HTML before sending plain text', () => {
  assert.equal(
    stripMarkupForTranslation('# **Design2Code: Benchmarking**'),
    'Design2Code: Benchmarking',
  );
  assert.equal(
    stripMarkupForTranslation('**Chenglei Si**<sup>*1*</sup>, [Paper](https://example.com)'),
    'Chenglei Si*1*, Paper',
  );
  assert.equal(stripMarkupForTranslation('a < b and c > d'), 'a < b and c > d');
  assert.equal(stripMarkupForTranslation('line one<br>line two'), 'line one\nline two');
});

test('joins PDF visual line wraps while preserving paragraphs and lists', () => {
  assert.equal(normalizeTextLineBreaks('This is a\nwrapped line.'), 'This is a wrapped line.');
  assert.equal(normalizeTextLineBreaks('这是\n换行文本。'), '这是换行文本。');
  assert.equal(
    normalizeTextLineBreaks('First paragraph\nline.\n\nSecond paragraph\nline.'),
    'First paragraph line.\n\nSecond paragraph line.',
  );
  assert.equal(normalizeTextLineBreaks('inter-\nnational'), 'international');
  assert.equal(normalizeTextLineBreaks('• First item\n• Second item'), '• First item\n• Second item');
  assert.equal(normalizeTextLineBreaks('one\r\ntwo'), 'one two');
});

test('protects comparison and markup-sensitive symbols around Microsoft translation', () => {
  const source = 'If a <= b && c >= d, use x < y.';
  const protectedText = protectTranslationSymbols(source);
  assert.equal(protectedText, 'If a ≤ b ＆＆ c ≥ d, use x ＜ y.');
  assert.equal(restoreTranslationSymbols(protectedText), source);
});

test('splits long text within code-point and UTF-8 byte budgets', () => {
  const paragraphs = 'Machine translation needs terminology. '.repeat(80);
  const codePointChunks = splitTextByCodePoints(paragraphs, 120);
  assert.ok(codePointChunks.length > 1);
  assert.ok(codePointChunks.every((chunk) => Array.from(chunk).length <= 120));

  const chinese = '科学论文的机器翻译需要准确的术语。'.repeat(60);
  const byteChunks = splitTextByUtf8Bytes(chinese, 100);
  assert.ok(byteChunks.length > 1);
  assert.ok(byteChunks.every((chunk) => new TextEncoder().encode(chunk).length <= 100));
});

test('Microsoft provider sends a bare JSON array and returns a translation result', async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    return jsonResponse([{
      detectedLanguage: { language: 'en' },
      translations: [{ text: '如果 ≤ b 且 c ≥ d', to: 'zh-Hans' }],
    }]);
  }) as typeof fetch;

  try {
    const result = await microsoftTranslationProvider.translate('If a <= b && c >= d', 'auto', 'zh-Hans');
    assert.equal(result.text, '如果 <= b 且 c >= d');
    assert.equal(result.detectedLanguage, 'en');
    assert.equal(calls.length, 1);
    assert.match(calls[0].url, /^https:\/\/edge\.microsoft\.com\/translate\/translatetext\?/);
    assert.match(calls[0].url, /from=&/);
    assert.match(calls[0].url, /to=zh-Hans/);
    assert.deepEqual(JSON.parse(String(calls[0].init?.body)), ['If a ≤ b ＆＆ c ≥ d']);
  } finally {
    restoreFetch(originalFetch);
  }
});

test('MyMemory provider uses an explicit language pair and the public GET API', async () => {
  const originalFetch = globalThis.fetch;
  const calls: string[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input));
    return jsonResponse({
      responseData: { translatedText: '你好世界' },
      responseStatus: 200,
      quotaFinished: false,
    });
  }) as typeof fetch;

  try {
    const result = await mymemoryTranslationProvider.translate('Hello world', 'auto', 'zh-Hans');
    assert.equal(result.text, '你好世界');
    assert.equal(calls.length, 1);
    assert.match(calls[0], /^https:\/\/api\.mymemory\.translated\.net\/get\?/);
    assert.match(calls[0], /langpair=en%7Czh-CN/);
  } finally {
    restoreFetch(originalFetch);
  }
});

test('caches successful translations and does not cache failed ones', async () => {
  const originalFetch = globalThis.fetch;
  clearTranslationCache();
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    return jsonResponse([{ translations: [{ text: '你好' }] }]);
  }) as typeof fetch;

  try {
    const first = await translateText('Hello', { provider: 'microsoft', targetLanguage: 'zh-Hans' });
    const second = await translateText('Hello', { provider: 'microsoft', targetLanguage: 'zh-Hans' });
    assert.equal(first.text, '你好');
    assert.equal(first.targetLanguage, 'zh-Hans');
    assert.equal(second.text, '你好');
    assert.equal(calls, 1);
  } finally {
    restoreFetch(originalFetch);
    clearTranslationCache();
  }
});

test('does not cache failed translations', async () => {
  const originalFetch = globalThis.fetch;
  clearTranslationCache();
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    return new Response('upstream unavailable', { status: 503 });
  }) as typeof fetch;

  try {
    await assert.rejects(
      translateText('Failure case', { provider: 'microsoft', targetLanguage: 'zh-Hans' }),
      /Microsoft 翻译服务暂时不可用/,
    );
    await assert.rejects(
      translateText('Failure case', { provider: 'microsoft', targetLanguage: 'zh-Hans' }),
      /Microsoft 翻译服务暂时不可用/,
    );
    assert.equal(calls, 2);
  } finally {
    restoreFetch(originalFetch);
    clearTranslationCache();
  }
});

test('automatic mode falls back from Microsoft to MyMemory', async () => {
  const originalFetch = globalThis.fetch;
  clearTranslationCache();
  let calls = 0;
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls += 1;
    if (String(input).includes('edge.microsoft.com')) {
      return new Response('upstream unavailable', { status: 503 });
    }
    return jsonResponse({
      responseData: { translatedText: '你好' },
      responseStatus: 200,
      quotaFinished: false,
    });
  }) as typeof fetch;

  try {
    const result = await translateText('Hello', { provider: 'auto', targetLanguage: 'zh-Hans' });
    assert.equal(result.provider, 'mymemory');
    assert.equal(result.fallback, true);
    assert.equal(result.text, '你好');
    assert.equal(calls, 2);
  } finally {
    restoreFetch(originalFetch);
    clearTranslationCache();
  }
});

test('an explicitly chosen provider does not silently fall back', async () => {
  const originalFetch = globalThis.fetch;
  clearTranslationCache();
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    return new Response('upstream unavailable', { status: 503 });
  }) as typeof fetch;

  try {
    await assert.rejects(
      translateText('Hello', { provider: 'microsoft', targetLanguage: 'zh-Hans' }),
      /Microsoft 翻译服务暂时不可用/,
    );
    assert.equal(calls, 1);
  } finally {
    restoreFetch(originalFetch);
    clearTranslationCache();
  }
});

test('cache is LRU with TTL and an entry cap', () => {
  const cache = new TranslationCache({ maxEntries: 2, ttlMs: 60_000 });
  cache.set('a', { text: 'A' });
  cache.set('b', { text: 'B' });
  assert.equal(cache.get('a')?.text, 'A');
  cache.set('c', { text: 'C' });
  assert.equal(cache.get('b'), undefined);
  assert.equal(cache.get('a')?.text, 'A');
  assert.equal(cache.get('c')?.text, 'C');

  const expired = new TranslationCache({ ttlMs: -1 });
  expired.set('x', { text: 'X' });
  assert.equal(expired.get('x'), undefined);
});

test('skips translation when the text already uses the requested target language', async () => {
  const result = await translateText('这是一篇中文论文', { targetLanguage: 'zh-Hans' });
  assert.equal(result.skipped, true);
  assert.equal(result.text, '这是一篇中文论文');
});

test('reports Browser Chrome API availability without throwing', async () => {
  assert.equal(await chromeTranslationAvailability('en', 'zh-Hans'), 'unsupported');
});

test('Chrome provider translates bounded chunks locally', async () => {
  const holder = globalThis as { Translator?: unknown };
  const originalTranslator = holder.Translator;
  let calls = 0;
  holder.Translator = {
    availability: async () => 'available',
    create: async () => ({
      translate: async (text: string) => {
        calls += 1;
        return `T:${text.length}`;
      },
    }),
  };
  try {
    const result = await chromeTranslationProvider.translate('Sentence. '.repeat(1000), 'en', 'zh-Hans');
    assert.ok(calls >= 2);
    assert.match(result.text, /^T:\d+(\nT:\d+)+$/);
    assert.equal(result.detectedLanguage, 'en');
    assert.equal(await chromeTranslationAvailability('en', 'zh-Hans'), 'available');
  } finally {
    if (originalTranslator === undefined) delete holder.Translator;
    else holder.Translator = originalTranslator;
  }
});

test('describes ordinary and aborted errors', () => {
  assert.equal(describeTranslationError(new Error('boom')), 'boom');
  const abort = new Error('aborted');
  abort.name = 'AbortError';
  assert.equal(describeTranslationError(abort), '');
});
