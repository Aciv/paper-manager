"""PyMuPDF-only layout facts used by the extraction worker.

Keeping this module free of PyMuPDF4LLM/ONNX imports lets the extraction
worker load CRF and PyMuPDF without also loading the recognition stack.
"""
from __future__ import annotations

from pathlib import Path
from typing import Sequence

import pymupdf

from papers_parser.paper_extractor import LayoutLine, SourceBlock
from papers_parser.rpc_control import RpcCheckpoint


def extract_layout_lines_from_storage(
    upload_dir: Path,
    source_storage_key: str,
    blocks: Sequence[SourceBlock],
    max_lines: int,
    checkpoint: RpcCheckpoint = None,
) -> tuple[LayoutLine, ...]:
    """Derive temporary line facts from a Go-created key below ``upload_dir``."""
    key = Path(source_storage_key)
    root = upload_dir.resolve()
    if not source_storage_key or key.is_absolute():
        raise ValueError("source storage key must be a relative path")
    source_path = (root / key).resolve()
    if not source_path.is_relative_to(root) or not source_path.is_file():
        raise ValueError("source storage key is outside the parser upload directory")
    if source_path.suffix.lower() != ".pdf":
        raise ValueError("source storage key does not identify a PDF")
    return extract_layout_lines_from_pdf(source_path, blocks, max_lines, checkpoint)


def extract_layout_lines_from_pdf(
    source_path: Path,
    blocks: Sequence[SourceBlock],
    max_lines: int,
    checkpoint: RpcCheckpoint = None,
) -> tuple[LayoutLine, ...]:
    """Build CRF-only layout facts from text lines inside persisted Blocks."""
    if max_lines <= 0:
        raise ValueError("maximum layout line count must be positive")
    output: list[LayoutLine] = []
    with pymupdf.open(str(source_path)) as pdf:
        for page_index, page in enumerate(pdf):
            if checkpoint is not None:
                checkpoint()
            page_blocks = [
                block for block in blocks
                if block.location is not None and block.location.page_number == page_index + 1
            ]
            if not page_blocks:
                continue
            order_in_page = 0
            for raw_block in page.get_text("dict", sort=True).get("blocks", []):
                for raw_line in raw_block.get("lines", []):
                    if checkpoint is not None and len(output) % 1000 == 0:
                        checkpoint()
                    spans = raw_line.get("spans", [])
                    text = "".join(str(span.get("text", "")) for span in spans).strip()
                    bbox = raw_line.get("bbox")
                    if not text or not _valid_bbox(bbox, page.rect):
                        order_in_page += 1
                        continue
                    parent, markdown_range = _layout_parent_block(text, bbox, page_blocks)
                    if parent is None:
                        order_in_page += 1
                        continue
                    char_count = max(sum(len(str(span.get("text", ""))) for span in spans), 1)
                    sizes = [float(span.get("size", 0)) for span in spans if float(span.get("size", 0)) > 0]
                    font_size = sum(float(span.get("size", 0)) * len(str(span.get("text", ""))) for span in spans) / char_count if sizes else None
                    max_font_size = max(sizes) if sizes else None
                    bold_ratio = sum(len(str(span.get("text", ""))) for span in spans if int(span.get("flags", 0)) & 16) / char_count
                    italic_ratio = sum(len(str(span.get("text", ""))) for span in spans if int(span.get("flags", 0)) & 2) / char_count
                    font_name = next((str(span.get("font", "")).strip() for span in spans if str(span.get("font", "")).strip()), None)
                    width = float(bbox[2]) - float(bbox[0])
                    column_index = 0 if width >= page.rect.width * 0.65 or float(bbox[0]) < page.rect.width * 0.5 else 1
                    output.append(LayoutLine(
                        page_number=page_index + 1,
                        order_in_page=order_in_page,
                        order_in_document=len(output),
                        text=text,
                        page_width=page.rect.width,
                        page_height=page.rect.height,
                        bbox_x0=float(bbox[0]), bbox_y0=float(bbox[1]),
                        bbox_x1=float(bbox[2]), bbox_y1=float(bbox[3]),
                        font_name=font_name,
                        font_size=font_size,
                        max_font_size=max_font_size,
                        bold_ratio=bold_ratio,
                        italic_ratio=italic_ratio,
                        column_index=column_index,
                        markdown_codepoint_start=None if markdown_range is None else markdown_range[0],
                        markdown_codepoint_end=None if markdown_range is None else markdown_range[1],
                        parent_block_ordinal=parent.ordinal,
                    ))
                    if len(output) > max_lines:
                        raise ValueError("derived layout line count exceeds parser limit")
                    order_in_page += 1
    return tuple(output)


def _valid_bbox(bbox, page_rect: pymupdf.Rect) -> bool:
    if not isinstance(bbox, (list, tuple)) or len(bbox) != 4:
        return False
    x0, y0, x1, y1 = bbox
    if not all(isinstance(value, (int, float)) for value in (x0, y0, x1, y1)):
        return False
    return (
        0 <= x0 < x1 <= page_rect.width
        and 0 <= y0 < y1 <= page_rect.height
    )


def _layout_parent_block(text: str, bbox, blocks: Sequence[SourceBlock]):
    """Return the strongest overlapping semantic block and a local text range."""
    line_area = max((float(bbox[2]) - float(bbox[0])) * (float(bbox[3]) - float(bbox[1])), 1.0)
    best = None
    best_score = 0.0
    for block in blocks:
        location = block.location
        if location is None:
            continue
        overlap_x = max(0.0, min(float(bbox[2]), location.bbox_x1) - max(float(bbox[0]), location.bbox_x0))
        overlap_y = max(0.0, min(float(bbox[3]), location.bbox_y1) - max(float(bbox[1]), location.bbox_y0))
        score = overlap_x * overlap_y / line_area
        if score > best_score:
            best, best_score = block, score
    if best is None or best_score < 0.45:
        return None, None
    if best.location is None or not best.text or best.location.markdown_codepoint_end < best.location.markdown_codepoint_start:
        return best, None
    offset = best.text.find(text)
    if offset < 0:
        return best, None
    return best, (
        best.location.markdown_codepoint_start + offset,
        best.location.markdown_codepoint_start + offset + len(text),
    )
