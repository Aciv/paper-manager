# 加载 paper_manager 技能

- papers-manager-library：导入论文、管理源信息和文档集合。
- papers-manager-reading：阅读论文、生成笔记、定位证据和比较集合内论文。

将这两个文件夹完整复制到 Agent 的技能目录，保留内部 references 文件夹：

- Claude Code：~/.claude/skills/
- Codex：~/.agents/skills/
- 其他客户端：放入该客户端支持的技能目录。

这两个技能依赖 paper_manager 的 MCP 服务。先在 Launcher 管理页启动服务，
再从管理页的 MCP 页复制实际连接配置到 Agent；不要使用固定示例端口。
技能本身不包含模型客户端、API Key 或运行实例的 token。

重新加载客户端后，按需要使用 papers-manager-library 或 papers-manager-reading。
复制到客户端目录的技能由用户自行管理；卸载程序不会删除该副本。
