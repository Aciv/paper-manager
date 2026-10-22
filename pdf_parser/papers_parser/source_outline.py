"""Bounded source-outline selection for one recognition generation."""
from __future__ import annotations

import math
import re
from bisect import bisect_left
from dataclasses import dataclass

import document_parser_pb2


OUTLINE_SCHEMA_VERSION = 1
MAX_OUTLINE_ITEMS = 512
MAX_OUTLINE_DEPTH = 8
MAX_OUTLINE_TITLE_CODEPOINTS = 512
MAX_OUTLINE_DIAGNOSTICS = 32

_ATX_HEADING = re.compile(r"^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$")
_SETEXT_HEADING = re.compile(r"^[ \t]*(=+|-+)[ \t]*$")
_FENCE = re.compile(r"^[ \t]{0,3}(`{3,}|~{3,})")
_MARKDOWN_EMPHASIS = re.compile(r"(\*\*|~~)(.+?)\1")


@dataclass
class _Candidate:
    title: str
    level: int
    start_page: int = 0
    start_x: float = 0.0
    start_y: float = 0.0
    start_block: int = -1
    markdown_start: int = -1


def select_source_outline(pdf, markdown: str, blocks, checkpoint=None):
    """Prefer a usable native outline, otherwise derive final-Markdown headings."""
    diagnostics: list[str] = []
    native = _native_candidates(pdf, blocks, diagnostics, checkpoint)
    if native:
        return _build_outline("pdf_outline", native, blocks, len(markdown), diagnostics)
    headings = _markdown_candidates(markdown, blocks, diagnostics, checkpoint)
    if headings:
        return _build_outline("markdown_headings", headings, blocks, len(markdown), diagnostics)
    _diagnose(diagnostics, "outline_unavailable")
    return document_parser_pb2.RecognitionOutline(
        schema_version=OUTLINE_SCHEMA_VERSION,
        source="none",
        diagnostics=diagnostics,
    )


def _native_candidates(pdf, blocks, diagnostics: list[str], checkpoint=None) -> list[_Candidate]:
    try:
        toc = pdf.get_toc(simple=False)
    except Exception:
        _diagnose(diagnostics, "pdf_outline_read_failed")
        return []
    if not toc:
        _diagnose(diagnostics, "pdf_outline_missing")
        return []
    result: list[_Candidate] = []
    blocks_by_page = {}
    for block in blocks:
        blocks_by_page.setdefault(block.page_number, []).append(block)
    for page_number, page_blocks in blocks_by_page.items():
        page_blocks.sort(key=lambda block: (block.bbox_y0, block.ordinal))
        blocks_by_page[page_number] = (page_blocks, [block.bbox_y0 for block in page_blocks])
    stack_levels: list[int] = []
    for index, item in enumerate(toc[: MAX_OUTLINE_ITEMS + 1]):
        if checkpoint is not None and index % 64 == 0:
            checkpoint()
        if index >= MAX_OUTLINE_ITEMS:
            _diagnose(diagnostics, "pdf_outline_item_limit")
            break
        if not isinstance(item, (list, tuple)) or len(item) < 3:
            _diagnose(diagnostics, "pdf_outline_invalid_item")
            continue
        title = _title(item[1])
        try:
            page_number = int(item[2])
        except (TypeError, ValueError):
            page_number = 0
        if not title or page_number < 1 or page_number > len(pdf):
            _diagnose(diagnostics, "pdf_outline_invalid_destination")
            continue
        raw_level = _positive_int(item[0], 1)
        while stack_levels and stack_levels[-1] >= raw_level:
            stack_levels.pop()
        level = min(len(stack_levels) + 1, MAX_OUTLINE_DEPTH)
        if level == MAX_OUTLINE_DEPTH and len(stack_levels) >= MAX_OUTLINE_DEPTH:
            _diagnose(diagnostics, "pdf_outline_depth_clamped")
        stack_levels.append(raw_level)

        x = y = 0.0
        exact_destination = False
        if len(item) > 3 and isinstance(item[3], dict):
            point = item[3].get("to")
            if point is not None:
                try:
                    x, y = float(point.x), float(point.y)
                    rect = pdf[page_number - 1].rect
                    exact_destination = (
                        math.isfinite(x) and math.isfinite(y)
                        and 0 <= x <= rect.width and 0 <= y <= rect.height
                    )
                except (AttributeError, TypeError, ValueError, IndexError):
                    exact_destination = False
        block = _nearest_page_block(blocks_by_page.get(page_number), y if exact_destination else None)
        result.append(_Candidate(
            title=title,
            level=level,
            start_page=page_number,
            start_x=x if exact_destination else 0.0,
            start_y=y if exact_destination else 0.0,
            start_block=block.ordinal if block is not None else -1,
            markdown_start=(block.markdown_codepoint_start if block is not None else -1),
        ))
    if not result:
        _diagnose(diagnostics, "pdf_outline_wholly_unusable")
    return result


