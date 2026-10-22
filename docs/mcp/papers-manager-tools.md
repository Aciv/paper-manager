# papersManager MCP tool contract

Status: specification implemented by `internal/mcp` and the in-process MCP
listener. The manifest contains 43 tools across four groups: `library`,
`reading`, `base`, and `citation`. The manifest remains the source of truth for
tool names and schemas. Per-host connection and skill installation are covered
by [agent-installation.md](agent-installation.md).

## Purpose

Expose papersManager's existing application services to Agent hosts through a
stable, bounded MCP tool surface. The MCP server is an adapter:

- it does not introduce a second storage model;
- it does not create Agent identities or preference tables;
- it calls the same application services as HTTP (or the same HTTP endpoints
  when implemented as a client adapter);
- it returns section IDs and content, never private storage keys or paths.

Humans and Agents use the same resources and revision rules.

## Tool naming

Every tool name starts with the `papers_manager_` prefix (for example,
`papers_manager_reading_note_outline`). This is the reserved capability
namespace for this server. A host may additionally namespace tools by server
name; skills should match the prefix or the `papers-manager` server identity,
not assume one particular outer wrapper. The manifest exposes this as
`server.toolNamePrefix`.

The manifest's `group` field is descriptive only. Base operations (including
Base bibliography generation) use the independent `papers_manager_base_*`
collection, and citation operations use `papers_manager_citation_*`; they are
not folded into `papers_manager_library_*`.

Skills treat an absent prefix/server as “MCP unavailable” and fail closed.

## Transport and access

- Transport: MCP Streamable HTTP only, served at `/mcp` by the in-process
  adapter. stdio is not implemented by the server.
- The default listener is loopback (`127.0.0.1:8090`). Non-loopback HTTP is not
  allowed until bearer-token authentication is configured and verified.
- Hosts that only support stdio can use an external stdio-to-HTTP bridge such
  as `mcp-remote`; the bridge is a client-side adapter, not part of this
  server's contract.
- There is no per-user ownership model yet. Do not claim per-Agent isolation.
- Secrets and API tokens are server configuration, never tool inputs or tool
  output.
- `papers_manager_library_import_url` accepts HTTP(S) URLs only. Local-file
  import is a separate, explicitly enabled tool
  (`papers_manager_library_import_file`) and is confined to
  `MCP_LOCAL_IMPORT_ROOT`, including symlink resolution. When that setting is
  empty the tool fails closed with `FORBIDDEN`.
- Block images are returned as MCP image content and bounded by
  `MCP_MAX_IMAGE_BYTES` (default 12 MiB). Private storage keys never cross the
  MCP boundary.

## Common response envelope

Tools return structured JSON (as MCP structured content or serialized text):

```json
{
  "ok": true,
  "data": {}
}
```

```json
{
  "ok": false,
  "error": {
    "code": "REVISION_CONFLICT",
    "message": "reading note revision changed",
    "retryable": false,
    "details": {}
  }
}
```

On failure, set the MCP `isError` flag as well. Do not return raw database
errors, storage paths, credentials, parser output, or unbounded provider
responses.

Error codes:

| Code | Retryable | Meaning |
| --- | --- | --- |
| `INVALID_ARGUMENT` | no | Missing or malformed input. |
| `FORBIDDEN` | no | Access boundary rejected the caller. |
| `CANCELLED` | no | The request context was cancelled. |
| `NOT_FOUND` | no | Document, note, section, or Base not found. |
| `NOTE_NOT_FOUND` | no | The document exists but has no reading note. |
| `REVISION_CONFLICT` | no | `expected_revision` is stale. Re-read before retrying. |
| `UNAVAILABLE` | yes | Temporary service/queue/publication unavailability. |
| `PAYLOAD_TOO_LARGE` | no | Response/input exceeds the MCP or backend bound. |
| `INVALID_EVIDENCE` | no | Evidence link is malformed or targets the wrong range/Block. |
| `CONFLICT` | no | A non-revision lifecycle conflict (processing/replacement). |
| `INTERNAL` | maybe | Unexpected server error; do not expose internals. |

## Tool annotations

- `readOnlyHint: true` for search/read/metadata reads, note export, and
  evidence validation.
- `destructiveHint: true` for document/note/Base/reference deletion, whole-note
  or whole-section replacement, citation replacement, and revision-fenced edits
  that can overwrite content.
