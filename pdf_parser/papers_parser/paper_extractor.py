"""
Paper metadata extraction engines independent of gRPC transport.

The deterministic engine is the rules fallback. The CRF/hybrid/shadow engines
live in ``crf_engine.py`` and load the runtime-owned ``paper_crf`` bundle. The
``PaperExtractionEngine`` protocol stays small so the engines can share
protobuf handling and server startup.
"""
from __future__ import annotations

import re
from dataclasses import dataclass, field
from typing import Protocol, Sequence

from papers_parser.regex_metadata_find import (
    MarkdownTextSpan,
    extract_author_names,
    extract_paper_sections,
    filter_reference_items,
    is_affiliation_only,
    is_markdown_heading,
    section_kind,
    split_reference_items,
)


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
class LayoutLine:
    """Private parser line facts shared with the runtime CRF adapter."""

    page_number: int
    order_in_page: int
    order_in_document: int
    text: str
    page_width: float
    page_height: float
    bbox_x0: float
    bbox_y0: float
    bbox_x1: float
    bbox_y1: float
    font_name: str | None = None
    font_size: float | None = None
    max_font_size: float | None = None
    bold_ratio: float | None = None
    italic_ratio: float | None = None
    column_index: int | None = None
    markdown_codepoint_start: int | None = None
    markdown_codepoint_end: int | None = None
    parent_block_ordinal: int | None = None


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
class ReferenceCandidate:
    """Optional structured fields for one raw bibliography entry.

    The deterministic engine currently preserves only the raw entry. Phase 23
    will populate the optional fields from the loaded Citation CRF; keeping the
    shape here makes the gRPC contract and Go persistence path additive now.
    """

    raw_reference: str
    authors: tuple[str, ...] = ()
    title: str = ""
    venue: str = ""
    published_year: int = 0
    doi: str = ""
    arxiv_id: str = ""
    volume: str = ""
    pages: str = ""
    confidence: float = 0.0


@dataclass(frozen=True)
class ExtractionOutput:
    """Transport-neutral extraction response."""

    title: TextCandidate | None = None
    authors: tuple[TextCandidate, ...] = ()
    abstract: TextCandidate | None = None
    references_detected: bool = False
    references: tuple[str, ...] = ()
    reference_candidates: tuple[ReferenceCandidate, ...] = ()
    extractor_name: str = ""
    extractor_version: str = ""


@dataclass(frozen=True)
class _BlockText:
    """An extracted value whose provenance is already known block-by-block."""

    value: str
    blocks: tuple[SourceBlock, ...]


@dataclass(frozen=True)
class _BlockPaperSections:
    """Paper sections assembled from ordered source blocks."""

    title: _BlockText | None = None
    title_confidence: float = 0.0
    authors: tuple[_BlockText, ...] = ()
    abstract: _BlockText | None = None
    references_detected: bool = False
    references: tuple[str, ...] = ()


class PaperExtractionEngine(Protocol):
    """Boundary implemented by deterministic rules and future CRF models."""

    def extract(
        self,
        markdown: str,
        blocks: Sequence[SourceBlock],
        layout_lines: Sequence[LayoutLine] = (),
    ) -> ExtractionOutput:
        """Extract candidates from one exact Markdown revision."""


