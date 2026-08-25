from __future__ import annotations

import logging
import math
import os
from uuid import UUID

import grpc

import document_parser_pb2
import document_parser_pb2_grpc
from logger import get_logger
from papers_parser.paper_extractor import (
    ExtractionOutput,
    PaperExtractionEngine,
    SourceBlock,
    SourceLocation,
)

def _log_level_from_env() -> int:
    """Return a safe standard-library logging level from ``LOG_LEVEL``."""
    configured = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, configured, None)
    return level if isinstance(level, int) else logging.INFO


LOG = get_logger(__name__, _log_level_from_env())


class PaperMetadataExtractionRPC(
    document_parser_pb2_grpc.PaperMetadataExtractorServicer
):
    """gRPC transport for a replaceable paper-extraction engine."""

    def __init__(
        self,
        engine: PaperExtractionEngine,
        max_markdown_bytes: int,
        max_blocks: int,
    ):
        """Build the extractor with limits selected by the server bootstrap."""
        if max_markdown_bytes <= 0:
            raise ValueError("max_markdown_bytes must be positive")
        if max_blocks <= 0:
            raise ValueError("max_blocks must be positive")
        self.engine = engine
        self.max_markdown_bytes = max_markdown_bytes
        self.max_blocks = max_blocks

    def Extract(self, request, context):  # noqa: N802 - gRPC generated name
        try:
            markdown, blocks = _decode_extraction_request(
                request,
                self.max_markdown_bytes,
                self.max_blocks,
            )
        except ValueError as exc:
            return context.abort(
                grpc.StatusCode.INVALID_ARGUMENT,
                str(exc)[:8192],
            )
        if not context.is_active():
            context.abort(
                grpc.StatusCode.CANCELLED,
                "paper extraction request was cancelled",
            )
        try:
            output = self.engine.extract(markdown, blocks)
            response = _encode_extraction_output(output)
            if not context.is_active():
                context.abort(
                    grpc.StatusCode.CANCELLED,
                    "paper extraction request was cancelled",
                )
            LOG.info(
                "paper metadata extracted document=%s title_found=%s "
                "author_count=%d reference_count=%d",
                request.document_id,
                response.HasField("title"),
                len(response.authors),
                len(response.references),
            )
            return response
        except grpc.RpcError:
            raise
        except Exception:
            LOG.exception(
                "paper metadata extraction failed document=%s",
                request.document_id,
            )
            context.abort(
                grpc.StatusCode.INTERNAL,
                "paper metadata extraction failed",
            )


def _decode_extraction_request(
    request,
    max_markdown_bytes: int,
    max_blocks: int,
) -> tuple[str, tuple[SourceBlock, ...]]:
    """Validate a server-bounded protobuf request and map it to engine input."""
    try:
        UUID(request.document_id)
    except ValueError as exc:
        raise ValueError("document_id must be a UUID") from exc
    if not request.markdown:
        raise ValueError("markdown is required")
    if len(request.markdown) > max_markdown_bytes:
        raise ValueError("markdown exceeds extraction size limit")
    try:
        markdown = request.markdown.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ValueError("markdown must be valid UTF-8") from exc

    if len(request.blocks) > max_blocks:
        raise ValueError("block count exceeds extraction limit")
    blocks: list[SourceBlock] = []
    ordinals: set[int] = set()
    for block in request.blocks:
        if block.ordinal < 0:
            raise ValueError("block ordinal cannot be negative")
        if block.ordinal in ordinals:
            raise ValueError(f"duplicate block ordinal {block.ordinal}")
        ordinals.add(block.ordinal)
        block_type = block.type.strip()
        if not block_type:
            raise ValueError(f"block {block.ordinal} type is required")
        location = None
        if block.HasField("location"):
            location = _decode_source_location(block.location, len(markdown))
        blocks.append(
            SourceBlock(
                ordinal=block.ordinal,
                type=block_type,
                text=block.text,
                location=location,
            )
        )
    return markdown, tuple(blocks)


def _decode_source_location(location, markdown_length: int) -> SourceLocation:
    if location.page_number < 1:
        raise ValueError("source location page_number must be positive")
    if (
        not math.isfinite(location.page_width)
        or not math.isfinite(location.page_height)
        or location.page_width <= 0
        or location.page_height <= 0
    ):
        raise ValueError("source location page dimensions must be positive")
    if (
        not all(
            math.isfinite(value)
            for value in (
                location.bbox_x0,
                location.bbox_y0,
                location.bbox_x1,
                location.bbox_y1,
            )
        )
        or location.bbox_x0 < 0
        or location.bbox_y0 < 0
        or location.bbox_x1 <= location.bbox_x0
        or location.bbox_y1 <= location.bbox_y0
        or location.bbox_x1 > location.page_width
        or location.bbox_y1 > location.page_height
    ):
        raise ValueError("source location bbox is outside its page")
    if (
        location.markdown_codepoint_start < 0
        or location.markdown_codepoint_end
        < location.markdown_codepoint_start
        or location.markdown_codepoint_end > markdown_length
    ):
        raise ValueError("source location Markdown range is invalid")
    return SourceLocation(
        page_number=location.page_number,
        page_width=location.page_width,
        page_height=location.page_height,
        bbox_x0=location.bbox_x0,
        bbox_y0=location.bbox_y0,
        bbox_x1=location.bbox_x1,
        bbox_y1=location.bbox_y1,
        markdown_codepoint_start=location.markdown_codepoint_start,
        markdown_codepoint_end=location.markdown_codepoint_end,
    )


def _encode_extraction_output(output: ExtractionOutput):
    if not output.extractor_name.strip():
        raise ValueError("extractor_name is required")
    references = list(output.references)
    if any(not reference.strip() for reference in references):
        raise ValueError("references cannot contain empty values")
    response = document_parser_pb2.PaperExtractionResult(
        references=references,
        extractor_name=output.extractor_name,
        extractor_version=output.extractor_version,
    )
    if output.title is not None:
        response.title.CopyFrom(_encode_text_candidate(output.title))
    for author in output.authors:
        response.authors.add().CopyFrom(_encode_text_candidate(author))
    if output.abstract is not None:
        response.abstract.CopyFrom(_encode_text_candidate(output.abstract))
    return response


def _encode_text_candidate(candidate):
    if not candidate.value.strip():
        raise ValueError("candidate value is required")
    if (
        not math.isfinite(candidate.confidence)
        or candidate.confidence < 0
        or candidate.confidence > 1
    ):
        raise ValueError("candidate confidence must be between 0 and 1")
    message = document_parser_pb2.PaperTextCandidate(
        value=candidate.value,
        confidence=candidate.confidence,
    )
    for evidence in candidate.evidence:
        if (
            evidence.block_ordinal < 0
            or evidence.page_number < 1
            or evidence.markdown_codepoint_start < 0
            or evidence.markdown_codepoint_end
            < evidence.markdown_codepoint_start
        ):
            raise ValueError("candidate evidence is invalid")
        message.evidence.add(
            block_ordinal=evidence.block_ordinal,
            page_number=evidence.page_number,
            markdown_codepoint_start=evidence.markdown_codepoint_start,
            markdown_codepoint_end=evidence.markdown_codepoint_end,
        )
    return message
