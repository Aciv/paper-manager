# 跨平台发行审计与验证

默认交付为解压运行：Windows 打开根目录 paper_manager.exe，Linux/macOS 运行
./paper_manager；首次在管理页初始化。无需先运行 Install 脚本。
使用及可选维护说明见 [INSTALL.md](INSTALL.md)，最新证据见 [VALIDATION.md](VALIDATION.md)。
下面保留 D0/D1 历史审计；其中“下一步”“尚未实现”等是当时状态，不是当前交付结论。

日期：2026-10-24。当前结论：**D0/D1原生技术验证完成，保留Asynq和共享worker
架构，下一步为D2 Launcher；自包含发行物和最终再分发验收仍在D3。**

## 支持矩阵和证据

| 目标 | 候选 CI 基线 | 已有证据 | 待补 |
| --- | --- | --- | --- |
| Windows x86_64 | windows-2022、Python 3.12 | run40085276527完整通过：原生wheel、真实parser、队列/Lua/AOF/崩溃恢复、普通账号PostgreSQL生命周期 | 自包含发行包、Windows10/11安装实测、生产CRF权重分发 |
| Linux x86_64 | ubuntu-22.04、glibc 2.35、Python 3.12 | 原生CI完整通过；本机Ubuntu24.04/WSL2同参数三轮性能/内存与真实论文压力测试通过 | 私有运行时/数据库发行目录、干净系统安装验收 |
| macOS arm64 | macos-14、Python 3.12 | 原生CI完整通过：wheel、真实parser、队列/数据库、生命周期与基准 | 自包含目录、生产CRF权重、签名/公证及安装验收 |

