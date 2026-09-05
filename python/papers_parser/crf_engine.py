"""Runtime adapters for the reproducible paper CRF artifacts.

The training package remains the only owner of feature functions.  This module
loads those exact functions and artifacts once at parser startup, then applies
small, conservative post-processing around them.
"""
from __future__ import annotations

import json
import logging
import os
import re
import sys
import threading
from pathlib import Path
from typing import Sequence

import pycrfsuite

from logger import get_logger
from papers_parser.paper_extractor import (
    ExtractionOutput,
    LayoutLine,
    ReferenceCandidate,
    SourceEvidence,
    TextCandidate,
)
from papers_parser.regex_metadata_find import filter_reference_items

PROJECT_ROOT = Path(__file__).resolve().parents[2]
if str(PROJECT_ROOT) not in sys.path:
    sys.path.insert(0, str(PROJECT_ROOT))

from model_train.paper_crf.citation_features import (  # noqa: E402
    CITATION_FEATURE_VERSION,
    CITATION_TOKEN_RE,
    extract_citation_features,
)
from model_train.paper_crf.line_features import FEATURE_VERSION, extract_line_features  # noqa: E402
from model_train.paper_crf.reference_layout import decode_reference_layout  # noqa: E402
from model_train.paper_crf.schema import CitationToken, LayoutLine as TrainingLayoutLine  # noqa: E402


def _log_level_from_env() -> int:
    """Read the shared parser log level without accepting arbitrary objects."""
    configured = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, configured, None)
    return level if isinstance(level, int) else logging.INFO


LOG = get_logger(__name__, _log_level_from_env())


# Canonical runtime tokenizer lives with the training features so fitting and
# inference can never drift apart again (see retokenize_citation_corpus).
_TOKEN_RE = CITATION_TOKEN_RE
_DOI_RE = re.compile(r"10\.\d{4,9}/[-._;()/:A-Z0-9]+", re.I)
_ARXIV_RE = re.compile(r"(?:arXiv:\s*)?((?:[a-z-]+(?:\.[a-z-]+)?/)?\d{4}\.\d{4,5}(?:v\d+)?)", re.I)
# A venue marker whose right side ends the title zone. Kept deliberately
# narrow (arXiv/CoRR/Proceedings): the fallback must never swallow an unknown
# venue as a title. No word boundary is used: "_CoRR_" is italic markup whose
# trailing underscore would defeat "\b".
_VENUE_START_RE = re.compile(r"_?(?:arXiv|CoRR|Proceedings)", re.I)
_ET_AL_TEXT = {"et", "al"}
# Date-like first segment: "2023", "2023a", "2022a", "2023-03-12", and month
# forms such as "March 2023". Used to skip year stamps while scanning for a
# missing title after the author zone.
_DATE_LIKE_RE = re.compile(
    r"(?:(?:19|20)\d{2}[a-z]?(?:\s*[-/]\s*\d{1,2}[a-z]?)*|"
    r"(?:Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|Jun(?:e)?|Jul(?:y)?|"
    r"Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)"
    r"[a-z]*[\s,]*[0-9]{2,4}[a-z]?)[\s,.;:]*$",
    re.I,
)


class _CRF:
    """One immutable artifact plus a lock around pycrfsuite's mutable tagger."""

    def __init__(self, model_path: Path, feature_version: str | tuple[str, ...]) -> None:
        if not model_path.is_file():
            raise ValueError(f"CRF artifact is missing: {model_path}")
        manifest_path = model_path.with_suffix(".json")
        if not manifest_path.is_file():
            raise ValueError(f"CRF manifest is missing: {manifest_path}")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        accepted_versions = (feature_version,) if isinstance(feature_version, str) else feature_version
        if manifest.get("feature_version") not in accepted_versions:
            raise ValueError(
                f"CRF feature version mismatch for {model_path.name}: "
                f"{manifest.get('feature_version')!r} not in {accepted_versions!r}"
            )
        self.model_path = model_path
        self.feature_version = str(manifest["feature_version"])
        self._tagger = pycrfsuite.Tagger()
        self._tagger.open(str(model_path))
        self._lock = threading.Lock()

    def predict(self, features: Sequence[dict[str, object]]) -> tuple[list[str], list[float]]:
        if not features:
            return [], []
        with self._lock:
            labels = self._tagger.tag(features)
            confidence = [self._tagger.marginal(label, index) for index, label in enumerate(labels)]
        return labels, confidence


