"""
Deterministic paper-section extraction from parser-generated Markdown.

This module keeps paper metadata extraction small and explainable. It extracts
the title, author names, abstract, and raw reference entries from the Markdown
emitted by :mod:`pymupdf4llm`. It deliberately does not parse or look up DOI
values: ``references`` remains a ``list[str]`` for later independent handling.
"""
from __future__ import annotations

import argparse
import json
import re
from dataclasses import asdict, dataclass, field
from pathlib import Path


_HEADING_RE = re.compile(r"^(?P<marks>#+)\s+(?P<text>.*?)\s*$")
_BOLD_RE = re.compile(r"\*\*(?P<text>.+?)\*\*")
_HTML_TAG_RE = re.compile(r"<[^>]+>")
_MULTI_SPACE_RE = re.compile(r"\s+")
_EMAIL_RE = re.compile(r"[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}", re.IGNORECASE)
_SUPERSCRIPT_RE = re.compile(r"<sup>.*?</sup>", re.IGNORECASE)
_AUTHOR_SEPARATOR_RE = re.compile(r"\s*(?:;|；|、|\s+and\s+|\s+和\s+)\s*", re.IGNORECASE)
_COMMA_SEPARATOR_RE = re.compile(r"\s*[,，]\s*")
_REFERENCE_ITEM_START_RE = re.compile(r"^\s*(?:\[\d+\]|\d+[.)]|[-*+])\s+")

_ABSTRACT_HEADINGS = {"abstract", "abstruct", "摘要"}
_REFERENCES_HEADINGS = {"references", "bibliography", "参考文献"}
_AFFILIATION_HINTS = {
    "academy", "college", "company", "department", "group", "institute",
    "laboratory", "school", "university", "公司", "大学", "学院", "实验室", "研究所",
}


@dataclass
class MarkdownPaperSections:
    """Paper-oriented sections detected by deterministic Markdown rules."""

    title: str = ""
    authors: tuple[str, ...] = ()
    abstract: str = ""
    # Raw reference Markdown is preserved exactly as an independent list.
    references: list[str] = field(default_factory=list)
    # Source ranges identify the exact Markdown region selected by a rule.
    # The extractor uses them to attach PDF-block evidence without re-searching
    # the extracted text in the full document.
    title_span: MarkdownTextSpan | None = None
    author_spans: tuple[MarkdownTextSpan, ...] = ()
    abstract_span: MarkdownTextSpan | None = None


@dataclass(frozen=True)
class MarkdownTextSpan:
    """One extracted value and its half-open code-point range in Markdown."""

    value: str
    start: int
    end: int


def extract_paper_sections(markdown: str) -> MarkdownPaperSections:
    """Extract title, authors, abstract, and raw references from Markdown.

    The first heading is the title regardless of depth. Bold text after that
    title and before the first later section is treated as author candidates.
    ``Abstract``/``Abstruct``/``摘要`` and
    ``References``/``Bibliography``/``参考文献`` delimit the corresponding
    sections. Reference strings are not DOI-parsed or enriched here.
    """
    if not markdown or not markdown.strip():
        return MarkdownPaperSections()

    normalized_markdown = markdown.replace("\r\n", "\n").replace("\r", "\n")
    lines = normalized_markdown.split("\n")
    line_offsets = _line_offsets(lines)
    title_index, title_span = _find_title(lines, line_offsets)
    if title_index is None:
        return MarkdownPaperSections()
    assert title_span is not None

    abstract_index = _find_heading(lines, title_index + 1, _ABSTRACT_HEADINGS)
    references_index = _find_heading(lines, title_index + 1, _REFERENCES_HEADINGS)
    first_body_heading = _find_first_body_heading(lines, title_index + 1)
    header_end = _first_index(
        index for index in (abstract_index, references_index, first_body_heading) if index is not None
    )
    if header_end is None:
        header_end = min(len(lines), title_index + 31)

    author_spans, last_author_line = _extract_authors(
        lines, line_offsets, title_index + 1, header_end,
    )
    abstract_span = _extract_abstract(lines, line_offsets, abstract_index)
    if abstract_span is None:
        abstract_span = _extract_fallback_abstract(
            lines, line_offsets, last_author_line, first_body_heading,
        )

    references: list[str] = []
    if references_index is not None:
        references = _split_reference_items(_join_section(lines, references_index + 1))
    return MarkdownPaperSections(
        title=title_span.value,
        authors=tuple(span.value for span in author_spans),
        abstract="" if abstract_span is None else abstract_span.value,
        references=references,
        title_span=title_span,
        author_spans=tuple(author_spans),
        abstract_span=abstract_span,
    )