class DeterministicPaperExtractionEngine:
    """Adapt existing explainable Markdown rules to the extraction contract."""

    name = "markdown-rules"
    version = "1"
    requires_layout = False

    def extract(
        self,
        markdown: str,
        blocks: Sequence[SourceBlock],
        layout_lines: Sequence[LayoutLine] = (),
    ) -> ExtractionOutput:
        """Extract directly from blocks, using Markdown rules only without block data."""
        block_sections = _extract_sections_from_blocks(blocks)
        if block_sections is not None:
            return ExtractionOutput(
                title=_candidate_from_blocks(block_sections.title, block_sections.title_confidence),
                authors=tuple(
                    candidate
                    for value in block_sections.authors
                    if (candidate := _candidate_from_blocks(value, 0.85)) is not None
                ),
                abstract=_candidate_from_blocks(block_sections.abstract, 0.85),
                references_detected=block_sections.references_detected,
                references=block_sections.references,
                reference_candidates=tuple(
                    ReferenceCandidate(raw_reference=reference)
                    for reference in block_sections.references
                ),
                extractor_name=self.name,
                extractor_version=self.version,
            )

        sections = extract_paper_sections(markdown)
        title = _candidate_from_markdown_span(sections.title_span, 0.95)
        abstract = _candidate_from_markdown_span(sections.abstract_span, 0.85)
        authors = tuple(
            candidate
            for span in sections.author_spans
            if (candidate := _candidate_from_markdown_span(span, 0.85)) is not None
        )
        references = filter_reference_items(sections.references)
        return ExtractionOutput(
            title=title,
            authors=authors,
            abstract=abstract,
            references_detected=sections.references_detected,
            references=tuple(references),
            reference_candidates=tuple(
                ReferenceCandidate(raw_reference=reference)
                for reference in references
            ),
            extractor_name=self.name,
            extractor_version=self.version,
        )


def _extract_sections_from_blocks(
    blocks: Sequence[SourceBlock],
) -> _BlockPaperSections | None:
    """Walk source blocks once and attach every extracted value to its origin."""
    ordered = tuple(sorted(
        (block for block in blocks if block.text.strip()), key=lambda block: block.ordinal,
    ))
    if not ordered:
        return None

    title_index = _find_title_block(ordered)
    if title_index is None:
        return _BlockPaperSections()
    title_block = ordered[title_index]
    title = _BlockText(_clean_block_text(title_block.text), (title_block,))
    title_confidence = _title_block_confidence(title_block)

    authors: list[_BlockText] = []
    fallback_abstract_blocks: list[SourceBlock] = []
    abstract_blocks: list[SourceBlock] = []
    references: list[str] = []
    references_detected = False
    mode = "header"

    for block in ordered[title_index + 1:]:
        kind = _block_section_kind(block)
        if kind == "abstract":
            mode = "abstract"
            continue
        if kind == "references":
            mode = "references"
            references_detected = True
            continue
        if mode == "header":
            names = extract_author_names(block.text)
            if names:
                authors.extend(_BlockText(name, (block,)) for name in names)
                continue
        if _is_section_boundary(block):
            mode = "body"
            continue

        if mode == "references":
            references.extend(split_reference_items(block.text))
            continue
        if mode == "abstract":
            abstract_blocks.append(block)
            continue
        if mode != "header":
            continue
        fallback_abstract_blocks.append(block)

    abstract = _join_block_text(abstract_blocks)
    if abstract is None:
        fallback = _join_block_text(fallback_abstract_blocks)
        if fallback is not None and not is_affiliation_only(fallback.value):
            abstract = fallback
    return _BlockPaperSections(
        title=title,
        title_confidence=title_confidence,
        authors=tuple(authors),
        abstract=abstract,
        references_detected=references_detected,
        # Cross-block merge: one global pass so a reference list split across
        # blocks is deduplicated by 序号 instead of per block.
        references=tuple(filter_reference_items(references)),
    )


def _find_title_block(blocks: Sequence[SourceBlock]) -> int | None:
    """Return an explicit title, a title-like heading, or a weak text fallback."""
    for index, block in enumerate(blocks):
        if block.type == "title":
            return index
    for index, block in enumerate(blocks):
        if _is_plausible_title_heading(block):
            return index
    for index, block in enumerate(blocks):
        if _is_plausible_first_page_title(block):
            return index
    return None


def _title_block_confidence(block: SourceBlock) -> float:
    """Encode structural title evidence for hybrid arbitration with the CRF."""
    if block.type == "title":
        return 1.0
    if _is_plausible_title_heading(block):
        return 1.0
    return 0.60


