---
name: papers-manager-library
description: 管理本地 papersManager 论文库：搜索、导入、检查、修改或删除论文，维护元数据、标签、Base 成员和处理状态。用于入库、整理或书目级操作，不用于阅读注释或 Base 总结；删除和覆盖操作必须先获得用户明确确认。需要 papersManager MCP server；若当前 host 没有这些工具，不要使用本 skill。
---

# PapersManager Library

## 适用范围

用于管理已经或即将进入 papersManager 的论文：

- 搜索和定位论文；
- 通过 URL 导入论文；
- 修正文件名、DOI、标题、作者、摘要、venue、标签；
- 管理 Base 成员；
- 查看处理状态、失败原因和可重试阶段；
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
  `papers_manager_library_list_bases`、`papers_manager_library_get_base`、
  `papers_manager_processing_status`。
- 写入工具：`papers_manager_library_import_url`、
  `papers_manager_library_update_metadata`、`papers_manager_library_delete`、
  `papers_manager_library_add_to_base`、`papers_manager_library_remove_from_base`。
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

### 导入 URL

使用 `papers_manager_library_import_url`：

- 只接受用户提供的 HTTP/HTTPS URL；
- 不接受服务器本地路径或任意文件系统访问；
- 导入成功返回 document ID 和初始处理状态；
- 不要无限等待解析完成；使用 `papers_manager_processing_status` 做有界轮询，或把轮询留给用户界面；
- 失败时报告阶段和可重试入口，不要重复盲目提交。

### 修改元数据

使用 `papers_manager_library_update_metadata`，只修改用户明确要求的字段。

- 不要用模型推断覆盖已有标题、作者、venue、DOI 或标签；
- 修改 DOI 前说明可能影响引用匹配和去重；
- 不把原文摘要复制到笔记或无关字段；
- 修改后返回实际保存的字段和来源。

### Base 成员

使用 `papers_manager_library_list_bases`、`papers_manager_library_get_base`、`papers_manager_library_add_to_base`、`papers_manager_library_remove_from_base`。

- 加入/移除 Base 只改变成员关系，不复制、不删除论文；
- 不要为了整理 Base 而删除源文档；
- 大批量成员变更前给出清单并确认。

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

失败时只建议用户可用的重试/取消动作，不要声称已经自动修复。

## 错误

- `NOT_FOUND`：对象不存在或已被删除；重新搜索。
- `DUPLICATE_DOI` / `DUPLICATE_FILE`：说明重复对象，询问用户是打开已有论文还是取消。
- `INVALID_ARGUMENT`：缺少或非法字段；不要自动猜测。
- `UNAVAILABLE`：处理或服务暂时不可用；稍后重试。
- `CONFLICT`：对象状态已变化；重新读取后再操作。

## 工具

工具名称和输入 schema 以 MCP server 的 `tools/list` 为准；本 skill 只描述意图和安全边界。快速索引见 [references/tools.md](references/tools.md)。

## 不要做

- 不要修改阅读笔记；
- 不要生成论文总结；
- 不要暴露 API key、存储路径、数据库连接；
- 不要删除未经明确确认的论文；
- 不要用搜索结果数量推断全库内容。
