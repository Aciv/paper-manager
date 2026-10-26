#!/usr/bin/env python3
"""Run parser recognition plus rules/CRF extraction for one local PDF.

This is a local diagnostic, not an HTTP upload: it does not contact PostgreSQL,
Redis, or catalog providers and never writes the PDF to project storage.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path
from uuid import uuid4

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "pdf_parser"), str(ROOT)]

import document_parser_pb2  # noqa: E402
from papers_parser.engine_factory import build_extraction_engine  # noqa: E402
from papers_parser.extractor_service import _decode_extraction_request  # noqa: E402
from papers_parser.layout_lines import extract_layout_lines_from_pdf  # noqa: E402
from papers_parser.pdf2md_service import DocumentParser  # noqa: E402


class _Context:
    """Minimal local stand-in for gRPC cancellation and validation calls."""

    def is_active(self) -> bool:
        return True

    def time_remaining(self) -> float:
        return 300.0

    def abort(self, _code, message: str):
        raise RuntimeError(message)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pdf", type=Path, help="local PDF to parse")
    parser.add_argument("--engine", choices=("rules", "crf", "hybrid", "shadow"), default="hybrid")
    parser.add_argument(
        "--reference-crf-enabled",
        action=argparse.BooleanOptionalAction,
        default=os.getenv("PAPER_REFERENCE_CRF_ENABLED", "false").strip().lower()
        in {"1", "true", "yes", "on"},
        help="evaluate the optional reference-region CRF; default keeps references rule-owned",
    )
    parser.add_argument("--model-dir", type=Path, default=ROOT / "pdf_parser" / "paper_crf")
    parser.add_argument("--image-dpi", type=int, default=72)
    # Production parser/extraction caps (see .env.example and server.py). The
    # previous diagnostic used len(source)+1 / 64 MiB / 100k, which bypassed
    # the size check and exercised a larger budget than deployments run with.
    parser.add_argument("--max-file-size", type=int, default=int(os.getenv("PARSER_MAX_FILE_SIZE", 100 << 20)))
    parser.add_argument("--extraction-markdown-bytes", type=int,
                        default=int(os.getenv("PARSER_MAX_EXTRACTION_MARKDOWN_BYTES", 32 << 20)))
    parser.add_argument("--extraction-blocks", type=int,
                        default=int(os.getenv("PARSER_MAX_EXTRACTION_BLOCKS", 50_000)))
    parser.add_argument("--layout-lines", type=int,
                        default=int(os.getenv("PARSER_MAX_EXTRACTION_LAYOUT_LINES", 100_000)))
    parser.add_argument("--output", type=Path, help="optional JSON result path (always records every raw reference and candidate)")
    parser.add_argument("--include-references", action="store_true", help="also print every raw reference/candidate in the console view")
    args = parser.parse_args()
    pdf = args.pdf.resolve()
    if not pdf.is_file() or pdf.suffix.lower() != ".pdf":
        parser.error("pdf must be an existing .pdf file")

    source = pdf.read_bytes()
    recognizer = DocumentParser(pdf.parent, args.max_file_size, args.image_dpi)
    started = time.perf_counter()
    recognition = recognizer.Recognize(document_parser_pb2.RecognitionRequest(
        document_id=str(uuid4()), filename=pdf.name, content_type="application/pdf", content=source,
    ), _Context())
    recognition_seconds = time.perf_counter() - started
    request = document_parser_pb2.PaperExtractionRequest(
        document_id=str(uuid4()), markdown=recognition.markdown,
        blocks=[document_parser_pb2.PaperSourceBlock(
            ordinal=block.ordinal, type=block.type, text=block.text,
            location=document_parser_pb2.PaperSourceLocation(
                page_number=block.page_number, page_width=block.page_width, page_height=block.page_height,
                bbox_x0=block.bbox_x0, bbox_y0=block.bbox_y0, bbox_x1=block.bbox_x1, bbox_y1=block.bbox_y1,
                markdown_codepoint_start=block.markdown_codepoint_start,
                markdown_codepoint_end=block.markdown_codepoint_end,
            ),
        ) for block in recognition.blocks],
    )
    markdown, blocks, _ = _decode_extraction_request(
        request, args.extraction_markdown_bytes, args.extraction_blocks,
    )
    engine = build_extraction_engine(
        args.engine,
        args.model_dir,
        reference_crf_enabled=args.reference_crf_enabled,
    )
    started = time.perf_counter()
    lines = extract_layout_lines_from_pdf(pdf, blocks, args.layout_lines) if getattr(engine, "requires_layout", False) else ()
    result = engine.extract(markdown, blocks, lines)
    extraction_seconds = time.perf_counter() - started
    references = list(result.references)
    candidates = [candidate.__dict__ for candidate in result.reference_candidates]

    def render(preview: bool) -> dict:
        # The console preview stays compact unless --include-references is
        # given; the --output file always records every reference/candidate.
        return {
            "pdf": str(pdf), "engine": args.engine, "pages": recognition.page_count,
            "markdown_bytes": len(recognition.markdown), "blocks": len(recognition.blocks), "layout_lines": len(lines),
            "timing_seconds": {"recognition": round(recognition_seconds, 4), "extraction": round(extraction_seconds, 4), "total": round(recognition_seconds + extraction_seconds, 4)},
            "result": {
                "extractor": result.extractor_name, "version": result.extractor_version,
                "title": None if result.title is None else result.title.value,
                "authors": [item.value for item in result.authors],
                "abstract_characters": 0 if result.abstract is None else len(result.abstract.value),
                "reference_count": len(references), "references": references[:5] if preview else references,
                "reference_candidate_count": len(candidates), "reference_candidates": candidates[:5] if preview else candidates,
            },
        }

    console_report = render(preview=not args.include_references)
    rendered = json.dumps(console_report, ensure_ascii=False, indent=2)
    print(rendered)
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        file_report = render(preview=False)
        args.output.write_text(json.dumps(file_report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
