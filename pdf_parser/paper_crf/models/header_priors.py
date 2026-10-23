"""Rule-derived, source-only priors for Header CRF feature extraction.

These are deliberately weak signals rather than a second title extractor. They
translate the existing Markdown rule's useful structure (first heading, header
interval, and body-section boundary) into features that the CRF may override.
"""

from __future__ import annotations

import re
from typing import Sequence

from .schema import LayoutLine


_MARKDOWN_HEADING_RE = re.compile(r"^\s*#{1,6}\s+\S")
_BODY_HEADING_RE = re.compile(
    r"^\s*(?:#{1,6}\s+)?(?:"
    r"(?:\d+(?:\.\d+)*|[IVXLC]+|[一二三四五六七八九十]+)[.)]?\s+)?"
    r"(?:introduction|background|related work|method(?:ology)?|materials? and methods?|"
    r"results?|discussion|conclusion|引言|绪论|方法|结果|讨论|结论)\s*[.:]*\s*$",
    re.IGNORECASE,
)
_ABSTRACT_OR_REFERENCE_RE = re.compile(r"^\s*(?:#{1,6}\s+)?(?:abstract|abstruct|摘要|references|bibliography|参考文献)\s*[.:]*\s*$", re.I)
_FRONT_MATTER_RE = re.compile(
    r"(?:©|copyright|creative commons|all rights reserved|\bin\s*:|\bproceedings\b|\bdoi\b|"
    r"https?\s*:|\barxiv\b|\breceived\b|\baccepted\b|\bpublished\b)",
    re.IGNORECASE,
)
_JOURNAL_CITATION_RE = re.compile(
    r"^[A-Z][A-Za-z .-]{1,80},\s*\d+(?:\s*\([^)]*\))?\s*,\s*\d+(?:\s*[-–]\s*\d+)?(?:\s*,\s*(?:19|20)\d{2})?\s*$"
)
_AFFILIATION_RE = re.compile(
    r"\b(?:academy|college|company|department|group|institute|laboratory|school|university)\b|"
    r"(?:公司|大学|学院|实验室|研究所)",
    re.IGNORECASE,
)
_EMAIL_RE = re.compile(r"[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}", re.IGNORECASE)
_WORD_RE = re.compile(r"[^\W_]+", re.UNICODE)


def is_markdown_heading(text: str) -> bool:
    """Match the same ATX-heading shape used by the Markdown fallback."""
    return bool(_MARKDOWN_HEADING_RE.match(text))


def is_front_matter_noise(text: str) -> bool:
    """Detect publisher/identifier boilerplate commonly preceding a title."""
    return bool(_FRONT_MATTER_RE.search(text) or _JOURNAL_CITATION_RE.match(text.strip()))


def is_title_candidate(text: str) -> bool:
    """Return a conservative header-line candidate without asserting TITLE."""
    stripped = text.strip()
    word_count = len(_WORD_RE.findall(stripped))
    return bool(
        stripped
        and 2 <= word_count <= 40
        and len(stripped) <= 320
        and not is_front_matter_noise(stripped)
        and not _EMAIL_RE.search(stripped)
        and not _AFFILIATION_RE.search(stripped)
        and not _ABSTRACT_OR_REFERENCE_RE.match(stripped)
    )


def header_title_priors(lines: Sequence[LayoutLine]) -> list[dict[str, object]]:
    """Return per-line title priors derived exclusively from raw line text/order."""
    texts = [line.text.strip() for line in lines]
    markdown_indexes = [index for index, text in enumerate(texts) if is_markdown_heading(text)]
    first_markdown = markdown_indexes[0] if markdown_indexes else None
    body_indexes = [
        index for index, text in enumerate(texts)
        if _BODY_HEADING_RE.match(text) and index != first_markdown
    ]
    first_body = body_indexes[0] if body_indexes else None
    first_nonempty = next((index for index, text in enumerate(texts) if text), None)
    candidates = [index for index, text in enumerate(texts) if is_title_candidate(text)]
    candidate_rank = {index: rank for rank, index in enumerate(candidates)}
    first_candidate = candidates[0] if candidates else None
    output: list[dict[str, object]] = []
    for index, text in enumerate(texts):
        rank = candidate_rank.get(index)
        output.append({
            "header.markdown_heading": is_markdown_heading(text),
            "header.is_first_markdown_heading": index == first_markdown,
            "header.is_first_nonempty": index == first_nonempty,
            "header.front_matter_noise": is_front_matter_noise(text),
            "header.is_title_candidate": rank is not None,
            "header.is_first_title_candidate": index == first_candidate,
            "header.title_candidate_rank": "none" if rank is None else str(min(rank, 5)),
            "header.distance_from_first_title_candidate": (
                "none" if first_candidate is None else str(min(abs(index - first_candidate), 5))
            ),
            "header.is_body_heading": bool(_BODY_HEADING_RE.match(text)),
            "header.before_first_body_heading": first_body is None or index < first_body,
        })
    return output
