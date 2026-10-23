"""Lightweight parser gRPC front end.

The server process only owns transport, admission control, and worker
lifecycle. PyMuPDF/PyMuPDF4LLM/ONNX/CRF imports live in kind-specific worker
processes started on demand, so an idle parser keeps a small resident set.
"""
from __future__ import annotations

import argparse
import ctypes
import gc
import logging
import os
import signal
import threading
import time
from concurrent import futures
from pathlib import Path

import grpc

import document_parser_pb2
import document_parser_pb2_grpc
from logger import get_logger
from papers_parser.worker_pool import (
    SharedWorkerPool,
    WorkerPool,
    WorkerPoolConfig,
    WorkerQueueFull,
    WorkerRPCError,
    WorkerUnavailable,
)
from papers_parser.worker_protocol import METHOD_EXTRACT, METHOD_RECOGNIZE, METHOD_RENDER


def _log_level_from_env() -> int:
    """Return a safe standard-library logging level from ``LOG_LEVEL``."""
    configured = os.getenv("LOG_LEVEL", "INFO").strip().upper()
    level = getattr(logging, configured, None)
    return level if isinstance(level, int) else logging.INFO


LOG = get_logger(__name__, _log_level_from_env())

PARSER_ROOT = Path(__file__).resolve().parents[1]
PROJECT_ROOT = PARSER_ROOT.parent
DEFAULT_UPLOAD_DIR = PROJECT_ROOT / "data" / "uploads"


def _resolve_project_path(value: str | Path) -> Path:
    """Resolve relative parser configuration paths from the repository root."""
    path = Path(value).expanduser()
    if not path.is_absolute():
        path = PROJECT_ROOT / path
    return path.resolve()


def _env_flag(name: str, default: bool = False) -> bool:
    """Read a boolean environment flag using the project's usual spellings."""
    raw = os.getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


DEFAULT_MAX_FILE_SIZE = int(os.getenv("DEFAULT_MAX_FILE_SIZE", 100 << 20))
DEFAULT_GRPC_MESSAGE_OVERHEAD = int(os.getenv("DEFAULT_GRPC_MESSAGE_OVERHEAD", 1 << 20))
MAX_EXTRACTION_MARKDOWN_BYTES = int(os.getenv("MAX_EXTRACTION_MARKDOWN_BYTES", 32 << 20))
MAX_EXTRACTION_BLOCKS = int(os.getenv("MAX_EXTRACTION_BLOCKS", 50000))
MAX_EXTRACTION_LAYOUT_LINES = int(os.getenv("MAX_EXTRACTION_LAYOUT_LINES", 100000))
DEFAULT_EXTRACTION_ENGINE = os.getenv("PAPER_EXTRACTION_ENGINE", "rules").strip().lower()
# A CRF model directory is a paper_crf bundle root: models/ + weights/ + specs/.
# The packaged runtime bundle is the default; PAPER_CRF_MODEL_DIR overrides it
# only for evaluating another promoted bundle.
DEFAULT_CRF_MODEL_DIR = _resolve_project_path(
    os.getenv("PAPER_CRF_MODEL_DIR", str(PARSER_ROOT / "paper_crf")),
)
# The reference-region CRF is optional and off by default: reference entries
# stay rule-owned. Set true only to evaluate the learned reference region.
DEFAULT_REFERENCE_CRF_ENABLED = _env_flag("PAPER_REFERENCE_CRF_ENABLED", False)
DEFAULT_IMAGE_DPI = int(os.getenv("DEFAULT_IMAGE_DPI", 144))
DEFAULT_MAX_PAGES = int(os.getenv("PARSER_MAX_PAGES", 2000))
DEFAULT_MAX_PAGE_EDGE_POINTS = float(os.getenv("PARSER_MAX_PAGE_EDGE_POINTS", 5000.0))
DEFAULT_MAX_CROP_PIXELS = int(os.getenv("PARSER_MAX_CROP_PIXELS", 20_000_000))
DEFAULT_MAX_CROPS_PER_DOCUMENT = int(os.getenv("PARSER_MAX_CROPS_PER_DOCUMENT", 500))
DEFAULT_MAX_CROP_BYTES = int(os.getenv("PARSER_MAX_CROP_BYTES", 48 << 20))
DEFAULT_MAX_RECOGNITION_MARKDOWN_BYTES = int(
    os.getenv("PARSER_MAX_RECOGNITION_MARKDOWN_BYTES", 32 << 20)
)
DEFAULT_MAX_IMAGE_DPI = int(os.getenv("PARSER_MAX_IMAGE_DPI", 300))
DEFAULT_MAX_WORKERS = int(os.getenv("PARSER_MAX_WORKERS", 8))
DEFAULT_MAX_CONCURRENT_RPCS = int(os.getenv("PARSER_MAX_CONCURRENT_RPCS", 8))
DEFAULT_SHUTDOWN_GRACE_SECONDS = float(os.getenv("PARSER_SHUTDOWN_GRACE_SECONDS", 30))
DEFAULT_RPC_MAX_SECONDS = float(os.getenv("PARSER_RPC_MAX_SECONDS", 900))
DEFAULT_EXECUTION_MODE = os.getenv("PARSER_EXECUTION_MODE", "worker").strip().lower()
DEFAULT_MEMORY_TRIM_SECONDS = float(os.getenv("PARSER_MEMORY_TRIM_SECONDS", 60))

