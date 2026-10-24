# Examples

For a fully written method/results example, read
[Attention Is All You Need](worked-example.md). The examples below describe
tool workflows; the worked example demonstrates content depth and indexing.

## Quick understanding of one paper

1. `papers_manager_library_search` or `papers_manager_library_get` to resolve the document.
2. `papers_manager_document_structure` to read the source outline.
3. `papers_manager_reading_note_outline` to see existing note coverage and descriptions.
4. Read only the sections needed for the user's question with
   `papers_manager_reading_note_read_section` or `papers_manager_document_read_section`.
5. If the answer depends on a specific result, use `papers_manager_document_read_blocks` and
   `papers_manager_evidence_validate`.
6. Return a bounded answer with links or explicit evidence gaps.

Do not start by requesting the full Markdown export.

## Create a first reading note

User request: “给这篇论文建一份分析方法部分的笔记，并带原文证据。”

1. Resolve the document and read the source outline.
2. Confirm that no note exists; ask before creating if the user did not
   explicitly request it.
3. Read the relevant source sections and evidence, then draft useful content.
4. Summarize the actual mechanism and coverage in the section description;
   optionally include source section/page mapping.
5. Validate evidence targets, then create the readable note with
   `papers_manager_reading_note_create` and `expected_revision=0`.
   Do not publish an empty skeleton before reading.
6. Verify the returned revision and report any evidence gaps.

## Update one existing section

1. Read `papers_manager_reading_note_outline`; locate the target section ID.
2. Read the section and capture the current `expected_revision`.
3. Use `papers_manager_reading_note_apply_edits` with body-only edits or a complete
   `replaceSection`.
4. If the tool returns `REVISION_CONFLICT`, call `papers_manager_reading_note_outline` again,
   reload the section, and re-plan. Never retry the stale payload.
5. After success, report the new revision and changed section.

## Section-level mapping

A note section can carry:

```markdown
# 方法

<!-- papers-manager:section-meta v=1
{"description":"解释编码器与解码器如何通过重构目标学习表示，并分析该目标的适用条件；对应原文 §3。"}
-->
```

This is metadata for the directory and Agent navigation. It is not a substitute
for claim-level evidence links.

## Base summary

1. `papers_manager_base_list` and `papers_manager_base_get`; record the Base
   UUID, name, and member count.
2. Read metadata and note outlines/descriptions for a bounded member set.
3. Select candidates; read their notes before source sections.
4. Compare methods/results and verify key claims with page/Blocks. Keep the
   verification state and a page/section hint for the report.
5. Before writing, ask which style to use unless the user already specified one:
   `neutral` (default), `editorial`, `swiss`, `brutalist`, `cyberpunk`, or a
   custom style in one sentence. If the user has no preference, use `neutral`.
   For custom, build directly from the sentence; no template or placeholder
   contract is needed. Fill one style from
   [html_template/](html_template/README.md) and write one
   self-contained HTML report to
   `reports/base-summary/<base_id>-<YYYYMMDD-HHMMSS>.html`. The template's
   section headings are a display example, not a required structure; adapt,
   merge, or omit them. Pick exactly one style. Do not overwrite an existing
   report. Report the path, missing-note and evidence-gap warnings, and skipped
   members.
6. Use document-level links only:
   `http://localhost:5173/#/library?doc=<document_id>`. Put the referenced note
   section in the link text, for example `《论文短标题》· 方法`; do not use
   `papers-manager://` links in the standalone HTML and do not claim the link
   opens a note section. Do not persist the report as a reading note or Base
   record.
