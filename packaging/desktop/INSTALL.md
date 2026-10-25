# paper_manager 桌面候选包使用说明

版本 1.0.0。当前归档为未签名的验证候选包，尚未发布正式 Release。
用户无需安装 Docker、Go、Node、Python、PostgreSQL 或 Redis；运行组件随包提供。
请完整解压归档，保留 bundle、installer 及同级脚本，不要只复制 launcher。

## 安装与启动

用普通用户执行，不要使用 sudo 或“以管理员身份运行”。

| 平台 | 解压后的入口 | 默认程序目录 | 默认数据目录 |
| --- | --- | --- | --- |
| Windows x86_64 | Install.cmd | %LOCALAPPDATA%/paper_manager-program | %APPDATA%/papers-manager |
| macOS arm64 | Install.command | ~/Library/Application Support/paper_manager-program | ~/Library/Application Support/papers-manager |
| Linux x86_64 | ./Install.sh | $XDG_DATA_HOME/paper_manager-program | $XDG_DATA_HOME/papers-manager |

Linux 未设置 XDG_DATA_HOME 时使用 ~/.local/share。Windows 创建桌面快捷方式，
macOS 创建 ~/Applications/paper_manager.app，Linux 写入用户 applications 目录。
快捷方式创建失败时保留安装，并在终端提示原因；可以使用程序目录内的固定入口。

安装器复制私有组件，以当前用户初始化本应用数据库，验证服务后启动 Launcher。
不安装全局数据库服务，不修改系统 PATH。程序和数据目录必须独立，不能互相嵌套。
可给入口脚本传入 --program-dir、--data-dir、--shortcut-dir，值均为绝对路径；
--no-launch 可在安装后暂不启动。首版不自动接管开发项目或 Docker 的已有数据。

安装后使用快捷方式，或程序目录内的 paper_manager（Unix）/paper_manager.ps1（Windows）。
关闭浏览器标签不会停止后台服务，请通过管理页退出 Launcher。
初始化失败时查看数据目录中的 logs，保留目录，排除原因后执行 retry。

Windows 安装器只接受完整路径均为 ASCII 的程序目录和数据目录；包含中文或其他非 ASCII
字符时，在开始安装前提示并退出，不移动或删除现有文件。可指定
--program-dir C:/paper_manager --data-dir C:/paper_manager_data；父目录和用户名部分也计入检测。
这是首版明确限制，不再依赖各卷可能禁用的 8.3 短路径。Linux/macOS 保留中文路径支持。
Windows 验收仍记录底层运行时对无短路径别名的中文目录明确拒绝，再完整验证 ASCII 安装方案。
首次下载提示、杀毒和系统隔离仍需用户机器验证。

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
安装目录卸载脚本会复制临时辅助程序，避免删除正在执行的 Windows EXE。
卸载删除受管理程序和未被修改的快捷方式，保留数据、检查点和所有权记录。
重新安装精确原版本可继续使用数据；首版不支持卸载后直接安装任意新版本。
没有“一键彻底删除数据”功能，需要时由用户确认并手动处理自己的数据目录。

## 许可与验证边界

项目自有代码为 MIT，见仓库 LICENSE；第三方组件保留自身条款，见
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。签名用于验证发布者身份和文件完整性；
当前没有签名或 macOS 公证，与应用登录令牌无关。
平台证据和未完成门槛见 [VALIDATION.md](VALIDATION.md)，构建成功不能代替安装链路通过。