DEFAULT_RECOGNIZE_WORKER_MAX = int(
    os.getenv("PARSER_RECOGNIZE_WORKER_MAX", min(4, max(DEFAULT_MAX_CONCURRENT_RPCS, 1)))
)
DEFAULT_EXTRACT_WORKER_MAX = int(
    os.getenv("PARSER_EXTRACT_WORKER_MAX", min(2, max(DEFAULT_MAX_CONCURRENT_RPCS, 1)))
)
DEFAULT_WORKER_IDLE_SECONDS = float(os.getenv("PARSER_WORKER_IDLE_SECONDS", 300))
DEFAULT_WORKER_MAX_JOBS = int(os.getenv("PARSER_WORKER_MAX_JOBS", 50))
DEFAULT_WORKER_QUEUE_TIMEOUT = float(os.getenv("PARSER_WORKER_QUEUE_TIMEOUT", 30))
DEFAULT_WORKER_START_TIMEOUT = float(os.getenv("PARSER_WORKER_START_TIMEOUT", 90))
DEFAULT_RENDER_WORKER_MAX = int(os.getenv("PARSER_RENDER_WORKER_MAX", min(4, max(DEFAULT_MAX_CONCURRENT_RPCS, 1))))
DEFAULT_RENDER_WORKER_IDLE_SECONDS = float(os.getenv("PARSER_RENDER_WORKER_IDLE_SECONDS", 600))
DEFAULT_RECOGNIZE_CONCURRENCY = int(os.getenv("PARSER_RECOGNIZE_CONCURRENCY", "4"))
DEFAULT_EXTRACT_CONCURRENCY = int(os.getenv("PARSER_EXTRACT_CONCURRENCY", "4"))
DEFAULT_RENDER_CONCURRENCY = int(os.getenv("PARSER_RENDER_CONCURRENCY", "4"))
DEFAULT_RENDER_TIMEOUT = float(os.getenv("PARSER_RENDER_TIMEOUT", 15))
DEFAULT_RENDER_MAX_DPI = int(os.getenv("PARSER_RENDER_MAX_DPI", 300))
DEFAULT_RENDER_MAX_PIXELS = int(os.getenv("PARSER_RENDER_MAX_PIXELS", 20_000_000))
DEFAULT_RENDER_MAX_OUTPUT_BYTES = int(os.getenv("PARSER_RENDER_MAX_OUTPUT_BYTES", 12 << 20))


