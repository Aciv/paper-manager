"""Kind-specific parser worker process.

The lightweight server starts this module with one ``--kind``. Heavy imports
are intentionally selected here, after the server has already forked the
process, so a render/read worker never imports CRF/ONNX and an extraction
worker never imports the recognition stack.
"""
from __future__ import annotations

import argparse
import base64
import json
import os
from pathlib import Path

import document_parser_pb2
from logger import get_logger
from papers_parser.worker_protocol import METHOD_EXTRACT, METHOD_RECOGNIZE, METHOD_RENDER
from papers_parser.worker_runtime import WorkerAbort, serve_shared_worker, serve_worker
from papers_parser.worker_transport import await_parent

LOG = get_logger(__name__)


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="papersManager parser worker")
    parser.add_argument("--kind", choices=("recognize", "extract", "render"), required=True)
    parser.add_argument("--socket", type=Path, required=True)
    parser.add_argument("--config", required=True, help="base64-encoded JSON config")
    parser.add_argument("--idle-seconds", type=float, required=True)
    parser.add_argument("--max-jobs", type=int, required=True)
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument("--shared", action="store_true", help="one connection per request")
    return parser.parse_args()


def _config(args: argparse.Namespace) -> dict:
    try:
        raw = base64.b64decode(args.config.encode("ascii"), validate=True)
        decoded = json.loads(raw.decode("utf-8"))
    except (ValueError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise SystemExit(f"invalid worker config: {exc}") from exc
    if not isinstance(decoded, dict):
        raise SystemExit("worker config must be a JSON object")
    return decoded


def _build_recognize_handler(config: dict):
    from papers_parser.pdf2md_service import DocumentParser

    upload_dir = Path(config["upload_dir"])
    parser = DocumentParser(
        upload_dir=upload_dir,
        max_file_size=int(config["max_file_size"]),
        image_dpi=int(config["image_dpi"]),
        max_pages=int(config["max_pages"]),
        max_page_edge_points=float(config["max_page_edge_points"]),
        max_crop_pixels=int(config["max_crop_pixels"]),
        max_crops_per_document=int(config["max_crops_per_document"]),
        max_crop_bytes=int(config["max_crop_bytes"]),
        max_markdown_bytes=int(config["max_recognition_markdown_bytes"]),
        max_response_bytes=max(
            int(config["grpc_max_message_size"]) - int(config["grpc_message_overhead"]),
            1,
        ),
        rpc_max_seconds=float(config["rpc_max_seconds"]),
    )

    def handler(payload: bytes, context) -> bytes:
        request = document_parser_pb2.RecognitionRequest()
        try:
            request.ParseFromString(payload)
        except Exception as exc:
            raise WorkerAbort("INVALID_ARGUMENT", "invalid recognition request") from exc
        response = parser.Recognize(request, context)
        return response.SerializeToString()

    return METHOD_RECOGNIZE, handler


def _build_extract_handler(config: dict):
    from papers_parser.engine_factory import build_extraction_engine
    from papers_parser.extractor_service import PaperMetadataExtractionRPC

    engine = build_extraction_engine(
        str(config["paper_extraction_engine"]),
        Path(config["paper_crf_model_dir"]),
        reference_crf_enabled=bool(config.get("reference_crf_enabled", False)),
    )
    extractor = PaperMetadataExtractionRPC(
        engine,
        int(config["max_extraction_markdown_bytes"]),
        int(config["max_extraction_blocks"]),
        Path(config["upload_dir"]),
        int(config["max_extraction_layout_lines"]),
        rpc_max_seconds=float(config["rpc_max_seconds"]),
    )

    def handler(payload: bytes, context) -> bytes:
        request = document_parser_pb2.PaperExtractionRequest()
        try:
            request.ParseFromString(payload)
        except Exception as exc:
            raise WorkerAbort("INVALID_ARGUMENT", "invalid extraction request") from exc
        response = extractor.Extract(request, context)
        return response.SerializeToString()

    return METHOD_EXTRACT, handler


def _build_render_handler(config: dict):
    from papers_parser.render_pdf_region import (
        RegionRenderError,
        render_page_image,
        resolve_storage_source,
    )

    upload_dir = Path(config["upload_dir"])
    max_dpi = int(config["render_max_dpi"])
    max_pixels = int(config["render_max_pixels"])
    max_output_bytes = int(config["render_max_output_bytes"])

    def handler(payload: bytes, context) -> bytes:
        request = document_parser_pb2.PdfRenderRequest()
        try:
            request.ParseFromString(payload)
        except Exception as exc:
            raise WorkerAbort("INVALID_ARGUMENT", "invalid render request") from exc
        bbox = None
        if not request.full_page:
            if not request.HasField("bbox"):
                raise WorkerAbort("INVALID_ARGUMENT", "bbox is required unless full_page is true")
            bbox = (
                request.bbox.x0,
                request.bbox.y0,
                request.bbox.x1,
                request.bbox.y1,
            )
        try:
            source = resolve_storage_source(upload_dir, request.source_storage_key)
            png, width, height, page_width, page_height = render_page_image(
                source,
                request.page_number,
                normalized_bbox=bbox,
                full_page=request.full_page,
                dpi=request.dpi or 144,
                max_dpi=max_dpi,
                max_pixels=max_pixels,
            )
        except RegionRenderError as exc:
            raise WorkerAbort("INVALID_ARGUMENT", str(exc)) from exc
        if len(png) > max_output_bytes:
            raise WorkerAbort("RESOURCE_EXHAUSTED", "rendered page exceeds output limit")
        response = document_parser_pb2.PdfRenderResult(
            png=png,
            content_type="image/png",
            width=width,
            height=height,
            page_width=page_width,
            page_height=page_height,
        )
        return response.SerializeToString()

    return METHOD_RENDER, handler


def main() -> None:
    await_parent()
    trace_startup = os.getenv("PARSER_WORKER_STARTUP_TRACE") == "1"
    if trace_startup:
        import faulthandler
        faulthandler.dump_traceback_later(15, repeat=True)
    args = _parse_args()
    config = _config(args)
    if args.max_jobs <= 0:
        raise SystemExit("--max-jobs must be positive")
    if args.idle_seconds <= 0:
        raise SystemExit("--idle-seconds must be positive")
    if args.concurrency <= 0:
        raise SystemExit("--concurrency must be positive")
    if args.kind == "recognize":
        method, handler = _build_recognize_handler(config)
    elif args.kind == "extract":
        method, handler = _build_extract_handler(config)
    else:
        method, handler = _build_render_handler(config)
    if trace_startup:
        faulthandler.cancel_dump_traceback_later()
    LOG.info(
        "parser worker starting kind=%s pid=%d extraction_engine=%s concurrency=%d",
        args.kind,
        os.getpid(),
        config.get("paper_extraction_engine", ""),
        args.concurrency,
    )
    serve = serve_shared_worker if args.shared else serve_worker
    options = {"concurrency": args.concurrency} if args.shared else {}
    serve(
        socket_path=args.socket,
        expected_method=method,
        handler=handler,
        idle_seconds=args.idle_seconds,
        max_jobs=args.max_jobs,
        max_payload_bytes=int(config["grpc_max_message_size"]),
        **options,
    )


if __name__ == "__main__":
    main()
