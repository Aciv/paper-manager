"""Focused tests for deterministic Markdown paper-section extraction."""
from __future__ import annotations

import unittest

from papers_parser.markdown_metadata import extract_paper_sections


class MarkdownPaperSectionsTest(unittest.TestCase):
    def assert_reference_texts(self, sections, expected: list[str]) -> None:
        self.assertEqual(sections.references, expected)

    def test_extracts_bold_title_authors_abstract_and_references(self) -> None:
        markdown = """# **A Paper About Parsing**

### **Alice Zhang, Bob Li**

## **Abstract**

This paper describes a deterministic parser.

## **Introduction**

Body text.

## **References**

[1] Example Paper. https://doi.org/10.1000/example

## **Appendix**

Ignored.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.title, "A Paper About Parsing")
        self.assertEqual(sections.authors, ("Alice Zhang", "Bob Li"))
        self.assertEqual(sections.abstract, "This paper describes a deterministic parser.")
        self.assert_reference_texts(sections, ["[1] Example Paper. https://doi.org/10.1000/example"])

    def test_extracts_inline_bold_authors_and_ignores_superscripts(self) -> None:
        markdown = """# **Sample Paper**

**Xinhao Huang**<sup>1</sup> **, Jinke Yu**<sup>2</sup> **, Zulong Chen**<sup>2*</sup>
1 HKUST 2 Alibaba Group

## **摘要**

这是摘要内容。

## **参考文献**

[1] 一篇论文。
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.authors, ("Xinhao Huang", "Jinke Yu", "Zulong Chen"))
        self.assertEqual(sections.abstract, "这是摘要内容。")
        self.assert_reference_texts(sections, ["[1] 一篇论文。"])

    def test_uses_conservative_fallback_abstract(self) -> None:
        markdown = """# **Fallback Abstract**

### **Alice Zhang**

This paragraph is the abstract without an explicit heading.

## **1 Introduction**

Body text.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.abstract, "This paragraph is the abstract without an explicit heading.")

    def test_does_not_treat_affiliation_as_fallback_abstract(self) -> None:
        markdown = """# **No Abstract**

### **Alice Zhang**

1 Example University, Example City\nalice@example.edu

## **Introduction**

Body text.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.abstract, "")

    def test_ignores_heading_depth_and_uses_the_first_heading_as_title(self) -> None:
        markdown = """####### **Depth Does Not Matter**

### **Alice Zhang**

#### **Abstract**

The heading depth is parser layout noise.

###### **References**

[1] Example.

### **Appendix**

Ignored.
"""

        sections = extract_paper_sections(markdown)

        self.assertEqual(sections.title, "Depth Does Not Matter")
        self.assertEqual(sections.authors, ("Alice Zhang",))
        self.assertEqual(sections.abstract, "The heading depth is parser layout noise.")
        self.assert_reference_texts(sections, ["[1] Example."])

    def test_joins_wrapped_numbered_reference_lines(self) -> None:
        markdown = """# **Reference Items**

## References

[1] First Paper.
    Continued citation details.
[2] Second Paper.
"""

        sections = extract_paper_sections(markdown)

        self.assert_reference_texts(
            sections,
            ["[1] First Paper. Continued citation details.", "[2] Second Paper."],
        )


if __name__ == "__main__":
    unittest.main()
