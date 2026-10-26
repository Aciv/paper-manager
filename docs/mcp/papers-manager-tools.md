# MCP 工具契约

[文档导航](../README.md) · [客户端与技能接入](agent-installation.md)

当前 `internal/mcp` 已实现 48 个工具，分为 `library`、`reading`、`base`、`citation`
四组。精确的工具名称、必填项、枚举和输入模式见[机器清单](papers-manager-tools.json)；
本文解释行为、边界和消费者应遵守的规则。JSON 字段与工具标识符不翻译。

## 设计边界

MCP 是应用服务的适配层，与 HTTP 共用数据库、文件存储、服务和笔记 revision 规则。
不引入另一套存储，也不创建 Agent 身份、偏好或独立资源所有权。
响应返回文档/章节 ID 和内容，不返回私有 storage key、路径或凭据。

全部工具使用 `papers_manager_` 前缀；宿主可再加自己的服务器命名空间。
技能应按工具前缀或 `papers-manager` 服务识别能力，不能依赖某个宿主固定的外层包装。
`group` 只用于描述分组，Base 和引用仍有独立的 `papers_manager_base_*`、
`papers_manager_citation_*` 工具前缀。

## 传输与访问

- 仅支持 **MCP Streamable HTTP**，路径为 `/mcp`；没有内置 stdio 或旧 HTTP+SSE 入口。
- 普通 API 默认关闭 MCP，默认地址为 `127.0.0.1:8090`；Launcher 使用管理页显示的实际地址。
- 当前实现中，`MCP_TOKEN` 为空会关闭认证；非回环监听只记录警告，不会拒绝启动。
  token 非空时，每次请求都必须提供 `Authorization: Bearer <token>`。
- 无多用户权限隔离。网络可达且通过 token 验证的客户端共享同一套资源权限。
- 仅支持 stdio 的宿主可使用客户端桥接；桥接仍调用同一个 `/mcp`，不直接访问数据库。
- 本地导入默认禁用；`MCP_LOCAL_IMPORT_ROOT` 非空才允许导入受控根目录中的普通文件，
  路径检查包含符号链接解析，越界返回 `FORBIDDEN`。
- 单项与批量导入共用 `MCP_IMPORT_CONCURRENCY`，默认最多 20 个导入调用同时执行。
  超限立即返回 `RESOURCE_EXHAUSTED`；每批最多 20 项，持有一个名额并顺序执行。
- 图片响应使用 MCP 图片内容块，受 `MCP_MAX_IMAGE_BYTES`（默认 12 MiB）等上限约束。

## 返回结构、错误与注解

成功返回：

```json
{"ok": true, "data": {}}
```

失败返回并设置 MCP `isError`：

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

不得返回原始数据库错误、私有存储路径、凭据或无大小限制的外部响应。

| 错误码 | 可直接重试 | 含义 |
| --- | --- | --- |
| `INVALID_ARGUMENT` | 否 | 缺失或不合法的输入 |
| `FORBIDDEN` | 否 | 访问边界拒绝 |
| `CANCELLED` | 否 | 请求上下文取消 |
| `NOT_FOUND` | 否 | 文档、章节、引用或 Base 不存在 |
| `NOTE_NOT_FOUND` | 否 | 文档存在但没有阅读笔记 |
| `REVISION_CONFLICT` | 否 | 版本已变化，重新读取并规划编辑 |
| `UNAVAILABLE` | 是 | 服务、队列或发布过程暂不可用 |
| `RESOURCE_EXHAUSTED` | 等待后 | 并发准入已满 |
| `PAYLOAD_TOO_LARGE` | 否 | 输入/输出超过上限，应缩小读取或写入范围 |
| `INVALID_EVIDENCE` | 否 | 证据格式或目标范围不合法 |
| `CONFLICT` | 否 | 处理、替换等生命周期冲突 |
| `INTERNAL` | 视情况 | 非预期服务错误，不暴露内部细节 |

工具注解中，搜索、读取、导出和证据验证使用 `readOnlyHint`；删除、替换和可能覆盖内容的
编辑标为破坏性操作。`idempotentHint` 仅用于可以安全重复的操作，不能把跨 revision 的
笔记修改当作幂等。URL 导入、引用 PDF 下载和书目生成可能访问外部服务。

## 1. 论文库与处理状态

### 查询

