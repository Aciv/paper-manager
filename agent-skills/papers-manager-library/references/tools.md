# Library tool index

Schema authority: the MCP server's `tools/list` response. This file only
explains when to use each capability. Base and citation tools are separate
collections, not `papers_manager_library_*` operations.

## Library and processing

| Tool | Purpose | Safety |
| --- | --- | --- |
| `papers_manager_library_search` | Search by title, DOI, tags, or ID; paginated. | Read-only |
| `papers_manager_library_get` | Read one document and its paper metadata. | Read-only |
| `papers_manager_library_get_many` | Read 1–200 documents and their paper metadata in one call; optional processing. | Read-only; >200 IDs rejected; missing IDs reported |
| `papers_manager_library_import_url` | Queue one HTTP(S) URL; returns task id immediately. Optional `base_id` and stable UUID `request_id`. | Poll import_status; request_id may only replay identical input |
| `papers_manager_library_import_urls` | Queue 1–20 URLs; optional aligned `dois`, `base_id`, UUID `request_id`. Base membership updates per completed item. | Returns immediately; background deadline defaults to 2 minutes; poll import_status and inspect every result |
| `papers_manager_library_import_status` | Read import task state, completed/total and partial per-item document IDs. | Read-only; completed results retained 24 hours; PDF processing is queried separately |
| `papers_manager_library_import_file` | Import one local file under `MCP_LOCAL_IMPORT_ROOT`. Optional `base_id` adds the document to a Base. | Disabled unless configured; path is confined to the root; shares the independent 2-call local import concurrency gate |
| `papers_manager_library_import_files` | Import 1–20 local files under `MCP_LOCAL_IMPORT_ROOT`, sequentially; all paths validated before any import. Optional `base_id` applies to every accepted/duplicate item. | Disabled unless configured; one invalid path rejects the whole call; shares the independent 2-call local import concurrency gate |
| `papers_manager_library_update_metadata` | Correct filename, DOI, title, authors, abstract, venue, tags, and publication fields. | Only explicit field changes |
| `papers_manager_library_delete` | Delete one document. | Requires `confirm=true` and user confirmation |
| `papers_manager_processing_status` | Read async stage state for one document. | Read-only |
| `papers_manager_processing_status_many` | Read async stage state for 1–200 documents in one call. | Read-only; >200 IDs rejected; missing IDs reported |
| `papers_manager_processing_retry` | Retry one lane (`auto`/`markdown`/`extraction`/`catalog`/`reference`). | Only when the user asks; no unbounded retry loops |
| `papers_manager_processing_cancel` | Cancel `markdown` or `paper_info`. | State-changing; does not cancel the other lane |

## Base collection

| Tool | Purpose | Safety |
| --- | --- | --- |
| `papers_manager_base_list` | List Bases. | Read-only |
| `papers_manager_base_get` | Read one Base and optionally its members. | Read-only |
| `papers_manager_base_list_documents` | List one member page. | Read-only |
| `papers_manager_base_create` | Create a Base. | No document copy |
| `papers_manager_base_update` | Rename a Base or change its description. | Only explicit fields |
| `papers_manager_base_delete` | Delete a Base and memberships. | Requires `confirm=true`; never deletes documents |
| `papers_manager_base_add_document` | Add membership. | Membership only |
| `papers_manager_base_remove_document` | Remove membership. | Membership only; never deletes documents |
| `papers_manager_base_bibliography_generate` | Queue a `.bib` snapshot. | Poll the task; do not claim synchronous completion |
| `papers_manager_base_bibliography_status` | Read the bibliography task/snapshot. | Read-only |

## Citation collection

| Tool | Purpose | Safety |
| --- | --- | --- |
| `papers_manager_citation_list` | List outgoing references. | Read-only |
| `papers_manager_citation_list_cited_by` | List citing rows for a document. | Read-only |
| `papers_manager_citation_add` | Add one manual reference row. | Explicit input only |
| `papers_manager_citation_replace` | Replace the whole outgoing bibliography. | Requires `confirm=true` and user confirmation |
| `papers_manager_citation_remove` | Delete one reference row. | Requires `confirm=true` and user confirmation |
| `papers_manager_citation_update_reference` | Correct source fields. | Manual provenance; resolution is fenced |
| `papers_manager_citation_resolve` | Link a row to an existing document. | Does not create a document |
| `papers_manager_citation_rematch` | Retry local DOI/title matching. | Local only; no external provider |
| `papers_manager_citation_download_pdf` | Queue reference PDF acquisition. | Network action; success creates a document |

Do not use `reading_note_*` or `document_*` reading tools from this skill
except to confirm that a document exists. Do not use this skill to generate
notes or summaries.

## Base 批量目录

`papers_manager_base_reading_outlines(base_id, outline_type=both, page, page_size, section_limit)`：分页批量取原文/notes目录，不读正文或description。检查本页status/summary和逐项state、complete、has_more；外层ok不表示目录齐全。主agent批量验证优先使用，单篇worker修改时仍需新鲜revision。

Import metadata/options: single tools accept metadata (camelCase fields), processing.source_lookup=auto|skip,
and duplicate_metadata=none|fill_missing|replace. Batch tools accept an options array aligned with paths/urls;
{} retains defaults. Only an explicit user choice enables skip. Files remain imported if importMetadata.status
is failed; repair the metadata by document ID. Never resubmit an accepted batch to fix metadata.
processing_status_many exposes readingReady and workflowStatus, plus summary and missingDocumentIds.
Use readingReady to pipeline notes work; source skip and manual-reference waiting do not block notes.
