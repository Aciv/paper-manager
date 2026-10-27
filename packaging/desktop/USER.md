# paper_manager 1.0.1

1. 完整解压，保留程序旁的 bundle 文件夹。
2. Windows 打开 paper_manager.exe；Linux/macOS 运行 ./paper_manager。
3. 首次在管理页点击“首次初始化 / 重试”，完成后点击“启动”。
4. 结束使用时点击“退出 Launcher”。关闭浏览器不会停止后台服务。

请用普通用户运行，不要使用 root、sudo 或“以管理员身份运行”。
建议使用纯英文路径，避免中文路径带来的组件兼容问题。
浏览器未自动打开时，手动访问终端打印的 Management 地址。
终端显示 Launcher 的版本清单读取、配置适配、初始化、启停阶段和错误摘要。
正常启动不扫描整个程序包计算哈希。
等待超过 5 秒会显示当前阶段和已等待时间；数据库恢复、结构检查及必要升级完成前请保持程序运行。
启动早期的校验提示先显示在终端，配置加载后的提示同时保存在 data/logs/launcher.log。
管理页概览显示当前处理阶段、已等待时间及最近 Launcher 日志，日志页也可选择 launcher。
各服务的内部日志仅保存在 data/logs/，不会刷到终端。

数据保存在解压目录的 data/，包括数据库、论文、笔记、配置和日志。
Launcher 开着、其他服务停止时，可在管理页“配置”修改端口和并发参数。
端口配置在程序根目录 launcher.json，默认管理页/API/MCP/PostgreSQL/Redis/PDF 解析依次为 28450–28455。
启动会检查六个端口；被占用或被系统禁止时列出原因，请手动修改 launcher.json 后重启，不自动换端口。
管理页修改端口会同步 launcher.json；直接修改 data/runtime.json 的端口不会覆盖它。
其他完整配置是 data/runtime.json，可通过管理页查看或打开编辑器修改。
需要 Agent 技能时，将 skills/ 下的两个文件夹完整复制到客户端技能目录，见 skills/README.md。
MCP 的实际地址与连接配置从管理页复制。
根目录 .launcher_env 是可选启动配置；修改后需退出并重启 Launcher。
未填写的选项保持默认值；高级用法：--data-dir 绝对路径 指定其他数据目录。
也可以正常退出后，将完整 data/ 复制到新版解压目录，或用 --data-dir 指向原数据目录。

不要移动或替换运行中的程序。手动更新时，先通过管理页退出 Launcher，
完整解压新版，再把原来的整个 data/ 放入新版目录；保留 data/runtime.json，
其中保存了数据库密码和 MCP token。需要保留端口与自定义配置时，一并复制 launcher.json 和 .launcher_env。
已有数据但缺少 launcher.json 时会用旧端口生成；新版压缩包自带的 launcher.json 则以文件中的端口为准。
新版会使用当前目录中的组件并沿用原数据，启动时自动执行尚未应用的数据库迁移。
原地更新请整体替换 bundle/ 和对应程序入口，避免合并覆盖留下旧程序文件；
个人修改的 skills/ 和其他个人文件自行保留。涉及数据结构迁移时，建议先保留一份停服后的 data/ 副本。
退出后可以整体移动或改名解压文件夹，保留其中的 bundle/ 和 data/。
意外关闭或系统崩溃后，重新启动会由数据库检查并恢复未正常关闭的数据；
请勿手动删除 postmaster.pid、数据库文件或重新初始化来绕过错误。
如果恢复失败，保留 data/，从管理页查看 PostgreSQL/Redis 日志。
卸载前先通过管理页退出 Launcher，再运行同目录 Uninstall 入口并确认。
卸载保留 data/、launcher.json、.launcher_env 和旁边的其他个人文件；修改过的 skills/ 也会保留。
彻底清除时，请自行确认并删除数据目录；直接删除整个解压文件夹也会删除其中的数据。
