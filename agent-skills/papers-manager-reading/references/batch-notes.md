# 批量阅读与阅读笔记生成

一次处理 2 篇以上论文时使用本流程。单篇内容标准见
[notes-standard.md](notes-standard.md)，写入与 revision 规则见
[note-generation.md](note-generation.md)，证据规则见 [evidence.md](evidence.md)，
读取层级见 [reading-levels.md](reading-levels.md)。

批量流程只解决三件事：**有界加载**、**一篇论文一个写入者**、**可核验的批次汇总**。
它不改变单篇笔记的质量标准，也不授权整篇替换或删除已有笔记。

## 1. 先判断任务类型

- 用户要求为多篇论文或整个 Base **建/更新阅读笔记**：使用本流程。
- 用户只是提问、比较或总结 Base：使用 [base-summary.md](base-summary.md)；
  默认不创建笔记，除非用户明确要求保存。
- 只有一篇论文：直接走单篇笔记流程，不需要批次和子 agent。
- 用户说“给这些论文都建笔记”但没说深度：使用 `standard`。只有用户明确说
  “快速看看”才用 `brief`，明确说“完整分析/复现/审阅”才用 `deep`。
- 已有笔记默认**不改动**。用户明确要求“补充/更新/扩展”时才增量更新；整篇替换
  和删除仍需要单独、明确的授权。
- 如果用户没有说明范围（例如“整理一下这个 Base”），先确认是所有成员还是候选子集。
  成员超过 20 篇或要求全部 `deep` 时，先报告成员数和预计波次再开始；用户已明确说
  “全部”则按波次执行，不要静默截断。

## 2. 建立批次清单

主 agent 先建立清单，不要先读正文：

1. 从 Base 取成员：`papers_manager_base_list_documents`；用户直接给出 document ID
   时以用户清单为准。
2. 批量读取元数据：`papers_manager_library_get_many`，每次 1–200 个 ID。
3. 批量读取处理状态：`papers_manager_processing_status_many`，每次 1–200 个 ID。
4. 检查 `missingDocumentIds`。缺失的 ID 单独报告，不要重试整批，也不要猜替代论文。
5. 每篇论文记录一条清单：

```text
document_id | 标题 | 作者/年份 | markdown 状态 | 笔记状态（未知可留空） | 计划动作
```

`markdown` 尚未成功时，当前无法可靠读取章节。默认把该篇标记为
`blocked:not_ready` 并继续其他论文，不要为等待它无限轮询；只有用户要求时才调用
重试工具。

计划动作表示主 agent 的**写入意图**，最终结果以 worker 读取到的实际状态为准：

- `create`：用户要求生成；worker 若发现已有笔记且未获更新授权，返回
  `skipped:note_exists`；
- `update`：用户明确要求补充/更新；已有笔记则增量更新，不存在则创建（除非用户明确
  只更新已有笔记）；
- `skip`：主 agent 已明确排除（用户指定范围外、用户要求不动），不派 worker；
- `blocked`：文档缺失、markdown 未就绪或缺少必要读取能力；
- `exclude`：与本次任务范围无关。

主 agent 不需要预先读取每篇笔记正文。笔记是否存在、revision 和章节结构由该篇
worker 在自己的一篇论文范围内读取；这样主上下文只保留清单和结果。worker 的最终
`skipped:note_exists` 是正常结果，不是失败。

## 3. 批次加载规则

### 元数据和状态

- `library_get_many`、`processing_status_many` 是批量读取入口；不要对每个 ID
  逐个调用单项工具。
- 一次最多 200 个 ID；更多时按页拆分。清单可以一次读取 200 个，但实际阅读波次
  仍按下面的 3 篇上限执行。

### 每篇论文的读取

- 先读 source outline 和 note outline（含 revision、章节树、description），再决定
  要读哪些章节。
- `brief` 通常从 2–4 个定向章节开始；`standard` 通常从 4–8 个开始；`deep` 通常
  从 8–12 个开始。这些是**起始预算，不是配额**：论证覆盖不足就继续读，材料不够就
  少读，不要为凑章节数或字数创建空壳。
- 已有笔记覆盖了某节时，先读笔记；只有需要核验数字、图表、公式或笔记不足时再回原文。
- 数字、表格、图、公式、跨论文比较结论和相互矛盾之处，使用
  `papers_manager_document_read_blocks` 或
  `papers_manager_document_read_page_image` 核验，并用
  `papers_manager_evidence_validate` 检查证据目标。