- `idempotentHint` only where repeated identical calls are safe; note edits are
  not treated as idempotent across revisions.
- `openWorldHint: false` except for URL import, reference-PDF download, and
  Base bibliography generation, which may contact external DOI/provider
  services.

## Tools

### Library

#### `papers_manager_library_search`

Search documents or paper metadata. Read-only.

Input:

- `query` (string, optional): title, filename, author, or document ID;
- `doi` (string, optional);
- `tags` (string array, optional);
- `page` (integer, default 1);
- `page_size` (integer, default 10, max 50).

Output: `items` with document ID, title, authors, year/venue, DOI, tags,
processing summary, and total count. Do not include storage keys.

#### `papers_manager_library_get`

Read one document and its metadata. Read-only.

Input: `document_id`.

Output: document metadata, paper metadata, source state, and a compact
processing summary. Missing metadata is allowed.

#### `papers_manager_library_import_url`

Import one HTTP(S) URL and start the normal processing pipeline.

Input: `url` (URI), optional `doi`.

Output: document ID, created/updated state, and initial processing state. The
tool returns quickly; do not block until recognition completes. Use
`papers_manager_processing_status` for bounded polling.

#### `papers_manager_library_import_file`

Import one regular local file and start the normal pipeline. This tool exists
for local single-user deployments and is disabled unless
`MCP_LOCAL_IMPORT_ROOT` is configured.

Input: `path` (absolute, or relative to the configured root), optional `doi`.
Paths outside the root, symlink escapes, directories, unsupported types, and
files over `FILE_MAX_UPLOAD_SIZE` are rejected before the service is called.
The service stores a copy; the source file is not referenced afterwards.

Output: the created document and initial processing state.

#### `papers_manager_library_update_metadata`

Correct explicitly requested metadata fields.

Input: `document_id`; optional `original_filename`, `doi`, `title`, `authors`,
`abstract`, `venue`, `tags`, `source_name`, `source_url`, `published_at`,
`volume`, `issue`, `pages`, `language`. Omitted fields are unchanged.
`published_at` accepts `YYYY-MM-DD` or RFC3339; an empty date is not a clear
operation (omit it to keep the current value).

Output: the persisted fields and their source. Do not infer values from the
paper text.

#### `papers_manager_library_delete`

Delete one document and its dependent records.

Input: `document_id`, `confirm` (must be `true`).

Output: deletion result. The tool must reject `confirm` omitted/false. The
skill requires explicit user confirmation before calling it.

#### `papers_manager_processing_status`

Read the single processing projection for one document.

Input: `document_id`.

Output: stage states, active/terminal spans, safe error codes, and reference
progress. Never expose storage paths or raw provider responses.

#### `papers_manager_processing_retry`

Retry one processing lane.

Input: `document_id`; `target` = `auto` (default), `markdown`, `extraction`,
`catalog`, or `reference`. `auto` uses the document service's existing choice;
`markdown` restarts recognition; the other targets retry exactly one
paper-information stage. Active work returns `CONFLICT`.

#### `papers_manager_processing_cancel`

Cancel one processing lane.

Input: `document_id`; `target` = `markdown` (default) or `paper_info`.
Cancelling markdown does not fence paper-information work, and vice versa.

### Source reading

#### `papers_manager_document_structure`

Read the persisted source outline. Read-only.

Input: `document_id`.

Output: recognition generation, source (`pdf_outline`/`markdown_headings`),
sections with page/bbox/block-ordinal/Markdown-range hints, and diagnostics.
Never infer a new outline in the adapter.

#### `papers_manager_document_read_section`

Read one section of the persisted source outline by ordinal. Read-only.

Input: `document_id`, `ordinal`.

Output: section title/level/parent, page range, block-ordinal range, Markdown
slice when the outline has a Markdown range, and diagnostics. The adapter uses
the persisted outline and parsed Markdown; it never infers a new outline. If
only page/Block mapping exists, return those hints with a diagnostic instead of
inventing section text. Hard response limit: 4 MiB.

#### `papers_manager_document_read_markdown`

Read bounded full parsed Markdown as a fallback when a section is not
addressable through the outline. Read-only.

Input: `document_id`.

