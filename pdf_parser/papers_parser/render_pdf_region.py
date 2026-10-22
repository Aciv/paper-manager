"""PyMuPDF-only page/region renderer used by the render worker.

This module deliberately imports only PyMuPDF, so the render worker never
loads PyMuPDF4LLM, ONNX Runtime, or CRF. PNG bytes are written to stdout for the
standalone CLI; the gRPC worker calls the functions directly.
"""
from __future__ import annotations

import argparse
import math
import sys
from pathlib import Path

import pymupdf


class RegionRenderError(ValueError):
    """The requested PDF page region cannot be rendered safely."""


def render_region(
    source: Path,
    page_number: int,
    bbox: tuple[float, float, float, float],
    dpi: int,
    max_dpi: int,
    max_pixels: int,
) -> bytes:
    if page_number < 1:
        raise RegionRenderError("page number must be positive")
    if dpi <= 0 or max_dpi <= 0 or dpi > max_dpi:
        raise RegionRenderError("DPI is outside the configured range")
    if max_pixels <= 0:
        raise RegionRenderError("max pixels must be positive")
    if len(bbox) != 4 or not all(math.isfinite(value) for value in bbox):
        raise RegionRenderError("bbox coordinates must be finite")

    clip = pymupdf.Rect(bbox)
    if clip.x0 < 0 or clip.y0 < 0 or clip.is_empty or clip.is_infinite:
        raise RegionRenderError("bbox must be a positive rectangle")
    if not source.is_file():
        raise RegionRenderError("source PDF is unavailable")

    try:
        document = pymupdf.open(str(source))
    except Exception as exc:
        raise RegionRenderError("source is not a readable PDF") from exc
    try:
        if not document.is_pdf:
            raise RegionRenderError("source is not a PDF")
        if page_number > document.page_count:
            raise RegionRenderError("page number exceeds document page count")
        page = document[page_number - 1]
        page_rect = page.rect
        if (
            clip.x0 < page_rect.x0
            or clip.y0 < page_rect.y0
            or clip.x1 > page_rect.x1
            or clip.y1 > page_rect.y1
        ):
            raise RegionRenderError("bbox is outside the requested page")

        scale = dpi / 72.0
        requested_pixels = max(clip.width * scale, 1.0) * max(clip.height * scale, 1.0)
        if requested_pixels > max_pixels:
            # PyMuPDF rounds pixmap dimensions up, so retain a small margin.
            scale *= math.sqrt(max_pixels / requested_pixels) * 0.99
        pixmap = page.get_pixmap(
            matrix=pymupdf.Matrix(scale, scale),
            clip=clip,
            alpha=False,
        )
        if pixmap.width * pixmap.height > max_pixels:
            raise RegionRenderError("rendered region exceeds pixel limit")
        return pixmap.tobytes("png")
    finally:
        document.close()


def resolve_storage_source(upload_dir: Path, storage_key: str) -> Path:
    """Resolve a Go-owned storage key below the shared upload root."""
    key = Path(storage_key)
    root = upload_dir.resolve()
    if not storage_key or key.is_absolute():
        raise RegionRenderError("source storage key must be a relative path")
    source = (root / key).resolve()
    if not source.is_relative_to(root) or not source.is_file():
        raise RegionRenderError("source storage key is outside the parser upload directory")
    if source.suffix.lower() != ".pdf":
        raise RegionRenderError("source storage key does not identify a PDF")
    return source


def render_page_image(
    source: Path,
    page_number: int,
    *,
    normalized_bbox: tuple[float, float, float, float] | None,
    full_page: bool,
    dpi: int,
    max_dpi: int,
    max_pixels: int,
) -> tuple[bytes, int, int, float, float]:
    """Render a full page or a normalized rectangle and return PNG metadata.

    Normalized coordinates are relative to ``page.rect``. This keeps the
    public contract independent of page rotation while the renderer remains
    the owner of PDF coordinate conversion.
    """
    if page_number < 1:
        raise RegionRenderError("page number must be positive")
    if dpi <= 0 or max_dpi <= 0 or dpi > max_dpi:
        raise RegionRenderError("DPI is outside the configured range")
    if max_pixels <= 0:
        raise RegionRenderError("max pixels must be positive")
    if not source.is_file():
        raise RegionRenderError("source PDF is unavailable")

    try:
        document = pymupdf.open(str(source))
    except Exception as exc:
        raise RegionRenderError("source is not a readable PDF") from exc
    try:
        if not document.is_pdf:
            raise RegionRenderError("source is not a PDF")
        if page_number > document.page_count:
            raise RegionRenderError("page number exceeds document page count")
        page = document[page_number - 1]
        page_rect = page.rect
        if full_page:
            clip = pymupdf.Rect(page_rect)
        else:
            if normalized_bbox is None or len(normalized_bbox) != 4:
                raise RegionRenderError("bbox is required unless full_page is true")
            if not all(math.isfinite(value) for value in normalized_bbox):
                raise RegionRenderError("bbox coordinates must be finite")
            x0, y0, x1, y1 = normalized_bbox
            if not (0 <= x0 < x1 <= 1 and 0 <= y0 < y1 <= 1):
                raise RegionRenderError("bbox must be a positive normalized rectangle")
            clip = pymupdf.Rect(
                page_rect.x0 + x0 * page_rect.width,
                page_rect.y0 + y0 * page_rect.height,
                page_rect.x0 + x1 * page_rect.width,
                page_rect.y0 + y1 * page_rect.height,
            )
        scale = dpi / 72.0
        requested_pixels = max(clip.width * scale, 1.0) * max(clip.height * scale, 1.0)
        if requested_pixels > max_pixels:
            scale *= math.sqrt(max_pixels / requested_pixels) * 0.99
        pixmap = page.get_pixmap(
            matrix=pymupdf.Matrix(scale, scale),
            clip=clip,
            alpha=False,
        )
        if pixmap.width * pixmap.height > max_pixels:
            raise RegionRenderError("rendered page exceeds pixel limit")
        return (
            pixmap.tobytes("png"),
            pixmap.width,
            pixmap.height,
            page_rect.width,
            page_rect.height,
        )
    finally:
        document.close()


def _parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Render one bounded PDF page region as PNG")
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--page-number", type=int, required=True)
    parser.add_argument("--bbox-x0", type=float, required=True)
    parser.add_argument("--bbox-y0", type=float, required=True)
    parser.add_argument("--bbox-x1", type=float, required=True)
    parser.add_argument("--bbox-y1", type=float, required=True)
    parser.add_argument("--dpi", type=int, required=True)
    parser.add_argument("--max-dpi", type=int, required=True)
    parser.add_argument("--max-pixels", type=int, required=True)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)
    try:
        output = render_region(
            args.source,
            args.page_number,
            (args.bbox_x0, args.bbox_y0, args.bbox_x1, args.bbox_y1),
            args.dpi,
            args.max_dpi,
            args.max_pixels,
        )
    except RegionRenderError as exc:
        print(f"PDF region render failed: {exc}", file=sys.stderr)
        return 2
    except Exception as exc:  # keep the CLI boundary deterministic and concise
        print(f"PDF region render failed: {type(exc).__name__}", file=sys.stderr)
        return 1
    sys.stdout.buffer.write(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
