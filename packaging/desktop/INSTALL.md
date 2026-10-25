# paper_manager 解压运行与维护

版本 1.0.0。完整解压对应平台归档，以普通用户运行；无需安装 Docker、Go、Node、
Python、PostgreSQL 或 Redis。所有运行组件随包提供。当前候选包未签名，尚未发布正式 Release。

## 默认：解压后运行

| 平台 | 归档和入口 | 默认数据目录 |
| --- | --- | --- |
| Windows x86_64 | ZIP；双击根目录 paper_manager.exe | %APPDATA%/papers-manager |
| macOS arm64 | tar.gz；终端运行 ./paper_manager | ~/Library/Application Support/papers-manager |
| Linux x86_64 | tar.gz；终端运行 ./paper_manager | $XDG_DATA_HOME/papers-manager |

Linux 未设置 XDG_DATA_HOME 时使用 ~/.local/share。保留整个解压目录，不能只复制 EXE。
Go 已编译成原生程序，React 已构建成静态网页；运行时不需要 Go 或 Node 环境。
私有 Python 用于解析器；默认启动不经过 Python 安装脚本。

1. 打开根目录 paper_manager.exe / paper_manager，浏览器显示管理页。
2. 首次点击“首次初始化 / 重试”，完成后点击“启动”。初始化只创建本应用私有数据库，
   不安装全局服务、不修改 PATH。正常重启复用已有数据。
3. 通过管理页查看服务状态和日志，完成使用后点击“退出 Launcher”。关闭浏览器不会停止服务。

程序保留在解压目录，数据库、论文、图片和 notes 存在独立数据目录。
不要使用 sudo 或管理员身份启动。可传 --data-dir 指定绝对数据路径，
--open-browser=false 禁止自动打开浏览器；终端仍打印管理地址。
Windows 包所在目录和数据目录请使用完整 ASCII 路径，含中文等字符时可能被底层运行时拒绝。
例如将 ZIP 解压到 C:/paper_manager，并运行：

```bat
paper_manager.exe --data-dir C:/paper_manager_data
```

首次初始化失败时查看管理页错误和数据目录 logs，排除原因后重试；不要删除数据库或配置强行重建。
签名/系统隔离和首次下载提示可能需要用户确认；当前不是 macOS .app 或 Windows 图形安装器。

## 数据保留与更新边界

默认模式不是“把数据也放在 U 盘里”的便携数据库。退出后删除解压目录不会删除用户数据，
但再次使用需要保留原程序目录；不要移动正在运行的目录。
运行另一个构建时不会隐式接管原数据库，也不会沿用旧绝对路径偷偷运行旧程序。
同构建完整副本且原目录仍在时可校验后重新绑定；原目录缺失或构建不同时提示退出并保留数据。
当前免安装模式没有原地更新或自动转为受管理安装的功能，不能靠直接覆盖旧文件完成升级。

## 可选：受管理安装

已有 Install.cmd / Install.command / Install.sh 仍保留供需要受管理版本目录与升级维护的人使用；
普通解压运行无需执行。脚本使用随包的私有 Python 调用原生 Go Launcher 维护命令，
复制组件、初始化并验证后启动，不要求系统安装 Python。

| 平台 | 可选入口 | 默认受管理程序目录 |
| --- | --- | --- |
| Windows | Install.cmd | %LOCALAPPDATA%/paper_manager-program |
| macOS | Install.command | ~/Library/Application Support/paper_manager-program |
| Linux | ./Install.sh | $XDG_DATA_HOME/paper_manager-program |

程序和数据目录必须独立，不能互相嵌套。Windows 安装器在操作前拒绝非 ASCII 完整路径。
可传 --program-dir、--data-dir（绝对路径）和 --no-launch；首版不接管免安装、开发或 Docker 已有数据。
快捷方式默认不创建。--create-shortcut 或 --shortcut-dir 显式启用，创建失败只提示。
安装后使用程序目录的固定入口：Unix 为 paper_manager，Windows 为 paper_manager.ps1。
Windows 命令示例：