class CRFPaperExtractionEngine:
    """Layout-aware CRF cascade. Construction fails when artifacts are invalid."""

    name = "paper-crf"
    version = "v1"
    requires_layout = True

    def __init__(
        self,
        header_model: Path,
        reference_model: Path,
        citation_model: Path,
        header_use_layout: bool = False,
        reference_use_layout: bool = False,
        header_threshold: float = 0.912,
        reference_threshold: float = 0.946,
    ) -> None:
        self._header = _CRF(header_model, FEATURE_VERSION)
        # The retained reference-text artifact predates the Header-only v2
        # priors. Extra header-prefixed runtime features are ignored by CRF,
        # while all v1 reference features remain identical.
        self._reference = _CRF(reference_model, ("paper-line-crf/v1", FEATURE_VERSION))
        self._citation = _CRF(citation_model, CITATION_FEATURE_VERSION)
        self._header_use_layout = header_use_layout
        self._reference_use_layout = reference_use_layout
        self._header_threshold = header_threshold
        self._reference_threshold = reference_threshold

    def model_summary(self) -> dict[str, str]:
        """Return safe artifact identifiers for startup observability."""
        return {
            "header_model": self._header.model_path.name,
            "header_feature_version": self._header.feature_version,
            "reference_model": self._reference.model_path.name,
            "reference_feature_version": self._reference.feature_version,
            "citation_model": self._citation.model_path.name,
            "citation_feature_version": self._citation.feature_version,
        }

    def extract(
        self,
        markdown: str,
        blocks,
        layout_lines: Sequence[LayoutLine] = (),
    ) -> ExtractionOutput:
        if not layout_lines:
            return ExtractionOutput(extractor_name=self.name, extractor_version=self.version)
        lines = _training_lines(layout_lines)
        header_labels, header_confidence = self._header.predict(extract_line_features(lines, use_layout=self._header_use_layout))
        title = _first_text_candidate(lines, header_labels, header_confidence, "TITLE", self._header_threshold)
        authors = _text_candidates(lines, header_labels, header_confidence, "AUTHOR", self._header_threshold)
        abstract = _joined_text_candidate(lines, header_labels, header_confidence, "ABSTRACT", self._header_threshold)

        reference_labels, reference_confidence = self._reference.predict(extract_line_features(lines, use_layout=self._reference_use_layout))
        reference_indexes = [
            index for index, label in enumerate(reference_labels)
            if label in {"REF_HEAD", "B-REF", "I-REF"}
        ]
        if reference_indexes and min(reference_confidence[index] for index in reference_indexes) >= self._reference_threshold:
            decoded = decode_reference_layout("runtime", lines, reference_labels)
            references = tuple(filter_reference_items(entry.raw_text for entry in decoded.entries))
            references_detected = True
        else:
            references = ()
            references_detected = False
        return ExtractionOutput(
            title=title,
            authors=authors,
            abstract=abstract,
            references_detected=references_detected,
            references=references,
            reference_candidates=self.reference_candidates(references),
            extractor_name=self.name, extractor_version=self.version,
        )

    def reference_candidates(self, references: Sequence[str]) -> tuple[ReferenceCandidate, ...]:
        """Parse field candidates for exact raw entries selected by the caller."""
        output: list[ReferenceCandidate] = []
        for reference in references:
            tokens = _citation_tokens(reference)
            labels, confidence = self._citation.predict(extract_citation_features(tokens, use_context=True))
            values = _citation_fields(tokens, labels)
            title = values.get("TITLE", "")
            if not title.strip():
                title = _fallback_title(reference, tokens, labels)
            if not title.strip():
                title = _segment_fallback_title(reference, tokens, labels)
            field_confidence = [score for label, score in zip(labels, confidence) if label != "O"]
            authors = tuple(value.strip() for value in values.get("AUTHOR", "").split(",") if value.strip())
            output.append(ReferenceCandidate(
                raw_reference=reference, authors=authors, title=title, venue=values.get("VENUE", ""),
                published_year=_year(values.get("YEAR", "")), doi=_first(_DOI_RE.findall(reference)),
                arxiv_id=_arxiv(reference), volume=values.get("VOLUME", ""), pages=values.get("PAGES", ""),
                confidence=sum(field_confidence) / len(field_confidence) if field_confidence else 0.0,
            ))
        return tuple(output)


