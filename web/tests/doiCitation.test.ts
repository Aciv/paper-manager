import assert from 'node:assert/strict';
import test from 'node:test';

import {
  DOI_CITATION_FORMATS,
  citationDownloadFilename,
  describeDoiCitationError,
  doiCitationErrorMessage,
  doiResolverUrl,
  fetchDoiCitation,
  htmlToText,
  normalizeDoi,
  prepareCitationBody,
} from '../src/doiCitation.ts';

const bibtex = DOI_CITATION_FORMATS.find((format) => format.id === 'bibtex')!;
const cslJSON = DOI_CITATION_FORMATS.find((format) => format.id === 'csl-json')!;
const apa = DOI_CITATION_FORMATS.find((format) => format.id === 'apa')!;

test('normalizes pasted DOI forms for resolution', () => {
  assert.equal(normalizeDoi('10.1038/nature12373'), '10.1038/nature12373');
  assert.equal(normalizeDoi('  https://doi.org/10.1038/nature12373  '), '10.1038/nature12373');
  assert.equal(normalizeDoi('http://dx.doi.org/10.1038/NATURE12373.'), '10.1038/NATURE12373');
  assert.equal(normalizeDoi('doi: 10.1038/nature12373'), '10.1038/nature12373');
  assert.equal(normalizeDoi('not a doi'), '');
  assert.equal(normalizeDoi(''), '');
  assert.equal(normalizeDoi(undefined), '');
});

test('builds a resolver URL without losing suffix separators', () => {
  assert.equal(doiResolverUrl('10.1038/nature12373'), 'https://doi.org/10.1038/nature12373');
  assert.equal(
    doiResolverUrl('https://doi.org/10.1000/a?b=1#frag'),
    'https://doi.org/10.1000/a%3Fb%3D1%23frag',
  );
  assert.equal(
    doiResolverUrl('10.1016/S0140-6736(97)11096-0'),
    'https://doi.org/10.1016/S0140-6736(97)11096-0',
  );
});

test('exposes every requested content-negotiation format', () => {
  assert.equal(new Set(DOI_CITATION_FORMATS.map((format) => format.id)).size, DOI_CITATION_FORMATS.length);
  assert.equal(bibtex.accept, 'application/x-bibtex');
  assert.equal(cslJSON.accept, 'application/vnd.citationstyles.csl+json');
  assert.equal(apa.accept, 'text/x-bibliography; style=apa');
  assert.equal(DOI_CITATION_FORMATS.find((format) => format.id === 'gbt7714')?.accept,
    'text/x-bibliography; style=china-national-standard-gb-t-7714-2015-numeric');
});

test('formatted citations are converted from HTML fragments to text', () => {
  const formatted = '<i>Nanometre</i>-scale &amp; <sup>13</sup>C &#x2013; thermometry&nbsp;here';
  assert.equal(htmlToText(formatted), 'Nanometre-scale & 13C – thermometry here');
  assert.equal(htmlToText('<div>First</div><div>Second</div>'), 'First\nSecond');
});

test('exchanges keep their native syntax while CSL JSON is pretty-printed', () => {
  assert.equal(prepareCitationBody('  @article{x, title={T}}\n', bibtex), '@article{x, title={T}}');
  assert.equal(prepareCitationBody('{"title":"T"}', cslJSON), '{\n  "title": "T"\n}');
  assert.equal(prepareCitationBody('<i>Title</i> &amp; more', apa), 'Title & more');
});

test('maps provider errors to actionable messages', () => {
  assert.equal(
    doiCitationErrorMessage(406, '{"code":"style-not-found","message":"Style [vancouver] does not exist"}'),
    'doi.org 不支持该引文格式：Style [vancouver] does not exist',
  );
  assert.equal(doiCitationErrorMessage(404, 'not found'), 'doi.org 未找到该 DOI。');
  assert.equal(doiCitationErrorMessage(429, ''), 'doi.org 请求过于频繁，请稍后再试。');
  assert.equal(doiCitationErrorMessage(503, ''), 'doi.org 暂时不可用（HTTP 503），请稍后重试。');
  assert.equal(describeDoiCitationError(new Error('boom')), 'boom');
  const aborted = new Error('aborted');
  aborted.name = 'AbortError';
  assert.equal(describeDoiCitationError(aborted), '');
});

test('derives a safe download filename from the DOI', () => {
  assert.equal(citationDownloadFilename('https://doi.org/10.1038/nature12373', bibtex), 'citation-10.1038-nature12373.bib');
  assert.equal(citationDownloadFilename('', cslJSON), 'citation.json');
});

test('fetches the negotiated representation and caches successful lookups', async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ url: string; accept: string }> = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    calls.push({ url: String(input), accept: headers.get('accept') ?? '' });
    return new Response(' @article{x, title={T}}\n', {
      status: 200,
      headers: { 'content-type': 'application/x-bibtex' },
    });
  }) as typeof fetch;

  try {
    const first = await fetchDoiCitation('10.1000/cache-test', bibtex);
    const second = await fetchDoiCitation('https://doi.org/10.1000/cache-test', bibtex);
    assert.equal(first, '@article{x, title={T}}');
    assert.equal(second, first);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, 'https://doi.org/10.1000/cache-test');
    assert.equal(calls[0].accept, 'application/x-bibtex');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('does not cache failed lookups and surfaces the provider error', async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    return new Response('{"message":"Style [x] does not exist"}', {
      status: 406,
      headers: { 'content-type': 'application/json' },
    });
  }) as typeof fetch;

  try {
    await assert.rejects(
      fetchDoiCitation('10.1000/failure-test', apa),
      /doi\.org 不支持该引文格式：Style \[x\] does not exist/,
    );
    await assert.rejects(fetchDoiCitation('10.1000/failure-test', apa), /不支持该引文格式/);
    assert.equal(calls, 2);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
