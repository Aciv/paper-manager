---
name: papers-manager-reading
description: 在本地 papersManager 论文库中分层阅读、总结和注释论文。用于理解论文、决定读哪些 section、创建或更新 section 笔记、把笔记映射到论文 source outline、插入或验证 page/Block 证据，以及基于已有笔记总结 Base。默认从元数据、source outline 和 section 描述开始，只读选中的 section；所有笔记写入都必须带 expected revision。需要 papersManager MCP server；MCP 不可用时只报告不可用，不要用 scripts、curl 或其他 HTTP 客户端兜底。
---

# PapersManager Reading

## 适用范围

用于单篇论文的阅读、总结、注释，以及基于已有笔记的 Base 总结：

- 快速判断一篇论文是否相关；
- 决定读哪些论文 section；
- 生成或更新阅读笔记；
- 把笔记 section 映射到论文的 source outline；
- 添加、验证和定位证据；
- 比较多篇论文的笔记和结构。

不用于论文入库、元数据修正、Base 成员维护或删除。这些使用 `papers-manager-library` skill。

## 运行前置

- 本 skill 依赖 papersManager MCP server。工具名统一使用 `papers_manager_`
  前缀；Host 可能再包一层命名空间，按前缀或 server 名称识别，不要依赖某个固定外层包装。
- 读取/验证工具：`papers_manager_library_get` / `papers_manager_library_search`、
  `papers_manager_document_structure`、`papers_manager_document_read_section`、
  `papers_manager_document_read_markdown`、`papers_manager_document_read_blocks`、
  `papers_manager_document_read_block_image`、
  `papers_manager_document_read_page_image`、
  `papers_manager_reading_note_outline`、`papers_manager_reading_note_read_section`、
  `papers_manager_reading_note_export`、`papers_manager_evidence_validate`。
  `evidence_validate` 只解析和校验链接，不写笔记。
- 写入工具：`papers_manager_reading_note_create`、
  `papers_manager_reading_note_apply_edits`、
  `papers_manager_reading_note_replace`、
  `papers_manager_reading_note_replace_section`、
  `papers_manager_reading_note_delete`。
- 整篇替换/删除只在用户明确要求时使用；默认增量编辑使用 `apply_edits`。
- 缺少读取工具、MCP 无法连接，或工具返回“未知工具/未实现”时：停止并明确报告
  papersManager MCP 不可用。
- 只有读取工具可用时，可以继续只读分析，但不得声称已经创建或修改笔记。
- 缺少写入工具时停止写操作；不要用 shell、curl、scripts 或其他 HTTP 客户端兜底，
  不要猜测工具名，不要伪造成功结果。
- `UNAVAILABLE` 可以有限重试；未知工具或未配置应直接停止。

## 阅读分级

默认按 L0 → L4 逐级升级，不要一上来读整篇论文：

| 层级 | 读取内容 | 工具 | 用途 |
| --- | --- | --- | --- |
| L0 | 标题、作者、摘要、venue、DOI、tags | `papers_manager_library_search` / `papers_manager_library_get` | 判断相关性 |
| L1 | source outline、note directory、section description | `papers_manager_document_structure`、`papers_manager_reading_note_outline` | 规划读哪些部分 |
| L2 | 选中的 source/note section Markdown | `papers_manager_document_read_section`、`papers_manager_reading_note_read_section` | 精读、分析、写笔记 |
| L3 | 具体 page、Block、rect、表格/图片 | `papers_manager_document_read_blocks`、`papers_manager_document_read_block_image`、`papers_manager_document_read_page_image`、`papers_manager_evidence_validate` | 验证主张、查看整页/区域、图表、生成证据 |
| L4 | 整篇 PDF 或整篇 note export | `papers_manager_reading_note_export` / 用户界面 | 用户明确要求或前面层级不足 |

详细选择策略见 [references/reading-levels.md](references/reading-levels.md)。

核心规则：

- 先用 source outline 了解论文结构，再用 note directory 了解已有分析；
- note section 标题不是论文 section，不要把两者合并；
- 只读候选 section，不遍历全文；
- 只有需要验证具体主张时才进入 L3；
- Base 总结先用 L0/L1 扫描成员，再读候选的 L2，最后对关键主张读 L3；
- 遇到预算或响应大小限制时，缩小 section 范围，不要静默截断证据。

## 读取流程

1. 解析 document；有多个候选时先让用户确认。
2. 读取 source outline，记录来源是 `pdf_outline` 还是 `markdown_headings`。
3. 读取 note directory；如果没有笔记，不要自动创建。
4. 用 section description 和 source outline 选择目标 section。
5. 只读取目标 section；需要证据时再读取对应 page/Blocks。
6. 记录后续可能使用的 page、Block UUID、source section 和 note section ID。

## 注释生成

默认使用“分析优先”结构，而不是复制论文目录。完整笔记的结构、内容和排版约束见
[references/notes-standard.md](references/notes-standard.md)，默认模板是：

```markdown
# 论文概览
# 问题与动机
# 方法与关键设计
# 实验设置与结果
# 局限与风险
# 开放问题
# 证据缺口
```

规则：

