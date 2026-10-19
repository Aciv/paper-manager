import type { PaperCitation } from './api';

export type CitationLinkMatch = {
  referenceId: string;
  confidence: number;
};

const DOI_PATTERN = /10\.\d{4,9}\/[\w.()/:;-]+/giu;
const ARXIV_PATTERN = /(?:arxiv\s*:\s*|abs\s*\/\s*)?(\d{4}\.\d{4,5})(?:v\d+)?/giu;
const TOKEN_PATTERN = /[\p{L}\p{N}]+/gu;
const STOP_WORDS = new Set([
  'and', 'the', 'with', 'from', 'for', 'of', 'in', 'on', 'to', 'a', 'an',
  'et', 'al', 'vol', 'volume', 'issue', 'pages', 'page', 'pp', 'doi', 'arxiv',
]);

function normalize(value: string): string {
  return value.normalize('NFKC').toLocaleLowerCase()
    // PDF text layers commonly preserve visual line-end hyphenation while the
    // parser's raw reference has already joined the word ("proto- typing").
    .replace(/([\p{L}\p{N}])-\s+(?=[\p{Ll}])/gu, '$1')
    .replace(/[^\p{L}\p{N}]+/gu, ' ').trim();
}

function identifiers(value: string): Set<string> {
  const result = new Set<string>();
  for (const match of value.matchAll(DOI_PATTERN)) result.add(`doi:${match[0].toLocaleLowerCase().replace(/[.,;:)]+$/u, '')}`);
  for (const match of value.matchAll(ARXIV_PATTERN)) result.add(`arxiv:${match[1]}`);
  return result;
}

function tokens(value: string): Set<string> {
  const result = new Set<string>();
  for (const match of normalize(value).matchAll(TOKEN_PATTERN)) {
    const token = match[0];
    if ((token.length >= 3 || /^\d{4}$/u.test(token)) && !STOP_WORDS.has(token)) result.add(token);
  }
  return result;
}

function coverage(needle: Set<string>, haystack: Set<string>): number {
  if (needle.size === 0) return 0;
  let shared = 0;
  for (const token of needle) if (haystack.has(token)) shared += 1;
  return shared / needle.size;
}

function scoreCitation(target: string, targetTokens: Set<string>, targetIdentifiers: Set<string>, citation: PaperCitation): number {
  const raw = citation.rawReference?.trim() ?? '';
  const searchable = [raw, citation.title ?? '', citation.doi ?? '', citation.sourceUrl ?? ''].join(' ');
  const citationIdentifiers = identifiers(searchable);
  for (const id of citationIdentifiers) if (targetIdentifiers.has(id)) return 1;

  const normalizedTarget = normalize(target);
  const normalizedRaw = normalize(raw);
  const normalizedTitle = normalize(citation.title ?? '');
  // rawReference came from this PDF and is the strongest text evidence after
  // an exact identifier. Structured titles are a fallback for split entries.
  if (normalizedRaw.length >= 24 && normalizedTarget.includes(normalizedRaw)) return 0.99;
  if (normalizedTitle.length >= 12 && normalizedTarget.includes(normalizedTitle)) return 0.97;

  const titleTokens = tokens(citation.title ?? '');
  const rawTokens = tokens(raw);
  const titleCoverage = coverage(titleTokens, targetTokens);
  const rawCoverage = coverage(rawTokens, targetTokens);
  const titleScore = titleTokens.size >= 3 ? 0.35 + titleCoverage * 0.6 : 0;
  const rawScore = rawTokens.size >= 4 ? 0.25 + rawCoverage * 0.65 : 0;
  return Math.max(titleScore, rawScore);
}

// A native PDF destination window can contain neighboring bibliography rows.
// Keep every strong candidate for the hover preview; callers that need a
// single result can use matchCitationTarget's ambiguity guard below.
export function matchCitationTargets(targetText: string, citations: PaperCitation[]): CitationLinkMatch[] {
  const target = targetText.trim();
  if (!target || citations.length === 0) return [];
  const targetTokens = tokens(target);
  const targetIdentifiers = identifiers(target);
  return citations
    .map(citation => ({ referenceId: citation.referenceId, confidence: scoreCitation(target, targetTokens, targetIdentifiers, citation) }))
    .filter(candidate => candidate.confidence >= 0.8)
    .sort((left, right) => right.confidence - left.confidence);
}

export function matchCitationTarget(targetText: string, citations: PaperCitation[]): CitationLinkMatch | undefined {
  const ranked = matchCitationTargets(targetText, citations);
  const best = ranked[0];
  if (!best) return undefined;
  const second = ranked[1];
  if (best.confidence < 1 && second && best.confidence - second.confidence < 0.08) return undefined;
  return best;
}
