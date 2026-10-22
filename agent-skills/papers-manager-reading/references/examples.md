# Examples

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
3. Use `papers_manager_reading_note_create` with `expected_revision=0` and a small analysis
   skeleton. Do not mirror every paper heading.
4. For the method section, add a section description such as
   `对应原文 §3，pp.4–6`.
5. Read the relevant source section and Blocks, then add claim-level evidence
   links.
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
{"description":"对应原文 §3，pp.4–6；说明模型结构和训练目标。"}
-->
```

This is metadata for the directory and Agent navigation. It is not a substitute
for claim-level evidence links.

## Base summary

1. `papers_manager_library_list_bases` and `papers_manager_library_get_base`; record member count.
2. Read metadata and note outlines/descriptions for a bounded member set.
3. Select candidates; read their notes before source sections.
4. Compare methods/results and verify key claims with page/Blocks.
5. Return a transient Markdown summary with missing-note and evidence-gap
   warnings. Do not persist it automatically.
