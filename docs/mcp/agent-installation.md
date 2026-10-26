# MCP 与 Agent 技能接入

[文档导航](../README.md) · [工具契约](papers-manager-tools.md) ·
[技能源包](../../agent-skills/README.md)

本文说明如何连接现有 MCP 服务并安装两套技能。应用不附带 Agent 客户端或模型；
客户端接入与模型凭据由用户自行配置。以下保留现有客户端配置示例，具体命令选项以
本机客户端版本为准，宿主升级不改变服务端工具契约。

## 1. 启动 MCP

### 源码模式

在根目录 `.env` 设置：

```env
MCP_ENABLED=true
MCP_ADDR=127.0.0.1:8090
MCP_TOKEN=
MCP_MAX_REQUEST_BYTES=8388608
MCP_MAX_IMAGE_BYTES=12582912
MCP_IMPORT_CONCURRENCY=20
# 按需启用本地文件导入，目录应尽量小；省略则禁用。
# MCP_LOCAL_IMPORT_ROOT=/absolute/path/to/import-root
```

运行 `make dev`，或在已有数据库、Redis、解析器的前提下运行 `make dev-app`。
MCP 与 API 在同一 Go 进程中，但使用独立端口。源码默认地址为：

```text
http://127.0.0.1:8090/mcp
```

`MCP_ADDR` 只写 `host:port`，不是完整 URL。桌面 Launcher 会生成自己的端口和 token，
从管理页复制实际连接配置，不要照搬开发模式的固定地址。

### 传输与 token

服务端只支持 **MCP Streamable HTTP**，不提供原生 stdio 或旧 HTTP+SSE。
Claude Code、Codex、OpenCode、DeepSeek Harness 的对应客户端可直接连接 HTTP；
仅支持 stdio 的宿主见后面的桥接说明。

`MCP_TOKEN` 为空会关闭认证；非回环监听也只警告，不会拒绝启动。
设置 token 后所有请求必须携带：

```http
Authorization: Bearer <MCP_TOKEN>
```

客户端可通过 `PAPERS_MANAGER_MCP_TOKEN` 环境变量提供同一个值，变量名不必与服务端相同。
不要把真实 token 提交到 `.mcp.json`、`config.toml`、`opencode.json` 或 DSH 配置。
客户端必须从设置过变量的终端启动。未启用 token 时，省略下面示例中的认证头或对应选项。

### 连通性检查

```bash
curl -sS http://127.0.0.1:8090/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"1"}}}'
```

这只是验证地址、协议和 token。技能执行论文与笔记操作时应使用 MCP 工具，
不能在 MCP 不可用时自行改用 curl 或数据库绕过契约。

## 2. 客户端配置位置

| 客户端 | MCP 配置 | 技能目录 |
| --- | --- | --- |
| Claude Code | `.mcp.json` 或 CLI | `.claude/skills/`、`~/.claude/skills/` |
| Codex | `~/.codex/config.toml` 或 CLI | `.agents/skills/`、`~/.agents/skills/` |
| OpenCode | `opencode.json` 中的 `mcp` | `.opencode/skills/`、`.agents/skills/`、`.claude/skills/` |
| DeepSeek Harness | MCP Center 或 MCP 客户端插件 | `.dsh/skills/`、`~/.dsh/skills/`、`.agents/skills/` |

服务端清单有 48 个工具。宿主可能加命名空间前缀，不必逐个手动注册工具。
配置后重启或重新加载客户端，再检查工具是否可见。

## 3. Claude Code

CLI 示例：

```bash
claude mcp add --transport http --scope user \
  --header "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}" \
  papers-manager http://127.0.0.1:8090/mcp
```

选项放在服务名之前；使用 `--scope project` 可生成项目配置。
也可在项目根目录写 `.mcp.json`：

```json
{
  "mcpServers": {
    "papers-manager": {
      "type": "http",
      "url": "http://127.0.0.1:8090/mcp",
      "headers": {
        "Authorization": "Bearer ${PAPERS_MANAGER_MCP_TOKEN}"
      }
    }
  }
}
```

项目级配置可能要求首次授权。使用 `claude mcp list` 或会话中的 `/mcp` 检查。
技能可完整复制到用户目录：

