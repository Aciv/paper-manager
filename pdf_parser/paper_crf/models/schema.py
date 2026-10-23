"""Versioned, dependency-free contracts for paper CRF development.

Only source facts belong in :class:`LayoutLine`. Normalized coordinates,
relative font size, gaps, indentation, lexical patterns, and neighboring-line
features are derived by the future shared feature module so training and live
inference cannot drift.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field
import math
from typing import Any, Mapping, Sequence


LAYOUT_SCHEMA_VERSION = "paper-layout-line/v1"
SEQUENCE_SCHEMA_VERSION = "paper-crf-sequence/v1"
REFERENCE_ENTRY_SCHEMA_VERSION = "paper-reference-entry/v1"
CITATION_TOKEN_SCHEMA_VERSION = "paper-citation-token/v1"
ARTIFACT_SCHEMA_VERSION = "paper-crf-artifact/v1"

HEADER_LABELS = ("TITLE", "AUTHOR", "ABSTRACT", "O")
REFERENCE_LAYOUT_LABELS = ("PRE", "REF_HEAD", "B-REF", "I-REF", "POST")
_CITATION_FIELDS = ("AUTHOR", "TITLE", "VENUE", "YEAR", "ID", "VOLUME", "PAGES")
CITATION_LABELS = ("O",) + tuple(
    f"{prefix}-{field_name}"
    for field_name in _CITATION_FIELDS
    for prefix in ("B", "I")
)


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def _optional_non_negative(value: int | None, name: str) -> None:
    if value is not None:
        _require(isinstance(value, int) and not isinstance(value, bool), f"{name} must be an integer")
        _require(value >= 0, f"{name} cannot be negative")


@dataclass(frozen=True)
class LayoutLine:
    """One reading-order PDF line with optional Markdown/block provenance."""

    document_id: str
    page_number: int | None
    order_in_page: int
    order_in_document: int
    text: str
    page_width: float | None
    page_height: float | None
    bbox_x0: float | None
    bbox_y0: float | None
    bbox_x1: float | None
    bbox_y1: float | None
    font_name: str | None = None
    font_size: float | None = None
    max_font_size: float | None = None
    bold_ratio: float | None = None
    italic_ratio: float | None = None
    column_index: int | None = None
    markdown_codepoint_start: int | None = None
    markdown_codepoint_end: int | None = None
    parent_block_ordinal: int | None = None
    label: str | None = None
    schema_version: str = LAYOUT_SCHEMA_VERSION

    def validate(self, allowed_labels: Sequence[str] | None = None) -> None:
        """Reject malformed geometry, provenance, style ratios, or labels."""
        _require(self.schema_version == LAYOUT_SCHEMA_VERSION, "unsupported layout schema version")
        _require(bool(self.document_id.strip()), "document_id is required")
        if self.page_number is not None:
            _require(self.page_number >= 1, "page_number must be positive")
        _require(self.order_in_page >= 0 and self.order_in_document >= 0, "line order cannot be negative")
        _require(bool(self.text.strip()), "line text is required")
        geometry = (
            self.page_width, self.page_height, self.bbox_x0, self.bbox_y0,
            self.bbox_x1, self.bbox_y1,
        )
        present = tuple(value is not None for value in geometry)
        _require(all(present) or not any(present), "layout geometry must be entirely present or absent")
        if all(present):
            page_width, page_height, x0, y0, x1, y1 = geometry
            assert None not in geometry
            _require(all(math.isfinite(value) for value in geometry), "layout geometry must be finite")
            _require(page_width > 0 and page_height > 0, "page dimensions must be positive")
            _require(0 <= x0 < x1 <= page_width and 0 <= y0 < y1 <= page_height,
                     "line bbox is outside its page")
        for value, name in ((self.font_size, "font_size"), (self.max_font_size, "max_font_size")):
            if value is not None:
                _require(math.isfinite(value) and value > 0, f"{name} must be positive")
        for value, name in ((self.bold_ratio, "bold_ratio"), (self.italic_ratio, "italic_ratio")):
            if value is not None:
                _require(math.isfinite(value) and 0 <= value <= 1, f"{name} must be in [0, 1]")
        _optional_non_negative(self.column_index, "column_index")
        _optional_non_negative(self.parent_block_ordinal, "parent_block_ordinal")
        _optional_non_negative(self.markdown_codepoint_start, "markdown_codepoint_start")
        _optional_non_negative(self.markdown_codepoint_end, "markdown_codepoint_end")
        if self.markdown_codepoint_start is None or self.markdown_codepoint_end is None:
            _require(
                self.markdown_codepoint_start is None and self.markdown_codepoint_end is None,
                "Markdown range must be entirely present or absent",
            )
        else:
            _require(
                self.markdown_codepoint_end >= self.markdown_codepoint_start,
                "Markdown range is reversed",
            )
        if self.label is not None and allowed_labels is not None:
            _require(self.label in allowed_labels, f"unsupported label {self.label!r}")

    def to_dict(self) -> dict[str, Any]:
        self.validate()
        return asdict(self)

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "LayoutLine":
        line = cls(**dict(value))
        line.validate()
        return line


@dataclass(frozen=True)
class TrainingSequence:
    """One indivisible, document-grouped sample emitted by a converter."""

    task: str
    source: str
    document_id: str
    sequence_id: str
    lines: tuple[LayoutLine, ...] = ()
    tokens: tuple["CitationToken", ...] = ()
    split: str | None = None
    schema_version: str = SEQUENCE_SCHEMA_VERSION

    def validate(self) -> None:
        _require(self.schema_version == SEQUENCE_SCHEMA_VERSION, "unsupported sequence schema version")
        _require(self.task in {"header", "reference_layout", "citation"}, "unsupported task")
        _require(bool(self.source.strip()) and bool(self.document_id.strip()), "source and document_id are required")
        _require(bool(self.sequence_id.strip()), "sequence_id is required")
        _require(self.split in {None, "train", "validation", "test", "hard_test"}, "unsupported split")
        if self.task == "citation":
            _require(bool(self.tokens) and not self.lines, "citation sequences require only tokens")
            for token in self.tokens:
                token.validate()
        else:
            _require(bool(self.lines) and not self.tokens, "line sequences require only lines")
            allowed = HEADER_LABELS if self.task == "header" else REFERENCE_LAYOUT_LABELS
            for line in self.lines:
                line.validate(allowed)
        _require(all(item.document_id == self.document_id for item in (*self.lines, *self.tokens)),
                 "sequence items must share document_id")

    def to_dict(self) -> dict[str, Any]:
        self.validate()
        value = asdict(self)
        value["lines"] = [asdict(line) for line in self.lines]
        value["tokens"] = [asdict(token) for token in self.tokens]
        return value

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "TrainingSequence":
        data = dict(value)
        data["lines"] = tuple(LayoutLine.from_dict(line) for line in data.get("lines", ()))
        data["tokens"] = tuple(CitationToken(**token) for token in data.get("tokens", ()))
        sequence = cls(**data)
        sequence.validate()
        return sequence


@dataclass(frozen=True)
class ReferenceEntry:
    """One bibliography entry assembled from contiguous layout lines."""

    document_id: str
    entry_index: int
    line_orders: tuple[int, ...]
    raw_text: str
    schema_version: str = REFERENCE_ENTRY_SCHEMA_VERSION

    def validate(self) -> None:
        _require(self.schema_version == REFERENCE_ENTRY_SCHEMA_VERSION, "unsupported reference-entry schema version")
        _require(bool(self.document_id.strip()), "document_id is required")
        _require(self.entry_index >= 0, "entry_index cannot be negative")
        _require(bool(self.raw_text.strip()), "raw_text is required")
        _require(bool(self.line_orders), "line_orders are required")
        _require(all(value >= 0 for value in self.line_orders), "line order cannot be negative")
        _require(tuple(sorted(set(self.line_orders))) == self.line_orders, "line_orders must be unique and sorted")


@dataclass(frozen=True)
class CitationToken:
    """One token and its half-open range within a single reference entry."""

    document_id: str
    reference_index: int
    token_index: int
    text: str
    char_start: int
    char_end: int
    label: str = "O"
    schema_version: str = CITATION_TOKEN_SCHEMA_VERSION

    def validate(self) -> None:
        _require(self.schema_version == CITATION_TOKEN_SCHEMA_VERSION, "unsupported citation-token schema version")
        _require(bool(self.document_id.strip()), "document_id is required")
        _require(self.reference_index >= 0 and self.token_index >= 0, "token indexes cannot be negative")
        _require(bool(self.text), "token text is required")
        _require(0 <= self.char_start < self.char_end, "token range is invalid")
        _require(self.label in CITATION_LABELS, f"unsupported citation label {self.label!r}")


@dataclass(frozen=True)
class ModelArtifactManifest:
    """Metadata required to reproduce and safely load one trained CRF."""

    model_name: str
    model_version: str
    feature_version: str
    labels: tuple[str, ...]
    corpus_manifest_sha256: str
    model_sha256: str
    training_parameters: Mapping[str, Any] = field(default_factory=dict)
    metrics: Mapping[str, float | int | None] = field(default_factory=dict)
    schema_version: str = ARTIFACT_SCHEMA_VERSION

    def validate(self) -> None:
        _require(self.schema_version == ARTIFACT_SCHEMA_VERSION, "unsupported artifact schema version")
        _require(bool(self.model_name.strip()), "model_name is required")
        _require(bool(self.model_version.strip()), "model_version is required")
        _require(bool(self.feature_version.strip()), "feature_version is required")
        _require(bool(self.labels) and len(set(self.labels)) == len(self.labels), "labels must be unique")
        for digest, name in (
            (self.corpus_manifest_sha256, "corpus_manifest_sha256"),
            (self.model_sha256, "model_sha256"),
        ):
            _require(len(digest) == 64 and all(ch in "0123456789abcdef" for ch in digest), f"{name} must be lowercase SHA-256")
