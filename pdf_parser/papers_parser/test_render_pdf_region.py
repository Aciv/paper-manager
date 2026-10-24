from __future__ import annotations

import io
import tempfile
import unittest
from pathlib import Path

import pymupdf

from papers_parser.render_pdf_region import (
    RegionRenderError,
    render_page_image,
    render_region,
    resolve_storage_source,
)


class RenderPDFRegionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.source = Path(self.tempdir.name) / "source.pdf"
        document = pymupdf.open()
        page = document.new_page(width=300, height=200)
        page.draw_rect((20, 30, 220, 130), color=(1, 0, 0), fill=(1, 1, 1))
        page.insert_text((30, 60), "bounded region")
        document.save(str(self.source))
        document.close()

    def test_renders_requested_bbox_as_png(self) -> None:
        output = render_region(self.source, 1, (20, 30, 220, 130), 144, 300, 1_000_000)
        pixmap = pymupdf.Pixmap(io.BytesIO(output))
        self.assertEqual((pixmap.width, pixmap.height), (400, 200))

    def test_downsamples_to_pixel_budget(self) -> None:
        output = render_region(self.source, 1, (0, 0, 300, 200), 300, 300, 20_000)
        pixmap = pymupdf.Pixmap(io.BytesIO(output))
        self.assertLessEqual(pixmap.width * pixmap.height, 20_000)

    def test_rejects_page_and_bbox_outside_document(self) -> None:
        with self.assertRaises(RegionRenderError):
            render_region(self.source, 2, (0, 0, 10, 10), 144, 300, 1_000_000)
        with self.assertRaises(RegionRenderError):
            render_region(self.source, 1, (0, 0, 301, 10), 144, 300, 1_000_000)

    def test_renders_full_page_and_normalized_region(self) -> None:
        full_png, width, height, page_width, page_height = render_page_image(
            self.source, 1, normalized_bbox=None, full_page=True,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(full_png.startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertEqual((width, height), (300, 200))
        self.assertEqual((page_width, page_height), (300, 200))
        region_png, region_width, region_height, _, _ = render_page_image(
            self.source, 1, normalized_bbox=(0.5, 0.5, 1.0, 1.0), full_page=False,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(region_png)
        self.assertEqual((region_width, region_height), (150, 100))

    def test_renders_rotated_page(self) -> None:
        document = pymupdf.open()
        page = document.new_page(width=300, height=200)
        page.insert_text((30, 60), "rotated")
        page.set_rotation(90)
        document.save(str(self.source))
        document.close()
        png, width, height, _, _ = render_page_image(
            self.source, 1, normalized_bbox=None, full_page=True,
            dpi=72, max_dpi=300, max_pixels=1_000_000,
        )
        self.assertTrue(png)
        self.assertGreater(width, 0)
        self.assertGreater(height, 0)

    def test_storage_key_cannot_escape_upload_root(self) -> None:
        root = Path(self.tempdir.name)
        self.assertEqual(resolve_storage_source(root, "source.pdf"), self.source.resolve())
        with self.assertRaises(RegionRenderError):
            resolve_storage_source(root, "../source.pdf")
        with self.assertRaises(RegionRenderError):
            resolve_storage_source(root, str(self.source))


if __name__ == "__main__":
    unittest.main()
