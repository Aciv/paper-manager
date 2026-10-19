"""Synchronous PDF-to-Markdown gRPC parser.

The Go API owns document state and database writes. This process receives one
authorized source, converts it to Markdown, and returns only parser output.
"""
from __future__ import annotations

import argparse
import logging
import os
import signal
import threading
from concurrent import futures
from pathlib import Path

import grpc

import document_parser_pb2_grpc
from logger import get_logger
from papers_parser.extractor_service import PaperMetadataExtractionRPC
from papers_parser.crf_engine import (
    CRFPaperExtractionEngine,
    HybridPaperExtractionEngine,
    ShadowPaperExtractionEngine,
)
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


def _resolve_project_path(value: str | Path) -> Path:
    """Resolve relative parser configuration paths from the repository root.

    The development launcher changes its working directory to ``python/``
    before starting this module. Configuration lives in the repository-root
    ``.env``, so resolving ``./model_train/artifacts`` from the process working
    directory would incorrectly look below ``python/``. Absolute deployment
    paths, such as Compose's ``/models``, are left unchanged.
    """
    path = Path(value).expanduser()
    if not path.is_absolute():
        path = PROJECT_ROOT / path
    return path.resolve()

DEFAULT_MAX_FILE_SIZE = int(os.getenv("DEFAULT_MAX_FILE_SIZE", 100 << 20))
DEFAULT_GRPC_MESSAGE_OVERHEAD = int(os.getenv("DEFAULT_GRPC_MESSAGE_OVERHEAD", 1 << 20))
MAX_EXTRACTION_MARKDOWN_BYTES = int(os.getenv("MAX_EXTRACTION_MARKDOWN_BYTES", 32 << 20))
MAX_EXTRACTION_BLOCKS = int(os.getenv("MAX_EXTRACTION_BLOCKS", 50000))
MAX_EXTRACTION_LAYOUT_LINES = int(os.getenv("MAX_EXTRACTION_LAYOUT_LINES", 100000))
DEFAULT_EXTRACTION_ENGINE = os.getenv("PAPER_EXTRACTION_ENGINE", "rules").strip().lower()
DEFAULT_CRF_MODEL_DIR = _resolve_project_path(
    os.getenv("PAPER_CRF_MODEL_DIR", "model_train/artifacts"),
)
# 72 DPI is intentionally the default for cropped page regions. 720 DPI uses
# roughly 100x as many pixels as 72 DPI and is unsuitable for routine block
# previews; deployments that need print-quality captures can override it.
DEFAULT_IMAGE_DPI = int(os.getenv("DEFAULT_IMAGE_DPI", 72))
DEFAULT_MAX_PAGES = int(os.getenv("PARSER_MAX_PAGES", 2000))
DEFAULT_MAX_PAGE_EDGE_POINTS = float(os.getenv("PARSER_MAX_PAGE_EDGE_POINTS", 5000.0))
DEFAULT_MAX_CROP_PIXELS = int(os.getenv("PARSER_MAX_CROP_PIXELS", 20_000_000))
DEFAULT_MAX_CROPS_PER_DOCUMENT = int(os.getenv("PARSER_MAX_CROPS_PER_DOCUMENT", 500))
DEFAULT_MAX_CROP_BYTES = int(os.getenv("PARSER_MAX_CROP_BYTES", 48 << 20))
DEFAULT_MAX_RECOGNITION_MARKDOWN_BYTES = int(
    os.getenv("PARSER_MAX_RECOGNITION_MARKDOWN_BYTES", 32 << 20)
)
DEFAULT_MAX_IMAGE_DPI = int(os.getenv("PARSER_MAX_IMAGE_DPI", 300))
DEFAULT_MAX_WORKERS = int(os.getenv("PARSER_MAX_WORKERS", 10))
DEFAULT_MAX_CONCURRENT_RPCS = int(os.getenv("PARSER_MAX_CONCURRENT_RPCS", 10))
DEFAULT_SHUTDOWN_GRACE_SECONDS = float(os.getenv("PARSER_SHUTDOWN_GRACE_SECONDS", 30))
DEFAULT_RPC_MAX_SECONDS = float(os.getenv("PARSER_RPC_MAX_SECONDS", 900))