- 默认不要读取整篇 Markdown、整篇笔记 export 或整份 PDF。只有目录/章节无法定位、
  用户明确要求通读，或定向读取失败后才升级。
- `PAYLOAD_TOO_LARGE`：缩小到单节、页面或 Blocks；不要静默截断证据，也不要把截断
  后的内容当作已覆盖。
- `descriptions_complete=false` 只表示目录描述未完整加载，不代表章节无关或为空；
  需要时按标题读取。

### 并发

- 默认每波最多 3 篇，每篇一个 worker；不要一次启动整个 Base。
- 每个 worker 必须先确认自己能调用同一套 papersManager MCP 工具。不能访问 MCP 时
  不要委派，改由主 agent 顺序执行。
- 遇到 `RESOURCE_EXHAUSTED`、连接失败或 `UNAVAILABLE` 时，把并发降到 1–2 并做有限
  退避；不要紧密循环重试，也不要为绕开限制另起更多 agent。
- 一篇论文失败不应取消其他论文；记录失败并在该波结束后决定是否单独重试。

## 4. 子 agent 规则

host 提供子 agent/任务工具时才使用并行；否则按相同顺序逐篇处理并如实说明是顺序执行。
不要假装已经委派，也不要用 shell 启动外部进程来模拟子 agent。

每个 worker 的边界：

- **只处理一篇论文、只写这一篇的笔记**。不要读取、创建、修改或删除其他文档的笔记。
- **同一篇笔记只能有一个写入者**。主 agent 不得同时给同一 document 派两个 worker，
  也不得在 worker 运行时自己改同一篇笔记。
- worker 不再派生下级 agent；完成或失败后把结构化结果返回给主 agent。
- 只能使用 papersManager MCP 工具。不得使用 `curl`、shell、脚本或其他 HTTP 客户端；
  不得猜测工具名，不得绕过 MCP 写入边界。
- 如果子 agent 看不到本 skill 的 `references/`，主 agent 必须把下面的 prompt 模板、
  对应深度的要求以及 `notes-standard.md` 的关键约束直接放进任务描述；不能假设 worker
  会自动加载技能文档。
- 批量生成请求已经构成写入授权，但整篇替换、删除和“重建已有笔记”仍是单独的高风险
  操作，没有明确授权时不要执行。
- 创建笔记时使用 `expected_revision=0`；更新时先读目录和受影响章节，携带实际 revision。
  `REVISION_CONFLICT` 后重新读取并重新规划，不重放旧操作。
- 无法确认笔记是否存在（例如 note outline 读取失败）时，不要调用 create 或整篇替换；
  返回 `blocked` 并报告原因，避免覆盖已有内容。
- 返回结果必须是真实执行结果：没有写成功就不能返回 `created`/`updated`，没有核验的
  内容不能写成已核验。

## 5. Worker prompt 模板

把下面的模板填好后再交给子 agent。方括号内容必须替换；不要只给一个论文标题就让
worker 自己猜范围。

```text
你是 papersManager 阅读笔记 worker。只处理 document_id=<DOC_ID>。
不要读取、创建、修改或删除其他文档的笔记；不要派生子 agent。

任务：<create | update> 一篇 <brief | standard | deep> 阅读笔记。
用户原始要求：<用户原话>
写入授权：批量生成已授权；未授权整篇替换、删除或重建已有笔记。
当前计划动作：<create | update>；若发现已有笔记但动作是 create，则返回 skipped。

执行步骤：
1. 读 papers_manager_reading_note_outline（revision、章节树、description）和
   papers_manager_document_structure（source outline）。
2. 若当前 skill 的 references/ 可读，读 notes-standard.md、note-generation.md、
   evidence.md，按对应深度写作；需要方法与结果展开示例时读 worked-example.md。
   若不可读，使用主 agent 在任务中附上的同等约束，不要自行降低深度或放宽证据要求。
3. 先读目录和 description，再定向读取必要章节。brief/standard/deep 的起始阅读预算
   分别是 2–4 / 4–8 / 8–12 个定向章节；覆盖不足时继续，材料不足时少读，
   不要为凑数建空壳。
4. 数字、图表、公式、比较结论必须回原文核验；用 Blocks/页图定位，并校验证据链接。
   不能核验的内容明确写“未核验/读取受限/未发现报告”，不得猜页码、Block 或数值。
5. 没有笔记时用 papers_manager_reading_note_create，expected_revision=0；已有笔记且
   任务为 update 时先读受影响章节，再用实际 revision 增量编辑。正文不新增 H1–H3，
   结构变化使用 section 操作。每节写 description；不写 pipe table、raw HTML 或 LaTeX。
6. 失败处理：REVISION_CONFLICT 重读目录和章节后重新规划；UNAVAILABLE 有限重试；
   PAYLOAD_TOO_LARGE 缩小读取单元。无法确认笔记是否存在时不要 create 或整篇替换，
   返回 blocked。不要用 shell/curl，不要写其他论文，不要删除笔记。

只返回以下字段（JSON 或等价的结构化文本）：
{
  "document_id": "...",
  "status": "created | updated | skipped | failed | blocked",
  "reason": "note_exists | not_ready | missing_document | no_material | scope_excluded | ...",
  "revision": 0,
  "sections_written": ["..."],
  "coverage": "实际覆盖的问题/方法/结果",
  "evidence_gaps": ["..."],
  "unread_scope": ["..."],
  "error_code": "...",
  "error_message": "..."
}
```

