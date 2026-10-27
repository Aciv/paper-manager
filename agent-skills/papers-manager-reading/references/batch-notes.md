# 批量阅读与笔记流水线

处理多篇论文，或“导入论文并生成 notes”时使用。单篇内容标准见
[notes-standard.md](notes-standard.md)，写入见 [note-generation.md](note-generation.md)，
证据见 [evidence.md](evidence.md)，按需读取见 [reading-levels.md](reading-levels.md)。
本流程不降低单篇质量，也不授权替换或删除已有笔记。

## 1. 任务与并发预算

- 批量建笔记默认 `standard`；用户明确要求速读才用 `brief`，明确要求深入分析才用 `deep`。
- 已有笔记默认跳过；明确要求补充/更新才增量编辑，整篇替换或删除仍需明确授权。
- 用户范围不明时澄清；已经要求全部处理时不要截断清单。大任务简短报告总量和并发，再持续执行。
- 主任务及其所有子任务合计默认 **8 个活跃论文 worker**，最多 **16 个**，取 host 可用子 agent 数和
  用户指定更低值；不要每批分别开满额度。一个 worker 当前只负责一篇，完成即补下一篇。
- 服务端工具预算按 **普通读 128 / 普通写 32 / 重型读取 4 / 本地导入 2** 独立分组；没有统一外层上限。
  同组在所有客户端间共享，整篇 Markdown/notes 导出和图片使用重型组，不挤占普通目录/状态查询。
  worker 数与在途工具数不同，思考中的 worker 不占工具名额。
- 本地文件导入默认最多 2 个并行调用，每调用可含 1–20 项；URL 下载用已有后台队列和下载预算。
- `RESOURCE_EXHAUSTED` 时先查看 `details.group/limit`，降低对应组的调用并发并按约 1/2/4 秒有限退避；
  若普通写入持续饱和，再减半活跃派发量并暂停补位，不能因重型组满额就停止其他组；持续失败则报告，
  不开新会话绕过。连接失败或 `UNAVAILABLE` 时同样降载，不把正常 `queued/running` 当故障重试。
- host 没有子 agent 或子 agent 不能访问同一 MCP 时，由主 agent 按相同流程顺序直接保存；
  不用 shell 模拟 worker，也不让不可写的子 agent 回传大段草稿代替工具结果。

此预算是 HTTP 业务压测后选择的保守调度值，不保证图片解析或所有 MCP 批量操作也有相同延迟。
同一篇 notes 始终只有一个写入者；主 agent 不与 worker 同时修改它，worker 不再派生子 agent。

## 2. 只维护进度清单，不预载正文

已有 Base 优先用 `papers_manager_base_reading_outlines`，outline_type=both，分页取得 ID、标题、markdown 状态、原文和 notes 目录；按 has_more 继续。
若 tools/list 尚无该批量入口，回退到 Base 分页及已有批量元数据/状态工具，只对确需验证的笔记用单篇工具，不能虚构批量结果。
不用主 agent 逐篇 reading_note_outline 验证是否存在；worker 真正编辑时仍按需确认目标篇的最新 revision/内容。用户直接给出 ID 时以用户清单为准。
Base 批量页已含标题、markdown 状态和目录，不在同一轮重复查询相同字段；只有需要作者/DOI/详细阶段时才补对应批量读取。
直接给出的 ID、新导入 ID 或后续待解析集合使用 `papers_manager_library_get_many` / `papers_manager_processing_status_many`，
每次 1–200 个 ID。处理 `missingDocumentIds`，不猜替代论文或重跑全批。

清单只保留：

```text
输入URL/路径 | task_id | document_id | 标题 | markdown状态 | 动作 | worker | 结果revision/缺口
```

按 document ID 去重：accepted、duplicate、多个 URL 或重复 Base 成员指向同一篇时，只派一次。
动作 `create` 表示已存在笔记则跳过；`update` 表示按授权增量更新，不存在则创建，除非用户只允许更新已有笔记。
主 agent 可用批量目录快照了解笔记存在性、revision 和章节数；正文由该篇 worker 按需读取，主 agent 不逐篇预读。
批量结果 status=succeeded/partial/failed 只表示当前页所请求目录的完整性；summary 统计本页，逐项 state 区分缺失/未就绪/错误。
合法空 notes 是已存在，不等于没有笔记；complete=false 是截断，不等于空白。不能把外层 ok=true 当作所有目录均可用。

