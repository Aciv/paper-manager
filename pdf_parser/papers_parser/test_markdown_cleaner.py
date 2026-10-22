"""Focused tests for safe parser Markdown cleanup and source-map remapping."""
from __future__ import annotations

import unittest

from papers_parser.markdown_cleaner import clean_markdown_page


class MarkdownCleanerTest(unittest.TestCase):
    def test_removes_known_noise_and_preserves_markdown_structure(self) -> None:
        raw = """1
# A


**==> picture [x] intentionally omitted <==**
<br>
```
x
```
|---|
- A
$$
\ufffdvalid\x00 text   
2
"""

        result = clean_markdown_page(raw)

        self.assertEqual(
            result.text,
            "# A\n\n```\nx\n```\n|---|\n- A\n$$\nvalid text\n",
        )
        self.assertGreaterEqual(result.removed_lines, 4)
        self.assertGreater(result.removed_characters, 0)

    def test_maps_ranges_after_a_removed_line(self) -> None:
        raw = "**==> picture [x] intentionally omitted <==**\n# **Title**\n"
        result = clean_markdown_page(raw)
        raw_start = raw.index("#")
        raw_end = len(raw) - 1

        self.assertEqual(result.text, "# **Title**\n")
        self.assertEqual(result.map_range(raw_start, raw_end), (0, len("# **Title**")))

    def test_keeps_a_short_heading_and_non_edge_numeric_content(self) -> None:
        raw = "# A\n\n2\n\nBody\n"

        result = clean_markdown_page(raw)

        self.assertIn("# A", result.text)
        self.assertIn("\n2\n", result.text)


if __name__ == "__main__":
    unittest.main()
