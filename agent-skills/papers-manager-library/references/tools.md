# Library tool index

Schema authority: the MCP server's `tools/list` response. This file only
explains when to use each capability. Base and citation tools are separate
collections, not `papers_manager_library_*` operations.

## Library and processing

| Tool | Purpose | Safety |
| --- | --- | --- |
| `papers_manager_library_search` | Search by title, DOI, tags, or ID; paginated. | Read-only |
| `papers_manager_library_get` | Read one document and its paper metadata. | Read-only |
| `papers_manager_library_import_url` | Import an HTTP(S) URL and start processing. | Requires a user-supplied URL |
| `papers_manager_library_import_file` | Import one local file under `MCP_LOCAL_IMPORT_ROOT`. | Disabled unless configured; path is confined to the root |
| `papers_manager_library_update_metadata` | Correct filename, DOI, title, authors, abstract, venue, tags, and publication fields. | Only explicit field changes |
| `papers_manager_library_delete` | Delete one document. | Requires `confirm=true` and user confirmation |
| `papers_manager_processing_status` | Read async stage state for one document. | Read-only |
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
