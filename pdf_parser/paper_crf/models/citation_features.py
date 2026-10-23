"""Pure token features for the reduced citation-field CRF.

Two inference-identical feature variants share the same row schema:

- ``extract_citation_features`` treats the whole entry as one sequence
  (``paper-citation-crf/v1``).
- ``extract_dot_sentence_features`` splits the entry at sentence-ending
  periods and resets the context window per segment; periods inside numeric
  identifiers (``10.1000/...``, ``2303.08774``) never split
  (``paper-citation-crf/v1-dot-sentence``).
"""

from __future__ import annotations

import re
from typing import Sequence

from .schema import CitationToken


CITATION_FEATURE_VERSION = "paper-citation-crf/v1"
CITATION_DOT_FEATURE_VERSION = "paper-citation-crf/v1-dot-sentence"
_YEAR_RE = re.compile(r"^(?:19|20)\d{2}[a-z]?$", re.I)
_DOI_RE = re.compile(r"^10\.\d{4,9}/", re.I)
_URL_RE = re.compile(r"^(?:https?://|www\.)", re.I)
_ARXIV_RE = re.compile(r"^(?:arxiv:)?(?:[a-z-]+(?:\.[a-z-]+)?/)?\d{4}\.\d{4,5}(?:v\d+)?$", re.I)

# Canonical runtime citation tokenizer: identifiers stay whole, hyphenated
# words stay whole, everything else is a word or a single non-space character.
# Training corpora MUST be tokenized with this exact pattern so fitting and
# inference see identical token streams (see scripts.retokenize_citation_corpus).
CITATION_TOKEN_RE = re.compile(
    r"10\.\d{4,9}/[-._;()/:A-Z0-9]+|arXiv:\d{4}\.\d{4,5}(?:v\d+)?|"
    r"\d{4}\.\d{4,5}(?:v\d+)?|\w+(?:[-'’]\w+)*|[^\w\s]",
    re.I,
)


def tokenize_citation(value: str) -> list[tuple[str, int, int]]:
    """Return ``(text, start, end)`` matches of the canonical tokenizer."""
    return [(match.group(), match.start(), match.end()) for match in CITATION_TOKEN_RE.finditer(value)]


def _shape(text: str) -> str:
    return "".join(
        "A" if char.isupper() else "a" if char.islower() else "0" if char.isdigit() else char
        for char in text[:48]
    )


def _token_row(token: CitationToken, index: int, total: int, previous_text: str, next_text: str) -> dict[str, object]:
    """One row for ``token``; context words are supplied by the caller so the
    whole-sequence and dot-sentence variants stay feature-identical."""
    text = token.text
    lower = text.casefold()
    row: dict[str, object] = {
        "bias": 1.0,
        "token.lower": lower,
        "token.prefix1": lower[:1], "token.prefix2": lower[:2], "token.prefix3": lower[:3],
        "token.suffix1": lower[-1:], "token.suffix2": lower[-2:], "token.suffix3": lower[-3:],
        "token.shape": _shape(text),
        "token.is_upper": text.isupper(), "token.is_title": text.istitle(),
        "token.is_digit": text.isdigit(), "token.has_digit": any(char.isdigit() for char in text),
        "token.is_year": bool(_YEAR_RE.match(text)), "token.is_doi": bool(_DOI_RE.match(text)),
        "token.is_url": bool(_URL_RE.match(text)), "token.is_arxiv": bool(_ARXIV_RE.match(text)),
        "token.punctuation": text if len(text) == 1 and not text.isalnum() else "",
        "position.decile": str(min(9, index * 10 // total)),
    }
    if previous_text is not None:
        row["context.previous"] = previous_text
    if next_text is not None:
        row["context.next"] = next_text
    return row


def extract_citation_features(tokens: Sequence[CitationToken], use_context: bool) -> list[dict[str, object]]:
    """Build train/inference-identical features without corpus or model state."""
    total = max(len(tokens), 1)
    rows: list[dict[str, object]] = []
    for index, token in enumerate(tokens):
        previous = tokens[index - 1].text.casefold() if index else "<BOS>"
        following = tokens[index + 1].text.casefold() if index + 1 < len(tokens) else "<EOS>"
        row = _token_row(token, index, total, previous if use_context else None, following if use_context else None)
        rows.append(row)
    return rows


def dot_sentence_segments(tokens: Sequence[CitationToken]) -> list[list[CitationToken]]:
    """Partition tokens into segments ending at sentence-ending periods.

    A standalone ``.`` token splits only when it is not surrounded by digits
    (``10.1000/...``, ``2303.08774`` decimals stay inside one segment).
    """
    if not tokens:
        return []
    segments: list[list[CitationToken]] = []
    current: list[CitationToken] = []
    for index, token in enumerate(tokens):
        current.append(token)
        if token.text != ".":
            continue
        previous_digit = bool(index) and tokens[index - 1].text[-1:].isdigit()
        next_digit = index + 1 < len(tokens) and bool(tokens[index + 1].text[:1].isdigit())
        if previous_digit or next_digit:
            continue
        segments.append(current)
        current = []
    if current:
        segments.append(current)
    return segments


def extract_dot_sentence_features(tokens: Sequence[CitationToken], use_context: bool) -> list[dict[str, object]]:
    """Whole-entry rows whose context window resets at sentence boundaries."""
    rows: list[dict[str, object]] = []
    for segment in dot_sentence_segments(tokens):
        total = max(len(segment), 1)
        for index, token in enumerate(segment):
            previous = segment[index - 1].text.casefold() if index else "<BOS>"
            following = segment[index + 1].text.casefold() if index + 1 < len(segment) else "<EOS>"
            rows.append(_token_row(token, index, total, previous if use_context else None, following if use_context else None))
    return rows
