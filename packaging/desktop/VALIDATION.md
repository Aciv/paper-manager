# 当前验收与限制

历史 D0–D4 安装器、平台和依赖验收记录保留于
[归档记录](../archive/desktop/VALIDATION.md)，不能当作当前工作区的通过证明。

## 已定位的上次 Action 问题

运行 [40203852674](https://github.com/Aciv/paper-master/actions/runs/40203852674)
测试的是提交 `0eda69b`，并非当前工作区；三个平台构建成功，Linux 运行测试通过。
Windows 在 ASCII 重试时，初始化完成紧接启动返回 409；当前实现将终态发布和任务准入释放
放在同一临界区，避免状态已显示停止、内部仍拒绝启动的窗口。
macOS 主服务测试通过，旧安装器升级测试失败；当时丢弃 Launcher 控制台输出，无法证明根因。
现在保存启动输出，并兼容同一运行目录的符号链接别名。

当前发行路线为解压运行，旧安装器升级检查归档；新的完整验收针对实际精简用户包，
覆盖默认包内数据、停服配置、启动参数应用和对应平台卸载。
平台集成测试统一使用 ASCII 目录，不以中文目录作为发行条件；README 提示用户使用英文路径。

## 本轮状态

Launcher 配置与便携布局的本机检查已通过：Go 全量测试/构建、Launcher race、32 项桌面工具、
154 项 Python、工作流静态检查，以及 Linux 普通 UID1000 离线只读环境中的真实初始化、
启停、配置应用、根目录环境文件读取和卸载数据保留。Windows 包装脚本的模拟执行回归通过。
本地包复用已验证原生依赖，仅作工作区 fixture，不冒充当前提交的正式发行产物。
前端由另一个 agent 完成优化与验证；用户允许共同提交，最后集中 Full tests 覆盖完整快照。
当前新布局仍需三平台最终原生验收，不能据旧记录标记全部通过。


## 本轮集中 Action

[40262351015](https://github.com/Aciv/paper-manager/actions/runs/40262351015)
准确检出 `5ff4b37`；真实 PostgreSQL/Redis 的 Go race 全量通过。
三平台的打包工具测试准备缺 `grpcio`，提前失败，尚未生成用户包；已补验证依赖，
全新 venv 仅安装 requirements-validation 后 32 项工具测试通过。
后续追加的根目录 skills 需要新提交验收，不能将该运行当作新包通过记录。

新增 skills 的实际 Linux 便携归档已完成离线普通用户验证：运行中保护、退出后删除原样
skills、保留 data/ 与根目录环境文件。Go 测试另覆盖修改技能和新增个人文件的保留行为。
这仍是本机 fixture 验证，不替代新提交的原生三平台验收。


## Windows 自删除入口修复（2026-10-26）

[40263361301](https://github.com/Aciv/paper-manager/actions/runs/40263361301)
检出 `ad55edd`，三平台构建、数据库 Go race、Linux/macOS 原生测试通过。
Windows 已通过主服务、PDF/笔记、停服配置和并发参数检查，最后确认卸载返回非零：
记录先显示 `Program removed. User data kept.`，随后 `The batch file cannot be found`。
根因是卸载子进程删除正在执行的 CMD 后，CMD 继续读取该文件。

修复入口先结束批处理文件上下文，再同步执行 PowerShell，保留其真实退出码。
Windows 本机原生回归已通过：旧入口复现假失败、删除后返回 0/23 的准确传递、
当前 Launcher 的取消/身份校验拒绝/成功删除，以及个人文件、data 和环境文件保留。
不是用忽略退出码或放宽最终断言修复。Windows 构建后增加真实 Launcher 入口回归，
现有完整普通账户验收继续保留。本轮未重跑 Actions，不把本机聚焦结果当成新提交全矩阵通过。

## 异常退出与目录移动（2026-10-26）

旧版本把 postmaster.pid 的存在直接当作活跃实例，阻断强杀后的正常恢复。
现在 Launcher 验证私有实例标记后启动 PostgreSQL，由数据库自己检查活跃 PID、
锁和 WAL；Launcher 不删除 PID 文件，不重建已有数据库，不接管残留活跃服务。
Windows 服务不再共享 Launcher 控制台，保留 kill-on-close Job 兜底；
信号处理提前注册，打开浏览器不再阻塞服务启动和退出处理。
关闭窗口的收尾仍受 Windows 时限约束，不能用优雅退出替代崩溃恢复。

便携配置增加可选 bundle_hash，记录运行包指纹；移动后重新绑定组件路径，
不修改密码、端口或用户数据。旧配置缺少指纹时，仅在 data/ 与完整运行包一同
移动且旧运行目录已不存在的场景引导绑定；无法确认旧版本一致性，这是旧配置
兼容的边界。新配置通过指纹验证同一构建，外置旧数据仍保留原版本检查。

新增 Windows 原生回归，使用显式选择的 PostgreSQL 和全新临时数据库：
两次强杀管理进程、Job 回收、残留 PID 的原生恢复、SQL 数据保留、活跃实例拒绝、
数据目录改名后恢复及最后正常停库。它验证进程崩溃，不模拟真实磁盘损坏或断电。
此项接入 Windows 构建后的现有完整工作流，本轮不单独触发 Actions。

```powershell
# 使用普通 PowerShell 窗口，不能以管理员身份运行。
$env:PAPERS_MANAGER_TEST_POSTGRES_BIN = 'C:\path\to\bundle\postgres\bin'
go test ./internal/runtime/desktop -run '^TestWindowsPostgresOwnerCrashRecovery$' -count=1 -timeout=3m
```

运行40280271827的Linux/macOS整包和数据库验收通过，Windows新增恢复专项
错误地直接使用管理员runner启动PostgreSQL，因权限拒绝而失败；这发生在强杀前。
该接线已改为构建阶段只编译测试EXE，将其作为独立验证工具传给native-test，
通过既有普通账户包装器执行后继续完整Launcher与卸载验收。测试程序不进入用户压缩包。
专项必须实际通过，零退出但跳过也判失败；stdout、stderr与recovery.json随
test-results-Windows产物保留。测试自身提前拒绝管理员身份，避免等待连接超时。

运行40282459718中，普通账户PG强杀恢复专项通过（17.03秒），所有服务也已就绪。
Windows后续来源检查把CREATE_NO_WINDOW产生的系统控制台宿主误判为外部运行时。
验收现在仅允许系统目录中确切的System32/conhost.exe路径，其他外部程序及
别处同名文件仍失败；业务组件和私有DLL检查保持不变。完整进程路径在断言前
写入报告，系统辅助进程另列system_helper_paths，失败信息包含具体外部路径。
