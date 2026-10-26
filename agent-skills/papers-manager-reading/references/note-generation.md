# 笔记写入流程

内容和模板统一见 [notes-standard.md](notes-standard.md)，证据统一见 [evidence.md](evidence.md)。本文件只规定发布、增量编辑和 metadata。

多篇论文的批次划分、加载预算、单篇写入者分配和子 agent 提示词见
[batch-notes.md](batch-notes.md)。批量任务仍逐篇遵守本文件的 revision 和编辑规则。

## 先阅读，再发布

1. 读取现有笔记目录、revision 和 source outline，确定创建还是增量更新。
2. 阅读所需来源，整理主张、成立条件和证据，形成可独立阅读的内容。
3. 生成章节 description，检查证据和实际覆盖范围。
4. 不存在笔记时，用 reading_note_create 和 expected_revision=0 创建。用户的生成/保存请求已构成授权，没有写入请求则只回答。
5. 已有笔记先读取受影响章节，再携带已读取快照的 revision 写入。
6. 检查结果的 revision、changed/deleted sections，按需回读确认最终内容。

不要先创建空模板再开始阅读。长任务可逐节发布，但每次必须保存有内容、有依据的完整章节，并说明尚未覆盖部分。中途失败只报告已保存内容。
目标更新保留无关内容，完整扩展可新增缺失分析，整篇替换需明确授权。

## 批量生成的写入边界

- 一篇论文的笔记在同一时间只能有一个写入者。主 agent 分配任务，子 agent 只返回该篇的
  revision、覆盖和缺口，不并发修改同一笔记，也不创建其他论文的笔记。
- 已有笔记默认不改动；用户明确要求补充/更新时只做增量更新。用户只要求“建笔记”时，
  已有笔记跳过并报告，不静默覆盖。
- 每篇都用实际读取到的 revision 写入；`REVISION_CONFLICT` 后重读再规划，不重放旧请求。
- 批次结果逐篇报告；单篇失败不冒充成功，也不取消其他论文。具体流程见
  [batch-notes.md](batch-notes.md)。

## Section 与编辑

每节为一个 H1–H3 标题及正文，正文编辑不能新增 H1–H3 标题。
结构新增使用 addSection，整节替换使用 replaceSection，正文编辑使用 replaceRange / replaceText / insertAt / appendText。

整篇首次创建可提交带 H1/H2/H3 的完整 Markdown，由服务端拆分目录和章节文件。
增量扩充方法/结果时，每个 H2/H3 都用 addSection 创建真实子节，并按 schema
提供 parent_id 和必要的 after_id；不要把多个子标题塞进父节正文或 replaceSection。
先读取目录确定父节与顺序，创建子节后以工具返回的 ID 为准。
父节仅保留总览，已有细节若移动到子节，应在同批允许的操作中或子节成功保存后
清理对应重复段落；失败时保留原内容，不能先删除尚未成功迁移的信息。

replaceText 使用 old_text、new_text；其余上述写内容操作使用 content。
参数以 MCP schema 为准，不用 HTTP camelCase 参数。
409/REVISION_CONFLICT 时重读受影响内容并重新规划，不重放旧操作。

## Section metadata

标题之后、正文之前可含以下块，允许空行：

```markdown
# 方法如何工作

<!-- papers-manager:section-meta v=1
{"description":"解释倒残差与线性瓶颈的结构和信息保留机制；对应原文 §3。"}
-->

正文……
```

- v1 只有 description，单段文本，不超过 512 Unicode code points。
- JSON 中的 --> 必须转义；metadata 属于章节 Markdown，存储层不解析。
- description 根据已读来源和已写正文生成，不含正文副本或证据列表。
- 修改正文后同步检查，避免目录描述与正文冲突。
- MCP/前端解析派生目录；描述未加载或缺失不代表章节无相关内容。