class _ParserProxy(document_parser_pb2_grpc.DocumentParserServicer):
    def __init__(self, pool: WorkerPool | SharedWorkerPool, call_timeout: float):
        self.pool = pool
        self.call_timeout = call_timeout

    def Recognize(self, request, context):  # noqa: N802 - gRPC generated name
        return self._call(context, request, document_parser_pb2.RecognitionResult())

    def _call(self, context, request, response):
        try:
            payload = self.pool.call(
                METHOD_RECOGNIZE,
                request.SerializeToString(),
                cancel_check=_cancel_check(context),
                timeout=self.call_timeout,
            )
        except WorkerQueueFull as exc:
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, str(exc))
        except WorkerUnavailable as exc:
            context.abort(grpc.StatusCode.UNAVAILABLE, str(exc))
        except WorkerRPCError as exc:
            _abort_worker_error(context, exc)
        except Exception:
            LOG.exception("parser proxy failed")
            context.abort(grpc.StatusCode.INTERNAL, "parser request failed")
        try:
            response.ParseFromString(payload)
        except Exception:
            LOG.exception("parser proxy returned an invalid response")
            context.abort(grpc.StatusCode.INTERNAL, "parser returned an invalid response")
        return response


class _ExtractorProxy(document_parser_pb2_grpc.PaperMetadataExtractorServicer):
    def __init__(self, pool: WorkerPool | SharedWorkerPool, call_timeout: float):
        self.pool = pool
        self.call_timeout = call_timeout

    def Extract(self, request, context):  # noqa: N802 - gRPC generated name
        try:
            payload = self.pool.call(
                METHOD_EXTRACT,
                request.SerializeToString(),
                cancel_check=_cancel_check(context),
                timeout=self.call_timeout,
            )
        except WorkerQueueFull as exc:
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, str(exc))
        except WorkerUnavailable as exc:
            context.abort(grpc.StatusCode.UNAVAILABLE, str(exc))
        except WorkerRPCError as exc:
            _abort_worker_error(context, exc)
        except Exception:
            LOG.exception("extractor proxy failed")
            context.abort(grpc.StatusCode.INTERNAL, "extraction request failed")
        response = document_parser_pb2.PaperExtractionResult()
        try:
            response.ParseFromString(payload)
        except Exception:
            LOG.exception("extractor proxy returned an invalid response")
            context.abort(grpc.StatusCode.INTERNAL, "extractor returned an invalid response")
        return response


class _RendererProxy(document_parser_pb2_grpc.PdfRendererServicer):
    def __init__(self, pool: WorkerPool | SharedWorkerPool, call_timeout: float):
        self.pool = pool
        self.call_timeout = call_timeout

    def RenderPage(self, request, context):  # noqa: N802 - gRPC generated name
        try:
            payload = self.pool.call(
                METHOD_RENDER,
                request.SerializeToString(),
                cancel_check=_cancel_check(context),
                timeout=self.call_timeout,
            )
        except WorkerQueueFull as exc:
            context.abort(grpc.StatusCode.RESOURCE_EXHAUSTED, str(exc))
        except WorkerUnavailable as exc:
            context.abort(grpc.StatusCode.UNAVAILABLE, str(exc))
        except WorkerRPCError as exc:
            _abort_worker_error(context, exc)
        except Exception:
            LOG.exception("render proxy failed")
            context.abort(grpc.StatusCode.INTERNAL, "render request failed")
        response = document_parser_pb2.PdfRenderResult()
        try:
            response.ParseFromString(payload)
        except Exception:
            LOG.exception("render proxy returned an invalid response")
            context.abort(grpc.StatusCode.INTERNAL, "renderer returned an invalid response")
        return response


def _cancel_check(context):
    def check() -> bool:
        try:
            if not context.is_active():
                return True
            remaining = context.time_remaining()
            return remaining is not None and remaining <= 0
        except Exception:
            return False

    return check


