# paper_manager 中文文档

项目入口：[中文 README](../README.md) · [英文 README](../README.en.md)

## 使用与运行

| 文档 | 适合查什么 |
| --- | --- |
| [运行与配置](running.md) | 三种启动方式、依赖准备、环境变量、日志、端口和常见问题 |
| [MCP 与技能接入](mcp/agent-installation.md) | Agent 客户端配置、技能安装、连接验证 |
| [解压运行与维护](../packaging/desktop/INSTALL.md) | 用户数据位置、首次初始化、可选安装及更新限制 |
| [发行包构建](../packaging/desktop/BUILD.md) | 构建与归档的区别、目标平台和离线验证；原有打包材料保留原语言 |

## 架构与契约

| 文档 | 内容 |
| --- | --- |
| [总体架构与实现](architecture.md) | 服务分工、存储、解析与检索流程、并发、笔记、接口入口和扩展位置 |
| [阅读笔记契约](reading-note-agent-contract.md) | 章节读写、revision、章节描述和证据链接 |
| [MCP 工具契约](mcp/papers-manager-tools.md) | 工具分组、参数、返回值、错误和操作边界 |
| [MCP 机器清单](mcp/papers-manager-tools.json) | 供程序使用的工具名称与输入模式；标识符和模式保持原格式 |
| [翻译模块扩展](translation-providers.md) | 前端翻译提供方注册、配置、缓存和接入限制 |

README 只介绍功能与快速启动。总体实现以架构文档为入口；专项文档保留需要精确遵守的
接口与格式约定，不在多个位置复制完整契约。配置默认值以源码和示例配置为准。

## 其他资料

- [Agent 技能源包](../agent-skills/README.md)：入库、分层阅读、笔记模板与比较报告。
- [开发脚本与测试入口](../scripts/README.md)：按类别运行 Go、Python、前端、API 和桌面检查。
- [平台验收记录](../packaging/desktop/VALIDATION.md)：每份候选包实际验证的平台和源码版本。
- [发行与维护规则](../packaging/desktop/RELEASE_POLICY.md)：版本、检查点、恢复和受控清理。
- [第三方依赖许可](../packaging/desktop/THIRD_PARTY_NOTICES.md)：项目 MIT 之外的组件条款。

`docs/` 的说明正文使用中文；命令、工具名、协议字段和机器清单保留原标识符。