class HybridPaperExtractionEngine:
    """Fill rule gaps with calibrated CRF facts while preserving known output."""

    name = "paper-hybrid"
    version = "v1"
    requires_layout = True

    def __init__(self, rules, crf: CRFPaperExtractionEngine) -> None:
        self._rules = rules
        self._crf = crf

    def extract(self, markdown: str, blocks, layout_lines: Sequence[LayoutLine] = ()) -> ExtractionOutput:
        base = self._rules.extract(markdown, blocks, layout_lines)
        # The reference region is rule-owned: the deterministic splitter is
        # the only region/extraction source. The learned line model can fill
        # absent header facts, but its reference entries neither replace an
        # empty rule region nor add items (both would duplicate 序号 or invent
        # unnumbered entries).
        references = base.references
        if not layout_lines:
            # No layout: the line-level CRFs cannot run, but Citation-CRF
            # field extraction only needs the raw entry strings and must still
            # populate candidates (titles, venues, years) so catalog seeding
            # and the UI work even when the source PDF is gone.
            candidates = self._crf.reference_candidates(references)
            return ExtractionOutput(
                title=base.title,
                authors=base.authors,
                abstract=base.abstract,
                references_detected=base.references_detected,
                references=tuple(references), reference_candidates=candidates,
                extractor_name=self.name + "-no-layout", extractor_version=self.version,
            )
        learned = self._crf.extract(markdown, blocks, layout_lines)
        title = base.title or learned.title
        authors = base.authors or learned.authors
        abstract = base.abstract or learned.abstract
        candidates = self._crf.reference_candidates(references)
        return ExtractionOutput(
            title=title,
            authors=authors,
            abstract=abstract,
            references_detected=base.references_detected,
            references=tuple(references), reference_candidates=candidates,
            extractor_name=self.name, extractor_version=self.version,
        )


class ShadowPaperExtractionEngine:
    """Compare CRF output with rules while returning rules unchanged.

    Shadow mode is deliberately operationally inert: only the deterministic
    extraction is returned to the caller and persisted by Go. The CRF result is
    discarded after updating aggregate counters, so it cannot alter metadata,
    citations, or catalog scheduling before it has passed Phase-24 promotion.
    """

    name = "paper-shadow"
    version = "v1"
    requires_layout = True

    def __init__(self, rules, crf: CRFPaperExtractionEngine) -> None:
        self._rules = rules
        self._crf = crf
        self._lock = threading.Lock()
        self._counters = {
            "runs": 0,
            "layout_unavailable": 0,
            "crf_failures": 0,
            "title_disagreements": 0,
            "author_disagreements": 0,
            "abstract_disagreements": 0,
            "reference_disagreements": 0,
        }

    def extract(self, markdown: str, blocks, layout_lines: Sequence[LayoutLine] = ()) -> ExtractionOutput:
        """Return rule output and log only cumulative, content-free deltas."""
        base = self._rules.extract(markdown, blocks, layout_lines)
        updates = {"runs": 1}
        if not layout_lines:
            updates["layout_unavailable"] = 1
            self._record(updates)
            return base
        try:
            learned = self._crf.extract(markdown, blocks, layout_lines)
        except Exception as exc:  # Rules remain usable despite model failure.
            updates["crf_failures"] = 1
            self._record(updates)
            LOG.warning("paper extraction shadow CRF failed error_type=%s", type(exc).__name__)
            return base
        if _candidate_value(base.title) != _candidate_value(learned.title):
            updates["title_disagreements"] = 1
        if _candidate_values(base.authors) != _candidate_values(learned.authors):
            updates["author_disagreements"] = 1
        if _candidate_value(base.abstract) != _candidate_value(learned.abstract):
            updates["abstract_disagreements"] = 1
        if _normalized_reference_set(base.references) != _normalized_reference_set(learned.references):
            updates["reference_disagreements"] = 1
        self._record(updates)
        return base

    def safe_counters(self) -> dict[str, int]:
        """Return a copy of aggregate counters for tests or health diagnostics."""
        with self._lock:
            return dict(self._counters)

    def _record(self, updates: dict[str, int]) -> None:
        with self._lock:
            for key, value in updates.items():
                self._counters[key] += value
            counters = dict(self._counters)
        LOG.info(
            "paper extraction shadow counters runs=%d layout_unavailable=%d "
            "crf_failures=%d title_disagreements=%d author_disagreements=%d "
            "abstract_disagreements=%d reference_disagreements=%d",
            counters["runs"],
            counters["layout_unavailable"],
            counters["crf_failures"],
            counters["title_disagreements"],
            counters["author_disagreements"],
            counters["abstract_disagreements"],
            counters["reference_disagreements"],
        )