```bash
mkdir -p ~/.claude/skills
cp -R agent-skills/papers-manager-library ~/.claude/skills/
cp -R agent-skills/papers-manager-reading ~/.claude/skills/
```

也可使用项目级 `.claude/skills/`。

## 4. Codex

CLI 示例：

```bash
codex mcp add papers-manager \
  --url http://127.0.0.1:8090/mcp \
  --bearer-token-env-var PAPERS_MANAGER_MCP_TOKEN
```

或在用户级 `~/.codex/config.toml`（或受信任项目的 `.codex/config.toml`）添加：

```toml
[mcp_servers.papers-manager]
url = "http://127.0.0.1:8090/mcp"
bearer_token_env_var = "PAPERS_MANAGER_MCP_TOKEN"
enabled = true
tool_timeout_sec = 120
```

这是 TOML，不是 Claude Code 的 JSON。URL 导入等调用可能等待远程请求，
`tool_timeout_sec` 可按需要设置。通过 `codex mcp list` 或会话中的 `/mcp` 检查。
技能安装到项目 `.agents/skills/` 或用户 `~/.agents/skills/`，见下文统一安装步骤。

## 5. OpenCode

项目 `opencode.json` 或用户 `~/.config/opencode/opencode.json` 示例：

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "papers-manager": {
      "type": "remote",
      "url": "http://127.0.0.1:8090/mcp",
      "headers": {
        "Authorization": "Bearer {env:PAPERS_MANAGER_MCP_TOKEN}"
      },
      "enabled": true,
      "timeout": 120000
    }
  }
}
```

环境变量替换使用 `{env:VAR}`，超时单位是毫秒。无 token 时省略 `headers`。
通过 `opencode mcp list` 和 `opencode mcp debug papers-manager` 检查。
技能可放 `.agents/skills/`，也可放 `.opencode/skills/` 或 `.claude/skills/`；
用户级目录为 `~/.config/opencode/skills/`。

## 6. DeepSeek Harness

DSH 使用通用 MCP 客户端即可，不需要另外实现一套论文工具。
现有示例使用社区 MCP Center 管理远程连接：

```bash
dsh plugin --profile web add dsh-mcp-center
```

在 Settings → MCP Center 中填写：

```text
name: papers-manager
transport: streamable-http
url: http://127.0.0.1:8090/mcp
Authorization: Bearer <token>
```

工具可能显示为 `mcp__<server>__<tool>`。使用前自行审阅第三方插件，
保存 token 的客户端配置应按私密文件处理。

也可在 `$DSH_HOME/profiles/<profile>/cordis.patch.yml` 挂载客户端包：

```yaml
insert:
  - id: papers-manager-mcp
    name: '@deepseek-ai/dsh-mcp-client'
    config:
      serverName: papers-manager
      transport: streamable-http
      url: http://127.0.0.1:8090/mcp
```

认证头与重载方式取决于所用 DSH 预览版本，应核对本机客户端文档。
技能发现目录依次包括项目 `.dsh/skills/`、`.agents/skills/`，以及用户
`~/.dsh/skills/`、`~/.agents/skills/`。
如后续制作专用插件，它只负责分发和配置，不应重写现有 MCP 工具。

## 7. 安装完整技能

源码位置：

```text
agent-skills/
  papers-manager-library/
    SKILL.md
    references/
  papers-manager-reading/
    SKILL.md
    references/
```

必须复制整个技能目录，包括按需读取的 `references/`，不能只复制 `SKILL.md`。
支持共享技能根目录的宿主可以使用：

```bash
mkdir -p .agents/skills
cp -R agent-skills/papers-manager-library .agents/skills/
cp -R agent-skills/papers-manager-reading .agents/skills/
```

Claude Code 可另复制到 `.claude/skills/`，或在支持目录符号链接的环境链接：

```bash
mkdir -p .claude/skills
ln -sfn "$PWD/.agents/skills/papers-manager-library" .claude/skills/papers-manager-library
ln -sfn "$PWD/.agents/skills/papers-manager-reading" .claude/skills/papers-manager-reading
```

不支持符号链接时直接复制。技能目录名应与 `SKILL.md` 的 `name` 一致。
客户端发现技能不等于已经连上 MCP；首次执行会检查工具能力。
MCP 缺失或不可达时应明确报告，缺少写工具时停止写笔记，不猜工具名、不改走旁路。

## 8. 仅支持 stdio 的客户端

可在客户端使用外部桥接，例如 [mcp-remote](https://github.com/geelen/mcp-remote)：

```bash
npx -y mcp-remote http://127.0.0.1:8090/mcp \
  --header "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}"
