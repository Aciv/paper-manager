"""Safe cleanup for Markdown generated from PDF pages.

The cleaner removes known parser/OCR noise while preserving Markdown syntax.
It also returns a raw-to-cleaned code-point map because PyMuPDF4LLM page-box
positions refer to the *unmodified* page Markdown.
"""
from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass


_PICTURE_PLACEHOLDER_RE = re.compile(
    r"^\s*\*\*==> picture \[.*?\] intentionally omitted <==\*\*\s*$",
    re.IGNORECASE,
)
_PICTURE_TEXT_MARKER_RE = re.compile(
    r"^\s*\*\*-{5,}\s*(?:Start|End) of picture text\s*-{5,}\*\*\s*$",
    re.IGNORECASE,
)
_GARBLED_TEXT_RE = re.compile(r"^\s*n{3,}\s*,?\s*<br>\s*$", re.IGNORECASE)
_HTML_BR_ONLY_RE = re.compile(r"^\s*<br>\s*$", re.IGNORECASE)
_PAGE_NUMBER_RE = re.compile(r"^\s*\d{1,4}\s*$")


@dataclass(frozen=True)
class MarkdownCleanupResult:
    """A cleaned page and a mapping from raw to cleaned string boundaries."""

    text: str
    offset_map: tuple[int, ...]
    removed_lines: int
    removed_characters: int

    def map_range(self, start: int, end: int) -> tuple[int, int] | None:
        """Map one valid raw Markdown range into the cleaned Markdown."""
        if start < 0 or end < start or end >= len(self.offset_map):
            return None
        return self.offset_map[start], self.offset_map[end]


def clean_markdown_page(text: str) -> MarkdownCleanupResult:
    """Remove known OCR/parser noise without destroying Markdown structure.

    The input is one parser page.  This lets the page-number rule remain
    conservative: only an isolated numeric first or last nonblank line is
    removed.  Headings, list items, tables, formulas, image links and code
    fences are never removed merely for being short.
    """
    if not text:
        return MarkdownCleanupResult(text="", offset_map=(0,), removed_lines=0, removed_characters=0)

    keep = [not _is_invalid_character(char) for char in text]
    ranges = list(_line_ranges(text))
    nonblank_lines = [
        index
        for index, (start, content_end, _) in enumerate(ranges)
        if _visible_line(text, keep, start, content_end).strip()
    ]
    first_nonblank = nonblank_lines[0] if nonblank_lines else None
    last_nonblank = nonblank_lines[-1] if nonblank_lines else None

    removed_lines = 0
    previous_blank = False
    for index, (start, content_end, end) in enumerate(ranges):
        # Strip trailing whitespace from a line but retain its newline.
        line_end = content_end
        while line_end > start and text[line_end - 1].isspace():
            keep[line_end - 1] = False
            line_end -= 1

        line = _visible_line(text, keep, start, line_end)
        stripped = line.strip()
        remove_line = _is_junk_line(stripped) or _is_isolated_page_number(
            stripped, index, first_nonblank, last_nonblank
        )
        if remove_line:
            _drop(keep, start, end)
            removed_lines += 1
            continue

        if not stripped:
            if previous_blank:
                _drop(keep, start, end)
                removed_lines += 1
                continue
            previous_blank = True
        else:
            previous_blank = False

    # A final empty line is formatting noise.  Remove it after blank-line
    # collapse so the generated Markdown has a stable ending.
    for start, content_end, end in reversed(ranges):
        if _visible_line(text, keep, start, content_end).strip():
            break
        if any(keep[position] for position in range(start, end)):
            _drop(keep, start, end)
            removed_lines += 1

    output: list[str] = []
    offset_map = [0] * (len(text) + 1)
    for index, char in enumerate(text):
        offset_map[index] = len(output)
        if keep[index]:
            # Normalize CRLF / legacy CR to LF without changing a source map
            # boundary more than necessary.  PyMuPDF4LLM normally emits LF.
            if char == "\r":
                continue
            output.append(char)
    offset_map[len(text)] = len(output)

    cleaned = "".join(output)
    return MarkdownCleanupResult(
        text=cleaned,
        offset_map=tuple(offset_map),
        removed_lines=removed_lines,
        removed_characters=len(text) - len(cleaned),
    )


def _line_ranges(text: str):
    """Yield ``(start, content_end, end)`` ranges, including each newline."""
    start = 0
    for line in text.splitlines(keepends=True):
        end = start + len(line)
        content_end = end
        if line.endswith("\r\n"):
            content_end -= 2
        elif line.endswith(("\n", "\r")):
            content_end -= 1
        yield start, content_end, end
        start = end
    if start < len(text):
        yield start, len(text), len(text)


def _visible_line(text: str, keep: list[bool], start: int, end: int) -> str:
    return "".join(text[position] for position in range(start, end) if keep[position])


def _is_invalid_character(char: str) -> bool:
    """Remove decoding artifacts and unsafe controls, not ordinary Unicode."""
    if char == "\ufffd":
        return True
    category = unicodedata.category(char)
    # Surrogates cannot be encoded to UTF-8.  Other private/unassigned Unicode
    # code points are retained because PDFs may use them for meaningful glyphs.
    if category == "Cs":
        return True
    return category == "Cc" and char not in {"\t", "\n", "\r"}


def _is_junk_line(stripped: str) -> bool:
    if not stripped:
        return False
    return bool(
        _HTML_BR_ONLY_RE.fullmatch(stripped)
        or _PICTURE_PLACEHOLDER_RE.fullmatch(stripped)
        or _PICTURE_TEXT_MARKER_RE.fullmatch(stripped)
        or _GARBLED_TEXT_RE.fullmatch(stripped)
    )


def _is_isolated_page_number(
    stripped: str, index: int, first_nonblank: int | None, last_nonblank: int | None
) -> bool:
    return bool(
        _PAGE_NUMBER_RE.fullmatch(stripped)
        and index in {first_nonblank, last_nonblank}
    )


def _drop(keep: list[bool], start: int, end: int) -> None:
    for position in range(start, end):
        keep[position] = False