def _candidate_value(candidate: TextCandidate | None) -> str:
    """Normalize an optional header candidate without exposing its text in logs."""
    return " ".join(candidate.value.casefold().split()) if candidate is not None else ""


def _candidate_values(candidates: Sequence[TextCandidate]) -> tuple[str, ...]:
    """Normalize a candidate list for content-free equality comparison."""
    return tuple(_candidate_value(candidate) for candidate in candidates)


def _normalized_reference_set(references: Sequence[str]) -> frozenset[str]:
    """Compare raw-reference membership without retaining values in counters."""
    return frozenset(" ".join(reference.casefold().split()) for reference in references)


def _author_end_index(tokens: Sequence[CitationToken], labels: Sequence[str], limit: int | None = None) -> int:
    """Last AUTHOR-labeled token index below ``limit``.

    A literal ``et al`` pair always terminates the author list, even when the
    CRF kept labeling words after it as authors (``et al. Palm 2 ...``).
    """
    end = min(limit, len(tokens)) if limit is not None else len(tokens)
    author_end = -1
    for index in range(end):
        if labels[index] in {"B-AUTHOR", "I-AUTHOR"}:
            author_end = index
    for index in range(end - 1):
        if tokens[index].text.casefold() == "et" and tokens[index + 1].text.casefold() == "al":
            author_end = index + 1
    return author_end


def _fallback_title(reference: str, tokens: Sequence[CitationToken], labels: Sequence[str]) -> str:
    """Rule title when the citation CRF labels no TITLE token.

    Splits the entry conceptually at its venue marker (``arXiv``/``CoRR``/
    ``Proceedings``): the title is the text between the end of the author
    list and that marker.
    """
    match = _VENUE_START_RE.search(reference)
    if not match:
        return ""
    # The token containing the marker start, whether the underscore is part of
    # the same token ("_arXiv") or a neighbouring punctuation token ("_").
    marker_index = next(
        (index for index, token in enumerate(tokens) if token.char_start <= match.start() < token.char_end),
        None,
    )
    if marker_index is None or marker_index <= 1:
        return ""
    author_end = _author_end_index(tokens, labels, marker_index)
    if author_end < 0 or author_end >= marker_index - 1:
        return ""
    title_tokens = [token.text for token in tokens[author_end + 1:marker_index]]
    title = _join_tokens(title_tokens)
    return title.strip(" .,;:()[]-–—")


