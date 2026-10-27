---
name: papers-manager-reading
description: 通过 papersManager MCP 阅读库内论文、回答原文问题、生成或增量更新阅读笔记，以及比较或总结 Base 内论文。用于单篇速读、证据定位、跨论文分析、批量笔记及导入后逐篇就绪的笔记流水线；入库、元数据和集合管理使用 papers-manager-library。需要运行中的 papersManager MCP server。
---

# PapersManager Reading

让用户快速理解论文，并让保存的理解可用于后续 Base 比较或主题总结。source outline 描述原文结构，note directory 描述已有分析，两者不合并。

## 按任务选择流程

- **回答问题 / 快速了解**：定位论文，读已有笔记或相关原文，核验影响答案的结果；回答后停止，默认不创建笔记。
- **生成单篇阅读笔记**：读取已有目录和原文结构，覆盖问题 → 方法 → 支持证据 → 成立边界。概览简短，方法和结果重点展开，用可独立索引的 H2/H3 子节组织具体机制、实验设置、主结果和消融，不能仅用粗体标签代替章节。允许读取全部必要章节；完整性由论证覆盖决定，不由少读或章节数决定。先读 [笔记标准](references/notes-standard.md)：其中分别解释 `brief` / `standard` / `deep` 的内容、规模和完成标准；默认 `standard`，`deep` 需要用户明确要求。写入规则见 [写入流程](references/note-generation.md)。
- **批量生成阅读笔记**：用户要求为多篇论文或整个 Base 建/更新笔记时，先读 [批量阅读与笔记生成](references/batch-notes.md)。建立进度清单，默认最多 8 个活跃论文 worker、上限 16，服从 host 更低限制，完成一篇补一篇；一篇只交给一个能访问同一 MCP 的 worker，由它直接保存 notes 并回传实际 revision 和精简结果。没有子 agent 能力则顺序执行，不把 Markdown 全文回传主 agent 再写一次。
- **导入并生成笔记**：入库步骤使用 `papers-manager-library`；主 agent 持续发现已入库 ID，批量查处理状态。每篇 `markdown` 成功即可派 worker，不等整批导入、所有论文解析或元数据检索结束；具体流水线与轮询预算见 [批量流程](references/batch-notes.md)。
- **补充已有笔记**：读取目标章节及相关原文，保留无关内容和既有结构。用户要求扩展时可新增章节；先读 [写入流程](references/note-generation.md)。
- **Base 比较 / 总结**：先判断是比较型（围绕用户给出的维度对照多篇）还是主题型（归纳方法版图、共识、分歧、证据与空白）；先筛选论文、阅读笔记、回原文核验。按 [Base 总结与比较](references/base-summary.md) 直接生成 Markdown 报告，不询问视觉风格、不读取 HTML 模板，不保存为单篇笔记。先调用 `papers_manager_application_info` 获取应用 Web 地址，不向用户询问端口；地址未配置时保留论文 UUID 和来源位置，不阻塞分析。

用户明确要求生成/保存笔记即已授权，不重复确认。方法问题不必扩成整篇分析；完整阅读请求不能只改写摘要。

## 取数与证据

按任务需要选择元数据、目录、章节、Blocks 或页图，不强制依次经过 L0–L4。具体升级条件见 [读取策略](references/reading-levels.md)。

- Base 多篇任务先用 `papers_manager_base_reading_outlines` 分页取得原文/notes 目录，检查 status/summary 和逐项 state；不逐篇预检或回读整份笔记。要做实际修改的 worker 仍按需读本篇最新内容。
- 多篇任务按需批量补充元数据/处理状态（单次 1–200 个 ID）；已有 Base 批量目录返回的字段不重复查询。主上下文只保留批次清单、阶段结果和最终结论；每篇正文由负责该篇的 worker 按需读取并直接写入工具。流水线、并发和委派规则见 [批量阅读与笔记生成](references/batch-notes.md)。
- 有 document ID 时直接读取；候选确实有歧义才询问用户。
- 元数据与原文身份冲突时，以实际阅读的原文为分析依据并报告差异，不擅自修库。
- 已有笔记是可修订分析，不是原文证据。关键数字、比较、图表和公式需回查来源。
- 创建链接前阅读 [证据规则](references/evidence.md)；公式遵循 [公式来源](references/formula-source.md)。链接可定位不等于主张被证明。
- 描述缺失或预算耗尽不等于章节没有相关内容；检查工具的完整性标记和诊断。

## MCP 与写入边界

- 使用当前 host 的 papers_manager_ MCP 工具，外层命名空间可能不同。参数及工具能力以 tools/list 为准，[工具索引](references/tools.md) 仅用于导航。
- 仅检查当前任务所需能力。缺少写工具仍可只读；缺少相关读取能力则报告具体阻塞。MCP 不可用时不得用 curl、shell 或其他 HTTP 客户端绕过；本地 Read 可读取技能文档。
- 所有笔记写入携带 expected_revision；整篇替换和删除需明确授权。默认增量编辑，正文不插入新的 H1–H3 标题，结构变化使用 section 操作。
- 主任务与全部子任务合计默认最多 8 个活跃论文 worker，最多 16 个，不按批次各开一组；服务端另按普通读 128、普通写 32、重型读取 4、本地导入 2 独立计量在途调用，不是 Agent 数量上限。`RESOURCE_EXHAUSTED` 时降低并发并有限退避，不开新会话绕过。
- 同一篇笔记在同一时间只能有一个写入者。批量任务由主 agent 分配并汇总；子 agent 只处理分到的一篇论文，不得并发修改同一笔记，也不得再派生下级 agent。
- REVISION_CONFLICT：重读目录及受影响章节，重新规划，不重放旧 revision。UNAVAILABLE：有限重试后报告。PAYLOAD_TOO_LARGE：缩小读写单元，不静默截断证据。
- 先形成有内容、有依据的可读版本，再发布；报告实际成功的 revision、章节和未覆盖范围。

调用示例见 [examples.md](references/examples.md)，按需读取。
首次生成 standard/deep 笔记，或用户要求增强方法/结果时，读取
[精修实例](references/worked-example.md)，学习展开深度与 H2/H3 索引方式；
不要复用示例的事实、外链或证据占位符作为目标论文证据。
