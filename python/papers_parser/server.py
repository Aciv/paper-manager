"""Synchronous PDF-to-Markdown gRPC parser.

The Go API owns document state and database writes. This process receives one
authorized source, converts it to Markdown, and returns only parser output.
"""
from __future__ import annotations

import argparse
import hashlib
import logging
import os
import tempfile
import urllib.request
from concurrent import futures
from pathlib import Path
from urllib.parse import urlsplit
from uuid import UUID

import grpc
import pymupdf
import pymupdf4llm

import document_parser_pb2
import document_parser_pb2_grpc

LOG = logging.getLogger(__name__)
SUPPORTED_TYPES = {"application/pdf"}
PROJECT_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_UPLOAD_DIR = PROJECT_ROOT / "data" / "uploads"
DEFAULT_MAX_FILE_SIZE = 100 << 20
DEFAULT_GRPC_MESSAGE_OVERHEAD = 1 << 20
# 72 DPI is intentionally the default for cropped page regions. 720 DPI uses
# roughly 100x as many pixels as 72 DPI and is unsuitable for routine block
# previews; deployments that need print-quality captures can override it.
DEFAULT_IMAGE_DPI = 72


class DocumentParser(document_parser_pb2_grpc.DocumentParserServicer):
    parser_name = "pymupdf4llm"

    def __init__(self, upload_dir: Path, max_file_size: int, image_dpi: int):
        self.upload_dir = upload_dir.resolve()
        self.max_file_size = max_file_size
        self.image_dpi = image_dpi

    def Recognize(self, request, context):  # noqa: N802 - gRPC generated name
        if request.content_type not in SUPPORTED_TYPES:
            context.abort(grpc.StatusCode.UNIMPLEMENTED, f"unsupported content type: {request.content_type}")
        try:
            UUID(request.document_id)
        except ValueError:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "document_id must be a UUID")
        if not request.filename:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "filename is required")

        source_path = None
        temporary_path = None
        try:
            source_path, temporary_path = self._materialize_source(request, context)
            if not context.is_active():
                context.abort(grpc.StatusCode.CANCELLED, "recognition request was cancelled")

            pages = pymupdf4llm.to_markdown(str(source_path), page_chunks=True)
            page_text = [page["text"] if isinstance(page, dict) else str(page) for page in pages]
            markdown = "\n\n".join(page_text).encode("utf-8")
            if not context.is_active():
                context.abort(grpc.StatusCode.CANCELLED, "recognition request was cancelled")

            stem = Path(request.filename).stem or "document"
            blocks = self._extract_blocks(source_path, pages, page_text)
            result = document_parser_pb2.RecognitionResult(
                markdown=markdown,
                markdown_filename=f"{stem}.md",
                content_type="text/markdown",
                size_bytes=len(markdown),
                sha256=hashlib.sha256(markdown).hexdigest(),
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
            return result
        except grpc.RpcError:
            raise
        except Exception as exc:
            LOG.exception("recognition failed for document=%s", request.document_id)
            context.abort(grpc.StatusCode.INTERNAL, str(exc)[:8192])
        finally:
            if temporary_path is not None:
                temporary_path.unlink(missing_ok=True)

    def _extract_blocks(self, source_path: Path, pages, page_text: list[str]):
        """Return source-map blocks and 72-DPI PNG crops for visual blocks.

        PyMuPDF4LLM's page_boxes positions refer to Python Unicode code-point
        offsets in the page Markdown. We join pages with two newlines, so the
        cumulative offset below describes the complete returned Markdown.
        Image/table bytes remain inside the gRPC response only long enough for
        the Go API to save them under its durable image/ category. They never
        become Base64 Markdown or a public parser filesystem path.
        """
        blocks = []
        markdown_offset = 0
        with pymupdf.open(str(source_path)) as pdf:
            for page_index, raw_page in enumerate(pages):
                text = page_text[page_index]
                if not isinstance(raw_page, dict) or page_index >= len(pdf):
                    markdown_offset += len(text) + (2 if page_index + 1 < len(page_text) else 0)
                    continue
                page_rect = pdf[page_index].rect
                for box in raw_page.get("page_boxes", []):
                    block_type = _block_type(box.get("class", ""))
                    if block_type is None:
                        continue
                    bbox = box.get("bbox")
                    if not _valid_bbox(bbox, page_rect):
                        continue
                    position = box.get("pos")
                    if _valid_position(position, text):
                        start, end = int(position[0]), int(position[1])
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
                        try:
                            image_data = self._render_crop(pdf[page_index], bbox)
                            image_content_type = "image/png"
                        except Exception:
                            LOG.warning("skip unreadable %s crop on page=%d", block_type, page_index + 1, exc_info=True)
                            continue
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
                markdown_offset += len(text) + (2 if page_index + 1 < len(page_text) else 0)
        return blocks

    def _render_crop(self, page, bbox) -> bytes:
        """Render one bounded PDF rectangle as PNG at the configured DPI."""
        scale = self.image_dpi / 72.0
        pixmap = page.get_pixmap(
            matrix=pymupdf.Matrix(scale, scale),
            clip=pymupdf.Rect(bbox),
            alpha=False,
        )
        return pixmap.tobytes("png")

    def _materialize_source(self, request, context):
        source_type = request.WhichOneof("source")
        if source_type == "file_path":
            source = Path(request.file_path).resolve()
            if not source.is_relative_to(self.upload_dir) or not source.is_file():
                context.abort(grpc.StatusCode.PERMISSION_DENIED, "file_path is outside the parser upload directory")
            self._assert_size(source.stat().st_size, context)
            return source, None

        if source_type == "content":
            self._assert_size(len(request.content), context)
            temporary = self._write_temporary_source(request.content)
            return temporary, temporary

        if source_type == "source_url":
            return self._download_source(request.source_url, context)

        context.abort(grpc.StatusCode.INVALID_ARGUMENT, "exactly one recognition source is required")

    def _write_temporary_source(self, content: bytes):
        descriptor, path = tempfile.mkstemp(prefix="recognition-", suffix=".pdf")
        with os.fdopen(descriptor, "wb") as output:
            output.write(content)
        return Path(path)

    def _download_source(self, source_url: str, context):
        parsed = urlsplit(source_url)
        if parsed.scheme not in {"http", "https"}:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "source_url must use http or https")
        timeout = context.time_remaining() or 60
        timeout = max(1, min(timeout, 60))
        try:
            with urllib.request.urlopen(source_url, timeout=timeout) as response:
                content = response.read(self.max_file_size + 1)
        except Exception as exc:
            context.abort(grpc.StatusCode.UNAVAILABLE, f"download source_url: {exc}")
        self._assert_size(len(content), context)
        temporary = self._write_temporary_source(content)
        return temporary, temporary

    def _assert_size(self, size: int, context) -> None:
        if size <= 0:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, "source document is empty")
        if size > self.max_file_size:
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, "source document exceeds parser size limit")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--address", default=os.getenv("PARSER_GRPC_ADDR", "[::]:50051"))
    parser.add_argument("--upload-dir", default=os.getenv("UPLOAD_DIR", str(DEFAULT_UPLOAD_DIR)))
    parser.add_argument("--max-file-size", type=int, default=int(os.getenv("PARSER_MAX_FILE_SIZE", DEFAULT_MAX_FILE_SIZE)))
    parser.add_argument("--image-dpi", type=int, default=int(os.getenv("PARSER_IMAGE_DPI", DEFAULT_IMAGE_DPI)))
    parser.add_argument(
        "--grpc-max-message-size",
        type=int,
        default=int(os.getenv("PARSER_GRPC_MAX_MESSAGE_SIZE", DEFAULT_MAX_FILE_SIZE + DEFAULT_GRPC_MESSAGE_OVERHEAD)),
    )
    args = parser.parse_args()
    if args.address.startswith(":"):
        args.address = f"127.0.0.1{args.address}"
    if args.max_file_size <= 0:
        raise ValueError("PARSER_MAX_FILE_SIZE must be positive")
    if args.grpc_max_message_size <= 0:
        raise ValueError("PARSER_GRPC_MAX_MESSAGE_SIZE must be positive")
    if args.image_dpi <= 0 or args.image_dpi > 720:
        raise ValueError("PARSER_IMAGE_DPI must be between 1 and 720")

    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=10),
        options=[
            ("grpc.max_receive_message_length", args.grpc_max_message_size),
            ("grpc.max_send_message_length", args.grpc_max_message_size),
        ],
    )
    document_parser_pb2_grpc.add_DocumentParserServicer_to_server(
        DocumentParser(Path(args.upload_dir), args.max_file_size, args.image_dpi), server
    )
    server.add_insecure_port(args.address)
    server.start()
    LOG.info("pymupdf4llm parser listening on %s", args.address)
    server.wait_for_termination()


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
    return all(isinstance(value, (int, float)) for value in (start, end)) and 0 <= start <= end <= len(text)


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


if __name__ == "__main__":
    logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO"))
    main()
