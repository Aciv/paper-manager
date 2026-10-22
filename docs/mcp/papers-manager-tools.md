# papersManager MCP tool contract

Status: specification implemented by `internal/mcp` and the in-process MCP
listener. The manifest remains the source of truth for tool names and schemas.
Per-host connection and skill installation are covered by
[agent-installation.md](agent-installation.md).

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
- Import accepts HTTP(S) URLs only. Do not expose arbitrary server filesystem
  paths or local file uploads through MCP.

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

- `readOnlyHint: true` for search/read/metadata reads.
- `destructiveHint: true` for delete, metadata replacement, note section
  replacement/deletion, and revision-fenced edits that can overwrite content.
- `idempotentHint` only where repeated identical calls are safe; note edits are
  not treated as idempotent across revisions.
- `openWorldHint: false` except for
  `papers_manager_library_import_url`, which contacts an external URL.

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

#### `papers_manager_library_update_metadata`

Correct explicitly requested metadata fields.

Input: `document_id`; optional `original_filename`, `doi`, `title`, `authors`,
`abstract`, `venue`, `tags`. Omitted fields are unchanged.

Output: the persisted fields and their source. Do not infer values from the
paper text.

#### `papers_manager_library_delete`

Delete one document and its dependent records.

Input: `document_id`, `confirm` (must be `true`).

Output: deletion result. The tool must reject `confirm` omitted/false. The
skill requires explicit user confirmation before calling it.

#### `papers_manager_library_list_bases` / `papers_manager_library_get_base`

Read Bases. `papers_manager_library_get_base` may include a bounded member page.

Inputs: `page`, `page_size`, `base_id`, `include_documents`.

#### `papers_manager_library_add_to_base` / `papers_manager_library_remove_from_base`

Change membership only. Inputs: `base_id`, `document_id`. Must not copy or
delete documents.

#### `papers_manager_processing_status`

Read the single processing projection for one document.

Input: `document_id`.

Output: stage states, active/terminal spans, safe error codes, and reference
progress. Never expose storage paths or raw provider responses.

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

Content fields follow the HTTP contract. Inline request text is capped at
1 MiB; larger replacements use the raw section/HTTP path, not MCP.

Output: new revision, directory, changed sections, deleted section IDs.

A stale revision returns `REVISION_CONFLICT`; never auto-refresh and retry the
same content.

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

## Deferred tools

Not part of this manifest yet:

- `base_summary` / `base_compare` — Phase 3B; until then the reading skill
  composes summaries from existing read tools.
- Native LLM generation — not a server-side MCP tool until a runner exists.
- Bibliography generation, citation resolution, and reference download —
  management workflows can use the existing HTTP APIs separately.
- Local filesystem imports — intentionally excluded.

## Mapping to existing HTTP surfaces

| Tool | Existing implementation surface |
| --- | --- |
| `papers_manager_library_search` | `GET /api/document/search`, `GET /api/paper/search` |
| `papers_manager_library_get` | `GET /api/document/search?id=`, `GET /api/paper/:id` |
| `papers_manager_library_import_url` | `POST /api/document/import-url` |
| `papers_manager_library_update_metadata` | `PATCH /api/document/:id`, `PATCH /api/paper/:id` |
| `papers_manager_library_delete` | `DELETE /api/document/:id` |
| `papers_manager_library_list_bases` / `papers_manager_library_get_base` | `GET /api/document/bases`, `GET /api/document/bases/:id`, `GET .../documents` |
| `papers_manager_library_add_to_base` / `papers_manager_library_remove_from_base` | `PUT|DELETE /api/document/bases/:baseID/documents/:documentID` |
| `papers_manager_processing_status` | `GET /api/document/:id/processing` |
| `papers_manager_document_structure` | `GET /api/document/:id/structure` |
| `papers_manager_document_read_section` | `GET /api/document/:id/structure` + `GET /api/document/:id/markdown` (adapter slices by outline range) |
| `papers_manager_document_read_markdown` | `GET /api/document/:id/markdown` |
| `papers_manager_document_read_blocks` | `GET /api/document/:id/blocks?page=N` |
| `papers_manager_reading_note_outline` | `GET /api/document/:id/reading-note` + section content endpoints |
| `papers_manager_reading_note_read_section` | `GET /api/document/:id/reading-note/sections/:sectionId` |
| `papers_manager_reading_note_create` | `PUT /api/document/:id/reading-note` with `expectedRevision=0` |
| `papers_manager_reading_note_apply_edits` | `POST /api/document/:id/reading-note/edits` |
| `papers_manager_evidence_validate` | consumer-side parser + `document/:id/markdown`, `/blocks`, `/structure` |

The adapter may call application services directly or compose these HTTP
reads; it must not duplicate persistence logic or bypass revision checks.

## Skill mapping

- `papers-manager-library` uses the library and processing tools.
- `papers-manager-reading` uses source reading, note, and evidence tools.
- Base summary uses library/list/get plus source/note reads until Phase 3B
  adds a dedicated tool.

## Versioning and tests

- Manifest `schemaVersion: 1`.
- Adding optional fields/tools is backward-compatible; renaming or changing
  semantics requires a new version or tool name.
- Section metadata remains version 1; evidence links remain version 1.
- Required tests: schema validation, loopback/token boundary, response bounds,
  revision conflicts, note creation only when absent, section-meta parsing,
  evidence validation, and no storage paths/secrets in output.