def _markdown_candidates(markdown: str, blocks, diagnostics: list[str], checkpoint=None) -> list[_Candidate]:
    result: list[_Candidate] = []
    lines = markdown.splitlines(keepends=True)
    offsets: list[int] = []
    offset = 0
    for line in lines:
        offsets.append(offset)
        offset += len(line)
    in_fence = False
    fence_char = ""
    fence_size = 0
    stack: list[int] = []
    ordered_blocks = sorted(blocks, key=lambda block: (block.markdown_codepoint_start, block.ordinal))
    block_starts = [block.markdown_codepoint_start for block in ordered_blocks]
    index = 0
    while index < len(lines) and len(result) < MAX_OUTLINE_ITEMS:
        if checkpoint is not None and index % 128 == 0:
            checkpoint()
        line = lines[index].rstrip("\r\n")
        fence = _FENCE.match(line)
        if fence:
            marker = fence.group(1)
            if not in_fence:
                in_fence, fence_char, fence_size = True, marker[0], len(marker)
            elif marker[0] == fence_char and len(marker) >= fence_size:
                in_fence = False
            index += 1
            continue
        if in_fence:
            index += 1
            continue
        match = _ATX_HEADING.match(line)
        heading_level = 0
        heading_title = ""
        heading_offset = offsets[index]
        if match:
            heading_level = len(match.group(1))
            heading_title = match.group(2)
        elif index + 1 < len(lines) and line.strip() and _SETEXT_HEADING.match(lines[index + 1].rstrip("\r\n")):
            marker = _SETEXT_HEADING.match(lines[index + 1].rstrip("\r\n")).group(1)
            heading_level = 1 if marker[0] == "=" else 2
            heading_title = line.strip()
            index += 1
        if heading_level:
            title = _markdown_heading_title(heading_title)
            if title:
                while stack and stack[-1] >= heading_level:
                    stack.pop()
                level = min(len(stack) + 1, MAX_OUTLINE_DEPTH)
                stack.append(heading_level)
                block = _block_for_markdown_offset(ordered_blocks, block_starts, heading_offset)
                result.append(_Candidate(
                    title=title,
                    level=level,
                    start_page=(block.page_number if block is not None else 0),
                    start_x=(block.bbox_x0 if block is not None else 0.0),
                    start_y=(block.bbox_y0 if block is not None else 0.0),
                    start_block=(block.ordinal if block is not None else -1),
                    markdown_start=heading_offset,
                ))
        index += 1
    if len(result) >= MAX_OUTLINE_ITEMS and index < len(lines):
        _diagnose(diagnostics, "markdown_outline_item_limit")
    if not result:
        _diagnose(diagnostics, "markdown_headings_missing")
    return result