def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--address", default=os.getenv("PARSER_GRPC_ADDR", "[::]:50051"))
    parser.add_argument("--upload-dir", default=os.getenv("UPLOAD_DIR", str(DEFAULT_UPLOAD_DIR)))
    parser.add_argument("--max-file-size", type=int, default=int(os.getenv("PARSER_MAX_FILE_SIZE", DEFAULT_MAX_FILE_SIZE)))
    parser.add_argument("--image-dpi", type=int, default=int(os.getenv("PARSER_IMAGE_DPI", DEFAULT_IMAGE_DPI)))
    parser.add_argument("--max-image-dpi", type=int, default=DEFAULT_MAX_IMAGE_DPI)
    parser.add_argument("--max-pages", type=int, default=DEFAULT_MAX_PAGES)
    parser.add_argument("--max-page-edge-points", type=float, default=DEFAULT_MAX_PAGE_EDGE_POINTS)
    parser.add_argument("--max-crop-pixels", type=int, default=DEFAULT_MAX_CROP_PIXELS)
    parser.add_argument("--max-crops-per-document", type=int, default=DEFAULT_MAX_CROPS_PER_DOCUMENT)
    parser.add_argument("--max-crop-bytes", type=int, default=DEFAULT_MAX_CROP_BYTES)
    parser.add_argument(
        "--max-recognition-markdown-bytes",
        type=int,
        default=DEFAULT_MAX_RECOGNITION_MARKDOWN_BYTES,
    )
    parser.add_argument(
        "--max-extraction-markdown-bytes",
        type=int,
        default=int(os.getenv("PARSER_MAX_EXTRACTION_MARKDOWN_BYTES", MAX_EXTRACTION_MARKDOWN_BYTES)),
    )
    parser.add_argument("--paper-extraction-engine", choices=("rules", "crf", "hybrid", "shadow"), default=DEFAULT_EXTRACTION_ENGINE)
    parser.add_argument("--paper-crf-model-dir", type=Path, default=DEFAULT_CRF_MODEL_DIR)
    parser.add_argument(
        "--max-extraction-blocks",
        type=int,
        default=int(os.getenv("PARSER_MAX_EXTRACTION_BLOCKS", MAX_EXTRACTION_BLOCKS)),
    )
    parser.add_argument(
        "--max-extraction-layout-lines",
        type=int,
        default=int(os.getenv("PARSER_MAX_EXTRACTION_LAYOUT_LINES", MAX_EXTRACTION_LAYOUT_LINES)),
    )
    parser.add_argument(
        "--grpc-max-message-size",
        type=int,
        default=int(os.getenv("PARSER_GRPC_MAX_MESSAGE_SIZE", DEFAULT_MAX_FILE_SIZE + DEFAULT_GRPC_MESSAGE_OVERHEAD)),
    )
    parser.add_argument("--max-workers", type=int, default=DEFAULT_MAX_WORKERS)
    parser.add_argument("--max-concurrent-rpcs", type=int, default=DEFAULT_MAX_CONCURRENT_RPCS)
    parser.add_argument(
        "--shutdown-grace-seconds",
        type=float,
        default=DEFAULT_SHUTDOWN_GRACE_SECONDS,
    )
    parser.add_argument("--rpc-max-seconds", type=float, default=DEFAULT_RPC_MAX_SECONDS)
    args = parser.parse_args()
    args.paper_crf_model_dir = _resolve_project_path(args.paper_crf_model_dir)
    if args.address.startswith(":"):
        args.address = f"127.0.0.1{args.address}"
    if args.max_file_size <= 0:
        raise ValueError("PARSER_MAX_FILE_SIZE must be positive")
    if args.grpc_max_message_size <= 0:
        raise ValueError("PARSER_GRPC_MAX_MESSAGE_SIZE must be positive")
    if args.image_dpi <= 0:
        raise ValueError("PARSER_IMAGE_DPI must be positive")
    if args.max_image_dpi <= 0 or args.image_dpi > args.max_image_dpi:
        raise ValueError("PARSER_IMAGE_DPI must not exceed PARSER_MAX_IMAGE_DPI")
    if args.max_pages <= 0 or args.max_page_edge_points <= 0:
        raise ValueError("PARSER_MAX_PAGES and PARSER_MAX_PAGE_EDGE_POINTS must be positive")
    if (
        args.max_crop_pixels <= 0
        or args.max_crops_per_document <= 0
        or args.max_crop_bytes <= 0
        or args.max_recognition_markdown_bytes <= 0
    ):
        raise ValueError("parser crop and recognition byte limits must be positive")
    if args.max_extraction_markdown_bytes <= 0:
        raise ValueError("PARSER_MAX_EXTRACTION_MARKDOWN_BYTES must be positive")
    if args.max_extraction_blocks <= 0:
        raise ValueError("PARSER_MAX_EXTRACTION_BLOCKS must be positive")
    if args.max_extraction_layout_lines <= 0:
        raise ValueError("PARSER_MAX_EXTRACTION_LAYOUT_LINES must be positive")
    if args.max_workers <= 0 or args.max_concurrent_rpcs <= 0:
        raise ValueError("PARSER_MAX_WORKERS and PARSER_MAX_CONCURRENT_RPCS must be positive")
    if args.max_concurrent_rpcs > args.max_workers:
        raise ValueError("PARSER_MAX_CONCURRENT_RPCS must not exceed PARSER_MAX_WORKERS")
    if args.shutdown_grace_seconds < 0 or args.rpc_max_seconds <= 0:
        raise ValueError("PARSER_SHUTDOWN_GRACE_SECONDS must be non-negative and PARSER_RPC_MAX_SECONDS positive")

    server = _create_server(args)
    document_parser_pb2_grpc.add_DocumentParserServicer_to_server(
        DocumentParser(
            Path(args.upload_dir),
            args.max_file_size,
            args.image_dpi,
            max_pages=args.max_pages,
            max_page_edge_points=args.max_page_edge_points,
            max_crop_pixels=args.max_crop_pixels,
            max_crops_per_document=args.max_crops_per_document,
            max_crop_bytes=args.max_crop_bytes,
            max_markdown_bytes=args.max_recognition_markdown_bytes,
            max_response_bytes=max(
                args.grpc_max_message_size - DEFAULT_GRPC_MESSAGE_OVERHEAD, 1
            ),
            rpc_max_seconds=args.rpc_max_seconds,
        ),
        server,
    )
    extraction_engine = _build_extraction_engine(args.paper_extraction_engine, args.paper_crf_model_dir)
    document_parser_pb2_grpc.add_PaperMetadataExtractorServicer_to_server(
        PaperMetadataExtractionRPC(
            extraction_engine,
            args.max_extraction_markdown_bytes,
            args.max_extraction_blocks,
            Path(args.upload_dir),
            args.max_extraction_layout_lines,
            rpc_max_seconds=args.rpc_max_seconds,
        ),
        server,
    )
    server.add_insecure_port(args.address)
    stop_requested = _install_shutdown_handlers()
    server.start()
    LOG.info(
        "parser services listening on %s configured_extraction_engine=%s active_extraction_engine=%s extraction_markdown_limit=%d extraction_block_limit=%d extraction_layout_line_limit=%d max_pages=%d max_page_edge_points=%s image_dpi=%d max_image_dpi=%d max_crop_pixels=%d max_crop_bytes=%d max_workers=%d max_concurrent_rpcs=%d rpc_max_seconds=%s shutdown_grace_seconds=%s",
        args.address,
        args.paper_extraction_engine,
        extraction_engine.name,
        args.max_extraction_markdown_bytes,
        args.max_extraction_blocks,
        args.max_extraction_layout_lines,
        args.max_pages,
        args.max_page_edge_points,
        args.image_dpi,
        args.max_image_dpi,
        args.max_crop_pixels,
        args.max_crop_bytes,
        args.max_workers,
        args.max_concurrent_rpcs,
        args.rpc_max_seconds,
        args.shutdown_grace_seconds,
    )
    stop_requested.wait()
    LOG.info(
        "stopping parser services (grace=%ss)",
        args.shutdown_grace_seconds,
    )
    stopped = server.stop(grace=args.shutdown_grace_seconds)
    if not stopped.wait(timeout=args.shutdown_grace_seconds + 5):
        LOG.warning("parser services did not stop within the grace period")
    LOG.info("parser services stopped")


