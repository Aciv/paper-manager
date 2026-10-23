"""Pure, shared feature extraction for Header and reference-layout CRFs."""

from __future__ import annotations

from collections import Counter
import math
import re
from typing import Iterable

from .header_priors import header_title_priors
from .schema import LayoutLine


FEATURE_VERSION = "paper-line-crf/v2"
_WORD_RE = re.compile(r"[^\W_]+", re.UNICODE)
_YEAR_RE = re.compile(r"\b(?:19|20)\d{2}[a-z]?\b")
_REF_PREFIX_RE = re.compile(r"^\s*(?:\[\d+\]|\d+[.)])\s*")
_AUTHOR_INITIAL_RE = re.compile(r"\b[A-Z][.]\s*(?:[A-Z][.]\s*)?[A-Z][\w'’-]+")
_REFERENCE_HEADING_RE = re.compile(r"^\s*(?:references|bibliography|literature cited|参考文献)\b", re.I)
_ABSTRACT_HEADING_RE = re.compile(r"^\s*(?:abstract|摘要)\b", re.I)


def _words(text: str) -> list[str]:
    return _WORD_RE.findall(text)


def _shape(text: str) -> str:
    """Compact token-shape prior; stable across unseen lexical content."""
    result = []
    for char in text[:80]:
        if char.isupper():
            result.append("A")
        elif char.islower():
            result.append("a")
        elif char.isdigit():
            result.append("0")
        elif char.isspace():
            result.append(" ")
        else:
            result.append(char)
    return "".join(result)


def _text_features(line: LayoutLine) -> dict[str, object]:
    text = line.text.strip()
    words = _words(text)
    chars = max(len(text), 1)
    lower = text.casefold()
    return {
        "bias": 1.0,
        "text.lower": lower[:160],
        "text.prefix": lower[:12],
        "text.suffix": lower[-12:],
        "text.shape": _shape(text),
        "text.word_count": str(len(words)),
        "text.char_bucket": str(min(len(text) // 20, 12)),
        "text.upper_ratio": round(sum(char.isupper() for char in text) / chars, 3),
        "text.digit_ratio": round(sum(char.isdigit() for char in text) / chars, 3),
        "text.punctuation_ratio": round(sum(not char.isalnum() and not char.isspace() for char in text) / chars, 3),
        "text.ends_period": text.endswith("."),
        "text.has_year": bool(_YEAR_RE.search(text)),
        "text.reference_prefix": bool(_REF_PREFIX_RE.match(text)),
        "text.author_initial_pattern": bool(_AUTHOR_INITIAL_RE.search(text)),
        "text.reference_heading": bool(_REFERENCE_HEADING_RE.match(text)),
        "text.abstract_heading": bool(_ABSTRACT_HEADING_RE.match(text)),
        "text.has_email": "@" in text,
    }


def _layout_features(lines: tuple[LayoutLine, ...], index: int) -> dict[str, object]:
    line = lines[index]
    features: dict[str, object] = {
        "layout.has_geometry": line.bbox_x0 is not None,
        "layout.has_font": line.font_size is not None,
        "layout.bold_ratio": round(line.bold_ratio or 0.0, 3),
        "layout.italic_ratio": round(line.italic_ratio or 0.0, 3),
        "layout.column": str(line.column_index) if line.column_index is not None else "unknown",
        "layout.page_transition_from_previous": index > 0 and lines[index - 1].page_number != line.page_number,
    }
    if line.bbox_x0 is not None and line.page_width is not None:
        features.update({
            "layout.x0": round(line.bbox_x0 / line.page_width, 3),
            "layout.x1": round((line.bbox_x1 or 0.0) / line.page_width, 3),
            "layout.width": round(((line.bbox_x1 or 0.0) - line.bbox_x0) / line.page_width, 3),
        })
    if line.bbox_y0 is not None and line.page_height is not None:
        features["layout.y0"] = round(line.bbox_y0 / line.page_height, 3)
        features["layout.y1"] = round((line.bbox_y1 or 0.0) / line.page_height, 3)
    if line.font_size is not None:
        known_sizes = [candidate.font_size for candidate in lines if candidate.font_size is not None]
        median = sorted(known_sizes)[len(known_sizes) // 2] if known_sizes else line.font_size
        features["layout.font_relative_median"] = round(line.font_size / median, 3)
        if line.max_font_size is not None:
            features["layout.font_relative_max"] = round(line.font_size / line.max_font_size, 3)
    if index:
        previous = lines[index - 1]
        if line.bbox_y0 is not None and previous.bbox_y1 is not None and line.page_number == previous.page_number:
            features["layout.vertical_gap"] = round(max(0.0, line.bbox_y0 - previous.bbox_y1), 2)
        if line.bbox_x0 is not None and previous.bbox_x0 is not None:
            features["layout.indent_delta"] = round(line.bbox_x0 - previous.bbox_x0, 2)
        if line.font_size is not None and previous.font_size is not None:
            features["layout.font_delta"] = round(line.font_size - previous.font_size, 2)
    return features


def extract_line_features(lines: Iterable[LayoutLine], use_layout: bool) -> list[dict[str, object]]:
    """Return CRFsuite-compatible features without reading external state.

    The function accepts the source facts persisted in :class:`LayoutLine`; it
    derives all normalization and neighbour signals here, so training and live
    inference share byte-for-byte feature semantics.
    """
    values = tuple(lines)
    title_priors = header_title_priors(values)
    result: list[dict[str, object]] = []
    for index, line in enumerate(values):
        features = _text_features(line)
        features.update(title_priors[index])
        features["context.prev_lower"] = values[index - 1].text.casefold()[:80] if index else "<BOS>"
        features["context.next_lower"] = values[index + 1].text.casefold()[:80] if index + 1 < len(values) else "<EOS>"
        features["context.position_decile"] = str(min(9, math.floor(index * 10 / max(len(values), 1))))
        if use_layout:
            features.update(_layout_features(values, index))
        result.append(features)
    return result


def golden_feature_snapshot(lines: Iterable[LayoutLine], use_layout: bool) -> dict[str, object]:
    """Return a stable, JSON-friendly feature snapshot for regression tests."""
    features = extract_line_features(lines, use_layout)
    types = Counter(type(value).__name__ for row in features for value in row.values())
    return {"feature_version": FEATURE_VERSION, "rows": features, "value_types": dict(sorted(types.items()))}
