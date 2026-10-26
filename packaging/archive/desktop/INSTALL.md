# paper_manager 1.0.0

1. 完整解压，保留程序旁的 bundle 文件夹。
2. Windows 打开 paper_manager.exe；Linux/macOS 运行 ./paper_manager。
3. 首次在管理页点击“首次初始化 / 重试”，完成后点击“启动”。
4. 结束使用时点击“退出 Launcher”。关闭浏览器不会停止后台服务。

请用普通用户运行，不要使用 root、sudo 或“以管理员身份运行”。
Windows 请把程序和数据放在完整 ASCII 路径中。
浏览器未自动打开时，手动访问终端打印的 Management 地址。

数据默认保存在：

- Windows：%APPDATA%/papers-manager
- macOS：~/Library/Application Support/papers-manager
- Linux：$XDG_DATA_HOME/papers-manager；未设置时为 ~/.local/share/papers-manager

需要更换数据目录时，启动程序加 --data-dir 绝对路径。
不要移动运行中的程序，也不要直接覆盖旧文件升级。

卸载前先通过管理页退出 Launcher，再运行同目录的 Uninstall 入口并确认。
卸载移除程序文件，保留论文、笔记和数据库。需要彻底清除时，请自行确认并删除上述数据目录。