任务状态：

- `waiting_import`：还没有 document ID；
- `waiting_parse`：有 ID，但 markdown 为 not_started/queued/running；
- `ready`：markdown 成功，可以读取 source structure；
- `writing`：已交给唯一 worker；
- `created/updated/skipped`：worker 的实际结果；
- `blocked/failed`：文档缺失、解析失败/取消、读取能力缺失或工具执行失败。

metadata_extraction、source_lookup、reference_lookup 还在执行或失败，不构成写 notes 的等待条件。
身份信息不足或与原文冲突时，worker 以实际阅读原文为准并说明差异；不能为补齐作者/DOI无限等候。

## 3. 导入、解析与写作滚动交接

**不设置“所有导入完成”或“所有解析完成”屏障。** 主 agent 作为调度者维持待处理集合与活跃 worker，
谁先具备可读原文、谁先进入写作；完成一篇立即补位，不等一波最慢的论文。

### URL 导入

1. 入库使用 library skill 的 `papers_manager_library_import_urls`，每批最多 20 项；保存稳定 request_id
   和返回的 task_id。响应丢失时先按原任务查询，不能因为超时盲目重投。
2. 调用 `papers_manager_library_import_status`；任务还在 running 时，检查 `result.items` 中已经完成的
   accepted/duplicate 项，提取其 document ID，不必等 `completed == total`。若结果暂未出现就继续等待，
   不猜 ID、不把 URL task 的 succeeded 当作 PDF 解析成功。
3. 对新增 ID 与 `waiting_parse` ID 批量调用 `papers_manager_processing_status_many`。markdown 成功即移入
   ready；立即在剩余 worker 预算内分派。主 agent 可继续收集下一项，不等已分派 worker 全部结束。
4. worker 返回后更新结果并释放该篇占位；调度下一篇。Base 附加失败但已有 ID 时，先按授权补成员关系，
   不重新下载；同一 document 不因再次出现而重复派发。

### 本地文件导入

`papers_manager_library_import_files` 同步返回整批接收结果，没有可轮询的本地导入 task_id，也不提供中间项流。
复合任务优先每批 1–5 项，使已解析论文更早交接；最大仍是 20，用户明确偏好大批时遵守。
host 支持异步工具调度时，可保留最多 2 个在途本地批次；一批返回就把 ID 加入上述解析/写作循环，
不要等其他本地批次结束。host 只能同步调用时，在小批次之间查询并调度已就绪 ID，不假装工具能中途返回。

### 等待与失败

- 只有 waiting_import/waiting_parse 且暂时无可处理结果时，约每 2–5 秒批量检查一次；不让每个子 agent
  各自高频轮询。合并重复查询，只查未终结的任务/论文。
- 默认连续无进展最多等待 2 分钟；用户给定的等待预算优先。有实际进展时继续处理有限清单；
  不能在没有进展时反复重置等待计时。
- 到期仍未就绪，记录 `blocked:not_ready`、task_id/ID 和当前阶段；后台任务继续执行，不取消、不重投。
  后续继续任务时从原 task_id 和 ID 恢复，已保存 notes 不重复生成。
- markdown 失败或取消时记录原因并处理其他论文；只有用户要求或已明确授权自动重试时才重试解析。
- 单篇写作失败不取消其他论文；允许纠正原因后只重试该篇一次，不能重跑整个清单。

## 4. Worker 直接读、核验并保存

每个 worker 自行按 source outline 和 note directory 按需读原文/笔记，不读取其他文档。
brief/standard/deep 可从 2–4 / 4–8 / 8–12 个定向章节开始，覆盖不足继续读取，材料少则少读；
这些是起始预算，不是配额，不为凑数量建空壳。方法、结果的细节深度遵守 notes-standard。

数字、图表、公式和比较结论用 Blocks/页图回原文核验，并优先用 `papers_manager_evidence_validate_many` 批量检查链接参数和页码范围；
它不检查 block 归属或论点支持，真实证据仍来自当前版本原文。
`descriptions_complete=false` 不代表相关材料不存在；默认不读取全篇 Markdown、全笔记 export 或整份 PDF，
定向读取不足或用户明确要求时再升级。超过载荷上限要缩小读取单元，不把截断内容当完整证据。