```

典型配置：

```json
{
  "mcpServers": {
    "papers-manager": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "http://127.0.0.1:8090/mcp",
        "--header", "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}"
      ],
      "env": {"PAPERS_MANAGER_MCP_TOKEN": "<set-locally>"}
    }
  }
}
```

具体变量替换方式由宿主决定。桥接需要客户端侧的 Node/npm，但不是 paper_manager
发行应用的运行依赖；它不会为服务端新增 stdio 入口。

## 9. Docker、WSL 与远程访问

### Docker

容器内监听 `127.0.0.1` 不能通过端口映射从宿主访问。
本仓库 Compose 已声明 MCP 端口映射，可在根目录 `.env` 设置：

```env
MCP_ENABLED=true
MCP_ADDR=0.0.0.0:8090
MCP_HOST_PORT=8090
MCP_TOKEN=<set-locally>
```

重新创建 API 容器后，宿主连接 `http://127.0.0.1:8090/mcp`。
非本仓库 Compose 部署的等价配置为：

```yaml
services:
  api:
    environment:
      MCP_ENABLED: "true"
      MCP_ADDR: "0.0.0.0:8090"
      MCP_TOKEN: "${MCP_TOKEN}"
    ports:
      - "127.0.0.1:8090:8090"
```

另一个容器中的 Agent 使用容器网络内 `http://api:8090/mcp`，并配置 token。
本地文件导入时需挂载目标目录，`MCP_LOCAL_IMPORT_ROOT` 和输入路径按 API 容器内视图填写，
不能直接传宿主机上 API 看不到的路径。

### WSL 与远程主机

Windows 客户端连接 WSL 服务时，先尝试 localhost 转发；不可达时检查 WSL 网络模式、
监听地址和防火墙。使用非回环地址时应设置 token。
远程主机可保留回环监听，通过 SSH 转发：

```bash
ssh -L 8090:127.0.0.1:8090 user@server
```

客户端仍连接本机 `http://127.0.0.1:8090/mcp`。直接网络访问应配置可达地址和 token；
当前没有按 Agent 或用户隔离资源的权限模型。

## 10. 验证与排查

先检查连接，再执行只读的 `papers_manager_library_search`，或查看 MCP `tools/list`。
四组工具分别涉及论文库、原文/笔记、Base 和引用。

| 现象 | 检查 |
| --- | --- |
| `401 unauthorized` | 服务端 token 与客户端头是否一致；环境变量是否已传入客户端进程 |
| `connection refused` | API 是否运行、MCP 是否启用、端口是否映射/可达 |
| `404` | URL 是否以 `/mcp` 结尾 |
| 不带 token 也能调用 | 服务端 `MCP_TOKEN` 是否为空；空值就是关闭认证 |
| 客户端看不到工具 | 服务名、URL、客户端重载和技能前置检查结果 |
| 超时 | 远程请求耗时及客户端工具超时配置；不要无限堆积重试 |
| 技能未发现 | 目录名、`SKILL.md`、`references/`、宿主实际扫描的技能根目录 |
| Docker 连不上 | 容器监听 `0.0.0.0`、宿主端口映射、token 与容器路径 |
| `REVISION_CONFLICT` | 重读笔记目录/章节并重新规划编辑；不能沿用旧内容盲目覆盖 |

Agent 可执行修改和删除，与网页操作使用同一资源权限。按宿主能力设置确认策略；
本地导入只开放明确目录，不向客户端返回私有存储路径。

更多格式约束见[阅读笔记契约](../reading-note-agent-contract.md)、
[笔记标准](../../agent-skills/papers-manager-reading/references/notes-standard.md)和
[批量阅读与笔记生成](../../agent-skills/papers-manager-reading/references/batch-notes.md)。
