---
name: papers-manager-library
description: 管理本地 papersManager 论文库：搜索、通过 URL 或受控本地文件导入、检查、修改或删除论文，维护元数据、标签、Base 生命周期与成员、引用与书目以及处理状态。用于入库、整理或书目级操作，不用于阅读注释或 Base 总结；删除和覆盖操作必须先获得用户明确确认。需要 papersManager MCP server；若当前 host 没有这些工具，不要使用本 skill。
---

# PapersManager Library

## 适用范围

用于管理已经或即将进入 papersManager 的论文：

- 搜索和定位论文；
- 通过 URL 导入论文，或在用户明确给出本地路径且 MCP 已配置 `MCP_LOCAL_IMPORT_ROOT` 时导入本地文件；
- 修正文件名、DOI、标题、作者、摘要、venue、标签、出版信息；
- 创建/改名/删除 Base，管理 Base 成员；
- 生成 Base 书目；
- 查询和编辑引用、重新匹配或下载引用 PDF；
- 查看处理状态、失败原因和可重试阶段，并按用户要求重试/取消；
- 删除论文。

不用于：

- 阅读、总结或注释论文；
- 生成或修改阅读笔记；
- 解析证据链接；
- LaTeX 编辑、OCR 或通用 PDF 转换。

阅读和注释使用 `papers-manager-reading` skill。

## 运行前置

- 本 skill 依赖 papersManager MCP server。工具名统一使用 `papers_manager_`
  前缀；Host 可能再包一层命名空间，按前缀或 server 名称识别，不要依赖某个固定外层包装。
- 读取工具：`papers_manager_library_search`、`papers_manager_library_get`、
  `papers_manager_library_get_many`、`papers_manager_processing_status`、
  `papers_manager_processing_status_many`、`papers_manager_base_list`、
  `papers_manager_base_get`、`papers_manager_base_list_documents`、
  `papers_manager_citation_list`、`papers_manager_citation_list_cited_by`、
  `papers_manager_base_bibliography_status`。
- 写入工具：`papers_manager_library_import_url`、
  `papers_manager_library_import_urls`、`papers_manager_library_import_file`、
  `papers_manager_library_import_files`、`papers_manager_library_update_metadata`、
  `papers_manager_library_delete`、`papers_manager_processing_retry`、
  `papers_manager_processing_cancel`、`papers_manager_base_create`、
  `papers_manager_base_update`、`papers_manager_base_delete`、
  `papers_manager_base_add_document`、`papers_manager_base_remove_document`、
  `papers_manager_base_bibliography_generate`、`papers_manager_citation_add`、
  `papers_manager_citation_replace`、`papers_manager_citation_remove`、
  `papers_manager_citation_update_reference`、`papers_manager_citation_resolve`、
  `papers_manager_citation_rematch`、`papers_manager_citation_download_pdf`。
- `papers_manager_library_import_file` / `import_files` 只有在服务端配置了
  `MCP_LOCAL_IMPORT_ROOT` 时可用；返回 `FORBIDDEN` 时让用户在网页上传，不要尝试
  绕过路径限制。
- 批次导入工具每次最多 20 项，单次调用内按顺序处理；超过 20 项时拆成多次调用，
  不要依赖并发调用绕开单项限制。
- 缺少读取工具、MCP 无法连接，或工具返回“未知工具/未实现”时：停止并明确报告
  papersManager MCP 不可用。
- 只有读取工具可用时，可以继续只读查询，但不得声称已经导入、修改或删除。
- 缺少写入工具时停止写操作；不要用 shell、curl、scripts 或其他 HTTP 客户端兜底，
  不要猜测工具名，不要伪造成功结果。
- `UNAVAILABLE` 可以有限重试；未知工具或未配置应直接停止。

## 先确认对象

1. 用户给的是标题、DOI 还是 document ID，先搜索确认。
2. 有多个同名结果时，展示标题/作者/年份/DOI，让用户选择；不要猜第一个。
3. 修改或删除前重新读取一次目标对象，确认它没有在处理中或被替换。

## 常用操作

### 搜索

使用 `papers_manager_library_search`，优先用 DOI 或精确标题。不要为了“看看有没有”而无界翻页；需要更多结果时明确说明当前页和总数。

### 批量读取状态与元数据

已经有一组 document ID（搜索结果、批次导入的 `items`、用户给出的列表）时，不要逐个调用单项工具：

- 需要标题、作者、venue、source、标签等论文源信息：
  `papers_manager_library_get_many`（每次 1–200 个 ID）；
- 只需要处理阶段和引用进度：`papers_manager_processing_status_many`（每次 1–200 个 ID）；
- 两者都会去重、按请求顺序返回，并把已不存在的 ID 放在 `missingDocumentIds`；
  不要因为个别 ID 缺失而重试整批；
- 超过 200 个 ID 时按页拆分（例如每页 100 个），不要无界并发批次；
- 元数据缺失是允许的，不要从正文推断或用 `update_metadata` 补猜。

### 导入 URL

使用 `papers_manager_library_import_url`：