Output: Markdown, size, page count, recognition generation. Hard response limit:
16 MiB; larger requests return `PAYLOAD_TOO_LARGE` and should use
`papers_manager_document_read_section` or page/Block reads. Do not expose the
storage key.

#### `papers_manager_document_read_blocks`

Read page-scoped Blocks for exact evidence. Read-only.

Input: `document_id`, optional `page_number`.

Output: Blocks with UUID, type, text, ordinal, page dimensions, and bbox.
Prefer a single page; a no-page request is bounded by the adapter and may return
`PAYLOAD_TOO_LARGE`.

#### `papers_manager_document_read_block_image`

Read the parser-stored PNG crop for one image or table Block. Read-only.

Input: `document_id`, `block_id`.

Output: JSON metadata plus an MCP image content block. The image is limited by
`MCP_MAX_IMAGE_BYTES` (default 12 MiB); the private storage key is never
returned. Text-only Blocks have no image and return `NOT_FOUND`.

#### `papers_manager_document_read_page_image`

Render one full PDF page or a normalized page region. Read-only. This is a
synchronous direct renderer call, not an Asynq task.

Input: `document_id`, `page_number`; optional `bbox`
(`{x0,y0,x1,y1}` normalized to `[0,1]`, omitted for the full page); optional
`dpi` (default 144, max 300).

Output: JSON metadata plus an MCP image content block. Bounds come from
`MCP_MAX_IMAGE_BYTES` and the parser render limits
(`PARSER_RENDER_MAX_DPI`, `PARSER_RENDER_MAX_PIXELS`,
`PARSER_RENDER_MAX_OUTPUT_BYTES`). The private storage key is never returned.
The renderer is a PyMuPDF-only worker and never loads PyMuPDF4LLM/ONNX/CRF.

### Reading notes

#### `papers_manager_reading_note_outline`

Read the note revision and section tree. This is a derived read model: the
adapter reads section Markdown and parses `section-meta` descriptions; the
backend does not provide descriptions.

Input: `document_id`, optional `include_descriptions` (default true).

Output:

```json
{
  "document_id": "…",
  "revision": 12,
  "sections": [
    {
      "id": "…",
      "parent_id": null,
      "order": 0,
      "level": 1,
      "title": "方法",
      "description": "对应原文 §3，pp.4–6。",
      "content_revision": 3,
      "size_bytes": 412
    }
  ],
  "descriptions_complete": true,
  "diagnostics": []
}
```

Bounds: at most 256 sections; at most 8 MiB of section Markdown read for
descriptions. If the budget is exhausted, return the directory with
`descriptions_complete=false` and a diagnostic, rather than silently omitting
sections.

#### `papers_manager_reading_note_read_section`

Read one note section body. Read-only.

Input: `document_id`, `section_id`.

Output: section metadata, Markdown (including heading and section-meta),
`line_count`, and current note revision. Hard response limit: 4 MiB; larger
sections return `PAYLOAD_TOO_LARGE`. Do not expose the storage key.

#### `papers_manager_reading_note_create`

Create a reading note only when it is absent.

Input: `document_id`, `markdown`, `expected_revision` (must be `0`).

Output: note manifest, new revision, and section directory. If a note already
exists, return `REVISION_CONFLICT`.

#### `papers_manager_reading_note_apply_edits`

Apply the existing semantic edit contract atomically against one revision.

Input:

- `document_id`;
- `expected_revision` (positive integer);
- `edits` (1–128 entries), using the same ops as HTTP
  `POST /reading-note/edits`: `replaceText`, `replaceRange`, `insertAt`,
  `appendText`, `replaceSection`, `addSection`, `deleteSection`.

Content fields follow the HTTP contract: `replaceText` uses `old_text` and
`new_text`; `replaceRange`, `insertAt`, `appendText`, `replaceSection`, and
`addSection` use `content`. Inline request text is capped at 1 MiB; larger
replacements use the raw section/HTTP path, not MCP.

Output: new revision, directory, changed sections, deleted section IDs.

A stale revision returns `REVISION_CONFLICT`; never auto-refresh and retry the
same content.

#### `papers_manager_reading_note_replace`

Replace the entire note from Markdown, fenced by `expected_revision`. Input:
`document_id`, `markdown`, `expected_revision`. Use this for whole-note
import/replacement; use `apply_edits` for incremental changes. The normal note
size and H1-H3 heading rules apply.