def _segment_fallback_title(reference: str, tokens: Sequence[CitationToken], labels: Sequence[str]) -> str:
    """Dot-segment title rule when both CRF title and marker fallback fail.

    Requires an extracted author zone. The raw text after the last author
    token is split at periods; segments that are empty, digit/punctuation
    only, date-like (``2023``, ``2023a``, ``2022-05-12``, ``March 2023``),
    URLs, or ``Accessed`` stamps are skipped. The earliest remaining segment
    is returned as the title.
    """
    author_end = _author_end_index(tokens, labels)
    if author_end < 0:
        return ""
    rest = reference[tokens[author_end].char_end:]
    # Split at a period followed by whitespace only: "Claude-3.7-sonnet" and
    # "2303.08774" keep their inner periods.
    for raw_segment in re.split(r"\.\s+", rest):
        segment = raw_segment.strip(" \t,;:'\"().[]")
        if not segment or not any(char.isalpha() for char in segment):
            continue
        if _DATE_LIKE_RE.fullmatch(segment):
            continue
        lowered = segment.casefold()
        if "://" in segment or lowered.startswith(("http", "www.", "accessed", "et al")):
            continue
        # URL path leakage ("com/datasets/...") and version fragments
        # (".5-pro", "2303.08774") are never titles.
        if "/" in segment or re.fullmatch(r"\d+(?:[-.]\d+)*(?:[-a-z][a-z-]*)?", lowered):
            continue
        return segment
    return ""


def _training_lines(lines: Sequence[LayoutLine]) -> tuple[TrainingLayoutLine, ...]:
    return tuple(TrainingLayoutLine(
        document_id="runtime", page_number=line.page_number, order_in_page=line.order_in_page,
        order_in_document=line.order_in_document, text=line.text, page_width=line.page_width,
        page_height=line.page_height, bbox_x0=line.bbox_x0, bbox_y0=line.bbox_y0,
        bbox_x1=line.bbox_x1, bbox_y1=line.bbox_y1, font_name=line.font_name,
        font_size=line.font_size, max_font_size=line.max_font_size, bold_ratio=line.bold_ratio,
        italic_ratio=line.italic_ratio, column_index=line.column_index,
        markdown_codepoint_start=line.markdown_codepoint_start,
        markdown_codepoint_end=line.markdown_codepoint_end,
        parent_block_ordinal=line.parent_block_ordinal,
    ) for line in lines)


def _evidence(line: TrainingLayoutLine) -> tuple[SourceEvidence, ...]:
    if line.parent_block_ordinal is None or line.markdown_codepoint_start is None or line.markdown_codepoint_end is None:
        return ()
    return (SourceEvidence(line.parent_block_ordinal, line.page_number or 1, line.markdown_codepoint_start, line.markdown_codepoint_end),)


def _first_text_candidate(lines, labels, confidence, label, threshold):
    for line, output, score in zip(lines, labels, confidence):
        if output == label and score >= threshold:
            return TextCandidate(line.text, score, _evidence(line))
    return None


def _text_candidates(lines, labels, confidence, label, threshold):
    return tuple(TextCandidate(line.text, score, _evidence(line)) for line, output, score in zip(lines, labels, confidence) if output == label and score >= threshold)


def _joined_text_candidate(lines, labels, confidence, label, threshold):
    selected = [(line, score) for line, output, score in zip(lines, labels, confidence) if output == label and score >= threshold]
    if not selected:
        return None
    return TextCandidate("\n".join(line.text for line, _ in selected), min(score for _, score in selected), tuple(item for line, _ in selected for item in _evidence(line)))


def _citation_tokens(reference: str) -> tuple[CitationToken, ...]:
    return tuple(CitationToken("runtime", 0, index, match.group(), match.start(), match.end()) for index, match in enumerate(_TOKEN_RE.finditer(reference)))


def _citation_fields(tokens, labels):
    values: dict[str, list[str]] = {}
    for token, label in zip(tokens, labels):
        if label == "O" or "-" not in label:
            continue
        field = label.split("-", 1)[1]
        values.setdefault(field, []).append(token.text)
    return {field: _join_tokens(items) for field, items in values.items()}


def _join_tokens(tokens):
    value = ""
    for token in tokens:
        if not value or token in ",.;:)]}" or value.endswith("([{"):
            value += token
        else:
            value += " " + token
    return value.strip()


def _year(value: str) -> int:
    match = re.search(r"(?:19|20)\d{2}", value)
    return int(match.group()) if match else 0


def _arxiv(value: str) -> str:
    match = _ARXIV_RE.search(value)
    return match.group(1) if match else ""


def _first(values):
    return values[0].rstrip(".,;)") if values else ""
