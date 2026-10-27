# Docker 发行配置与本机验证

当前完成镜像整理和本机验证准备，**尚未发布 GHCR 镜像**。
直接 `docker compose pull` 目前不能下载本项目发行镜像。
现有仓库根目录 `docker-compose.yml` 继续用于从源码构建；这里提供独立的发行配置。

## 1. 文件用途

| 文件 | 用途 |
| --- | --- |
| compose.yaml | 仅引用镜像的部署文件，无需源码与编译工具 |
| compose.build.yaml | 维护者本机构建覆盖文件，不是用户启动所必需 |
| .env.example | 版本、镜像前缀、端口、数据库密码和并发设置示例 |
| verify_parser.py | 镜像构建时严格核验两个小模型的哈希和 CRF 加载，不能静默降级 |

目前只验证 Linux 容器的 `linux/amd64` 路线，不据此宣称 ARM 或 NAS 全面兼容。
Windows/macOS 使用 Docker Desktop 的 Linux 容器环境；本轮没有新增原生平台测试或 Actions。
Docker 版直接由 Compose 管理服务，不运行桌面 Launcher，也不使用 `.launcher_env`。
首次启动由 PostgreSQL 镜像初始化数据库，API 执行项目迁移，无需点击桌面版初始化按钮。

## 2. 维护者先在本机制作镜像

从仓库根目录进入此文件夹：

```bash
cd packaging/docker
cp .env.example .env
```

编辑 `.env`：

```dotenv
PAPER_MANAGER_IMAGE_PREFIX=paper-manager-local
PAPER_MANAGER_VERSION=1.0.1
POSTGRES_PASSWORD=your_local_password
WEB_PORT=5173
MCP_PORT=8090
```

数据库密码使用字母、数字、下划线或连字符，避免数据库 URL 转义问题。
可选填写 `MCP_TOKEN`；填写后，Agent 客户端也要携带相同 token。
与开发版同时运行时，改用未被占用的网页/MCP 端口。

```bash
docker compose -f compose.yaml -f compose.build.yaml build
docker compose -f compose.yaml up -d --no-build
```

打开 `http://127.0.0.1:5173`；修改过端口时替换为实际值。
MCP 地址为 `http://127.0.0.1:8090/mcp`，不是网页端口。
数据库、Redis 和解析器没有宿主机端口映射；网页和 MCP 仅监听本机。

本机构建代理只通过 build args 传递，不写入运行镜像的环境变量。
如果 Linux/WSL 的 Docker 默认桥接子网与宿主网关重叠，导致构建代理或 DNS 不通，
可仅在构建时尝试：

```bash
PAPER_MANAGER_BUILD_NETWORK=host docker compose -f compose.yaml -f compose.build.yaml build
```

此选项不修改 Docker 守护进程、容器运行网络或现有服务。正常环境保持默认即可。
后续正式发行时，部署文件可独立分发，用户只需 `pull` 和 `up -d`；镜像发布工作流尚未实施。

## 3. 数据、配置与停止

数据默认存放在 **compose.yaml 所在目录** 的 `data/`：

```text
data/
  postgres/             # 数据库
  redis/                # 队列与持久化
  uploads/              # 原文件及 notes/ 章节 Markdown
  recognition-output/   # PDF 识别结果
  runtime/              # 结构缓存与 API 日志
  tmp/                  # 上传等临时文件
```

用 `PAPER_MANAGER_DATA_DIR` 指定其他位置；相对路径按 Compose 文件位置解析。
不要将桌面版已有的运行中数据库目录直接挂载给 Docker。
首次准备权限时，辅助容器只整理应用子目录；API 与解析器共享 UID/GID 10001。
PostgreSQL 和 Redis 保留各自镜像的初始化/用户机制，不修改宿主机账户。

日常操作：

```bash
docker compose -f compose.yaml logs --tail 100 api parser web
docker compose -f compose.yaml stop
docker compose -f compose.yaml up -d --no-build
docker compose -f compose.yaml down
```

容器删除或重建不会删除这里的 bind-mount 数据。不要删除 `data/` 来解决启动失败。
首次初始化后更改 `.env` 数据库密码，不会自动更改数据库里已经保存的密码。
修改业务配置后执行 `up -d --no-build`，让 Compose 重建受影响容器。
并发默认保持 Asynq 32、识别 4、提取 4、渲染 4、RPC 总预算 8；解析器保留 shared worker 模式。

## 4. 独立验收

从仓库根目录执行，需要 Docker Compose、Git 和 Python 3.12+；验证器仅使用标准库：

```bash
python scripts/docker/validate.py \
  --output-dir tmp/docker-validation \
  --image-prefix paper-manager-validation
```

验证器使用随机项目名、未占用端口、新数据目录和生成的测试凭据，不连接已有应用数据库。
先用 build override 构建三个应用镜像，再只用 image-only 部署文件运行。
检查网页、API、带 token 的 MCP、真实 PDF/Asynq/worker、blocks/Markdown、notes 和重建后数据保留。
结束时只停止自己的项目；报告、日志、私有 env 和测试数据保留在输出目录，便于检查。
输出目录包含本地测试凭据，不应提交或公开分发。

已构建同一份本地镜像时可加 `--skip-build`，每次运行仍创建全新的项目和数据。
本机网络有上述重叠问题时，可在命令前加 `PAPER_MANAGER_BUILD_NETWORK=host`。
验证报告记录镜像 ID、源提交和工作区是否有修改；本机未提交工作区验证不冒充正式发行。

## 5. 镜像内容

Go 镜像仅编译服务入口及所需源码，最终使用 scratch，不含 Go 编译器或开发环境。
Web 构建使用 npm 锁文件，最终只保留 Nginx、静态文件及项目/字体许可。
Python 复用 `packaging/desktop/requirements-runtime.lock` 的完整版本和 wheel 哈希，
保留完整运行依赖；只复制解析代码、协议和两个已授权的小模型，不复制训练目录或可选大模型。
基础镜像及 PostgreSQL/Redis 当前沿用既有版本系列；正式发布前仍需固定最终镜像 digest。
本轮不发布镜像、不改变协议或数据库 schema、不调整 Go module 名称。


## 6. 本轮本机证据（2026-10-26）

独立 `linux/amd64` 镜像已实际构建并验收通过：网页和 API、缺少 token 的 MCP 拒绝与正确
认证、真实 PDF 上传/Asynq/共享 worker/Markdown/blocks、笔记导入，以及 `down` 后重新 `up`
仍能读取原 Markdown 和笔记正文。验证项目已清理，原有 papersmanager 五个容器持续运行。
本机构建使用 `PAPER_MANAGER_BUILD_NETWORK=host` 绕过当前 WSL/默认桥接网段重叠。
报告位于 `tmp/docker-c1-runtime/validation.json`；报告记录未提交工作区标记及三个本地镜像 ID，
不是 GHCR 发布或 ARM 验收记录。
