# Go 后端业务压测

以下命令均在仓库根目录执行；工具源码位于 [`loadtest/`](../loadtest/)，实测结论见[性能报告](performance-results.md)。

通过本地 `make dev` 启动真实 Go API、Python、Vite，以及 Docker PostgreSQL/Redis。
每次生成独立 Compose 项目、卷、端口、数据库和文件目录。不要对自己的论文库运行造数程序。
当前编排入口适用于 Linux/WSL；Go 驱动可构建到其他平台，但未在其他平台执行本套压测。

## 运行

先准备正常开发依赖：Docker Compose、Go、Node、`web/node_modules` 和根目录 `.venv`。
Python 编排使用现有环境中的 `psutil`，无需安装压测服务或启动真实 LLM Agent。

```bash
# 完整矩阵：7 个语料规模 × 5 个并发档位，随后执行批量文件/URL 导入
.venv/bin/python loadtest/run.py

# 快速验证配置及主要业务读写
.venv/bin/python loadtest/run.py --sizes 1,10 --concurrency 1,4 \
  --duration 2s --warmup 1s --import-batches 0

# 重点验证 500 篇、延长采样；每个测量轮次前有独立预热
.venv/bin/python loadtest/run.py --sizes 500 --concurrency 4,8,16,32 \
  --duration 30s --warmup 5s
```

默认数据库逐档增长为 `1、10、50、100、250、500、1000` 篇，不是先造 1000 篇再缩小查询范围。
每档测试 `1、4、8、16、32` 个同时在途的客户端工作流，预热 2 秒，正式测量 8 秒。
闭环客户端收到响应后才发下一个请求。客户端数量与语料大小分别变化，并不等于每篇论文一个并发请求。
同一文档最多一个写入者，写入者各自循环访问互不重叠的文档分区；读者可以访问正在写的 notes。
小语料不足以提供独立写分区时，多余客户端只读。

每个写入工作流约每 4 次请求替换一次章节，其余请求读取：

- notes 目录、按需章节正文；
- 原文结构、某页的 20 个 blocks、源 Markdown；
- 论文元信息、论文搜索、Base 分页列表；
- 原 PDF 的前 64 KiB（HTTP Range）。

默认每篇有 200 个 blocks、合成多页目录、12 个约 8 KiB 的 notes 章节及 1 MiB 的源文件。
源 PDF 是有效的单页传输夹具，其他多页解析结果是直接写入的合成数据；不用于评价 PDF 定位或解析正确性。
文档、blocks、元信息直接通过项目模型造数；notes 必须通过真实上传接口创建，后续读写也走真实业务接口。
这些不是 MCP 会话级压测；模拟 Agent 的 HTTP 调用复用同一 Go 服务和数据库逻辑，不含大模型思考或 MCP 编解码耗时。

每次写入校验文档 ID 和单调 revision。每轮结束回读最后成功修改的章节，核对标记、版本和章节 ID。
这验证最终成功内容，不是完整历史审计；持续覆盖同一章节时，旧标记本来就会被替换。
错误返回按接口/状态计数，不把 409 或 429 自动吞掉当作成功；当前主负载没有设计同份 notes 的写竞争。

## 批量导入

默认并行提交两批，每批 20 项，分别运行：新文件、重复文件、新 URL、重复 URL、包含 404 和慢响应的 URL。
每组同时持续进行 8 秒 notes/PDF 混合访问，并单独记录其延迟。默认入库只需几秒时，其余时间继续观察后台影响。
若 URL 任务耗时超过 8 秒，后续等待不再叠加背景负载；可从 CompletionSeconds 判断覆盖范围。
文件大小沿用 `--pdf-bytes`，可用 `--import-batches 0` 跳过所有导入。
超过当前 HTTP 批量并发上限的请求可能合理地返回 429，工具会报告，避免误判为都已导入。

URL 指向压测进程自带的本地 HTTP 文件源。仅在该次隔离 Go 进程中通过现有
`SSRF_WHITELIST_EXTRA=127.0.0.1` 允许访问它，不修改项目默认配置和 SSRF 实现，不压公网网站。
每个正常批次必须完整返回 20 个结果；重复批次必须全部 duplicate；部分失败批次预期每批仅一个失败项。
新文档必须报告处理任务已排队。

报告区分文件批次返回/URL 入队耗时与 URL 下载入库完成耗时。
**导入结果 accepted 不是 PDF 已解析完毕。** 另记录当时的 markdown_status 分布；真实 Python 后台任务
会运行，因此导入期间的资源/延迟包含其竞争，但主矩阵没有解析任务，也不评判模型性能或元信息检索质量。
预配置 catalog 超时为 1 秒、任务超时 5 秒、最多重试 1 次，以避免外部查询拖延退出；这些不同于正常配置。

## 结果与边界

每次结果写入 `loadtest/results/<时间-进程号>/`，此目录不提交：

- `run.json`：参数、Git revision、机器资源和专用项目名；源码未提交时应结合工作区状态解读 revision。
- `results.json`：每档接口延迟/错误/吞吐、正确性结果、数据库累计计数和锁等待采样、导入结果。
- `summary.md`：主要矩阵及 500 篇接口明细。
- `resources.jsonl`：每秒采样 API、Python 和压测驱动的 RSS、CPU 时间、线程及 fd。
- `dev.log`、`application.log`、`driver.log`、`cleanup.log`：本次启动、服务、驱动与清理记录。

```bash
# 将资源采样与正式测量时间窗口对齐，生成资源摘要并补充 summary.md
.venv/bin/python loadtest/analyze.py loadtest/results/<本次目录>
```

保持数据库连接池 48/24、Asynq 并发 32 和正常 Info 日志。
将 HTTP 写入频率提高到 100000/min、burst 100000，避免 300/min 的正常用户限流主导吞吐；
保留上传并发 4、批次并发 2。压测关闭 Air 热重载（`DEV_AUTO_RELOAD=false`），避免编译和重启扰动数据。
未修改工作区 `.env`。`make dev` 仍会覆盖开发日志 `Logs/dev/`；请不要同时运行另一套 `make dev`。

数据库指标来自 `pg_stat_database` 的近似累计快照和每 500ms 的 `pg_stat_activity` 采样，存在统计刷新延迟，
短事务锁等待可能漏采；包括驱动自身的少量查询，不等于 SQL 语句数。
尚未采集 API 内部连接池 WaitCount/WaitDuration、SQL 调用次数或查询计划，不能仅凭此报告断言 N+1 或连接池饱和。
默认是同机、预热后的短轮次基线，不是网络客户端、冷启动、长期稳态、固定到达率或故障恢复验收。

结束时停止本次源进程，移除本次 Docker 项目和卷，删除造数存储，保留报告和日志。
`--keep-data` 会保留专用卷与 fixture 文件但停止组件，方便手工检查；下次仍新建项目，不复用旧数据库。
本次范围外的 Docker 容器和用户论文不受影响；崩溃/强杀编排器可能需要按 `run.json` 的项目名手动清理。

高并发边界测试可单独运行，避免将“500 篇容量”与“500 个并行客户端”混为一谈：

```bash
.venv/bin/python loadtest/run.py --sizes 500,1000 --concurrency 64,128,256,500,1000 \
  --duration 10s --warmup 2s --import-batches 0
```

驱动连接上限至少等于最高客户端并发，不会用 256 条连接悄悄限制 1000 客户端测试。
达到瓶颈时，排队延迟通常先增加；不要只看吞吐量是否还有增长。
