import assert from 'node:assert/strict';
import test from 'node:test';
import type { PaperCitation } from '../src/api.ts';
import { matchCitationTarget, matchCitationTargets } from '../src/citationLinkMatcher.ts';

function citation(referenceId: string, rawReference: string, title = '', doi = ''): PaperCitation {
  return {
    referenceId,
    citingDocumentId: 'paper',
    rawReference,
    title,
    doi,
    authors: [],
    metadataSource: 'extraction',
    resolutionStatus: 'not_started',
    resolutionGeneration: 0,
    createdAt: '',
    updatedAt: '',
  };
}

test('matches an unnumbered reference by title text', () => {
  const result = matchCitationTarget(
    'Smith, J. Deep Learning for Document Analysis. Journal of Tests, 2024.',
    [
      citation('other', 'Brown. A different work.', 'A different work'),
      citation('wanted', 'Smith, J. Deep Learning for Document Analysis.', 'Deep Learning for Document Analysis'),
    ],
  );
  assert.equal(result?.referenceId, 'wanted');
  assert.ok((result?.confidence ?? 0) >= 0.95);
});

test('uses DOI as an exact high-confidence identifier', () => {
  const result = matchCitationTarget(
    'continued on the next page https://doi.org/10.1145/1234.5678',
    [citation('wanted', 'An otherwise truncated entry.', '', '10.1145/1234.5678')],
  );
  assert.deepEqual(result, { referenceId: 'wanted', confidence: 1 });
});

test('does not select an ambiguous neighboring reference window', () => {
  const target = 'Graph neural networks for source code. Graph neural networks for program analysis.';
  const citations = [
    citation('one', 'Graph neural networks for source code.'),
    citation('two', 'Graph neural networks for program analysis.'),
  ];
  const result = matchCitationTarget(
    target,
    citations,
  );
  assert.equal(result, undefined);
  assert.deepEqual(matchCitationTargets(target, citations).map(match => match.referenceId), ['one', 'two']);
});

test('matches parser raw text after removing visual PDF line-end hyphenation', () => {
  const result = matchCitationTarget(
    'Deep learning-based pro- totyping of android GUI from hand-drawn mockups.',
    [citation('wanted', 'Deep learning-based prototyping of android GUI from hand-drawn mockups.')],
  );
  assert.equal(result?.referenceId, 'wanted');
  assert.equal(result?.confidence, 0.99);
});

test('does not guess from weak generic overlap', () => {
  const result = matchCitationTarget(
    'Proceedings of the international conference, 2024.',
    [citation('one', 'A paper in proceedings of an international conference, 2024.')],
  );
  assert.equal(result, undefined);
});