`revision` 必须填写实际写入结果中的值（新建笔记也以工具返回为准），不能照抄示例的
`0` 作为所有结果。如果 worker 的返回缺少 revision 或状态，主 agent 不能把它当作成功；
应读取 note outline 确认，或把该篇标记为失败。

## 6. 主 agent 的汇总与重试

1. 收集每篇结果，逐条检查 `status`、`revision`、`sections_written`、`coverage` 和
   `evidence_gaps`。`created`/`updated` 必须以实际写入结果为准。
2. 抽查关键结论和证据，而不是把所有笔记重新读一遍。至少核对新建/更新笔记的章节数与
   关键数字的来源；`deep` 或结果可疑时增加抽查。
3. 单篇失败只重试该篇，一次为限，并缩小读取范围或修正任务描述；不要因为一篇失败就
   重跑整个批次。
4. 用户同时编辑导致 `REVISION_CONFLICT` 时，重新读取并合并；不要覆盖用户的新内容。
5. 输出批次报告：

```text
批次：<Base/清单>；总数：N
created：a；updated：b；skipped：c；blocked：d；failed：e

| document_id | 标题 | 动作 | 结果 | revision | 覆盖/缺口 | 原因或错误 |
```

报告必须区分“已有笔记未改动”“markdown 未就绪”“本轮失败”和“用户范围外”，不能把
它们都写成“成功”。只对实际保存的笔记报告 revision。

## 7. 标准示例

用户请求：“给 Base B 里的 5 篇论文各建一份 standard 笔记。”

1. `papers_manager_base_list_documents` 得到 A、B、C、D、E 五个 document ID。
2. `papers_manager_library_get_many` 读取五个 ID 的元数据；
   `papers_manager_processing_status_many` 读取处理状态。
3. 建立清单：
   - A、B、C、E：用户要求生成，markdown 已就绪 → `create`；
   - D：markdown 失败 → `blocked:not_ready`，不重试。
4. 第一波启动 A、B、C 三个 worker（host 无子 agent 时按 A→B→C 顺序执行）。
   B 的 worker 读到已有笔记，但用户只说“建”而没有要求更新，返回
   `skipped:note_exists`；A、C 正常创建。D 不进入本波。
5. 第二波启动 E。每个 worker 返回自己的 revision、覆盖和缺口。若 A 因
   `PAYLOAD_TOO_LARGE` 失败，只对 A 缩小章节后重试一次；其他论文结果不受影响。
6. 汇总为一张批次表：A/C/E 的实际 revision，B 的“已有笔记未改动”，D 的“未就绪”，
   以及 A 的重试结果。最后明确说明尚未覆盖的论文。

## 8. Base 总结和比较中的批量加载

Base 总结/比较也可以使用同样的清单和波次，但**不写笔记**：

- 每个 worker 只负责一篇论文的证据行或指定章节摘要，返回 compact 结果；
- 主 agent 负责选择维度、判断可比性、汇总主题/分歧并撰写最终 HTML 报告；
- worker 不得写 report，不得替主 agent 下跨论文结论；
- 具体的报告结构、风格和链接规则见 [base-summary.md](base-summary.md)。

## 9. 常见错误

- 一次启动整个 Base 的 worker，或为每个 worker 再开下级 worker；
- 两个 worker 同时修改同一篇笔记；
- 主上下文预载所有笔记正文或整篇 PDF；
- 用摘要/标题猜方法、结果或证据；
- 已有笔记被“生成”任务静默覆盖；
- 一篇失败后重跑整个批次；
- 把 worker 的 `status=failed` 或缺少 revision 的结果写成成功；
- 用 shell、curl 或脚本绕过 MCP，或向没有 MCP 能力的子 agent 委派。
