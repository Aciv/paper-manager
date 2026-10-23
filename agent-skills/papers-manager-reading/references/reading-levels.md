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
- Base comparison → sections aligned across candidate papers.

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

## Base summary routing

1. Read Base membership and metadata (L0) for all members.
2. Read note outlines/descriptions and source-outline labels (L1) for members
   that look relevant. Do not fetch every note body.
3. Read L2 notes/sections only for candidates selected for comparison.
4. Read L3 pages/Blocks for the comparison claims that need verification.
5. Report missing notes/outlines and skipped members explicitly.

If the Base is too large for a single context, process members in stages and
retain a compact comparison table outside the model context.
