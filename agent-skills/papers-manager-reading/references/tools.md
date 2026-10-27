# Reading tool index

Schema authority: the MCP server's `tools/list` response. This file only maps
capabilities to the reading workflow.

| Tool | Use |
| --- | --- |
| `papers_manager_library_search` / `papers_manager_library_get` | Resolve a paper and read L0 metadata. |
| `papers_manager_library_get_many` | Read metadata for 1–200 document IDs in one call. |
| `papers_manager_processing_status_many` | Check markdown readiness for 1–200 documents before batch note generation. |
| `papers_manager_base_list` / `papers_manager_base_get` | Resolve Base identity and member count. |
| `papers_manager_base_list_documents` | Read actual Base membership before a Base summary/comparison or batch note run. |
| `papers_manager_document_structure` | Read the persisted source outline and diagnostics. |
| `papers_manager_document_read_section` | Default/ mode=markdown returns the original section Markdown. Use mode=blocks for paginated blocks including children, or mode=range for a range and optional IDs. |
| `papers_manager_document_read_markdown` | Read bounded full source Markdown when a section cannot be addressed through the outline. |
| `papers_manager_document_read_blocks` | Read blocks by page, half-open block_range or up to 200 block_ids; pass expected_generation across calls. |
| `papers_manager_document_read_block_image` | Read a parser-stored PNG crop for an image/table Block. |
| `papers_manager_document_read_page_image` | Render a full page or normalized page bbox as PNG (synchronous, not a task). |
| `papers_manager_reading_note_outline` | Read note revision, section tree, and parsed section descriptions. |
| `papers_manager_reading_note_read_section` | Read one note section body. |
| `papers_manager_reading_note_create` | Create an absent note when the user requests generation/saving; no repeated confirmation. |
| `papers_manager_reading_note_apply_edits` | Apply revision-fenced semantic edits (default). |
| `papers_manager_reading_note_replace` | Replace the whole note only when explicitly requested. |
| `papers_manager_reading_note_replace_section` | Replace one whole section; preserves the heading guard. |
| `papers_manager_reading_note_delete` | Delete the note only with explicit confirmation. |
| `papers_manager_reading_note_export` | Read the full concatenated note when L4 is explicitly needed. |
| `papers_manager_evidence_validate_many` | Check 1–100 links, reuse page metadata, and report succeeded/partial/failed per item and batch. |
| `papers_manager_evidence_validate` | Check link/UUID/rect syntax and known page bounds only; does not resolve blocks. |

Do not use library-management tools such as `papers_manager_library_delete` from this skill.
Do not claim that `base_summary` exists until it is registered in the MCP
manifest. Batch note generation uses the same reading tools; its orchestration
rules are in [batch-notes.md](batch-notes.md).

MCP arguments use snake_case (`document_id`, `section_id`, `expected_revision`). The HTTP API's camelCase field names do not apply to tool arguments.

## Base 批量目录

`papers_manager_base_reading_outlines(base_id, outline_type=both, page, page_size, section_limit)`：分页批量取原文/notes目录，不读正文或description。检查本页status/summary和逐项state、complete、has_more；外层ok不表示目录齐全。主agent批量验证优先使用，单篇worker修改时仍需新鲜revision。

Source section reads in mode=blocks/range return `recognition_generation`, `block_range`, `total`, and
`has_more`; follow pagination before claiming full coverage. Use mode=range for
selection without text, then pass that range or selected IDs plus
expected_generation to document_read_blocks. On CONFLICT, reload the outline and
reselect; do not reuse old IDs with a refreshed generation. Unmapped section
boundaries return UNAVAILABLE; use page reads instead.
