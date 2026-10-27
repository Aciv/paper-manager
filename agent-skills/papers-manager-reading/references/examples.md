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

## Batch note generation

Read [batch-notes.md](batch-notes.md) before starting. The short version:

1. Resolve the Base or document list, then batch metadata with
   `papers_manager_library_get_many` and readiness with
   `papers_manager_processing_status_many` (1–200 IDs per call).
2. Build a manifest with one action per paper: `create`, `update`, `skip`,
   `blocked`, or `exclude`. Do not silently overwrite an existing note.
3. Process at most three papers per wave. Assign one paper to one writer; if the
   host has subagents that can access the same MCP tools, use one subagent per
   paper, otherwise process sequentially.
4. Each worker reads its own note/source outline, then targeted sections; it
   creates or incrementally updates only that paper's note and returns status,
   revision, coverage, evidence gaps, and unread scope.
5. Aggregate per-paper results. Retry only the failed paper, at most once, and
   report existing notes, blocked papers, and failures separately from success.

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

## Base summary or comparison

1. `papers_manager_base_list` and `papers_manager_base_get`; record the Base
   UUID, name, and member count. Decide whether the request is a **comparison**
   (user gives a question/dimensions), a **summary/synthesis** (themes, method
   map, consensus, gaps), or a mixed request.
2. Batch metadata with `papers_manager_library_get_many`; read note outlines and
   descriptions for a bounded member set. Do not preload every note body.
3. For comparison, select candidates and read their notes before source sections.
   For summary, cover all members at metadata/outline level before deepening
   selected themes.
4. Compare or synthesize methods/results and verify material claims with
   page/Blocks. Keep the verification state, source hint, and missing-note
   status for the report.
5. Write a Markdown report directly to
   `reports/base-summary/<base_id>-<YYYYMMDD-HHMMSS>.md`. Organize it around the
   actual question, with concrete mechanisms, results, conditions, coverage and
   gaps. Do not ask for visual styles or read presentation templates. Do not
   overwrite an existing report or persist it as single-paper notes.
6. Call `papers_manager_application_info` once for `library_url`, then append
   `?doc=<document_id>` or `?base=<base_id>`. Preserve its scheme, host, port and
   deployment prefix. Do not derive the Web address from the MCP endpoint or
   ask the user for a port. If `web_available=false` (or an older server lacks
   this tool), retain UUIDs and source hints, mark local navigation unavailable,
   and continue the report. See [base-summary.md](base-summary.md).