| 工具 | 输入 | 行为与输出 |
| --- | --- | --- |
| `papers_manager_library_search` | 可选 `query`、`doi`、`tags`、`page`、`page_size` | 搜索标题、文件名、作者或文档 ID；默认第 1 页、每页 10 条，最多 50 条；返回文档与元数据摘要和总数 |
| `papers_manager_library_get` | `document_id` | 读取文档、论文元数据、来源与紧凑处理状态；允许元数据缺失 |
| `papers_manager_library_get_many` | `document_ids`；可选 `include_processing=false` | 一次读取 1–200 个 UUID，去重并保持请求顺序；不存在的 ID 放入 `missingDocumentIds` |
| `papers_manager_processing_status` | `document_id` | 返回阶段、span、安全错误码和引用进度 |
| `papers_manager_processing_status_many` | `document_ids` | 批量读取 1–200 项处理状态，与批量文档查询使用同一批量投影 |

已知一组 ID 时优先使用批量工具，避免逐项读取。批量返回的 `items` 可包含
`{document, paperMetadata, processing?}`，只要状态时使用状态批量工具。
查询不发起解析或外部查询，也不返回原始提供方响应。

### 导入

| 工具 | 输入 | 行为 |
| --- | --- | --- |
| `papers_manager_library_import_url` | `url`；可选 `doi`、`base_id`、`request_id` | 立即返回后台 URL 导入任务 ID |
| `papers_manager_library_import_urls` | `urls`；可选 `dois`、`base_id`、`request_id` | 后台顺序导入 1–20 个 URL；立即返回任务 ID |
| `papers_manager_library_import_status` | `task_id` | 读取任务状态、进度和逐项文档 ID，完成结果保留 24 小时 |
| `papers_manager_library_import_file` | `path`；可选 `doi`、`base_id` | 导入受控目录中的单个普通文件；服务保存副本，不长期引用输入路径 |
| `papers_manager_library_import_files` | `paths`；可选 `dois`、`base_id` | 导入受控目录中的 1–20 个普通文件 |

指定 `base_id` 时，将接受或已存在的文档幂等加入该 Base。
URL 只接受 HTTP(S)，下载还受远程 PDF 大小、地址、并发和重定向限制。
本地路径可为绝对路径或相对受控根目录路径，不能越界或通过符号链接逃逸。
本地批量导入先验证全部路径、类型和大小，一项不合法则整批拒绝，不创建文档。

URL 工具返回 `id/state/total`，提交后用 `papers_manager_library_import_status(task_id=id)` 有界轮询。
客户端断开不会取消已接收的 Asynq 下载任务。可选 `request_id` 为 UUID，原输入连同相同 ID 重发会返回原任务；
同一 ID 不允许换输入。后台结果逐项持久化，并立即把 accepted/duplicate 文档加入 Base。
查询返回 `queued/running/succeeded/partial/failed/stopped`、`completed/total` 和 `result`；`succeeded` 表示所有项已新增或已存在；`partial` 表示部分失败，`stopped` 表示提前停止。返回 `summary` 计数和 `retryUrls`（仅失败/未处理项，保留 DOI/文件名）。
`result.items` 的逐项状态为 `accepted/duplicate/rejected/failed`；后台 `BATCH_IMPORT_TIMEOUT` 默认 2 分钟，
到期返回部分结果、`stopped=true` 及未处理项。完成结果在队列保留 24 小时，之后查询返回 NOT_FOUND。
文档 ID 出现后，使用 processing_status 查询解析状态，不把下载任务完成当作论文解析就绪。
本地文件工具仍同步保存副本并立即投递解析任务；批量文件每项完成后加入 Base，不等待整批结束。

### 更新、删除与重试

`papers_manager_library_update_metadata` 输入 `document_id`，可选
`original_filename`、`doi`、`title`、`authors`、`abstract`、`venue`、`tags`、
`source_name`、`source_url`、`published_at`、`volume`、`issue`、`pages`、`language`。
未提供的字段不变；`published_at` 接受 `YYYY-MM-DD` 或 RFC3339，空日期不表示清空。
仅修改用户明确要求的值，不依据猜测填写元数据。

`papers_manager_library_delete` 输入 `document_id`、`confirm=true`，删除文档及依赖记录。
缺少确认或为 false 时拒绝；技能应先获得用户明确确认。

`papers_manager_processing_retry` 输入 `document_id` 和可选 `target`：
`auto`（默认）、`markdown`、`extraction`、`catalog`、`reference`。
除 `auto` 使用服务现有选择逻辑外，其余只重试指定阶段；工作仍活跃时返回冲突。

`papers_manager_processing_cancel` 输入 `document_id`，`target` 为
`markdown`（默认）或 `paper_info`。两条处理链分别取消，不互相替代。

## 2. 论文原文读取

这些工具均为只读，不在适配器中重新推断目录或创建持久化信息。