#### `papers_manager_reading_note_delete`

Delete the entire note. Input: `document_id`, `expected_revision`, `confirm`
(must be true). The skill requires explicit user confirmation.

#### `papers_manager_reading_note_export`

Return the full concatenated note Markdown and revision. Input: `document_id`.
Responses larger than 16 MiB return `PAYLOAD_TOO_LARGE`.

#### `papers_manager_reading_note_replace_section`

Replace one whole section with complete section Markdown, preserving its
heading level. Input: `document_id`, `section_id`, `markdown`,
`expected_revision`. The same revision fence and heading guard as `apply_edits`
apply.

### Evidence

#### `papers_manager_evidence_validate`

Parse and validate one evidence link against the current document. Read-only.

Input: `document_id`, `href` (`papers-manager://...`).

Output:

- parsed target (`page`, rectangles, Block UUID, section hint);
- diagnostics;
- whether the page is within the current page count;
- whether a Block UUID belongs to the current recognition generation;
- normalized target data for writing.

The adapter parses evidence links as a consumer. The core note service and
storage remain evidence-agnostic.

### Base

The Base collection manages collections and memberships only. It never copies
or deletes source documents.

#### `papers_manager_base_list`

List Bases. Input: `page`, `page_size`. Read-only.

#### `papers_manager_base_get`

Read one Base. Input: `base_id`; optional `include_documents`, `page`.
Member pages use the backend's fixed page size. Read-only.

#### `papers_manager_base_list_documents`

List one member page. Input: `base_id`, `page`, optional `exclude_unknown`.
The backend uses a fixed page size for this collection view. Read-only.

#### `papers_manager_base_create` / `papers_manager_base_update`

Create a Base (`name`, optional `description`) or rename/change one
(`base_id`, optional `name`, `description`). Omitted update fields are
unchanged.

#### `papers_manager_base_delete`

Delete a Base and its memberships. Input: `base_id`, `confirm` (must be true).
Source documents and notes are never deleted.

#### `papers_manager_base_add_document` / `papers_manager_base_remove_document`

Change membership. Inputs: `base_id`, `document_id`. Membership only; no
document copy or deletion.

#### `papers_manager_base_bibliography_generate`

Queue a `.bib` snapshot for a Base. Input: `base_id`. The tool returns a task
ID immediately; it is not a blocking export.

#### `papers_manager_base_bibliography_status`

Read one bibliography task. Input: `base_id`, `task_id`. A completed task
includes the snapshot, including `bibtex` and per-entry reports. Read-only.

### Citation

Citation tools operate on `paper_citations` rows. Each row belongs to one
citing paper; resolving a shared work never changes another paper's row.

#### `papers_manager_citation_list` / `papers_manager_citation_list_cited_by`

List one document's outgoing bibliography rows, or the rows that resolve to a
document. Input: `document_id`. Read-only.

#### `papers_manager_citation_add`

Add one manual row. Input: `document_id`, `raw_reference`, optional
`cited_document_id`.

#### `papers_manager_citation_replace`

Replace the entire outgoing bibliography. Input: `document_id`, `citations`
(0–500 entries of `{raw_reference, cited_document_id?}`), `confirm` (must be
true). This can remove manual rows.

#### `papers_manager_citation_remove`

Delete one row by reference ID. Input: `reference_id`, `confirm` (must be
true).

#### `papers_manager_citation_update_reference`

Correct one reference's source fields (`doi`, `title`, `authors`, `venue`,
`published_at`, `volume`, `issue`, `pages`, `source_url`). Omitted fields are
unchanged; empty text clears. Editing an unresolved row fences stale provider
results.

#### `papers_manager_citation_resolve`

Manually link one row to an existing document. Input: `reference_id`,
`resolved_document_id`.

#### `papers_manager_citation_rematch`

Retry local DOI/title matching against the current library. Input:
`reference_id`. No external provider is called; the result reports `matched`.

#### `papers_manager_citation_download_pdf`

Queue acquisition of one reference's PDF from its trusted source URL. Input:
`reference_id`. Success creates a document and links the row; duplicate requests
return `CONFLICT` while the previous attempt is pending.

## Deferred tools

Not part of this manifest yet:

- `base_summary` / `base_compare` — Phase 3B; until then the reading skill
  composes summaries from existing read tools.
- Native LLM generation — not a server-side MCP tool until a runner exists.
- Batch local-file or local-directory imports — only one explicitly selected
  file is imported, confined to `MCP_LOCAL_IMPORT_ROOT`.
- High-DPI whole-document pre-rendering — page/region images are rendered on
  demand and cached by the caller; there is no bulk page-image export yet.

## Mapping to existing HTTP surfaces

| Tool | Existing implementation surface |
| --- | --- |
| `papers_manager_library_search` | `GET /api/document/search`, `GET /api/paper/search` |
| `papers_manager_library_get` | `GET /api/document/search?id=`, `GET /api/paper/:id` |
| `papers_manager_library_import_url` | `POST /api/document/import-url` |
| `papers_manager_library_import_file` | `DocumentService.CreateDocumentFromLocalFile` (no HTTP equivalent; local single-user adapter) |
| `papers_manager_library_update_metadata` | `PATCH /api/document/:id`, `PATCH /api/paper/:id` |
| `papers_manager_library_delete` | `DELETE /api/document/:id` |
| `papers_manager_processing_status` | `GET /api/document/:id/processing` |
| `papers_manager_processing_retry` | `POST /api/document/:id/retry`, `POST /api/paper/:id/info/{extraction|catalog|reference}/retry` |
| `papers_manager_processing_cancel` | `POST /api/document/:id/cancel`, `POST /api/paper/:id/info/cancel` |
| `papers_manager_document_structure` | `GET /api/document/:id/structure` |
| `papers_manager_document_read_section` | `GET /api/document/:id/structure` + `GET /api/document/:id/markdown` (adapter slices by outline range) |
| `papers_manager_document_read_markdown` | `GET /api/document/:id/markdown` |
| `papers_manager_document_read_blocks` | `GET /api/document/:id/blocks?page=N` |
| `papers_manager_document_read_block_image` | `GET /api/document/:id/blocks/:blockID/image` |
| `papers_manager_document_read_page_image` | `GET /api/document/:id/pages/:page/image` (direct `PdfRenderer.RenderPage`, no Asynq) |
| `papers_manager_reading_note_outline` | `GET /api/document/:id/reading-note` + section content endpoints |
| `papers_manager_reading_note_read_section` | `GET /api/document/:id/reading-note/sections/:sectionId` |
| `papers_manager_reading_note_create` | `PUT /api/document/:id/reading-note` with `expectedRevision=0` |
| `papers_manager_reading_note_apply_edits` | `POST /api/document/:id/reading-note/edits` |
| `papers_manager_reading_note_replace` | `PUT /api/document/:id/reading-note` |
| `papers_manager_reading_note_delete` | `DELETE /api/document/:id/reading-note` |
| `papers_manager_reading_note_export` | `GET /api/document/:id/reading-note/export` |
| `papers_manager_reading_note_replace_section` | `PUT /api/document/:id/reading-note/sections/:sectionId` |
| `papers_manager_evidence_validate` | consumer-side parser + `document/:id/markdown`, `/blocks`, `/structure` |
| `papers_manager_base_*` | `/api/document/bases` CRUD, membership, and `/bibliography` endpoints |
| `papers_manager_citation_*` | `/api/paper/:documentID/citations*`, `/api/references/:referenceID/*` |

The adapter may call application services directly or compose these HTTP
reads; it must not duplicate persistence logic or bypass revision checks.

## Skill mapping

- `papers-manager-library` uses library, processing, Base, and citation tools
  for ingestion and management.
- `papers-manager-reading` uses source reading, note, image, and evidence tools.
- Base summary uses Base/list/get plus source/note reads until Phase 3B adds a
  dedicated tool. Base bibliography generation is available now.

## Versioning and tests

- Manifest `schemaVersion: 1`. The descriptive `group` field is an additive
  optional field; tool names themselves carry the stable collection prefix.
- Adding optional fields/tools is backward-compatible; renaming or changing
  semantics requires a new version or tool name.
- Section metadata remains version 1; evidence links remain version 1.
- Required tests: schema validation, loopback/token boundary, response bounds,
  local-import path containment, revision conflicts, note creation only when
  absent, section-meta parsing, evidence validation, error-code mapping, and
  no storage paths/secrets in output.
