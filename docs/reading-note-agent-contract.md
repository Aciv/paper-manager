# 阅读注释的通用读写契约

阅读注释不区分人类用户和 Agent。后端只暴露普通的资源接口；人类界面、Agent 宿主、MCP adapter 和 skill 都是同一组接口的消费者，不需要 `agent_profiles`、`agent_run_id` 或其他 Agent 专属记录。

## 读取

| 操作 | 接口 | 说明 |
| --- | --- | --- |
| 读取结构 | `GET /api/document/{id}/reading-note` | 返回 note `revision` 和 H1-H3 section 目录；不返回 section 正文，也不解析 section 内的元数据。 |
| 读取一个 section | `GET /api/document/{id}/reading-note/sections/{sectionId}/content` | 返回原始 `text/markdown`，包含标题、正文、metadata 和证据链接。 |
| 读取完整笔记 | `GET /api/document/{id}/reading-note/export` | 显式拼接全部 section；仅供导出或确实需要整篇内容的消费者。 |
| 读取论文结构 | `GET /api/document/{id}/structure` | 返回持久化 source outline，不包含 Blocks。 |
| 读取页面/Blocks | `GET /api/document/{id}/blocks?page=N` | 定位证据时使用页面或 Block 数据。 |

section 目录只包含 `id`、`parentId`、`order`、`level`、`title`、`contentRevision`、`sizeBytes` 等结构字段。**section description 不进入目录接口**；它由消费者从 section Markdown 中解析。

## 写入

- 整篇创建/替换：`PUT /api/document/{id}/reading-note?expectedRevision=N` 或 JSON body 中的 `expectedRevision`；创建时使用 `0`。
- 单 section 替换：`PUT /api/document/{id}/reading-note/sections/{sectionId}?expectedRevision=N`。
- 原语编辑：`POST /api/document/{id}/reading-note/operations`，使用 `insertText`、`deleteText`、`addSection`、`deleteSection`。
- Agent 语义编辑：`POST /api/document/{id}/reading-note/edits`，按 `expectedRevision` 快照执行 `replaceText`、`replaceRange`、`insertAt`、`appendText`、`replaceSection`、`addSection`、`deleteSection`。

所有写入都受 note revision 乐观锁保护。`409` 表示 revision 已变化，消费者必须重新读取，不能用旧内容重试覆盖。整篇替换期间读取可能临时返回 `503`。

## section metadata

section Markdown 可以在标题行之后包含一个可选的、版本化的 HTML 注释块：

```markdown
# 方法

<!-- papers-manager:section-meta v=1
{"description":"本节说明模型结构和训练目标。"}
-->

正文……
```

规则：

- 必须位于 section 开头：标题行之后、正文之前；允许标题和 metadata 之间有空行。
- `v` 是 metadata schema 版本，当前只支持 `1`。
- 注释块内是一个 JSON 对象；目前定义的字段只有 `description`。
- `description` 是单段 UTF-8 文本，最多 512 个 Unicode code point。JSON 中不能包含未转义的 `-->`。
- metadata 块是 section 正文的一部分，因此会随 section 文件、单 section 编辑、整篇导出和 `.md` 上传一起移动。
- 没有 metadata、marker 格式错误或版本未知时，section 其余内容仍然有效；消费者应把它当作“没有 description”，并忽略或报告诊断，不要删除正文。
- Go 后端把该块当作普通 Markdown 保存，**不解析、不索引、不投影**。

前端实现位于 `web/src/noteSectionMeta.ts`，阅读预览会先剥离 metadata 再渲染；Agent/MCP/skill 适配器应遵循同一格式自行解析。

### 生成带 description 的目录

由于后端不解析 Markdown，带 description 的目录是消费者的派生结果：

1. 读取 `GET /reading-note` 得到 section 目录和 revision。
2. 按需读取 section 原始 Markdown。
3. 解析 section-meta，组合出类似下面的只读投影：

```json
{
  "documentId": "…",
  "revision": 12,
  "sections": [
    {
      "id": "…",
      "parentId": null,
      "order": 0,
      "level": 1,
      "title": "方法",
      "description": "本节说明模型结构和训练目标。",
      "contentRevision": 3
    }
  ]
}
```

这不是新的持久化结构，不应写回数据库。Agent 可以先只读取需要的 section；如果确实需要一次生成全部 description，应按 section 数量、总量和超时做上限控制。

## 证据链接

`papers-manager://` 证据链接是普通 Markdown 内容，由前端、Agent、MCP 或 skill 自行解析；后端不解析、不索引、不持久化。生成或写入链接时遵循 `web/src/evidenceLinks.ts` 的 `v=1` 格式，至少包含有效 `page`；只有存在矩形或 Block hint 时才绘制 PDF 高亮。

## 后续适配层

3A-2 的 MCP/skill 只增加适配层，不改变上面的存储和 HTTP 契约：

- MCP tool 可以读取 section Markdown、解析 section-meta，并返回派生的 outline。
- MCP write tool 使用已有的 `expectedRevision` 语义编辑接口。
- skill bundle 记录 section-meta 写法、证据链接规则和读写流程。
- Agent/LLM 偏好放在 skill 或请求参数中；不落成笔记字段或后端 Agent 实体。