- 只接受用户提供的 HTTP/HTTPS URL；
- 不接受服务器本地路径或任意文件系统访问；
- 如果用户要求导入到某个 Base，传入可选的 `base_id`，不要先导入再单独调用成员工具；批次导入时 `base_id` 会应用到每个 accepted/duplicate 项；
- 用户一次给出多个 URL 时，优先使用 `papers_manager_library_import_urls`：每次 1–20 个 URL，可选 `dois` 必须与 URL 一一对齐或整体省略；超过 20 个拆成多次调用；
- 批次调用内按顺序处理，受 `BATCH_IMPORT_TIMEOUT`（默认 2 分钟）约束；超时会返回已完成项、`stopped=true` 和未处理项的失败状态；
- 批次结果必须逐项检查 `status`（accepted/duplicate/rejected/failed）和 `error`，不能只看整体成功；
- 导入成功返回 document ID 和初始处理状态；
- 导入工具（单项与批次）共享并发限制（默认最多同时 20 个调用）；遇到 `RESOURCE_EXHAUSTED` 时等待后重试，不要并发堆积；
- 不要无限等待解析完成；使用 `papers_manager_processing_status` 做有界轮询，或把轮询留给用户界面；
- 失败时报告阶段和可重试入口，不要重复盲目提交。

### 导入本地文件

只有在用户明确给出路径、且 `papers_manager_library_import_file` 可用时使用：

- 路径必须位于服务端 `MCP_LOCAL_IMPORT_ROOT` 内；
- 不要猜测路径、扫描目录或尝试导入目录；
- 用户一次给出多个本地路径时使用 `papers_manager_library_import_files`（每次 1–20 个）；工具会先整体校验所有路径，任一越界/不支持/超限会整批拒绝且不导入任何文件；
- `FORBIDDEN` 表示本地导入被禁用或路径越界；让用户改网页上传，不要用 shell 读取文件；
- 与 URL 导入共用并发上限（默认 20）；遇到 `RESOURCE_EXHAUSTED` 时等待后重试；
- 如果用户要求导入到某个 Base，传入可选的 `base_id`，批次中会应用到每个 accepted/duplicate 项；
- 导入成功后同样用 `papers_manager_processing_status` 做有界轮询。

### 修改元数据

使用 `papers_manager_library_update_metadata`，只修改用户明确要求的字段。

- 不要用模型推断覆盖已有标题、作者、venue、DOI 或标签；
- 修改 DOI 前说明可能影响引用匹配和去重；
- 不把原文摘要复制到笔记或无关字段；
- 修改后返回实际保存的字段和来源。

### Base 生命周期与成员

使用 `papers_manager_base_*` 独立集合：

- 读取：`papers_manager_base_list`、`papers_manager_base_get`、`papers_manager_base_list_documents`；
- 创建/改名：`papers_manager_base_create`、`papers_manager_base_update`；
- 删除：`papers_manager_base_delete`，必须要求用户确认并传 `confirm=true`；
- 成员：`papers_manager_base_add_document`、`papers_manager_base_remove_document`；
- 书目：`papers_manager_base_bibliography_generate` 入队后，用
  `papers_manager_base_bibliography_status` 有界轮询；不要声称同步生成完成。

- 加入/移除 Base 只改变成员关系，不复制、不删除论文；
- 删除 Base 只删除集合与成员关系，不删除源文档；
- 不要为了整理 Base 而删除源文档；
- 大批量成员变更前给出清单并确认。

### 引用与书目

使用 `papers_manager_citation_*` 独立集合，操作对象是某篇论文的引用行：

- 查询：`papers_manager_citation_list`、`papers_manager_citation_list_cited_by`；
- 新增/整体替换/删除：`citation_add`、`citation_replace`、`citation_remove`；
- 修正字段：`citation_update_reference`；
- 关联/本地重匹配：`citation_resolve`、`citation_rematch`；
- 获取 PDF：`citation_download_pdf`。

- `citation_replace` 和 `citation_remove` 会改变书目，必须要求用户明确确认并传
  `confirm=true`；
- 不要在没有用户要求时自动重解析全部引用或下载 PDF；
- `citation_rematch` 只查本地库，不调用外部 provider。

### 删除

使用 `papers_manager_library_delete`，必须：

1. 明确说明将删除的论文（标题、ID、DOI）；
2. 说明会同时影响其阅读笔记、引用关系和 Base 成员；
3. 要求用户明确确认，并传入 `confirm=true`；
4. 不提供批量静默删除。

## 处理状态

使用 `papers_manager_processing_status` 读取阶段状态。常见阶段：

- `markdown`
- `metadata_extraction`
- `source_lookup`
- `reference_lookup`

失败时可以使用 `papers_manager_processing_retry` 重试对应 lane，或用
`papers_manager_processing_cancel` 取消；只在用户要求或明确同意时重试，不要无限
轮询/自动反复重试，也不要声称已经自动修复。`auto` 交给文档服务选择，其余
target 只影响一个阶段。

## 错误

- `NOT_FOUND`：对象不存在或已被删除；重新搜索。
- `CONFLICT`：重复 DOI/文件、正在处理或对象状态已变化；重新读取后再操作。重复对象要询问用户是打开已有论文还是取消。
- `INVALID_ARGUMENT`：缺少或非法字段；不要自动猜测。
- `FORBIDDEN`：本地导入被禁用或路径越界；改用网页上传，不要绕过限制。
- `UNAVAILABLE`：处理或服务暂时不可用；稍后重试。

## 工具

工具名称和输入 schema 以 MCP server 的 `tools/list` 为准；本 skill 只描述意图和安全边界。快速索引见 [references/tools.md](references/tools.md)。

## 不要做

- 不要修改阅读笔记；
- 不要生成论文总结；
- 不要暴露 API key、存储路径、数据库连接；
- 不要删除未经明确确认的论文；
- 不要用搜索结果数量推断全库内容。
