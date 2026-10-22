"""
Deterministic paper-section extraction from parser-generated Markdown.

This module keeps paper metadata extraction small and explainable. It extracts
the title, author names, abstract, and raw reference entries from the Markdown
emitted by :mod:`pymupdf4llm`. It deliberately does not parse or look up DOI
values: ``references`` remains a ``list[str]`` for later independent handling.
"""
from __future__ import annotations

import argparse
import difflib
import json
import re
from dataclasses import asdict, dataclass, field
from pathlib import Path


_HEADING_RE = re.compile(r"^(?P<marks>#+)\s+(?P<text>.*?)\s*$")
_BOLD_RE = re.compile(r"\*\*(?P<text>.+?)\*\*")
_HTML_TAG_RE = re.compile(r"<[^>]+>")
_MULTI_SPACE_RE = re.compile(r"\s+")
# The domain groups cannot overlap (the dot is not part of the label class),
# so long lines backtrack linearly instead of quadratically.
_EMAIL_RE = re.compile(
    r"[A-Z0-9._%+-]+@[A-Z0-9-]+(?:\.[A-Z0-9-]{1,63})+",
    re.IGNORECASE,
)
_SUPERSCRIPT_RE = re.compile(r"<sup>.*?</sup>", re.IGNORECASE)
_AUTHOR_SEPARATOR_RE = re.compile(r"\s*(?:;|；|、|\s+and\s+|\s+和\s+)\s*", re.IGNORECASE)
_COMMA_SEPARATOR_RE = re.compile(r"\s*[,，]\s*")
# Reference prefixes are parsed with linear scans below. The previous nested
# quantifier (``(?:[-*+•▪◦>\s]\s*)*``) backtracked exponentially on a long run
# of list markers followed by a non-number.
_REFERENCE_MARKER_CHARS = "-*+•▪◦>"
_ORDINAL_BRACKET_RE = re.compile(r"^\[(\d{1,9})\]")
_ORDINAL_SUFFIX_RE = re.compile(r"^(\d{1,9})\s*[.)、]")
_MAX_REFERENCE_ITEMS = 5000
_MAX_REFERENCE_ITEM_CHARS = 65536
_MAX_EXTRACTED_TEXT_CHARS = 200_000
_MAX_EMAIL_SCAN_CHARS = 20_000

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
    # True only when an explicit References/Bibliography heading was found.
    references_detected: bool = False
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
        references_detected=references_index is not None,
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
    # Title/abstract spans feed the RPC result directly; cap pathological
    # sections while keeping the recorded offsets valid for the prefix.
    value = value[:_MAX_EXTRACTED_TEXT_CHARS]
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
    lines = [_strip_blockquote(line) for line in section.splitlines() if line.strip()]
    if not lines:
        return []
    if not any(_is_reference_item_start(line) for line in lines):
        return [_bound_reference_item(line) for line in lines[:_MAX_REFERENCE_ITEMS]]

    items: list[str] = []
    current: list[str] = []
    for line in lines:
        if _is_reference_item_start(line):
            if current:
                items.append(_bound_reference_item(" ".join(current)))
                if len(items) >= _MAX_REFERENCE_ITEMS:
                    return items
            current = [line]
        elif current:
            current.append(line)
        else:
            current = [line]
    if current and len(items) < _MAX_REFERENCE_ITEMS:
        items.append(_bound_reference_item(" ".join(current)))
    return items


def split_reference_items(section: str) -> list[str]:
    """Split one raw reference block while preserving each citation's text."""
    return _split_reference_items(section)


def _strip_blockquote(line: str) -> str:
    """Remove leading pymupdf4llm blockquote markers ("> > [1] ...")."""
    value = line.strip()
    while value.startswith(">"):
        value = value[1:].lstrip()
    return value


def _strip_reference_markers(value: str) -> str:
    """Strip whitespace and prefix markers in one linear pass.

    The marker set matches the historical grammar (blockquotes, list bullets,
    and bullet glyphs) but never backtracks.
    """
    value = value.lstrip()
    while value and value[0] in _REFERENCE_MARKER_CHARS:
        value = value[1:].lstrip()
    return value


def _leading_ordinal(value: str) -> tuple[str, int] | None:
    """Match ``[1]``/``1.``/``1)``/``1、`` at the start of ``value``."""
    match = _ORDINAL_BRACKET_RE.match(value)
    if match is None:
        match = _ORDINAL_SUFFIX_RE.match(value)
    if match is None:
        return None
    return match.group(1), match.end()