- 除非用户明确要求“简短”，完整阅读笔记按 standard 或 deep 模式覆盖问题、贡献、方法、设置、结果、局限与开放问题；按论文实际情况合并或拆分 section。
- 每个 section 要写具体机制、设置、数字、比较或限制；不要把摘要改写成几句话，也不要只给泛泛结论。
- 已有笔记沿用原结构，不重排、不重建；但用户要求“扩展/完整分析”时，应新增相关 section 并丰富现有 section，不能以“保持结构”为由拒绝。
- 新建笔记只在用户明确要求时进行；写入使用 latest `expected_revision`。
- 笔记 section 必须是 H1-H3；正文中不能新造 H1-H3 标题。
- 新增 section 使用 `addSection`；修改正文使用语义 edits，不改标题行。
- 可选“逐节模式”：创建一个 `# 逐节笔记`，下面用 H2 对应论文主要 section；只用于用户明确要求或长论文，不要默认复制全部目录。
- 每个 note section 可选一个 `description`，放在 section-meta 中，写一句导航信息；不要把正文复制成 description。
- 对关键事实、数字和比较尽量附证据链接；无法定位时明确写证据缺口。
- 当前阅读视图不支持 pipe table、raw HTML 或内联 LaTeX；生成内容应使用短段落、bullet、`**标签**：` 和证据链接，不要生成不可渲染的格式。

完整规范见 [references/note-generation.md](references/note-generation.md)。

## 公式来源

当前不从 PDF 文本层猜测或重写 LaTeX。公式的来源是 **Block 或 PDF page/rectangle 索引**：

- 优先使用 `document_read_blocks` 返回的 Block UUID、page 和 bbox；需要查看图表/表格时可对 image/table Block 调用 `document_read_block_image`；
- 没有公式 Block 时，用 page + normalized rect 定位公式区域；
- 在笔记中写一个可点击的公式引用，而不是复制乱码公式文本或编造 LaTeX：

```markdown
- [公式 (1) — p.4](papers-manager://self/evidence?v=1&page=4&rect=0.12,0.35,0.86,0.48)
  缩放点积注意力；变量和设计含义写在说明中。
```

- 重要公式可给多行说明，但公式本体留在原文，用证据链接回查；
- 如果无法定位公式区域，写明确的 `> 公式缺口：...`，不要伪造页码或 LaTeX；
- 如果未来 source 明确提供经过验证的 LaTeX，可以额外使用 `$...$` / `$$...$$`，但不要把 OCR/文本猜测当作 LaTeX。

完整约定见 [references/formula-source.md](references/formula-source.md)。

## 原文参考放哪里

分三层：

1. **具体主张旁边**：`papers-manager://self/evidence?v=1&page=...` 证据链接；page 必须有，rect/Block/section 可选。
2. **section description**：一句 source mapping，例如“对应原文 §3.2，pp.4–6”；这是导航信息，不是证据本身。
3. **笔记顶部/总结**：引用论文标题/DOI 一次即可；不要复制 bibliography 或整段原文。

重要结论没有可靠证据时，明确写：

```markdown
> 证据缺口：当前笔记没有可定位到原文的页码或 Block。
```

完整规则见 [references/evidence.md](references/evidence.md)。

## Base 总结

Base 总结按 L0/L1 → L2 → L3 执行：

- 先列出 Base 成员，读取元数据和 note outline/description；
- 用标题、摘要、已有笔记选择候选论文；
- 只读候选论文的笔记或选中 section；
- 对比较中的关键主张读取准确 page/Block 并附证据；
- 明确报告缺失笔记、缺失 source outline、证据缺口和被跳过的论文。

当前没有稳定的 `base_summary` 工具时，使用现有读取工具完成上述流程；不要假装存在批量总结接口。设计见 [references/base-summary.md](references/base-summary.md)。

## 写入与冲突

- 所有 note 写入都使用 `expected_revision`。
- 409/`REVISION_CONFLICT`：重新读取 manifest 和 section，再重新规划；不要用旧 revision 重试覆盖。
- 503/`UNAVAILABLE`：整篇导入或发布中；稍后重试，不要静默重写。
- 404/`NOTE_NOT_FOUND`：笔记不存在；只有用户要求时才创建。
- 413/`PAYLOAD_TOO_LARGE`：缩小 section 或改用文件导入；不要截断证据链接。
- 写入成功后检查返回的 revision、changed sections 和 deleted section IDs。

## 工具

MCP 工具参数统一使用 snake_case：`document_id`、`section_id`、`expected_revision`；不要传入 HTTP API 的 camelCase 字段名。

工具名称和输入 schema 以 MCP server 的 `tools/list` 为准。阅读相关索引见 [references/tools.md](references/tools.md)。

建议调用顺序：

```text
papers_manager_library_get
papers_manager_document_structure
papers_manager_reading_note_outline
papers_manager_document_read_section
papers_manager_reading_note_read_section
papers_manager_document_read_blocks
papers_manager_document_read_block_image   # only for image/table Blocks
papers_manager_document_read_page_image    # full page or normalized bbox
papers_manager_reading_note_export          # only when a full-note view is needed
papers_manager_evidence_validate
papers_manager_reading_note_apply_edits
```

不要猜测不存在的工具；MCP 不可用时停止写入，不要手写 HTTP 请求绕过 revision 检查。

## 反模式

- 把整篇论文或整份 note 一次性塞进上下文；
- 把 source outline 复制成 note 目录；
- 在笔记里复制大段原文；
- 没有 expected_revision 就写入；
- 409 后盲目重试；
- 伪造页码、Block、source section 或证据链接；
- 把 section description 当成证据；
- 创建用户没有要求的笔记；
- 为了比较 Base 而读取所有 PDF。