def _find_title(
    lines: list[str], line_offsets: list[int],
) -> tuple[int | None, MarkdownTextSpan | None]:
    """Return the first non-empty Markdown heading as its line index and title."""
    for index, line in enumerate(lines):
        heading = _parse_heading(line)
        if heading is None:
            continue
        value = _clean_inline_text(heading[1])
        if value:
            content_start = lines[index].find(heading[1])
            return index, MarkdownTextSpan(
                value=value,
                start=line_offsets[index] + content_start,
                end=line_offsets[index] + content_start + len(heading[1]),
            )
    return None, None


def _find_heading(lines: list[str], start: int, targets: set[str]) -> int | None:
    """Find the first normalized heading at or after ``start`` matching ``targets``."""
    for index in range(start, len(lines)):
        heading = _parse_heading(lines[index])
        if heading is not None and _normalize_heading(heading[1]) in targets:
            return index
    return None


def _find_first_body_heading(lines: list[str], start: int) -> int | None:
    """Find the first content heading while skipping metadata-section headings."""
    for index in range(start, len(lines)):
        heading = _parse_heading(lines[index])
        if heading is None:
            continue
        normalized = _normalize_heading(heading[1])
        if normalized in _ABSTRACT_HEADINGS or normalized in _REFERENCES_HEADINGS:
            continue
        if _is_author_heading(heading[1]):
            continue
        return index
    return None


def _extract_authors(
    lines: list[str], line_offsets: list[int], start: int, end: int,
) -> tuple[list[MarkdownTextSpan], int | None]:
    """Extract unique bold author names from the document header interval."""
    authors: list[MarkdownTextSpan] = []
    seen: set[str] = set()
    last_author_line: int | None = None
    for index in range(start, end):
        for match in _BOLD_RE.finditer(lines[index]):
            for candidate in _split_author_text(match.group("text")):
                key = candidate.casefold()
                if key in seen or not _looks_like_author(candidate):
                    continue
                seen.add(key)
                authors.append(
                    MarkdownTextSpan(
                        value=candidate,
                        start=line_offsets[index] + match.start("text"),
                        end=line_offsets[index] + match.end("text"),
                    )
                )
                last_author_line = index
    return authors, last_author_line


def _split_author_text(value: str) -> list[str]:
    """Normalize one bold author string and split it on supported separators."""
    clean = _clean_author_text(value)
    if not clean:
        return []
    result: list[str] = []
    for part in _AUTHOR_SEPARATOR_RE.split(clean):
        result.extend(piece for piece in _COMMA_SEPARATOR_RE.split(part) if piece)
    return result


def extract_author_names(value: str) -> tuple[str, ...]:
    """Return plausible author names contained in bold fragments of one block."""
    names: list[str] = []
    seen: set[str] = set()
    for match in _BOLD_RE.finditer(value):
        for candidate in _split_author_text(match.group("text")):
            key = candidate.casefold()
            if key in seen or not _looks_like_author(candidate):
                continue
            seen.add(key)
            names.append(candidate)
    return tuple(names)


def section_kind(value: str) -> str | None:
    """Classify a heading value as an abstract or reference section when known."""
    normalized = _normalize_heading(value)
    if normalized in _ABSTRACT_HEADINGS:
        return "abstract"
    if normalized in _REFERENCES_HEADINGS:
        return "references"
    return None


def is_markdown_heading(value: str) -> bool:
    """Report whether ``value`` uses the supported ATX Markdown heading syntax."""
    return _parse_heading(value) is not None


def is_affiliation_only(value: str) -> bool:
    """Expose the affiliation detector for block-oriented extraction fallback."""
    return _looks_like_affiliation_only(value)


def _extract_abstract(
    lines: list[str], line_offsets: list[int], abstract_index: int | None,
) -> MarkdownTextSpan | None:
    """Return the body under an explicit abstract heading, if present."""
    return None if abstract_index is None else _section_span(lines, line_offsets, abstract_index + 1)


def _extract_fallback_abstract(
    lines: list[str], line_offsets: list[int], last_author_line: int | None, first_body_heading: int | None,
) -> MarkdownTextSpan | None:
    """Use the text between authors and the first body heading as a safe fallback."""
    if last_author_line is None or first_body_heading is None or first_body_heading <= last_author_line + 1:
        return None
    span = _span_for_lines(lines, line_offsets, last_author_line + 1, first_body_heading)
    return None if span is None or _looks_like_affiliation_only(span.value) else span


def _section_span(
    lines: list[str], line_offsets: list[int], start: int,
) -> MarkdownTextSpan | None:
    """Return the cleaned text and source range from ``start`` to the next heading."""
    end = start
    while end < len(lines) and _parse_heading(lines[end]) is None:
        end += 1
    return _span_for_lines(lines, line_offsets, start, end)


