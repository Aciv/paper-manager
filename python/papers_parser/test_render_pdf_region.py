from __future__ import annotations

import io
import tempfile
import unittest
from pathlib import Path

import pymupdf

from papers_parser.render_pdf_region import RegionRenderError, render_region


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


if __name__ == "__main__":
    unittest.main()
