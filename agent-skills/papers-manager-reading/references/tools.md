# Reading tool index

Schema authority: the MCP server's `tools/list` response. This file only maps
capabilities to the reading workflow.

| Tool | Use |
| --- | --- |
| `papers_manager_library_search` / `papers_manager_library_get` | Resolve a paper and read L0 metadata. |
| `papers_manager_document_structure` | Read the persisted source outline and diagnostics. |
| `papers_manager_document_read_section` | Read one source outline section by ordinal, using its Markdown/page/Block mapping. |
| `papers_manager_document_read_markdown` | Read bounded full source Markdown when a section cannot be addressed through the outline. |
| `papers_manager_document_read_blocks` | Read page/Blocks for L3 evidence and exact verification. |
| `papers_manager_reading_note_outline` | Read note revision, section tree, and parsed section descriptions. |
| `papers_manager_reading_note_read_section` | Read one note section body. |
| `papers_manager_reading_note_create` | Create an absent note only after user confirmation. |
| `papers_manager_reading_note_apply_edits` | Apply revision-fenced semantic edits. |
| `papers_manager_evidence_validate` | Validate a `papers-manager://` evidence target before writing. |

Do not use library-management tools such as `papers_manager_library_delete` from this skill.
Do not claim that `base_summary` exists until it is registered in the MCP
manifest.

MCP arguments use snake_case (`document_id`, `section_id`, `expected_revision`). The HTTP API's camelCase field names do not apply to tool arguments.
