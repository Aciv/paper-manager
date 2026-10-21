"""Minimal one-shot PDF region renderer.

This module deliberately imports only PyMuPDF. It is a framework-level helper
for a future Go caller, not part of the gRPC parser server, HTTP API, frontend,
or Docker image yet. PNG bytes are written to stdout; diagnostics use stderr.
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
