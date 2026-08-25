"""Synchronous PDF-to-Markdown gRPC parser.

The Go API owns document state and database writes. This process receives one
authorized source, converts it to Markdown, and returns only parser output.
"""
from __future__ import annotations

import argparse
import logging
import os
from concurrent import futures
from pathlib import Path

import grpc

import document_parser_pb2_grpc
from logger import get_logger
from papers_parser.extractor_service import PaperMetadataExtractionRPC
from papers_parser.paper_extractor import DeterministicPaperExtractionEngine
from papers_parser.pdf2md_service import DocumentParser


def _log_level_from_env() -> int:
    """Return a safe standard-library logging level from ``LOG_LEVEL``."""
    configured = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, configured, None)
    return level if isinstance(level, int) else logging.INFO


LOG = get_logger(__name__, _log_level_from_env())

PROJECT_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_UPLOAD_DIR = PROJECT_ROOT / "data" / "uploads"

DEFAULT_MAX_FILE_SIZE = int(os.getenv("DEFAULT_MAX_FILE_SIZE", 100 << 20))
DEFAULT_GRPC_MESSAGE_OVERHEAD = int(os.getenv("DEFAULT_GRPC_MESSAGE_OVERHEAD", 1 << 20))
MAX_EXTRACTION_MARKDOWN_BYTES = int(os.getenv("MAX_EXTRACTION_MARKDOWN_BYTES", 32 << 20))
MAX_EXTRACTION_BLOCKS = int(os.getenv("MAX_EXTRACTION_BLOCKS", 50000))
# 72 DPI is intentionally the default for cropped page regions. 720 DPI uses
# roughly 100x as many pixels as 72 DPI and is unsuitable for routine block
# previews; deployments that need print-quality captures can override it.
DEFAULT_IMAGE_DPI = int(os.getenv("DEFAULT_IMAGE_DPI", 72))




def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--address", default=os.getenv("PARSER_GRPC_ADDR", "[::]:50051"))
    parser.add_argument("--upload-dir", default=os.getenv("UPLOAD_DIR", str(DEFAULT_UPLOAD_DIR)))
    parser.add_argument("--max-file-size", type=int, default=int(os.getenv("PARSER_MAX_FILE_SIZE", DEFAULT_MAX_FILE_SIZE)))
    parser.add_argument("--image-dpi", type=int, default=int(os.getenv("PARSER_IMAGE_DPI", DEFAULT_IMAGE_DPI)))
    parser.add_argument(
        "--max-extraction-markdown-bytes",
        type=int,
        default=int(os.getenv("PARSER_MAX_EXTRACTION_MARKDOWN_BYTES", MAX_EXTRACTION_MARKDOWN_BYTES)),
    )
    parser.add_argument(
        "--max-extraction-blocks",
        type=int,
        default=int(os.getenv("PARSER_MAX_EXTRACTION_BLOCKS", MAX_EXTRACTION_BLOCKS)),
    )
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
    if args.max_extraction_markdown_bytes <= 0:
        raise ValueError("PARSER_MAX_EXTRACTION_MARKDOWN_BYTES must be positive")
    if args.max_extraction_blocks <= 0:
        raise ValueError("PARSER_MAX_EXTRACTION_BLOCKS must be positive")

    server = grpc.server(
        futures.ThreadPoolExecutor(max_workers=10),
        options=[
            ("grpc.max_receive_message_length", args.grpc_max_message_size),
            ("grpc.max_send_message_length", args.grpc_max_message_size),
        ],
    )
    document_parser_pb2_grpc.add_DocumentParserServicer_to_server(
        DocumentParser(Path(args.upload_dir), args.max_file_size, args.image_dpi),
        server,
    )
    document_parser_pb2_grpc.add_PaperMetadataExtractorServicer_to_server(
        PaperMetadataExtractionRPC(
            DeterministicPaperExtractionEngine(),
            args.max_extraction_markdown_bytes,
            args.max_extraction_blocks,
        ),
        server,
    )
    server.add_insecure_port(args.address)
    server.start()
    LOG.info(
        "parser services listening on %s extraction_markdown_limit=%d extraction_block_limit=%d",
        args.address,
        args.max_extraction_markdown_bytes,
        args.max_extraction_blocks,
    )
    server.wait_for_termination()




if __name__ == "__main__":
    main()
