"""Paper metadata extraction engines independent of gRPC transport.

The deterministic engine is the current production implementation.  The
``PaperExtractionEngine`` protocol is intentionally small so a future CRF
engine can replace it without changing protobuf handling or server startup.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Protocol, Sequence

from papers_parser.markdown_metadata import extract_paper_sections


@dataclass(frozen=True)
class SourceLocation:
    """Compact PDF and Markdown location supplied by the Go application."""

    page_number: int
    page_width: float
    page_height: float
    bbox_x0: float
    bbox_y0: float
    bbox_x1: float
    bbox_y1: float
    markdown_codepoint_start: int
    markdown_codepoint_end: int


@dataclass(frozen=True)
class SourceBlock:
    """Transport-neutral source block used by extraction engines."""

    ordinal: int
    type: str
    text: str
    location: SourceLocation | None = None


@dataclass(frozen=True)
class SourceEvidence:
    """A candidate's link back to one persisted source-map block."""

    block_ordinal: int
    page_number: int
    markdown_codepoint_start: int
    markdown_codepoint_end: int


@dataclass(frozen=True)
class TextCandidate:
    """One extracted textual value with normalized confidence and evidence."""

    value: str
    confidence: float
    evidence: tuple[SourceEvidence, ...] = ()


@dataclass(frozen=True)
class ExtractionOutput:
    """Transport-neutral extraction response."""

    title: TextCandidate | None = None
    authors: tuple[TextCandidate, ...] = ()
    abstract: TextCandidate | None = None
    references: tuple[str, ...] = ()
    extractor_name: str = ""
    extractor_version: str = ""


class PaperExtractionEngine(Protocol):
    """Boundary implemented by deterministic rules and future CRF models."""

    def extract(self, markdown: str, blocks: Sequence[SourceBlock]) -> ExtractionOutput:
        """Extract candidates from one exact Markdown revision."""


class DeterministicPaperExtractionEngine:
    """Adapt existing explainable Markdown rules to the extraction contract."""

    name = "markdown-rules"
    version = "1"

    def extract(self, markdown: str, blocks: Sequence[SourceBlock]) -> ExtractionOutput:
        sections = extract_paper_sections(markdown)
        title = _candidate(sections.title, 0.95, markdown, blocks)
        abstract = _candidate(sections.abstract, 0.85, markdown, blocks)
        authors = tuple(
            candidate
            for value in sections.authors
            if (candidate := _candidate(value, 0.85, markdown, blocks)) is not None
        )
        return ExtractionOutput(
            title=title,
            authors=authors,
            abstract=abstract,
            references=tuple(sections.references),
            extractor_name=self.name,
            extractor_version=self.version,
        )


def _candidate(
    value: str,
    confidence: float,
    markdown: str,
    blocks: Sequence[SourceBlock],
) -> TextCandidate | None:
    value = value.strip()
    if not value:
        return None
    return TextCandidate(
        value=value,
        confidence=confidence,
        evidence=_find_evidence(value, markdown, blocks),
    )


def _find_evidence(
    value: str,
    markdown: str,
    blocks: Sequence[SourceBlock],
) -> tuple[SourceEvidence, ...]:
    """Find bounded source blocks overlapping or containing a candidate."""
    candidate_start = markdown.find(value)
    candidate_end = candidate_start + len(value) if candidate_start >= 0 else -1
    normalized_value = _normalize(value)
    evidence: list[SourceEvidence] = []
    seen_ordinals: set[int] = set()

    for block in blocks:
        location = block.location
        if location is None or block.ordinal in seen_ordinals:
            continue
        overlaps = (
            candidate_start >= 0
            and location.markdown_codepoint_start < candidate_end
            and location.markdown_codepoint_end > candidate_start
        )
        normalized_block = _normalize(block.text)
        text_matches = bool(
            normalized_value
            and normalized_block
            and (
                normalized_value in normalized_block
                or normalized_block in normalized_value
            )
        )
        if not overlaps and not text_matches:
            continue
        seen_ordinals.add(block.ordinal)
        evidence.append(
            SourceEvidence(
                block_ordinal=block.ordinal,
                page_number=location.page_number,
                markdown_codepoint_start=location.markdown_codepoint_start,
                markdown_codepoint_end=location.markdown_codepoint_end,
            )
        )
        # Candidate evidence is diagnostic context, not a copy of every block
        # in a long abstract. Keep responses bounded.
        if len(evidence) == 8:
            break
    return tuple(evidence)


def _normalize(value: str) -> str:
    return " ".join(value.casefold().split())