def _is_reference_item_start(line: str) -> bool:
    """Linear equivalent of the old reference-item start regex."""
    value = line.lstrip()
    while value.startswith(">"):
        value = value[1:].lstrip()
    while value and value[0] in "-*+•▪◦":
        candidate = value[1:].lstrip()
        if _leading_ordinal(candidate) is not None:
            value = candidate
            continue
        # A bare list bullet starts an item only when followed by whitespace.
        return value[0] in "-*+" and len(value) > 1 and value[1].isspace() and bool(candidate)
    ordinal = _leading_ordinal(value)
    if ordinal is None:
        return False
    _, end = ordinal
    return end < len(value) and value[end].isspace()


def _bound_reference_item(value: str) -> str:
    """Bound one raw item before it reaches the RPC/DB size limits."""
    return value[:_MAX_REFERENCE_ITEM_CHARS]


def reference_item_ordinal(text: str) -> str | None:
    """Return the leading sequence number of one reference item, if present.

    List markers (``-``/``*``/bullets) before the number are ignored; the
    ordinal must be the first number of the item, written ``[1]``, ``1.``,
    ``1)`` or ``1、``. Numbers later in the text are page/venue references, not
    the item identity.
    """
    value = _strip_reference_markers(text)
    ordinal = _leading_ordinal(value)
    return None if ordinal is None else ordinal[0]


_OVERLAP_DUPLICATE_RATIO = 0.9
# Template-like short lines (e.g. "Image 2: Method A generated UI") can share
# 90%+ of their characters while being unrelated; only longer texts carry
# enough signal for overlap-based deduplication.
_OVERLAP_DUPLICATE_MIN_CHARS = 40


def _char_overlap_ratio(a: str, b: str) -> float:
    """Contiguous character overlap over the shorter text, in ``[0, 1]``.

    Uses the total length of difflib's longest matching blocks, divided by the
    shorter text length. A duplicate (one entry truncated, or one carrying
    extra page/URL suffixes) therefore scores near ``1.0``, while unrelated
    citations with the same letter distribution stay far below ``0.9``.
    """
    shorter, longer = (a, b) if len(a) <= len(b) else (b, a)
    if not shorter:
        return 0.0
    matcher = difflib.SequenceMatcher(None, shorter, longer)
    matched = sum(block.size for block in matcher.get_matching_blocks())
    return matched / len(shorter)


def filter_reference_items(items: Iterable[str]) -> list[str]:
    """Consolidate reference items with the first item as the region style.

    The style of the first non-empty item decides how the whole region is
    handled:

    - Numbered region: the first detected leading 序号 is the only identity.
      Later items repeating an already-seen number are duplicates and are
      dropped; items without a leading number break the numbered style and
      are invalid.
    - Unnumbered region (bullet/paragraph style): no numbering criterion is
      applied. Two unnumbered items (each at least
      ``_OVERLAP_DUPLICATE_MIN_CHARS`` long) whose contiguous character
      overlap covers at least 90% of the shorter text are duplicates — the
      shorter one is dropped (or replaced by a later longer duplicate).
    """
    style_numbered: bool | None = None
    seen_numbers: set[str] = set()
    kept_unnumbered: list[str] = []
    output: list[str] = []
    for item in items:
        if not item.strip():
            continue
        ordinal = reference_item_ordinal(item)
        if style_numbered is None:
            style_numbered = ordinal is not None
        if style_numbered:
            if ordinal is None or ordinal in seen_numbers:
                continue
            seen_numbers.add(ordinal)
            output.append(item)
            continue
        if ordinal is not None:
            continue
        duplicate_index: int | None = None
        for index, kept in enumerate(kept_unnumbered):
            shorter_length = min(len(item), len(kept))
            if (
                shorter_length >= _OVERLAP_DUPLICATE_MIN_CHARS
                and _char_overlap_ratio(item, kept) >= _OVERLAP_DUPLICATE_RATIO
            ):
                duplicate_index = index
                break
        if duplicate_index is None:
            output.append(item)
            kept_unnumbered.append(item)
        elif len(item) > len(kept_unnumbered[duplicate_index]):
            kept = kept_unnumbered[duplicate_index]
            output[output.index(kept)] = item
            kept_unnumbered[duplicate_index] = item
    return output


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
    bounded = value[:_MAX_EMAIL_SCAN_CHARS]
    normalized = bounded.casefold()
    has_affiliation_hint = any(hint in normalized for hint in _AFFILIATION_HINTS)
    has_email = any(
        len(match.group(0).rsplit(".", 1)[-1]) >= 2
        for match in _EMAIL_RE.finditer(bounded)
    )
    return has_affiliation_hint and (has_email or len(bounded.splitlines()) <= 2)


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