| 工具 | 输入 | 输出与边界 |
| --- | --- | --- |
| `papers_manager_document_structure` | `document_id` | 持久化源目录、识别 generation、来源及页码/bbox/block ordinal/Markdown 范围提示 |
| `papers_manager_document_read_section` | `document_id`、`ordinal` | 读取源目录节信息及可用的 Markdown 切片；最多 4 MiB |
| `papers_manager_document_read_markdown` | `document_id` | 有界的完整解析 Markdown、大小、页数和 generation；最多 16 MiB |
| `papers_manager_document_read_blocks` | `document_id`；可选 `page_number` | Block ID、类型、文本、ordinal、页面尺寸和 bbox；优先按页，整篇可能超限 |
| `papers_manager_document_read_block_image` | `document_id`、`block_id` | 已保存的图片/表格 PNG，返回元数据及 MCP 图片；纯文本块返回 `NOT_FOUND` |
| `papers_manager_document_read_page_image` | `document_id`、`page_number`；可选 `bbox`、`dpi` | 按需渲染整页/区域，默认 144 DPI、最高 300；同步执行，不经过 Asynq |

目录来源为 `pdf_outline` 或 `markdown_headings`。源节只有页码/Block 映射而无 Markdown
范围时，返回位置提示和诊断，不能伪造节正文。完整 Markdown 超限后应改用分节或按页读取。
`bbox` 使用 `[0,1]` 归一化的 `{x0,y0,x1,y1}`；省略表示整页。
渲染还受 `PARSER_RENDER_MAX_DPI`、`PARSER_RENDER_MAX_PIXELS`、
`PARSER_RENDER_MAX_OUTPUT_BYTES` 和 MCP 图片大小限制。
渲染 worker 只使用 PyMuPDF，不加载识别的 Layout/ONNX/CRF 栈。

## 3. 阅读笔记

### 读取

`papers_manager_reading_note_outline` 输入 `document_id`，可选 `include_descriptions=true`。
返回 revision 和章节树；描述由 MCP 从章节 Markdown 的 `section-meta` 解析，不是后端字段。
示例：

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

目录最多 256 节，描述解析最多读取 8 MiB。预算不足时仍返回目录，设置
`descriptions_complete=false` 并说明诊断，不能静默漏掉章节。

`papers_manager_reading_note_read_section` 输入 `document_id`、`section_id`，返回章节
元数据、完整 Markdown（含标题和元数据块）、`line_count` 及当前 revision；最多 4 MiB。

`papers_manager_reading_note_export` 输入 `document_id`，显式拼接全篇并返回 Markdown
和 revision；响应超过 16 MiB 则拒绝。

### 写入

| 工具 | 必要输入 | 行为 |
| --- | --- | --- |
| `papers_manager_reading_note_create` | `document_id`、`markdown`、`expected_revision=0` | 仅在笔记不存在时创建，已有笔记返回版本冲突 |
| `papers_manager_reading_note_apply_edits` | `document_id`、`expected_revision`、`edits` | 原子应用 1–128 项语义编辑，返回新 revision、目录、变动节和删除 ID |
| `papers_manager_reading_note_replace` | `document_id`、`markdown`、`expected_revision` | 整篇导入/替换，遵守正常大小与 H1–H3 结构约束 |
| `papers_manager_reading_note_replace_section` | `document_id`、`section_id`、`markdown`、`expected_revision` | 替换一节，保留其标题级别 |
| `papers_manager_reading_note_delete` | `document_id`、`expected_revision`、`confirm=true` | 删除整份笔记，需要用户明确确认 |

语义编辑支持 `replaceText`、`replaceRange`、`insertAt`、`appendText`、
`replaceSection`、`addSection`、`deleteSection`。
`replaceText` 使用 `old_text`、`new_text`，其余内容字段使用 `content`。
内联请求文本总预算 1 MiB；更大替换应使用后端原始章节/文件接口，不能无限增大 MCP 请求。
行列与范围规则见[阅读笔记契约](../reading-note-agent-contract.md)。

收到 `REVISION_CONFLICT` 后必须重新读取并规划编辑；不能自动刷新 revision 并原样重试。
章节描述及证据是 Markdown 内容，后端不再单独持久化一份。

## 4. 证据验证

`papers_manager_evidence_validate` 是只读工具，输入 `document_id`、`href`。
解析 `papers-manager://` 链接，返回页码、矩形、Block UUID、章节提示、诊断及规范化目标，
检查页码范围和 Block 是否属于当前识别 generation。
MCP 作为消费者解析证据，不改变核心笔记服务对证据内容不做解析的边界。

## 5. Base 集合

