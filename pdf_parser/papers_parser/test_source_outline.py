from __future__ import annotations

import unittest
from types import SimpleNamespace

import document_parser_pb2
import pymupdf
from papers_parser.source_outline import select_source_outline


def block(ordinal: int, page: int, start: int, end: int, y: float):
    return document_parser_pb2.RecognitionBlock(
        ordinal=ordinal,
        type="text",
        text="block",
        page_number=page,
        page_width=600,
        page_height=800,
        bbox_x0=50,
        bbox_y0=y,
        bbox_x1=550,
        bbox_y1=y + 20,
        markdown_codepoint_start=start,
        markdown_codepoint_end=end,
    )


class FakePDF:
    def __init__(self, toc, pages: int = 2):
        self.toc = toc
        self.pages = [SimpleNamespace(rect=SimpleNamespace(width=600, height=800)) for _ in range(pages)]

    def get_toc(self, simple=False):
        return self.toc

    def __len__(self):
        return len(self.pages)

    def __getitem__(self, index):
        return self.pages[index]


class SourceOutlineTest(unittest.TestCase):
    def test_native_outline_wins_and_preserves_hierarchy(self):
        pdf = FakePDF([
            [1, "Introduction", 1, {"to": SimpleNamespace(x=50, y=90)}],
            [2, "Method", 1, {"to": SimpleNamespace(x=50, y=290)}],
            [1, "Results", 2, {"to": SimpleNamespace(x=50, y=100)}],
        ])
        markdown = "# ignored fallback\n" + "x" * 80
        result = select_source_outline(pdf, markdown, [
            block(0, 1, 0, 20, 80), block(1, 1, 21, 50, 280), block(2, 2, 51, 90, 90),
        ])
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual([item.parent_ordinal for item in result.sections], [-1, 0, -1])
        self.assertEqual(result.sections[1].start_block_ordinal, 1)

    def test_wholly_invalid_native_outline_falls_back_to_clean_markdown(self):
        pdf = FakePDF([[1, "Broken", 99, {}]])
        markdown = "# 介绍\n正文\n\n```md\n# not a heading\n```\n\nMethods\n-------\nbody"
        result = select_source_outline(pdf, markdown, [
            block(0, 1, 0, 10, 70), block(1, 2, 45, len(markdown), 70),
        ])
        self.assertEqual(result.source, "markdown_headings")
        self.assertEqual([item.title for item in result.sections], ["介绍", "Methods"])
        self.assertEqual(result.sections[0].markdown_codepoint_start, 0)
        self.assertIn("pdf_outline_wholly_unusable", result.diagnostics)

    def test_markdown_fallback_removes_emphasis_wrappers_from_titles(self):
        markdown = (
            "# **Introduction**\n"
            "## **1.1** **Method**\n"
            "## **C. Statistics** **Bench**\n"
        )
        result = select_source_outline(FakePDF([]), markdown, [])
        self.assertEqual(result.source, "markdown_headings")
        self.assertEqual(
            [item.title for item in result.sections],
            ["Introduction", "1.1 Method", "C. Statistics Bench"],
        )

    def test_missing_everything_returns_nonfatal_none(self):
        result = select_source_outline(FakePDF([]), "plain text", [])
        self.assertEqual(result.source, "none")
        self.assertEqual(list(result.sections), [])
        self.assertIn("outline_unavailable", result.diagnostics)

    def test_page_only_duplicate_titles_are_retained_and_item_count_is_bounded(self):
        toc = [[1, "Repeated", 1, {}] for _ in range(520)]
        result = select_source_outline(FakePDF(toc, pages=1), "body", [])
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual(len(result.sections), 512)
        self.assertTrue(all(section.title == "Repeated" for section in result.sections))
        self.assertTrue(all(section.start_page == 1 for section in result.sections))
        self.assertTrue(all(section.start_block_ordinal == -1 for section in result.sections))
        self.assertIn("pdf_outline_item_limit", result.diagnostics)

    def test_rotated_page_native_destination_remains_bounded(self):
        document = pymupdf.open()
        page = document.new_page(width=600, height=800)
        page.insert_text((72, 72), "Rotated")
        page.set_rotation(90)
        document.set_toc([[1, "Rotated", 1]])
        result = select_source_outline(document, "# Rotated\n", [block(0, 1, 0, 10, 60)])
        document.close()
        self.assertEqual(result.source, "pdf_outline")
        self.assertEqual(result.sections[0].start_page, 1)
        self.assertGreaterEqual(result.sections[0].start_block_ordinal, 0)


if __name__ == "__main__":
    unittest.main()