CI runner 不等于最低用户系统承诺。Windows Server runner 不证明所有
Windows 10 安装/窗口行为。macOS runner 要在日志中确认架构。
.github/workflows/desktop-d0.yml 已建立并执行三平台原生检查。
最初 run 40070038953 的 Windows/macOS 失败触发了下述 D1 修复；
修复过程见 [草稿 PR #1](https://github.com/Aciv/paper-master/pull/1)。
失败会真实保留，没有用 continue-on-error 掩盖。
默认 CI 不带 git-ignored 权重，rules 基线不能替代本机 hybrid/CRF 验证。
检查器会列出缺失模型；正式 CRF 发行必须使用 --require-models。

## Windows Redis 候选

- 本地原包：tmp/Redis-7.4.11-Windows-x64-cygwin-with-Service.zip。
- 来源：redis-windows/redis-windows，tag 7.4.11，发布于 2026-08-18。
- SHA-256：2289eca02c25e96a918812c3b05083c3a8bc9440f1b268cc48e14bd93ed2acb0。
- 本地哈希与 GitHub release asset digest 相同；不是只根据文件名判断版本。
- 用户于 2026-10-24 报告 tmp/asynq-compat-88BNlK 独立工程在 Windows 测试通过。
  尚未收到原始日志、执行参数和 Windows build，不能推断 persistence/crash 模式都执行过。
- 保留 Asynq v0.26.0；候选不再以 Memurai 为默认，也不启动队列替换。
- 随包 README 说明：redis-server.exe 命令行路径采用 /cygdrive/c/...；
  RedisService.exe 包装器提供路径转换，注明 .NET 10，但是否完全自包含尚未验证。
  Launcher 优先评估直接启动 redis-server.exe，使用独立 cwd/data 目录，
  不要求注册 Windows 系统服务。
- 附带 Cygwin、OpenSSL、GCC、iconv、intl、zlib 等 DLL，不能仅打包 exe。
  原 ZIP 没有 LICENSE/COPYING/source notices；这属于发行材料缺口。
- 上游 README 将其推荐用于本地开发，并未承诺生产可靠性；私有桌面发行
  必须自行验收 AOF、异常关闭恢复、路径、生命周期和安全更新。

许可审计事实：Redis 7.4 为 RSALv2 或 SSPLv1，不是 Redis 7.2 的 BSD。
Cygwin DLL 为 LGPLv3+，有链接例外；其他 DLL、服务包装器和来源源码仍需
逐项收集材料。这是待解决的再分发检查，不是“不能使用”的法律结论。
完整候选清单、版本与哈希见 dependencies.json；它不冒充正式发行锁文件。

来源：
- https://github.com/redis-windows/redis-windows/releases/tag/7.4.11
- https://redis.io/legal/licenses/
- https://cygwin.com/licensing.html

## D0 初始源码审计与 D1 必须保留的结构

下列1–10为适配前观察，保留作为历史依据；最终实现与验收见后面的D1章节。

1. server.py 的 _build_shared_pools 创建 recognize/extract/render 各一份
   SharedWorkerPool；每类只持有一个 _worker，重包在 worker.py 延迟加载。
   “单一 worker”按种类定义，不能擅自合并成所有 RPC 共用一个大进程。
2. worker_pool.py 的 AF_UNIX、start_new_session、os.killpg/os.getpgid，
   worker_runtime.py 的 SIGUSR1/信号处理是 Windows 适配点。
   shared 模式已经通过每请求连接 EOF 协作取消，不能为了统一 IPC 改成
   一个全局取消信号，导致同进程其他请求被取消。
3. server.py 导入轻量性的已有测试必须保留。容器默认 shared，但
   server.py 未配置时的默认执行模式仍是 worker；Launcher 必须显式选择 shared。
   D0 不擅自修改默认模式和任务预算。
4. 内部并发当前是每连接请求线程＋取消监视线程，并非一个可直接替换的
   futures.ThreadPoolExecutor。worker_pool 在父进程限制每类并发数。
   D1 要验证实际并发重叠和上限，不因“线程池”称谓假定已有固定池实现。
5. Asynq 在 internal/router/tasks.go 使用 Server.Run，其 Windows 信号路径
   有上游讨论；Launcher 适配时改为受控 Start/Shutdown 生命周期，
   不修改任务类型、重试协议或 Inspector 存储。
6. limiter 实际仍用于 catalog 和远程 PDF 下载，REDIS_ADDR 非空时用 Redis。
   两个真实 Redis 集成测试含 Lua；任务测试通过不能自动代表 limiter 通过。
   任务快照测试会清空专用 DB 15，禁止对用户实例运行。
7. Go 文件存储使用 filepath/配置路径；新 Launcher 必须隔离安装目录与数据目录。
   Python logger 默认写包内 Logs，发行时必须注入可写 LOG_PATH。
   旧 Go pdfregion 独立脚本启动是框架接口，不能用它替代 shared 解析架构。
8. CRF 权重约 122 MiB，存在本机但未跟踪；干净 checkout 的 hybrid 会降级，
   不能把这种降级误当性能优化或 CRF 可用验证。三个权重的哈希已记录。
9. parser requirements 有版本范围，ONNX/numpy 等传递依赖未锁定。
   本次记录本机解析版本；各平台最终 wheel 哈希/锁文件在 D3 产生。
10. 本机 PyMuPDF 元数据为 AGPL/商业双许可，pymupdf-layout 为
    PolyForm Noncommercial/商业双许可；项目没有跟踪的顶层 LICENSE。
    先识别发行形态与依赖条款，不能自动认定所有组件为 MIT/BSD。

## 测试命令

在仓库根目录用 parser Python 安装验证依赖（仅验证工具，不进生产镜像）：

    .venv/bin/python -m pip install -r packaging/desktop/requirements-validation.txt
    .venv/bin/python scripts/check_desktop_dependencies.py --require-models --redis-archive tmp/Redis-7.4.11-Windows-x64-cygwin-with-Service.zip --output tmp/desktop-d0/dependencies.json
    .venv/bin/python -m unittest discover -s scripts -p test_desktop_validation.py

Windows 用私有 python.exe 或开发 Python 替代 .venv/bin/python。
准确的现有 parser 测试入口是在 pdf_parser 目录运行：

    cd pdf_parser
    ../.venv/bin/python -m unittest discover -s papers_parser -p 'test_*.py'

根目录直接 discover 虽能加载测试，但现有 worker 子进程无法按模块找到
papers_parser，出现三个 exited during startup 错误；正确 cwd 下 127/127 通过。
这也是发行时必须消除 cwd 隐式依赖的证据；D0 记录，D1 适配。

私有 PostgreSQL 探针（需要对应平台真实二进制，不连接用户已有实例）：

    python scripts/check_private_postgres.py --bin-dir "<PostgreSQL 17 bin>" --output tmp/desktop-d0/postgres.json

探针使用全新带空格/中文的临时目录，测试 initdb、认证 SQL、停止、重启、
数据持久化和清理；普通用户运行，不注册系统服务。当前环境没有原生
PostgreSQL 工具且为 root，尚未通过该探针；容器 SQL 查询不能替代此验收。
Windows CI 会尝试 runner 的明确 17/bin 路径，缺失时真实失败；
runner 管理员环境通过也不能代替非管理员安装验收。

## Python 性能和内存基线（D1 前置门槛）

命令：

    .venv/bin/python scripts/benchmark_parser_shared.py --output tmp/desktop-d0/shared-baseline.json --jobs 8 --idle-seconds 15

真实子进程运行 server --execution-mode shared；经真实 gRPC 分别调用
Recognize、Extract、RenderPage。四页合成 PDF、DPI 144、layout threads=2，
hybrid、本机三个权重、每类并发预算始终为 4，客户端负载分别为 1 和 4。
每类预热后执行 8 次，分别记录冷启动、吞吐、p50/p95、父进程/全树 RSS、
worker PID/线程数，并验证每类单进程及空闲退出后 server 仍然存活。
基准控制器不加载 PyMuPDF/ONNX；PDF 由额外短命进程提前生成。

本次 Linux/WSL2 基线（适配尚未修改，所以不是前后改善报告）：

| 客户端并发 | 识别 jobs/s | 提取 jobs/s | 渲染 jobs/s | 全树采样峰值 RSS | worker 回收后 server RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 0.994 | 1.992 | 1.989 | 444.2 MiB | 39.9 MiB |
| 4 | 2.646 | 7.936 | 7.299 | 601.1 MiB | 40.2 MiB |

随后完成三轮控制实验，汇总后的 shared-median.json 用作 D1 正式比较基线：

| 客户端并发 | 识别 jobs/s | 提取 jobs/s | 渲染 jobs/s | 全树采样峰值 RSS 中位数 | 回收后 server RSS 中位数 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 1 | 0.996 | 1.991 | 1.988 | 429.1 MiB | 39.9 MiB |
| 4 | 2.277 | 7.941 | 7.012 | 705.2 MiB | 40.3 MiB |

三轮汇总仅代表稳定参数下的中心值，原始单轮范围同时保留；三个连续运行
都未修改 parser 生产代码。并发峰值仍会变化，不能从一次采样峰值推断泄漏。

峰值 RSS 是父子 RSS 求和，可能重复计算共享页，并非独占物理内存；
只与同系统、同采样方式的基线比较。合成提取/渲染约 0.5s 的延迟包含
现有取消监视轮询等待，不能把这组数字当真实长论文的通用性能。
报告/日志在 tmp/desktop-d0，包含机器和解释器元信息，不进入正式安装包。

D1 必须在同一机器/解释器/库/模型/线程配置/输入下比较，至少三轮取中位：

    # 改动前、改动后分别执行三轮，使用不同 output 保存，以下汇总改动前结果：
    .venv/bin/python scripts/benchmark_parser_shared.py --aggregate tmp/desktop-d0/shared-baseline.json tmp/desktop-d0/shared-verified.json tmp/desktop-d0/shared-third.json --output tmp/desktop-d0/shared-median.json
    # after-1/2/3 同样由真实运行生成，不能复制同一报告：
    .venv/bin/python scripts/benchmark_parser_shared.py --aggregate tmp/desktop-d0/after-1.json tmp/desktop-d0/after-2.json tmp/desktop-d0/after-3.json --baseline tmp/desktop-d0/shared-median.json --output tmp/desktop-d0/comparison.json

脚本遇到输入/系统/模型/依赖指纹不同直接拒绝比较；候选门槛：
吞吐下降 >20%、p95 增加 >25%（另留 20ms 噪声余量），或峰值 RSS
增加 >20%（另留 16MiB）、回收后父 RSS 增加 >20%（另留 8MiB）均报失败。
单轮失败先在安静机器重复确认，不以放宽阈值掩盖回归。
本次未改生产代码的第二轮与第一轮比较，c4 识别 p95 从约 1.52s 到 2.02s，
触发单轮门槛，吞吐约 2.65 到 2.27 jobs/s，内存未触发门槛。
原始失败结果保留在 shared-verified.json；这是当前测量噪声/轮询边界的
控制实验，不是已证实的 D1 回归。工具支持三轮中位数汇总并保留 min/max，
不能把一轮性能告警直接归因为改动，也不能删除告警后称全部稳定。
继续用 --pdf <合法本地PDF> --pages <页数> 和至少 20 个任务验证真实论文；
默认 4 页输入/8 个任务不能代替长文档、混合 4+4 压力验收。
还需验证持续高负载时无请求数倍增的重包进程，空闲后保留轻量 server。

## D1 实施与本机验收（2026-10-24）

- worker_transport.py 隔离平台差异：Unix socket 保留；Windows 使用
  loopback TCP、环境变量传递每次启动的随机认证令牌和私有 endpoint 文件。
  不增加公开 gRPC/HTTP 接口，不把凭据放在 argv 或日志中。
- 轻量 server 保留；recognize/extract/render 各一份 SharedWorkerPool。
  Windows Job Object 在启动握手放行重包导入前创建；Unix 使用私有 session。
  父进程管道 EOF 清理 worker/helper，请求连接 EOF 只取消自己的请求。
  协作取消仍受原生库调用边界限制；关闭 worker 会使未完成调用明确失败。
- worker 内部线程执行、模型仅加载一次；recognize4 + extract4、RPC8
  保持。修复 shared concurrency=1 误走 persistent 协议、Windows EOF/
  退出码竞态、glibc trim 启动错误、工作目录依赖和大 payload 发送超时。
  分段发送遵守整个调用 deadline；0.5s socket 轮询不再代表整个发送失败。
  启动失败会清理私有目录并返回 typed unavailable；关闭池会中断启动等待，
  不等到完整启动超时才回收子进程。
- Asynq 使用 Start/Shutdown，由主程序统一管理信号。任务类型、状态存储、
  Redis 数据格式和重试机制保持；修复各平台测试的文件句柄及规范路径断言。
- 原生 CI 启动 PostgreSQL17 和 Redis7.4.11，真正执行项目 SQL、Lua、
  并发/延迟/重试/取消/超时/drain，并检查 Go JSON 测试事件拒绝静默 skip。
  额外强制杀死执行中的 worker，重启 Redis，验证 pending/scheduled 与
  active lease 自然过期恢复；不修改内部 Redis key 或用 Inspector 手动重排。
- Windows PostgreSQL ANSI argv 无法直接表达中文路径，探针显式使用并记录
  GetShortPathNameW 别名。没有可用别名时明确失败；D2 目录策略需处理这个
  约束，不能从短路径探针推断任意 Unicode 参数都被数据库支持。
- Windows 真正普通账号验证脚本仅允许临时 Actions runner，创建隔离本地
  用户和私有 ACL 目录，检查 `is_admin=false`；不把 runneradmin 误作普通用户。

本机142项Python与9项验证工具测试、Go全量build/test、前端build
与34项 reading-note 测试通过。本地真实 Redis7.4.11＋临时 PostgreSQL17
容器全量集成通过，包括8项必执行测试及 AOF/崩溃租约恢复；本地容器证据
不能替代各系统原生数据库证据。原生 CI 结果与工件见 PR 的 Actions。

同机器/解释器/库/模型/输入、jobs8、layout_threads2 的三轮中位数比较：

| 客户端并发 | 识别 jobs/s（前→后） | 识别 p95 秒（前→后） | 全树峰值 MiB（前→后） | 回收后父 RSS MiB |
| --- | ---: | ---: | ---: | ---: |
| 1 | 0.996→0.961 | 1.007→1.062 | 429.1→432.2 | 40.6 |
| 4 | 2.277→2.622 | 2.009→1.732 | 705.2→627.4 | 40.9 |

最终固定线程池比较结果 shared-d1-pool-median.json：regressions=[]；
三个独立运行使用 shared-d1-pool-6/7/8.json，首轮与测试有短暂重叠，
未纳入最终比较。worker 内有实际固定线程池，客户端超时后原生调用尚未
返回时，后续请求仍不能突破物理执行预算；实际测试覆盖这个边界。
更短的取消监视轮询降低响应等待；性能数字仅代表这个固定输入。

另以本地16页/5.3MiB PDF、20个 recognize/extract 混合任务、客户端4+4并发、
4+4 worker预算运行两组并完成空闲回收：最终混合批次40.1/40.7秒，
全树峰值1438.5/1464.6MiB，回收后父进程53.2/60.1MiB。
长批次 idle60s，防止15秒的正常空闲回收干扰 PID 稳定性断言；
不据此改变生产默认 idle。最终报告 shared-d1-pool-paper-final.json 保留本地。
前一版报告 shared-d1-paper-mixed60.json 的37.6/37.9秒和1568.6/1570.5MiB
是中间版本，已被最终固定池/独立客户端预算结果 superseded，文件保留。
另用 d35d51e 原始源码快照、相同解释器/依赖/模型/论文和当前相同混合调度
复测：旧实现以 UNAVAILABLE / worker response timed out 失败，不能把未完成
的旧运行当作完整大论文内存基线。有效的定量前后比较仍是上述三轮固定输入；
大论文结果说明当前负载完成及资源回收，不推断相对旧版的内存改善比例。
不把论文内容/产物提交仓库。

复现入口（验证依赖见 requirements-validation.txt）：

    .venv/bin/python -m unittest discover -s pdf_parser/papers_parser -t pdf_parser -p 'test_*.py'
    .venv/bin/python -m unittest discover -s pdf_parser/papers_parser -t pdf_parser -p test_worker_transport.py
    .venv/bin/python -m unittest discover -s scripts -p test_desktop_validation.py
    go test ./...
    go build ./...
    (cd web && npm run build && npm run test:reading-note)
    .venv/bin/python scripts/benchmark_parser_shared.py --jobs 8 --output tmp/desktop-d0/after.json
    .venv/bin/python scripts/run_desktop_integration.py --redis-server <owned-native-redis-server> --output-dir tmp/desktop-d0/native

最后一个命令要求 PAPERS_MANAGER_TEST_POSTGRES_DSN 指向专用测试实例，
不能对用户数据库运行；它会启动自己的 Redis 并运行 go test -json -count=1 ./...。

## D0 结论与剩余发行验收

- Go for D2：保留Asynq、每类单一共享重包worker与内部固定线程池；
  Windows Redis候选来源哈希匹配，项目真实队列/Lua/恢复原生验证通过。
- 最初“CI尚未运行、D1尚未修改生产实现”的结论已被本次实施 superseded。
  Linux/macOS 已通过完整原生 run 40081950947；Windows普通账号 PostgreSQL
  17.11 验收已通过（run 40083445477 的 postgres-ordinary-user.json，
  is_admin=false）。Windows模型启动专项 run 40084025951 已通过。
  首次 Windows 管道监视线程触发 NumPy DLL 导入死锁，已用原生堆栈定位，
  改为仅依靠 Job Object 监视父进程，保留 Unix 管道监视：
  https://github.com/numpy/numpy/issues/24290。
  最终完整矩阵 [run40086630821](https://github.com/Aciv/paper-master/actions/runs/40086630821)
  全部success：三系统完整job及Windows18项worker专项。
  源码8632b66e80886c612f30e8f4fe7eb0699e85aed1；修复位于PR#1，未合并main。
  原生数据库8项required tests无skip；CI的4项生产CRF权重专项仍明确skip，
  小型生成CRF模型已执行读写/推理测试，生产模型分发与质量留在D3验收。
- Release no-go 仍成立：自包含解释器/数据库目录、权重分发与各依赖许可
  材料尚未验收；CI 工具链和 runner 自带安装不代表用户无需安装的发行物。
- 无数据库 migration、API/protobuf 或公开环境变量默认值变更。
  新内部 --shared 标志、进程令牌/父管道环境变量不面向应用调用者；
  PARSER_WORKER_STARTUP_TRACE=1 仅为 CI 启动卡住时输出堆栈诊断。
- D2 仍为 Launcher、私有运行时路径/生命周期及静态前端托管；D3 为
  实际自包含目录、版本/哈希锁、许可证、签名和干净系统发行验收。