def _is_plausible_title_heading(block: SourceBlock) -> bool:
    """Accept a top-of-first-page heading while rejecting body section headings."""
    if block.type != "section_header" and not is_markdown_heading(block.text):
        return False
    value = _clean_block_text(block.text)
    if section_kind(value) is not None:
        return False
    if re.match(r"^(?:\d+(?:\.\d+)*|[IVX]+)[.)\-:]?\s+", value, re.IGNORECASE):
        return False
    if value.casefold() in {
        "introduction", "related work", "background", "method", "methods",
        "methodology", "experiments", "results", "discussion", "conclusion",
        "conclusions", "acknowledgments", "acknowledgements",
    }:
        return False
    location = block.location
    if location is not None and (
        location.page_number != 1
        or location.page_height <= 0
        or location.bbox_y0 / location.page_height > 0.35
    ):
        return False
    return 5 <= len(value) <= 500 and bool(re.search(r"[A-Za-z]{2,}", value))


def _is_plausible_first_page_title(block: SourceBlock) -> bool:
    """Recognize a leading layout block when the parser omitted the title type."""
    location = block.location
    if block.type != "text" or location is None or location.page_number != 1:
        return False
    if location.page_height <= 0 or location.bbox_y0 / location.page_height > 0.25:
        return False
    value = _clean_block_text(block.text)
    if not 15 <= len(value) <= 500 or is_markdown_heading(block.text):
        return False
    if section_kind(value) is not None or "@" in value:
        return False
    return len(re.findall(r"[A-Za-z]{2,}", value)) >= 3


def _block_section_kind(block: SourceBlock) -> str | None:
    """Classify a block as the beginning of a recognized metadata section."""
    if block.type not in {"title", "section_header"} and not is_markdown_heading(block.text):
        return None
    return section_kind(block.text)


def _is_section_boundary(block: SourceBlock) -> bool:
    """Report whether a block begins ordinary body content after the paper header."""
    return block.type in {"title", "section_header"} or is_markdown_heading(block.text)


def _clean_block_text(value: str) -> str:
    """Remove minimal Markdown heading markup while preserving block text content."""
    value = value.strip()
    if value.startswith("#"):
        value = value.lstrip("#").strip()
    if value.startswith("**") and value.endswith("**"):
        value = value[2:-2]
    value = re.sub(r"^[\s\-–—•]+", "", value)
    value = re.sub(r"(?<=\w)\s+-\s+(?=\w)", "", value)
    return re.sub(r"\s+", " ", value).strip()


def _join_block_text(blocks: Sequence[SourceBlock]) -> _BlockText | None:
    """Combine consecutive content blocks while retaining their exact provenance."""
    if not blocks:
        return None
    value = "\n".join(block.text.strip() for block in blocks if block.text.strip()).strip()
    return None if not value else _BlockText(value, tuple(blocks))


def _candidate_from_blocks(
    value: _BlockText | None,
    confidence: float,
) -> TextCandidate | None:
    """Build a candidate directly from its already-selected source blocks."""
    if value is None or not value.value:
        return None
    return TextCandidate(
        value=value.value,
        confidence=confidence,
        evidence=_evidence_from_blocks(value.blocks),
    )


def _candidate_from_markdown_span(
    span: MarkdownTextSpan | None,
    confidence: float,
) -> TextCandidate | None:
    """Build a Markdown-only compatibility candidate without PDF-block evidence."""
    if span is None or not span.value:
        return None
    return TextCandidate(
        value=span.value,
        confidence=confidence,
        evidence=(),
    )


def _evidence_from_blocks(blocks: Sequence[SourceBlock]) -> tuple[SourceEvidence, ...]:
    """Convert directly selected blocks to bounded evidence without a text search."""
    evidence: list[SourceEvidence] = []
    seen_ordinals: set[int] = set()
    for block in blocks:
        location = block.location
        if location is None or block.ordinal in seen_ordinals:
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
        if len(evidence) == 8:
            break
    return tuple(evidence)
