
from __future__ import annotations


import logging
import math
import time

import os
import urllib.request

from pathlib import Path
from typing import Sequence
from urllib.parse import urlsplit
from uuid import UUID

import grpc
import pymupdf
import pymupdf4llm

import document_parser_pb2
import document_parser_pb2_grpc
from logger import get_logger
from papers_parser.markdown_cleaner import clean_markdown_page
from papers_parser.paper_extractor import LayoutLine, SourceBlock
from papers_parser.rpc_control import RpcCheckpoint, check_rpc



def _log_level_from_env() -> int:
    """Return a safe standard-library logging level from ``LOG_LEVEL``."""
    configured = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, configured, None)
    return level if isinstance(level, int) else logging.INFO


LOG = get_logger(__name__, _log_level_from_env())



SUPPORTED_TYPES = {"application/pdf"}


class ParserLimitExceeded(Exception):
    """A PDF exceeded a configured page, crop, or response-size budget."""


class RPCAbort(Exception):
    """A status intended for the client, raised from inside the broad handler.

    ``grpc.ServicerContext.abort`` raises a bare ``Exception``; calling it
    inside Recognize's generic ``except Exception`` block would be caught and
    converted into INTERNAL. Validation helpers raise this typed error instead
    and Recognize forwards its status unchanged.
    """

    def __init__(self, code: grpc.StatusCode, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


class DocumentParser(document_parser_pb2_grpc.DocumentParserServicer):
    parser_name = "pymupdf4llm"

    def __init__(
        self,
        upload_dir: Path,
        max_file_size: int,
        image_dpi: int,
        *,
        max_pages: int = 2000,
        max_page_edge_points: float = 5000.0,
        max_crop_pixels: int = 20_000_000,
        max_crops_per_document: int = 500,
        max_crop_bytes: int = 48 << 20,
        max_markdown_bytes: int = 32 << 20,
        max_response_bytes: int = 90 << 20,
        rpc_max_seconds: float = 900,
    ):
        self.upload_dir = upload_dir.resolve()
        self.max_file_size = max_file_size
        self.image_dpi = image_dpi
        self.max_pages = max_pages
        self.max_page_edge_points = max_page_edge_points
        self.max_crop_pixels = max_crop_pixels
        self.max_crops_per_document = max_crops_per_document
        self.max_crop_bytes = max_crop_bytes
        self.max_markdown_bytes = max_markdown_bytes
        self.max_response_bytes = max_response_bytes
        self.rpc_max_seconds = rpc_max_seconds

    def Recognize(self, request, context):  # noqa: N802 - gRPC generated name
        if request.content_type not in SUPPORTED_TYPES:
            context.abort(grpc.StatusCode.UNIMPLEMENTED, f"unsupported content type: {request.content_type}")
        try:
            UUID(request.document_id)
        except ValueError:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "document_id must be a UUID")
        if not request.filename:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "filename is required")

        pdf = None
        started_at = time.monotonic()

        def checkpoint() -> None:
            check_rpc(context, started_at, self.rpc_max_seconds)

        try:
            # The PDF stays in memory for every input mode. Writing content to a
            # temporary file only moved the memory cost to a tmpfs that is
            # commonly smaller than the upload limit.
            pdf = self._open_source(request, context)
            checkpoint()

            self._check_pdf_limits(pdf, checkpoint)
            # to_markdown accepts an open Document; raw bytes are not a valid
            # filename argument.
            pages = pymupdf4llm.to_markdown(pdf, page_chunks=True)
            if len(pages) > self.max_pages:
                raise ParserLimitExceeded("document exceeds parser page limit")

            raw_page_text = [page["text"] if isinstance(page, dict) else str(page) for page in pages]
            cleaned_pages = [clean_markdown_page(text) for text in raw_page_text]
            page_text = [page.text for page in cleaned_pages]
            markdown = "\n\n".join(page_text).encode("utf-8")
            if len(markdown) > self.max_markdown_bytes:
                raise ParserLimitExceeded("recognition Markdown exceeds parser size limit")
            checkpoint()

            stem = Path(request.filename).stem or "document"
            blocks = self._extract_blocks(
                pdf, pages, raw_page_text, cleaned_pages, checkpoint,
            )
            self._check_response_budget(markdown, blocks)
            result = document_parser_pb2.RecognitionResult(
                markdown=markdown,
                markdown_filename=f"{stem}.md",
                content_type="text/markdown",
                size_bytes=len(markdown),
                page_count=len(page_text),
                parser_name=self.parser_name,
                parser_version=getattr(pymupdf4llm, "__version__", ""),
                blocks=blocks,
            )
            LOG.info(
                "recognized document=%s pages=%d blocks=%d markdown_bytes=%d",
                request.document_id,
                len(page_text),
                len(blocks),
                len(markdown),
            )
            removed_lines = sum(page.removed_lines for page in cleaned_pages)
            removed_characters = sum(page.removed_characters for page in cleaned_pages)
            if removed_lines or removed_characters:
                LOG.info(
                    "markdown cleanup applied document=%s removed_lines=%d removed_characters=%d",
                    request.document_id,
                    removed_lines,
                    removed_characters,
                )
            return result
        except RPCAbort as exc:
            context.abort(exc.code, exc.message)
        except grpc.RpcError:
            raise
        except ParserLimitExceeded as exc:
            LOG.warning("recognition limit exceeded for document=%s: %s", request.document_id, exc)
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, str(exc)[:8192])
        except Exception as exc:
            LOG.exception("recognition failed for document=%s", request.document_id)
            context.abort(grpc.StatusCode.INTERNAL, str(exc)[:8192])
        finally:
            if pdf is not None:
                pdf.close()

    def _extract_blocks(
        self, pdf, pages, raw_page_text: list[str], cleaned_pages,
        checkpoint: RpcCheckpoint = None,
    ):
        """
        Return source-map blocks and 72-DPI PNG crops for visual blocks.

        PyMuPDF4LLM's page_boxes positions refer to Python Unicode code-point
        offsets in the page Markdown. We join pages with two newlines, so the
        cumulative offset below describes the complete returned Markdown.
        Image/table bytes remain inside the gRPC response only long enough for
        the Go API to save them under its durable image/ category. They never
        become Base64 Markdown or a public parser filesystem path.
        """
        blocks = []
        markdown_offset = 0
        crop_count = 0
        crop_bytes = 0
        for page_index, raw_page in enumerate(pages):
            if checkpoint is not None:
                checkpoint()
            raw_text = raw_page_text[page_index]
            cleaned_page = cleaned_pages[page_index]
            text = cleaned_page.text
            if not isinstance(raw_page, dict) or page_index >= len(pdf):
                markdown_offset += len(text) + (2 if page_index + 1 < len(cleaned_pages) else 0)
                continue
            page_rect = pdf[page_index].rect
            for box_index, box in enumerate(raw_page.get("page_boxes", [])):
                # Cropping is the expensive per-box operation; poll often
                # enough that cancellation releases the thread promptly.
                if checkpoint is not None and box_index % 100 == 0:
                    checkpoint()
                block_type = _block_type(box.get("class", ""))
                if block_type is None:
                    continue
                bbox = box.get("bbox")
                if not _valid_bbox(bbox, page_rect):
                    continue
                position = box.get("pos")
                if _valid_position(position, raw_text):
                    raw_start, raw_end = position
                    mapped_position = cleaned_page.map_range(raw_start, raw_end)
                    if mapped_position is None:
                        continue
                    start, end = mapped_position
                    block_text = text[start:end].strip()
                elif block_type in {"image", "table"}:
                    # Some layout engines do not associate a Markdown
                    # range with a visual region. Keep a zero-width anchor
                    # on its page rather than discarding a usable crop.
                    start = end = 0
                    block_text = f"{block_type} on page {page_index + 1}"
                else:
                    continue
                if not block_text and block_type not in {"image", "table"}:
                    continue
                image_data = b""
                image_content_type = ""
                if block_type in {"image", "table"}:
                    if crop_count >= self.max_crops_per_document:
                        raise ParserLimitExceeded("document exceeds parser crop count limit")
                    try:
                        image_data = self._render_crop(pdf[page_index], bbox)
                    except ParserLimitExceeded:
                        raise
                    except Exception:
                        LOG.warning("skip unreadable %s crop on page=%d", block_type, page_index + 1, exc_info=True)
                        continue
                    crop_count += 1
                    crop_bytes += len(image_data)
                    if crop_bytes > self.max_crop_bytes:
                        raise ParserLimitExceeded("document exceeds parser crop byte budget")
                    image_content_type = "image/png"
                blocks.append(
                    document_parser_pb2.RecognitionBlock(
                        ordinal=len(blocks),
                        type=block_type,
                        text=block_text,
                        page_number=page_index + 1,
                        page_width=page_rect.width,
                        page_height=page_rect.height,
                        bbox_x0=float(bbox[0]),
                        bbox_y0=float(bbox[1]),
                        bbox_x1=float(bbox[2]),
                        bbox_y1=float(bbox[3]),
                        markdown_codepoint_start=markdown_offset + start,
                        markdown_codepoint_end=markdown_offset + end,
                        image_data=image_data,
                        image_content_type=image_content_type,
                    )
                )
            markdown_offset += len(text) + (2 if page_index + 1 < len(cleaned_pages) else 0)
        return blocks

    def _render_crop(self, page, bbox) -> bytes:
        """Render one bounded PDF rectangle as PNG at the configured DPI.

        The raster is downsampled when the requested DPI would exceed the
        per-crop pixel budget, so a huge MediaBox cannot allocate hundreds of
        megabytes in one pixmap.
        """
        clip = pymupdf.Rect(bbox)
        scale = self.image_dpi / 72.0
        requested_pixels = max(clip.width * scale, 1.0) * max(clip.height * scale, 1.0)
        if requested_pixels > self.max_crop_pixels:
            # Keep a small margin: PyMuPDF rounds pixmap dimensions up.
            scale *= math.sqrt(self.max_crop_pixels / requested_pixels) * 0.99
        pixmap = page.get_pixmap(
            matrix=pymupdf.Matrix(scale, scale),
            clip=clip,
            alpha=False,
        )
        return pixmap.tobytes("png")

    def _check_pdf_limits(self, pdf, checkpoint: RpcCheckpoint = None) -> None:
        """Reject excessive page counts or page sizes before rendering work."""
        if pdf.page_count > self.max_pages:
            raise ParserLimitExceeded("document exceeds parser page limit")
        for page in pdf:
            if checkpoint is not None:
                checkpoint()
            if (
                page.rect.width > self.max_page_edge_points
                or page.rect.height > self.max_page_edge_points
            ):
                raise ParserLimitExceeded("document page exceeds parser size limit")

    def _check_response_budget(self, markdown: bytes, blocks) -> None:
        """Estimate the assembled gRPC response before paying to serialize it."""
        response_bytes = len(markdown)
        for block in blocks:
            response_bytes += len(block.text.encode("utf-8"))
            response_bytes += len(block.image_data)
        if response_bytes > self.max_response_bytes:
            raise ParserLimitExceeded("recognition response exceeds parser size limit")

    def _open_source(self, request, context):
        """Open the requested source as an in-memory PyMuPDF document.

        Every input mode stays in memory: writing content or downloaded bytes
        to a temporary file only relocated the memory pressure to a tmpfs that
        is commonly smaller than the configured upload limit.
        """
        source_type = request.WhichOneof("source")
        if source_type == "file_path":
            source = Path(request.file_path).resolve()
            if not source.is_relative_to(self.upload_dir) or not source.is_file():
                raise RPCAbort(grpc.StatusCode.PERMISSION_DENIED, "file_path is outside the parser upload directory")
            self._assert_size(source.stat().st_size)
            return pymupdf.open(str(source))

        if source_type == "content":
            self._assert_size(len(request.content))
            return pymupdf.open(stream=request.content, filetype="pdf")

        if source_type == "source_url":
            content = self._download_source(request.source_url, context)
            return pymupdf.open(stream=content, filetype="pdf")

        raise RPCAbort(grpc.StatusCode.INVALID_ARGUMENT, "exactly one recognition source is required")

    def _download_source(self, source_url: str, context) -> bytes:
        parsed = urlsplit(source_url)
        if parsed.scheme not in {"http", "https"}:
            raise RPCAbort(grpc.StatusCode.INVALID_ARGUMENT, "source_url must use http or https")
        timeout = context.time_remaining() or 60
        timeout = max(1, min(timeout, 60))
        try:
            with urllib.request.urlopen(source_url, timeout=timeout) as response:
                content = response.read(self.max_file_size + 1)
        except Exception as exc:
            raise RPCAbort(grpc.StatusCode.UNAVAILABLE, f"download source_url: {exc}") from exc
        self._assert_size(len(content))
        return content

    def _assert_size(self, size: int) -> None:
        if size <= 0:
            raise RPCAbort(grpc.StatusCode.INVALID_ARGUMENT, "source document is empty")
        if size > self.max_file_size:
            raise RPCAbort(grpc.StatusCode.RESOURCE_EXHAUSTED, "source document exceeds parser size limit")


def extract_layout_lines_from_storage(
    upload_dir: Path,
    source_storage_key: str,
    blocks: Sequence[SourceBlock],
    max_lines: int,
    checkpoint: RpcCheckpoint = None,
) -> tuple[LayoutLine, ...]:
    """Derive temporary line facts from a Go-created key below ``upload_dir``.

    The storage key is never returned to clients. Resolving it here keeps the
    PDF-layout schema and validation wholly inside the parser while Go remains
    responsible only for the source object's lifecycle.
    """
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



def _block_type(value: str) -> str | None:
    """Map PyMuPDF4LLM layout classes to persisted source-map block types."""
    mapping = {
        "text": "text",
        "title": "title",
        "section-header": "section_header",
        "caption": "caption",
        "list-item": "list_item",
        "footnote": "footnote",
        "formula": "formula",
        "image": "image",
        "picture": "image",
        "figure": "image",
        "table": "table",
    }
    return mapping.get(value)


def _valid_position(position, text: str) -> bool:
    if not isinstance(position, (list, tuple)) or len(position) != 2:
        return False
    start, end = position
    return all(isinstance(value, int) and not isinstance(value, bool) for value in (start, end)) and 0 <= start <= end <= len(text)


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