def _span_for_lines(
    lines: list[str], line_offsets: list[int], start: int, end: int,
) -> MarkdownTextSpan | None:
    """Join, trim, and retain the exact Markdown range for a line interval."""
    raw = "\n".join(lines[start:end])
    value = raw.strip()
    if not value:
        return None
    leading = len(raw) - len(raw.lstrip())
    return MarkdownTextSpan(
        value=value,
        start=line_offsets[start] + leading,
        end=line_offsets[start] + leading + len(value),
    )


def _join_section(lines: list[str], start: int) -> str:
    """Join non-heading lines from ``start`` through the next Markdown heading."""
    values: list[str] = []
    for line in lines[start:]:
        if _parse_heading(line) is not None:
            break
        values.append(line)
    return "\n".join(values).strip()


def _line_offsets(lines: list[str]) -> list[int]:
    """Return each line's code-point offset in the normalized Markdown text."""
    offsets: list[int] = []
    offset = 0
    for line in lines:
        offsets.append(offset)
        offset += len(line) + 1
    return offsets


def _split_reference_items(section: str) -> list[str]:
    """Split list-like references while preserving raw citation text."""
    lines = [line.strip() for line in section.splitlines() if line.strip()]
    if not lines:
        return []
    if not any(_REFERENCE_ITEM_START_RE.match(line) for line in lines):
        return lines

    items: list[str] = []
    current: list[str] = []
    for line in lines:
        if _REFERENCE_ITEM_START_RE.match(line):
            if current:
                items.append(" ".join(current))
            current = [line]
        elif current:
            current.append(line)
        else:
            current = [line]
    if current:
        items.append(" ".join(current))
    return items


def split_reference_items(section: str) -> list[str]:
    """Split one raw reference block while preserving each citation's text."""
    return _split_reference_items(section)


def _parse_heading(line: str) -> tuple[int, str] | None:
    """Parse a Markdown heading into its depth and uncleaned text, if valid."""
    match = _HEADING_RE.match(line)
    return None if match is None else (len(match.group("marks")), match.group("text"))


def _normalize_heading(value: str) -> str:
    """Produce a case-insensitive, whitespace-normalized heading comparison key."""
    return _MULTI_SPACE_RE.sub(" ", _clean_inline_text(value)).strip().casefold()


def _clean_inline_text(value: str) -> str:
    """Remove lightweight Markdown/HTML markup from a heading or inline value."""
    value = value.strip()
    if (match := _BOLD_RE.fullmatch(value)) is not None:
        value = match.group("text")
    value = _HTML_TAG_RE.sub("", value)
    return _MULTI_SPACE_RE.sub(" ", value).strip(" \t*_`#")


def _clean_author_text(value: str) -> str:
    """Remove author footnotes and formatting while retaining the name text."""
    value = _HTML_TAG_RE.sub("", value)
    return _MULTI_SPACE_RE.sub(" ", value).strip(" \t,，;；*†‡")


def _looks_like_author(value: str) -> bool:
    """Reject affiliation, email, numbering, and malformed values as author names."""
    if len(value) < 2 or len(value) > 120 or any(char.isdigit() for char in value):
        return False
    normalized = value.casefold()
    if normalized in {"author", "authors", "作者"} or "@" in value:
        return False
    if any(word in normalized for word in _AFFILIATION_HINTS):
        return False
    return not any(char in value for char in "\n:[]{}")


def _is_author_heading(value: str) -> bool:
    """Report whether a bold heading contains only plausible author candidates."""
    match = _BOLD_RE.fullmatch(value.strip())
    if match is None:
        return False
    candidates = _split_author_text(match.group("text"))
    return bool(candidates) and all(_looks_like_author(candidate) for candidate in candidates)


def _looks_like_affiliation_only(value: str) -> bool:
    """Detect affiliation/email text so it is not promoted to fallback abstract."""
    normalized = value.casefold()
    has_affiliation_hint = any(hint in normalized for hint in _AFFILIATION_HINTS)
    has_email = _EMAIL_RE.search(value) is not None
    return has_affiliation_hint and (has_email or len(value.splitlines()) <= 2)


def _first_index(indexes) -> int | None:
    """Return the earliest supplied index, or ``None`` when the iterable is empty."""
    values = list(indexes)
    return min(values) if values else None


def main() -> None:
    """Print deterministic Markdown metadata for local debugging."""
    parser = argparse.ArgumentParser(description="Extract paper metadata from Markdown")
    parser.add_argument("markdown_file", type=Path)
    args = parser.parse_args()
    markdown = args.markdown_file.read_text(encoding="utf-8")
    print(json.dumps(asdict(extract_paper_sections(markdown)), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