**worker 必须直接调用真实 MCP 工具创建/增量编辑 notes，自行核对写入结果并必要时回读。**
不要把“子 agent 生成 .md → 主 agent 读全文审核 → 主 agent 再调用写工具”作为默认流程；
也不要同时交付本地 .md 和相同正文给主 agent 重复保存。写工具不可用时返回 blocked，不把草稿报成成功。
用户明确另要文件导出或全文审核时才额外提供，不能替代真实 notes 写入。

主 agent 正常情况下只检查返回的状态、实际 revision、覆盖和缺口；
结果异常、证据可疑或用户明确要求审核时才定向抽查，并把具体缺口交回该篇 worker 修正，避免重载/重写整份正文。

## 5. Worker prompt 模板

替换方括号内容。若 worker 不能读取技能 references，主 agent 附上相应深度和证据约束，
不附整篇原文/笔记。只分配已经 markdown 就绪的论文，不把 worker 名额用于排队等待解析。

```text
你是 papersManager notes worker，只处理 document_id=<DOC_ID>，禁止再派生子 agent。
任务：<create | update> 一篇 <brief | standard | deep> 笔记。
用户要求：<原话>。已授权本篇写入，未授权整篇替换/删除已有笔记。

1. 确认可调用同一 MCP；读取 reading_note_outline 和 document_structure。
   动作为 create 且已有笔记则 skipped:note_exists；无法确认存在性时 blocked，不能猜测后覆盖。
2. 按 notes-standard.md、note-generation.md、evidence.md 定向阅读；方法和结果重点展开，
   用 H2/H3 建可索引子节，需要时参考 worked-example.md，不能仅改写摘要。
3. 关键主张回原文核验，验证证据链接；缺材料或读取受限如实记录，不编造位置与数值。
4. 直接用 papers_manager_reading_note_create(expected_revision=0)，或用实际 revision 调用
   reading_note_apply_edits / reading_note_replace_section 增量保存。结构变化使用 section 操作，
   正文编辑不插入 H1–H3；每节写 description，不写 pipe table/raw HTML/LaTeX。
5. 检查工具返回的实际 revision/章节，必要时回读；自己完成保存和核验，不回传正文让主 agent 代写，
   不以本地 .md 作为交付。缺写工具则 blocked。
6. REVISION_CONFLICT 后重读目录和目标节再规划，不重放旧范围；RESOURCE_EXHAUSTED/UNAVAILABLE
   有限退避并通知主 agent 降载。写入响应不确定时先查目录/目标节确认结果，不盲目重建或重放。

只返回精简结构化结果，不返回全文：
{
  "document_id": "...",
  "status": "created | updated | skipped | failed | blocked",
  "reason": "...",
  "revision": 0,
  "sections_written": ["实际章节ID或标题"],
  "coverage": "实际覆盖的问题、方法与结果",
  "evidence_gaps": ["..."],
  "unread_scope": ["..."],
  "error_code": "...",
  "error_message": "..."
}
```

revision 必须来自实际写入结果；没有 revision 的 created/updated 不能算成功，应查目录确认或报告失败。
没有读到或没有核验的内容不能报成已覆盖。返回量通常控制为每篇几百字，不带正文副本。

## 6. 示例与汇总

用户要求导入 A/B/C/D 后生成 standard notes，URL 任务仍在运行：

1. 已获得 A 的 ID，markdown 成功，立即派 A worker 直接读写；B 还在解析，C/D 尚未入库。
2. A 写作期间主 agent 查询新结果，B 就绪便派 B，不等 C/D 下载。导入、解析和 notes 写作同时推进。
3. A 完成后只返回实际 revision/覆盖/缺口；主 agent 记录，不再取回全文写一次。
4. C 的 markdown 失败则记 blocked，继续 D；D 就绪即派发。没有整批或固定波次屏障。
5. 最终报告每篇 created/updated/skipped/blocked/failed、实际 revision、覆盖及未完成原因。

已有 Base 的多篇任务使用同一滚动调度。只创建时，已有笔记返回 skipped，主 agent 不因其快速结束而重复派发。
Base 总结/比较也可用相同并发和按需加载，但不创建 notes：worker 返回单篇证据行/摘要，主 agent
负责跨论文可比性判断和最终报告，见 [base-summary.md](base-summary.md)。
