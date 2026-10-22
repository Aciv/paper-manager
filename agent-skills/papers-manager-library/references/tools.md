# Library tool index

Schema authority: the MCP server's `tools/list` response. This file only
explains when to use each capability.

| Tool | Purpose | Safety |
| --- | --- | --- |
| `papers_manager_library_search` | Search by title, DOI, tags, or ID; paginated. | Read-only |
| `papers_manager_library_get` | Read one document and its paper metadata. | Read-only |
| `papers_manager_library_import_url` | Import an HTTP(S) URL and start processing. | Requires a user-supplied URL |
| `papers_manager_library_update_metadata` | Correct filename, DOI, title, authors, abstract, venue, or tags. | Only explicit field changes |
| `papers_manager_library_delete` | Delete one document. | Requires `confirm=true` and user confirmation |
| `papers_manager_library_list_bases` | List Bases. | Read-only |
| `papers_manager_library_get_base` | Read one Base and, optionally, its members. | Read-only |
| `papers_manager_library_add_to_base` | Add one document to a Base. | Membership only |
| `papers_manager_library_remove_from_base` | Remove one document from a Base. | Membership only |
| `papers_manager_processing_status` | Read async stage state for one document. | Read-only |

Do not use `reading_note_*` or `document_*` reading tools from this skill
except to confirm that a document exists. Do not use this skill to generate
notes or summaries.