def _abort_worker_error(context, exc: WorkerRPCError) -> None:
    code = getattr(grpc.StatusCode, exc.code, grpc.StatusCode.INTERNAL)
    if not isinstance(code, grpc.StatusCode):
        code = grpc.StatusCode.INTERNAL
    context.abort(code, exc.message)


def _worker_config(args) -> dict:
    return {
        "upload_dir": str(args.upload_dir),
        "max_file_size": args.max_file_size,
        "image_dpi": args.image_dpi,
        "max_pages": args.max_pages,
        "max_page_edge_points": args.max_page_edge_points,
        "max_crop_pixels": args.max_crop_pixels,
        "max_crops_per_document": args.max_crops_per_document,
        "max_crop_bytes": args.max_crop_bytes,
        "max_recognition_markdown_bytes": args.max_recognition_markdown_bytes,
        "max_extraction_markdown_bytes": args.max_extraction_markdown_bytes,
        "paper_extraction_engine": args.paper_extraction_engine,
        "paper_crf_model_dir": str(args.paper_crf_model_dir),
        "reference_crf_enabled": args.reference_crf_enabled,
        "max_extraction_blocks": args.max_extraction_blocks,
        "max_extraction_layout_lines": args.max_extraction_layout_lines,
        "grpc_max_message_size": args.grpc_max_message_size,
        "grpc_message_overhead": DEFAULT_GRPC_MESSAGE_OVERHEAD,
        "rpc_max_seconds": args.rpc_max_seconds,
        "render_max_dpi": args.render_max_dpi,
        "render_max_pixels": args.render_max_pixels,
        "render_max_output_bytes": args.render_max_output_bytes,
    }


