# Reading levels

These are retrieval options, not a mandatory L0-to-L4 sequence. With a known
document and precise question, go directly to the needed source. For a complete
note, read every section needed to cover the argument, including appendices when
necessary; minimizing section count is not the objective.

## L0: metadata

Read only enough to identify and rank the paper:

- title, authors, venue, year, DOI, tags;
- abstract;
- document and Base membership.

Use this for relevance filtering and for deciding whether L1 is worth loading.
Do not infer the paper's method or results from the abstract alone.

## L1: structure and existing understanding

Read:

- the persisted source outline (`pdf_outline` or `markdown_headings`);
- the note manifest (section tree and revision);
- section descriptions parsed from `section-meta`.

The source outline describes the paper. The note directory describes existing
analysis. Keep them separate.

For a note directory with descriptions, prefer the derived
`papers_manager_reading_note_outline` result. If descriptions are unavailable, use titles and
then read only the sections that matter.
Check `descriptions_complete` and diagnostics: a missing description may reflect
the adapter's read budget, not missing content or lack of relevance.

## L2: selected sections

Read one of:

- a source section's Markdown (selected by outline range);
- a note section's Markdown.

Choose sections based on the user's task:

- method question → method/experimental setup sections;
- result verification → results, tables, ablations;
- novelty/limitations → introduction, related work, discussion, limitations;
- Base comparison → sections aligned across candidate papers;
- Base summary → sections that establish themes, method families, shared findings,
  disagreements and evidence maturity across members.

Do not load every section merely because it exists. Prefer the existing note
when it already covers the topic, then verify with the source.

## L3: exact evidence

Read page-scoped Blocks or a specific page/region when:

- a claim needs a verifiable source location;
- a table/figure/equation matters;
- the note has a block hint but the surrounding context is missing;
- different papers appear to contradict each other.

Use Block UUIDs, page numbers, and normalized rectangles from the current
recognition generation. Page/rectangle remains the fallback when a Block hint
is stale. Never invent a page or block.

## L4: whole document

Load the full Markdown, full note export, or PDF only when:

- the user explicitly asks for a full read;
- outline/section data is unavailable or corrupted;
- a bounded section read cannot answer the question after escalation.

Whole-document reads are the exception, not the default.

## Selection algorithm

1. Restate the user's objective in terms of paper sections or comparisons.
2. Read L0 for candidate papers.
3. Read L1 for the selected paper/Base; build a shortlist of sections.
4. Read L2 only for the shortlist.
5. For every important claim, decide whether it needs L3 verification.
6. Stop when the requested objective is covered. For full-note generation,
   verify coverage of problem, mechanism, supporting evidence and boundaries.

Practical budgets:

- prefer a small number of targeted sections over broad extraction;
- respect MCP response limits; if a section is too large, narrow the question
  or use page/Blocks;
- when a note already covers a section, do not re-read the full source unless
  verification is required;
- on budget exhaustion, report what was read and what remains unknown.

## Batch loading and delegation

When one task covers multiple papers, keep a compact manifest in the main
context and load papers in waves instead of preloading all bodies:

- batch metadata/status with `library_get_many` and
  `processing_status_many` (1–200 IDs per call);
- default to at most three papers per wave; when the task writes notes, allow
  only one writer per note;
- one paper per subagent when the host supports subagents and they can access
  the same MCP tools; otherwise process sequentially;
- a subagent must not read, write, or delete another paper's note, and must not
  spawn further subagents;
- aggregate status, revision, coverage, evidence gaps and unread scope from each
  worker; retry only the failed paper.

Full rules, the worker prompt template and a worked batch example are in
[批量阅读与笔记生成](batch-notes.md).

## Base summary and comparison routing

1. Decide whether the request is a comparison, a thematic summary/synthesis, or
   a mixed request; state the mode when it affects the report.
2. Read Base membership and metadata (L0) for all members.
3. Read note outlines/descriptions and source-outline labels (L1) for all
   members at minimum. For comparison, select candidates for deeper reading; for
   summary, select themes/method families and the papers that establish them.
   Do not fetch every note body.
4. Read L2 notes/sections only for the selected candidates/themes.
5. Read L3 pages/Blocks for the material claims that need verification.
6. Report missing notes/outlines and skipped members explicitly, then render the
   result as the self-contained HTML report described in
   [Base summary and comparison](base-summary.md).

If the Base is too large for a single context, process members in stages and
retain a compact comparison/synthesis table outside the model context. For
large Bases, batch loading and subagent rules follow
[batch-notes.md](batch-notes.md); only the final synthesis stays with the main
agent.