def _build_outline(source: str, candidates: list[_Candidate], blocks, markdown_length: int, diagnostics):
    last_block = -1
    last_markdown = -1
    for candidate in candidates:
        if candidate.start_block >= 0 and candidate.start_block < last_block:
            candidate.start_block = -1
            candidate.markdown_start = -1
            _diagnose(diagnostics, "outline_non_monotonic_mapping_dropped")
            continue
        if candidate.markdown_start >= 0 and candidate.markdown_start < last_markdown:
            candidate.start_block = -1
            candidate.markdown_start = -1
            _diagnose(diagnostics, "outline_non_monotonic_mapping_dropped")
            continue
        if candidate.start_block >= 0:
            last_block = candidate.start_block
        if candidate.markdown_start >= 0:
            last_markdown = candidate.markdown_start
    sections = []
    for index, candidate in enumerate(candidates):
        end_index = len(candidates)
        for later in range(index + 1, len(candidates)):
            if candidates[later].level <= candidate.level:
                end_index = later
                break
        successor = candidates[end_index] if end_index < len(candidates) else None
        end_block = successor.start_block if successor is not None and successor.start_block >= 0 else len(blocks)
        if candidate.start_block < 0:
            end_block = -1
        markdown_end = successor.markdown_start if successor is not None and successor.markdown_start >= 0 else markdown_length
        if candidate.markdown_start < 0:
            markdown_end = -1
        end_page, end_x, end_y = _section_end(blocks, candidate, end_block)
        parent = -1
        for previous in range(index - 1, -1, -1):
            if candidates[previous].level < candidate.level:
                parent = previous
                break
        sections.append(document_parser_pb2.RecognitionOutlineSection(
            ordinal=index,
            title=candidate.title,
            level=candidate.level,
            parent_ordinal=parent,
            start_page=candidate.start_page,
            start_x=candidate.start_x,
            start_y=candidate.start_y,
            end_page=end_page,
            end_x=end_x,
            end_y=end_y,
            start_block_ordinal=candidate.start_block,
            end_block_ordinal=end_block,
            markdown_codepoint_start=candidate.markdown_start,
            markdown_codepoint_end=markdown_end,
        ))
    return document_parser_pb2.RecognitionOutline(
        schema_version=OUTLINE_SCHEMA_VERSION,
        source=source,
        sections=sections,
        diagnostics=diagnostics[:MAX_OUTLINE_DIAGNOSTICS],
    )


def _section_end(blocks, candidate: _Candidate, end_block: int):
    if candidate.start_block >= 0 and end_block > candidate.start_block:
        block = blocks[min(end_block, len(blocks)) - 1]
        return block.page_number, block.bbox_x1, block.bbox_y1
    return candidate.start_page, candidate.start_x, candidate.start_y


def _nearest_page_block(page_index, y: float | None):
    if not page_index:
        return None
    page_blocks, positions = page_index
    if y is None:
        return page_blocks[0]
    index = bisect_left(positions, y)
    candidates = page_blocks[max(0, index - 1): min(len(page_blocks), index + 1)]
    return min(candidates, key=lambda block: abs(block.bbox_y0 - y))


def _block_for_markdown_offset(blocks, starts, offset: int):
    if not blocks:
        return None
    index = bisect_left(starts, offset)
    candidates = blocks[max(0, index - 1): min(len(blocks), index + 1)]
    containing = [block for block in candidates if block.markdown_codepoint_start <= offset <= block.markdown_codepoint_end]
    if containing:
        return containing[0]
    return min(candidates, key=lambda block: (abs(block.markdown_codepoint_start - offset), block.ordinal))


def _markdown_heading_title(value) -> str:
    text = str(value or "").strip()
    previous = None
    while text and text != previous:
        previous = text
        text = _MARKDOWN_EMPHASIS.sub(r"\2", text).strip()
    return _title(text)


def _title(value) -> str:
    text = str(value or "").strip()
    return text[:MAX_OUTLINE_TITLE_CODEPOINTS]


def _positive_int(value, default: int) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return default
    return parsed if parsed > 0 else default


def _diagnose(diagnostics: list[str], code: str) -> None:
    if len(diagnostics) < MAX_OUTLINE_DIAGNOSTICS and code not in diagnostics:
        diagnostics.append(code)