```bat
powershell -NoProfile -ExecutionPolicy Bypass -File "%LOCALAPPDATA%\paper_manager-program\paper_manager.ps1"
```

以下升级、检查点、清理和卸载命令仅针对通过此安装器建立的受管理安装。

## 手动升级

1. 下载对应平台的新包，核对 SHA-256 后完整解压。
2. 在管理页退出原 Launcher，等待所属服务停止。
3. 执行新包的安装入口，沿用原来的程序和数据目录；自定义目录必须再次明确传入。
4. 安装器验证平台、版本和文件清单，冷停复制持久化目录，再运行隔离的迁移/健康探针。
5. 通过后切换固定入口；失败则尝试恢复旧程序和旧数据，保留诊断和恢复材料。

同一版本同一归档重复安装可修复入口；同一版本不同构建被拒绝，升级必须提高版本。
PostgreSQL 主版本固定为 17，data_format/task_payload 固定为 1；不兼容升级明确拒绝。
没有自动下载更新器。检查点包含数据库、Redis、配置、PDF/图片、notes 和识别产物，
需要足够本机空间；本机检查点不能替代异机备份。

## 查看、恢复和清理

先退出运行实例。从下载归档调用私有解释器执行 installer/install.py：

```sh
# Linux/macOS；Windows 将解释器改成 bundle/python/python.exe。
./bundle/python/bin/python3 -I -B installer/install.py status
./bundle/python/bin/python3 -I -B installer/install.py retry --no-launch
./bundle/python/bin/python3 -I -B installer/install.py cleanup
./bundle/python/bin/python3 -I -B installer/install.py restore --checkpoint-id ID --confirm-restore
./bundle/python/bin/python3 -I -B installer/install.py delete-checkpoint --checkpoint-id ID --confirm-delete
```

自定义目录时，所有命令加上原来的 --program-dir 和 --data-dir。
status 列出版本和检查点占用；restore 替换检查点范围内的当前数据，需要确认，
并保留恢复前副本。检查点之后的新写入不会被合并。
恢复要求旧程序仍在；若已卸载，可直接调用下载包 launcher 的 --maintenance restore，
通过 --source-bundle 指向校验值匹配的原版本本地 bundle。

暂存闲置超过 24 小时可回收；旧版本/检查点超过 30 天才考虑回收，并保留最近两个及恢复引用。
失败恢复材料受到保护，未知目录和手动备份不自动删除。显式 delete-checkpoint 同时删除
该检查点所属的已知恢复副本，请先确认不再需要其中数据。业务 PDF/图片/notes 不做自动回收。

## 卸载

先退出 Launcher。使用程序目录的 uninstall（Unix）或 uninstall.ps1（Windows），
也可从下载归档执行 Uninstall.sh/Uninstall.command/Uninstall.cmd 并传入原目录。
安装目录卸载脚本会将 Windows 辅助程序复制到数据目录的 maintenance/staging，
避免删除正在执行的 EXE，也不依赖系统 TEMP 的权限。辅助程序有所有权记录；
正常卸载后移除，异常遗留由后续受控暂存清理处理。
卸载删除受管理程序和未被修改的快捷方式，保留数据、检查点和所有权记录。
重新安装精确原版本可继续使用数据；首版不支持卸载后直接安装任意新版本。
没有“一键彻底删除数据”功能，需要时由用户确认并手动处理自己的数据目录。

## 许可与验证边界

项目自有代码为 MIT，见仓库 LICENSE；第三方组件保留自身条款，见
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。签名用于验证发布者身份和文件完整性；
当前没有签名或 macOS 公证，与应用登录令牌无关。
平台证据和未完成门槛见 [VALIDATION.md](VALIDATION.md)，构建成功不能代替安装链路通过。