def _build_pools(args) -> tuple[WorkerPool, WorkerPool, WorkerPool]:
    common = _worker_config(args)
    recognize_pool = WorkerPool(
        WorkerPoolConfig(
            kind="recognize",
            max_workers=args.recognize_worker_max,
            idle_seconds=args.worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    extract_pool = WorkerPool(
        WorkerPoolConfig(
            kind="extract",
            max_workers=args.extract_worker_max,
            idle_seconds=args.worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    render_pool = WorkerPool(
        WorkerPoolConfig(
            kind="render",
            max_workers=args.render_worker_max,
            idle_seconds=args.render_worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    return recognize_pool, extract_pool, render_pool


def _build_shared_pools(args) -> tuple[SharedWorkerPool, SharedWorkerPool, SharedWorkerPool]:
    """One on-demand worker per kind; concurrency lives inside each worker."""
    common = _worker_config(args)
    recognize_pool = SharedWorkerPool(
        WorkerPoolConfig(
            kind="recognize",
            max_workers=1,
            concurrency=args.recognize_concurrency,
            idle_seconds=args.worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    extract_pool = SharedWorkerPool(
        WorkerPoolConfig(
            kind="extract",
            max_workers=1,
            concurrency=args.extract_concurrency,
            idle_seconds=args.worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    render_pool = SharedWorkerPool(
        WorkerPoolConfig(
            kind="render",
            max_workers=1,
            concurrency=args.render_concurrency,
            idle_seconds=args.render_worker_idle_seconds,
            max_jobs=args.worker_max_jobs,
            queue_timeout=args.worker_queue_timeout,
            start_timeout=args.worker_start_timeout,
            max_payload_bytes=args.grpc_max_message_size,
            worker_config=common,
        )
    )
    return recognize_pool, extract_pool, render_pool


class _InProcessRenderer(document_parser_pb2_grpc.PdfRendererServicer):
    """RenderPage servicer used by the shared-process execution mode."""

    def __init__(
        self,
        upload_dir: Path,
        max_dpi: int,
        max_pixels: int,
        max_output_bytes: int,
    ):
        self.upload_dir = upload_dir
        self.max_dpi = max_dpi
        self.max_pixels = max_pixels
        self.max_output_bytes = max_output_bytes

    def RenderPage(self, request, context):  # noqa: N802 - gRPC generated name
        from papers_parser.render_pdf_region import (
            RegionRenderError,
            render_page_image,
            resolve_storage_source,
        )

        bbox = None
        if not request.full_page:
            if not request.HasField("bbox"):
                context.abort(
                    grpc.StatusCode.INVALID_ARGUMENT,
                    "bbox is required unless full_page is true",
                )
            bbox = (
                request.bbox.x0,
                request.bbox.y0,
                request.bbox.x1,
                request.bbox.y1,
            )
        try:
            source = resolve_storage_source(self.upload_dir, request.source_storage_key)
            png, width, height, page_width, page_height = render_page_image(
                source,
                request.page_number,
                normalized_bbox=bbox,
                full_page=request.full_page,
                dpi=request.dpi or 144,
                max_dpi=self.max_dpi,
                max_pixels=self.max_pixels,
            )
        except RegionRenderError as exc:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, str(exc))
        if len(png) > self.max_output_bytes:
            context.abort(
                grpc.StatusCode.RESOURCE_EXHAUSTED,
                "rendered page exceeds output limit",
            )
        return document_parser_pb2.PdfRenderResult(
            png=png,
            content_type="image/png",
            width=width,
            height=height,
            page_width=page_width,
            page_height=page_height,
        )


def _add_inprocess_services(server, args) -> None:
    """Wire services into the gRPC process (the pre-worker execution model).

    One recognize service (and therefore one ONNX layout model) is shared by
    all gRPC threads. This restores the old memory profile; worker mode remains
    available via PARSER_EXECUTION_MODE=worker for isolation/cancellation.
    """
    # Worker mode caps each process; in-process mode has one shared pool.
    os.environ.setdefault("PARSER_LAYOUT_THREADS", str(max(1, os.cpu_count() or 4)))
    from papers_parser.engine_factory import build_extraction_engine
    from papers_parser.extractor_service import PaperMetadataExtractionRPC
    from papers_parser.pdf2md_service import DocumentParser

    recognize = DocumentParser(
        upload_dir=args.upload_dir,
        max_file_size=args.max_file_size,
        image_dpi=args.image_dpi,
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
    )
    document_parser_pb2_grpc.add_DocumentParserServicer_to_server(recognize, server)

    engine = build_extraction_engine(
        args.paper_extraction_engine,
        args.paper_crf_model_dir,
        reference_crf_enabled=args.reference_crf_enabled,
    )
    extractor = PaperMetadataExtractionRPC(
        engine,
        args.max_extraction_markdown_bytes,
        args.max_extraction_blocks,
        args.upload_dir,
        args.max_extraction_layout_lines,
        rpc_max_seconds=args.rpc_max_seconds,
    )
    document_parser_pb2_grpc.add_PaperMetadataExtractorServicer_to_server(
        extractor, server
    )
    document_parser_pb2_grpc.add_PdfRendererServicer_to_server(
        _InProcessRenderer(
            args.upload_dir,
            args.render_max_dpi,
            args.render_max_pixels,
            args.render_max_output_bytes,
        ),
        server,
    )


def _start_memory_trim(interval_seconds: float) -> None:
    """Return freed allocator arenas to the OS after large responses.

    The front end only forwards encoded protobuf payloads, but multi-megabyte
    recognition responses fragment per-thread glibc arenas. Python frees the
    objects, yet the arenas stay resident. A periodic GC plus malloc_trim keeps
    the idle resident set close to the worker-exit baseline instead of the
    batch high-water mark. No-op when glibc malloc_trim is unavailable.
    """
    if interval_seconds <= 0:
        LOG.info("parser memory trim disabled")
        return
    try:
        malloc_trim = ctypes.CDLL(None).malloc_trim
    except (AttributeError, OSError):
        LOG.info("parser memory trim unavailable (glibc malloc_trim not found)")
        return

    def trim_loop() -> None:
        while True:
            time.sleep(interval_seconds)
            try:
                gc.collect()
                malloc_trim(0)
            except Exception:
                LOG.debug("parser memory trim failed", exc_info=True)

    threading.Thread(
        target=trim_loop, name="parser-memory-trim", daemon=True
    ).start()
    LOG.info("parser memory trim enabled interval_seconds=%s", interval_seconds)


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
    parser.add_argument(
        "--paper-crf-model-dir",
        type=Path,
        default=DEFAULT_CRF_MODEL_DIR,
        help="paper_crf bundle root (models/weights/specs); defaults to pdf_parser/paper_crf",
    )
    parser.add_argument(
        "--reference-crf-enabled",
        action=argparse.BooleanOptionalAction,
        default=DEFAULT_REFERENCE_CRF_ENABLED,
        help="use the reference-region CRF; default false leaves references rule-owned",
    )
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
    parser.add_argument(
        "--execution-mode",
        choices=("worker", "shared", "inprocess"),
        default=DEFAULT_EXECUTION_MODE,
        help="worker: per-task subprocess pools; shared: one concurrent worker per kind; inprocess: old single-process thread pool",
    )
    parser.add_argument("--max-concurrent-rpcs", type=int, default=DEFAULT_MAX_CONCURRENT_RPCS)
    parser.add_argument("--recognize-concurrency", type=int, default=DEFAULT_RECOGNIZE_CONCURRENCY)
    parser.add_argument("--extract-concurrency", type=int, default=DEFAULT_EXTRACT_CONCURRENCY)
    parser.add_argument("--render-concurrency", type=int, default=DEFAULT_RENDER_CONCURRENCY)
    parser.add_argument("--recognize-worker-max", type=int, default=DEFAULT_RECOGNIZE_WORKER_MAX)
    parser.add_argument("--extract-worker-max", type=int, default=DEFAULT_EXTRACT_WORKER_MAX)
    parser.add_argument("--worker-idle-seconds", type=float, default=DEFAULT_WORKER_IDLE_SECONDS)
    parser.add_argument("--worker-max-jobs", type=int, default=DEFAULT_WORKER_MAX_JOBS)
    parser.add_argument("--worker-queue-timeout", type=float, default=DEFAULT_WORKER_QUEUE_TIMEOUT)
    parser.add_argument("--worker-start-timeout", type=float, default=DEFAULT_WORKER_START_TIMEOUT)
    parser.add_argument("--render-worker-max", type=int, default=DEFAULT_RENDER_WORKER_MAX)
    parser.add_argument(
        "--render-worker-idle-seconds",
        type=float,
        default=DEFAULT_RENDER_WORKER_IDLE_SECONDS,
    )
    parser.add_argument("--render-timeout", type=float, default=DEFAULT_RENDER_TIMEOUT)
    parser.add_argument("--render-max-dpi", type=int, default=DEFAULT_RENDER_MAX_DPI)
    parser.add_argument("--render-max-pixels", type=int, default=DEFAULT_RENDER_MAX_PIXELS)
    parser.add_argument(
        "--render-max-output-bytes",
        type=int,
        default=DEFAULT_RENDER_MAX_OUTPUT_BYTES,
    )
    parser.add_argument(
        "--shutdown-grace-seconds",
        type=float,
        default=DEFAULT_SHUTDOWN_GRACE_SECONDS,
    )
    parser.add_argument("--rpc-max-seconds", type=float, default=DEFAULT_RPC_MAX_SECONDS)
    parser.add_argument(
        "--memory-trim-seconds",
        type=float,
        default=DEFAULT_MEMORY_TRIM_SECONDS,
        help="0 disables the periodic gc/malloc_trim idle-memory sweep",
    )
    args = parser.parse_args()
    args.upload_dir = _resolve_project_path(args.upload_dir)
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
    if args.recognize_worker_max <= 0 or args.extract_worker_max <= 0:
        raise ValueError("parser worker maxima must be positive")
    if args.worker_idle_seconds <= 0 or args.worker_max_jobs <= 0:
        raise ValueError("PARSER_WORKER_IDLE_SECONDS and PARSER_WORKER_MAX_JOBS must be positive")
    if args.worker_queue_timeout < 0 or args.worker_start_timeout <= 0:
        raise ValueError("parser worker queue/start timeouts are invalid")
    if args.render_worker_max <= 0 or args.render_worker_max > 16:
        raise ValueError("PARSER_RENDER_WORKER_MAX must be between 1 and 16")
    if args.render_worker_idle_seconds <= 0:
        raise ValueError("PARSER_RENDER_WORKER_IDLE_SECONDS must be positive")
    if (
        args.recognize_concurrency <= 0
        or args.extract_concurrency <= 0
        or args.render_concurrency <= 0
    ):
        raise ValueError("parser shared-worker concurrency values must be positive")
    if args.render_timeout <= 0:
        raise ValueError("PARSER_RENDER_TIMEOUT must be positive")
    if args.render_max_dpi <= 0 or args.render_max_dpi > 300:
        raise ValueError("PARSER_RENDER_MAX_DPI must be between 1 and 300")
    if args.render_max_pixels <= 0 or args.render_max_pixels > 50_000_000:
        raise ValueError("PARSER_RENDER_MAX_PIXELS must be between 1 and 50000000")
    if args.render_max_output_bytes <= 0 or args.render_max_output_bytes > 32 << 20:
        raise ValueError("PARSER_RENDER_MAX_OUTPUT_BYTES must be between 1 and 33554432")
    if args.shutdown_grace_seconds < 0 or args.rpc_max_seconds <= 0:
        raise ValueError("PARSER_SHUTDOWN_GRACE_SECONDS must be non-negative and PARSER_RPC_MAX_SECONDS positive")
    if args.memory_trim_seconds < 0:
        raise ValueError("PARSER_MEMORY_TRIM_SECONDS must be non-negative")

    _start_memory_trim(args.memory_trim_seconds)
    server = _create_server(args)
    if args.execution_mode == "inprocess":
        _add_inprocess_services(server, args)
        server.add_insecure_port(args.address)
        stop_requested = _install_shutdown_handlers()
        server.start()
        LOG.info(
            "parser server listening on %s execution_mode=inprocess extraction_engine=%s "
            "max_workers=%d max_concurrent_rpcs=%d rpc_max_seconds=%s "
            "layout_threads=%s shutdown_grace_seconds=%s",
            args.address,
            args.paper_extraction_engine,
            args.max_workers,
            args.max_concurrent_rpcs,
            args.rpc_max_seconds,
            os.getenv("PARSER_LAYOUT_THREADS", "default"),
            args.shutdown_grace_seconds,
        )
        stop_requested.wait()
        LOG.info("stopping parser server (grace=%ss)", args.shutdown_grace_seconds)
        stopped = server.stop(grace=args.shutdown_grace_seconds)
        if not stopped.wait(timeout=args.shutdown_grace_seconds + 5):
            LOG.warning("parser server did not stop within the grace period")
        LOG.info("parser server stopped")
        return

    if args.execution_mode == "shared":
        shared_pools: list[SharedWorkerPool] = []
        try:
            recognize_pool, extract_pool, render_pool = _build_shared_pools(args)
            shared_pools = [recognize_pool, extract_pool, render_pool]
            call_timeout = max(args.rpc_max_seconds + 30, 30)
            document_parser_pb2_grpc.add_DocumentParserServicer_to_server(
                _ParserProxy(recognize_pool, call_timeout),
                server,
            )
            document_parser_pb2_grpc.add_PaperMetadataExtractorServicer_to_server(
                _ExtractorProxy(extract_pool, call_timeout),
                server,
            )
            document_parser_pb2_grpc.add_PdfRendererServicer_to_server(
                _RendererProxy(render_pool, args.render_timeout),
                server,
            )
            server.add_insecure_port(args.address)
            stop_requested = _install_shutdown_handlers()
            server.start()
            LOG.info(
                "parser server listening on %s execution_mode=shared extraction_engine=%s "
                "recognize_concurrency=%d extract_concurrency=%d render_concurrency=%d "
                "idle_seconds=%s render_idle_seconds=%s max_workers=%d "
                "max_concurrent_rpcs=%d rpc_max_seconds=%s shutdown_grace_seconds=%s",
                args.address,
                args.paper_extraction_engine,
                args.recognize_concurrency,
                args.extract_concurrency,
                args.render_concurrency,
                args.worker_idle_seconds,
                args.render_worker_idle_seconds,
                args.max_workers,
                args.max_concurrent_rpcs,
                args.rpc_max_seconds,
                args.shutdown_grace_seconds,
            )
            stop_requested.wait()
            LOG.info("stopping parser server (grace=%ss)", args.shutdown_grace_seconds)
            stopped = server.stop(grace=args.shutdown_grace_seconds)
            if not stopped.wait(timeout=args.shutdown_grace_seconds + 5):
                LOG.warning("parser server did not stop within the grace period")
        finally:
            for pool in shared_pools:
                pool.close()
            LOG.info("parser server stopped")
        return

    pools: list[WorkerPool] = []
    try:
        recognize_pool, extract_pool, render_pool = _build_pools(args)
        pools = [recognize_pool, extract_pool, render_pool]
        call_timeout = max(args.rpc_max_seconds + 30, 30)
        document_parser_pb2_grpc.add_DocumentParserServicer_to_server(
            _ParserProxy(recognize_pool, call_timeout),
            server,
        )
        document_parser_pb2_grpc.add_PaperMetadataExtractorServicer_to_server(
            _ExtractorProxy(extract_pool, call_timeout),
            server,
        )
        document_parser_pb2_grpc.add_PdfRendererServicer_to_server(
            _RendererProxy(render_pool, args.render_timeout),
            server,
        )
        server.add_insecure_port(args.address)
        stop_requested = _install_shutdown_handlers()
        server.start()
        LOG.info(
            "parser server listening on %s execution_mode=worker extraction_engine=%s recognize_worker_max=%d "
            "extract_worker_max=%d render_worker_max=%d worker_idle_seconds=%s "
            "render_worker_idle_seconds=%s worker_max_jobs=%d max_workers=%d "
            "max_concurrent_rpcs=%d rpc_max_seconds=%s render_timeout=%s shutdown_grace_seconds=%s",
            args.address,
            args.paper_extraction_engine,
            args.recognize_worker_max,
            args.extract_worker_max,
            args.render_worker_max,
            args.worker_idle_seconds,
            args.render_worker_idle_seconds,
            args.worker_max_jobs,
            args.max_workers,
            args.max_concurrent_rpcs,
            args.rpc_max_seconds,
            args.render_timeout,
            args.shutdown_grace_seconds,
        )
        stop_requested.wait()
        LOG.info("stopping parser server (grace=%ss)", args.shutdown_grace_seconds)
        stopped = server.stop(grace=args.shutdown_grace_seconds)
        if not stopped.wait(timeout=args.shutdown_grace_seconds + 5):
            LOG.warning("parser server did not stop within the grace period")
    finally:
        for pool in pools:
            pool.close()
        LOG.info("parser server stopped")


def _create_server(args) -> grpc.Server:
    """Build the gRPC server with bounded external concurrency."""
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


def _build_extraction_engine(
    mode: str,
    model_dir: Path,
    reference_crf_enabled: bool = False,
):
    """Lazy compatibility wrapper used by operational tests."""
    from papers_parser.engine_factory import build_extraction_engine

    return build_extraction_engine(
        mode, model_dir, reference_crf_enabled=reference_crf_enabled
    )


if __name__ == "__main__":
    main()