| 工具 | 输入 | 行为 |
| --- | --- | --- |
| `papers_manager_base_list` | 可选 `page`、`page_size` | 分页列出集合 |
| `papers_manager_base_get` | `base_id`；可选 `include_documents`、`page` | 读取集合，可附一页成员 |
| `papers_manager_base_list_documents` | `base_id`；可选 `page`、`exclude_unknown` | 按后端固定页大小列出成员 |
| `papers_manager_base_create` | `name`；可选 `description` | 创建集合 |
| `papers_manager_base_update` | `base_id`；可选 `name`、`description` | 修改名称/描述，未提供字段不变 |
| `papers_manager_base_delete` | `base_id`、`confirm=true` | 删除集合与成员关系，保留源论文及笔记 |
| `papers_manager_base_add_document` | `base_id`、`document_id` | 加入集合，不复制文档 |
| `papers_manager_base_remove_document` | `base_id`、`document_id` | 移除关系，不删除文档 |
| `papers_manager_base_bibliography_generate` | `base_id` | 提交书目快照任务，立即返回任务 ID |
| `papers_manager_base_bibliography_status` | `base_id`、`task_id` | 查询任务；完成后包含 `bibtex` 与逐项报告 |

## 6. 引用关系

每条引用属于一篇施引论文。解析出相同被引论文，不会改变其他论文的引用行。

| 工具 | 输入 | 行为 |
| --- | --- | --- |
| `papers_manager_citation_list` | `document_id` | 读取该论文的参考文献 |
| `papers_manager_citation_list_cited_by` | `document_id` | 读取已解析为指向该论文的引用行 |
| `papers_manager_citation_add` | `document_id`、`raw_reference`；可选 `cited_document_id` | 添加手动引用 |
| `papers_manager_citation_replace` | `document_id`、`citations`、`confirm=true` | 整体替换 0–500 条引用，可能删除手动行 |
| `papers_manager_citation_remove` | `reference_id`、`confirm=true` | 删除一条引用 |
| `papers_manager_citation_update_reference` | `reference_id` 及需修改的来源字段 | 修改引用源信息，未提供字段不变，空文本清空 |
| `papers_manager_citation_resolve` | `reference_id`、`resolved_document_id` | 显式关联库内文档 |
| `papers_manager_citation_rematch` | `reference_id` | 只按本地 DOI/title 重匹配，返回 `matched`，不调用外部服务 |
| `papers_manager_citation_download_pdf` | `reference_id` | 排队下载引用 PDF，成功创建/关联文档；已有待处理请求返回冲突 |

`citations` 每项为 `{raw_reference, cited_document_id?}`。
可编辑来源字段为 `doi`、`title`、`authors`、`venue`、`published_at`、`volume`、
`issue`、`pages`、`source_url`。修改未解析引用会阻止旧提供方结果覆盖新值。

## 7. HTTP 映射与技能

MCP 直接复用应用服务，不应复制数据库写入逻辑或绕过 revision。

| MCP 能力 | 现有 HTTP / 服务入口 |
| --- | --- |
| 论文库搜索/读取 | `/api/document/search`、`/api/paper/search`、`/api/paper/{id}` |
| 批量元数据/状态 | `POST /api/document/projections` |
| URL 导入 | `POST /api/document/import-url`、`batch-import-url` |
| 本地文件导入 | 应用服务本地文件入口；无对应公开 HTTP 文件路径接口 |
| 文档状态/重试/取消 | `/api/document/{id}/processing`、`retry`、`cancel` 与论文信息阶段接口 |
| 原文结构/Markdown/Blocks/图片 | `/api/document/{id}/structure`、`markdown`、`blocks`、页/区域图片接口 |
| 笔记读写 | `/api/document/{id}/reading-note` 及章节、`edits`、导出子路径 |
| Base 与书目 | `/api/document/bases` 及成员、书目子路径 |
| 引用 | `/api/paper/{documentID}/citations`、`/api/references/{referenceID}` 及子路径 |

入库技能使用库、状态、Base 与引用工具；阅读技能使用原文、笔记、图片和证据工具。
批量笔记由阅读技能在宿主端分批加载、按“一篇论文一个写入者”编排，MCP 不提供单独的
批量生成执行器；具体流程见
[批量阅读与笔记生成](../../agent-skills/papers-manager-reading/references/batch-notes.md)。
Base 比较和主题总结目前由阅读技能组合已有读取能力，在宿主端生成独立
HTML 报告，默认输出 `reports/base-summary/`，模板位于
`agent-skills/papers-manager-reading/references/html_template/`。
尚无独立 `base_summary` / `base_compare` 或服务端 LLM 生成工具。
不提供自动目录扫描；本地批次仅导入明确列出的文件，不提供整篇高清图片批量预渲染。

## 8. 版本与验证

机器清单 `schemaVersion` 为 1。添加可选字段/工具可保持兼容；更名或改变语义需要版本规划。
章节元数据和证据链接当前均为 v1。
验证重点包括输入模式、token 行为、响应边界、本地路径约束、revision 冲突、仅缺失时创建、
章节描述解析、证据验证、错误映射，以及不泄露存储路径和凭据。
