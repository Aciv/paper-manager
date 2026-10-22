# Base summary workflow

Status: workflow design for Phase 3B. Until a stable `base_summary` MCP tool
exists, use the existing read tools and assemble the result in the host.

## Inputs

- Base ID and, if needed, a bounded member subset.
- The user's comparison question or output format.
- A budget for member count, section reads, and exact evidence checks.

## Retrieval ladder

1. **L0 — membership and metadata**
   - list Base members;
   - read title, authors, year, venue, DOI, tags, abstract;
   - record missing notes and missing source outlines.

2. **L1 — compact structure**
   - read note outline/description and source-outline label for each member;
   - select candidate papers and candidate sections;
   - do not read every note body.

3. **L2 — selected notes/sections**
   - read notes first; they already contain analysis and evidence;
   - read source sections only where the note is missing or insufficient;
   - compare methods, assumptions, datasets, results, and limitations.

4. **L3 — verification**
   - for each material comparison claim, read the exact page/Block;
   - attach a link-only evidence link or an explicit evidence gap;
   - do not use raw PDFs unless the structured path cannot answer.

## Output

Produce transient Markdown with:

- scope: Base ID, member count, selected candidates, skipped members;
- comparison findings;
- evidence links or gaps;
- warnings for missing notes/outlines, stale hints, or budget exhaustion.

Do not persist the summary automatically. If the user wants a durable artifact,
ask where it belongs; the reading-note schema remains per-paper unless a future
explicit Base-note design is approved.

## Budgets

- Select a bounded candidate set before reading bodies.
- Prefer notes to source sections and sections to full documents.
- Stop expanding when the user's question is answered.
- Report exactly what was not read and why.

## Future tool

A future `base_summary` tool may package this workflow, but its result must
still expose:

- per-member status and missing inputs;
- evidence links/gaps;
- total budgets and partial results;
- no hidden full-document reads.

Until then, do not claim that a `base_summary` tool exists.