def _create_server(args) -> grpc.Server:
    """Build the gRPC server with bounded concurrency.

    ``maximum_concurrent_rpcs`` rejects excess requests with
    RESOURCE_EXHAUSTED instead of queueing them behind a saturated thread pool.
    """
    return grpc.server(
        futures.ThreadPoolExecutor(max_workers=args.max_workers),
        options=[
            ("grpc.max_receive_message_length", args.grpc_max_message_size),
            ("grpc.max_send_message_length", args.grpc_max_message_size),
        ],
        maximum_concurrent_rpcs=args.max_concurrent_rpcs,
    )


def _install_shutdown_handlers() -> threading.Event:
    """Stop accepting work on SIGTERM/SIGINT and let in-flight RPCs drain."""
    stop_requested = threading.Event()

    def handle_signal(signum, _frame) -> None:
        LOG.info("received signal %s; requesting parser shutdown", signum)
        stop_requested.set()

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)
    return stop_requested


def _build_extraction_engine(mode: str, model_dir: Path):
    """Construct rules/CRF/hybrid/shadow once; only CRF mode is fail-closed."""
    rules = DeterministicPaperExtractionEngine()
    if mode == "rules":
        return rules
    try:
        crf = CRFPaperExtractionEngine(
            header_model=model_dir / "phase21" / "header-text-title-priors.crfsuite",
            reference_model=model_dir / "phase21" / "reference-text.crfsuite",
            citation_model=model_dir / "phase22" / "citation-context.crfsuite",
        )
    except Exception:
        if mode == "crf":
            raise
        LOG.warning("%s CRF artifacts unavailable; using deterministic rules", mode, exc_info=True)
        return rules
    summary = crf.model_summary()
    LOG.info(
        "paper CRF artifacts loaded header=%s header_features=%s "
        "reference=%s reference_features=%s citation=%s citation_features=%s",
        summary["header_model"],
        summary["header_feature_version"],
        summary["reference_model"],
        summary["reference_feature_version"],
        summary["citation_model"],
        summary["citation_feature_version"],
    )
    if mode == "crf":
        return crf
    if mode == "shadow":
        return ShadowPaperExtractionEngine(rules, crf)
    return HybridPaperExtractionEngine(rules, crf)




if __name__ == "__main__":
    main()
