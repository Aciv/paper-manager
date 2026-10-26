# 运行与配置

[文档导航](README.md) · [总体架构](architecture.md)

三种方式复用同一业务实现，但配置入口和数据目录不同。不要同时用相同端口运行完整
Compose 栈和开发栈，也不要让多个实例直接共用同一套私有数据库目录。

## 1. 解压运行包

当前提供的是候选包构建框架，并非已发布的正式下载渠道。源码、旧候选包和新归档布局的
验收范围不同，见[验收记录](../packaging/desktop/VALIDATION.md)。

完整解压对应平台的包，以普通用户运行根目录原生入口：

```powershell
# Windows；使用完整 ASCII 路径
.\paper_manager.exe --data-dir C:/paper_manager_data
```

```bash
# Linux / macOS
./paper_manager
```

管理页首次显示需要初始化。点击“首次初始化 / 重试”，完成后点击“启动”。
Launcher 会管理私有数据库、Redis、解析器和 API；用户不需要安装开发工具。
关闭浏览器不会退出后台服务，结束使用应点击“退出 Launcher”。

免安装包的数据默认保存在解压目录的 `data/`，不写入用户的 AppData 或用户主目录。
已有旧版数据不自动搬迁，可用 `--data-dir` 指定原目录。源码单独运行 Launcher 时仍保留用户目录默认值。

管理页、API 和 MCP 使用首次选择并持久化的端口，不一定是开发模式的 `8080/8090`。
从管理页取得实际地址。数据目录含 `runtime.json`、数据库、文件、笔记和日志；
`runtime.json` 含私有凭据，不能删除后用一份新配置强行打开原数据库。

Launcher 保持运行、业务服务全部停止（包括首次初始化之前）时，可在管理页“配置”
修改业务端口、Asynq 并发及 Python 识别/提取/截图并发和 RPC 预算。
保存到 `data/runtime.json`，下次初始化或启动立即生效；无需退出管理网页。
完整 JSON 可在同一页面查看、打开本机编辑器或重新读取。
管理页自身端口、组件路径和身份凭据需重启 Launcher；业务运行中拒绝修改。

### 自定义 Launcher

Launcher 不读取仓库根目录 `.env`。可选配置文件默认位于 解压根目录的 `.launcher_env`：

```env
LAUNCHER_DATA_DIR=C:/paper_manager_data
LAUNCHER_OPEN_BROWSER=true
LAUNCHER_STARTUP_TIMEOUT=90s
LAUNCHER_STOP_TIMEOUT=75s
LAUNCHER_EXTRACTION_ENGINE=hybrid
```

只设置需要改变的项。路径应为绝对路径；Windows 推荐使用正斜线。
也可用 `--env-file` 指定文件、用 `--data-dir` 指定数据目录，或用
`--open-browser=false` 禁止自动打开浏览器。命令行优先于对应环境变量，
进程环境优先于配置文件。更多选项见 [.launcher_env.example](../.launcher_env.example)。

解压目录的 `bundle/` 保存程序资源，`data/` 保存可写数据；当前免安装模式没有原地升级和自动接管其他构建数据的功能。
不要直接覆盖正在使用的程序，也不要移动运行中的数据库。
用户包只保留对应平台的 Uninstall 入口，移除程序并保留数据，见
[使用说明](../packaging/desktop/USER.md)。原有受管理维护工具仅用于内部验证。

### 自行构建包

构建准备可执行文件、私有解释器、依赖和静态网页，并生成 `bundle.json`：

```bash
python scripts/desktop/build_desktop_bundle.py \
  --output tmp/desktop-bundle --cache tmp/desktop-downloads \
  --revision "$(git rev-parse HEAD)" --profile hybrid
```

再把已构建的目录归档：

```bash
python scripts/desktop/package_desktop_bundle.py \
  --bundle tmp/desktop-bundle --output tmp/releases/desktop.tar.gz
```

Windows 输出使用 `desktop.zip`。`--revision` 记录源码标识，不会检出对应代码；
`--profile hybrid` 包含两个已授权 CRF 小模型，`rules` 仅用于规则提取。
归档脚本不编译、不下载依赖，也不会把 Linux 组件转换成 Windows 组件。
当前完整构建和验收在目标系统进行，详见[构建说明](../packaging/desktop/BUILD.md)。

## 2. Docker Compose

需要 Docker 和 Compose 插件。在仓库根目录执行：

```bash
docker compose up --build -d
docker compose ps
docker compose logs -f api parser
```

Web 默认为 <http://localhost:5173>，API 为 <http://localhost:8080>。
Compose 启动 PostgreSQL、Redis、解析器、API 和生产网页。按需修改根目录 `.env`，
但只有 `docker-compose.yml` 显式引用的变量才会传给容器；不能假定宿主机所有配置都会生效。
容器之间使用服务名连接，不使用宿主机开发配置中的 `localhost` 数据库地址。

| 资源 | 默认保存位置 |
| --- | --- |
| 原文件、图片和笔记 | 宿主机 `data/uploads/` |
| 识别输出 | 宿主机 `data/recognition-output/` |
| PostgreSQL | `postgres-data` 卷，实际名称可含 Compose 项目前缀 |
| Redis 持久化 | `redis-data` 卷，实际名称可含 Compose 项目前缀 |

```bash
docker compose down  # 停止并删除容器，保留卷和宿主机文件
```

`down -v` 会删除数据库和 Redis 卷；它不是普通停止方式。只备份 PDF 目录不能恢复完整
论文库，还需要数据库、配置和笔记等持久化资源。不要直接复制正在写入的数据库目录。

### 构建代理

代理在宿主机时，容器内的 `127.0.0.1` 不指向宿主机。常见配置为：

```bash
export HTTP_PROXY=http://host.docker.internal:7890
export HTTPS_PROXY=http://host.docker.internal:7890
export NO_PROXY=localhost,127.0.0.1,postgres,redis,parser,api,web
docker compose up --build -d
```

替换实际端口；Compose 已为 Linux 配置宿主机网关映射。如果 Linux/WSL 代理只接受
本机回环连接，可在支持 host 构建网络的环境使用：

```bash
BUILD_NETWORK=host HTTP_PROXY=http://127.0.0.1:7890 \
  HTTPS_PROXY=http://127.0.0.1:7890 docker compose up --build -d
```

这些代理参数用于镜像构建，不等于给应用的外部元数据请求配置了运行时代理。

## 3. 源码开发

`make dev` 面向 Linux/WSL 的 Bash 环境，需要 Docker Compose、Make、`ss`、Go
（版本见 `go.mod`）、Node.js 22+ 和 Python 3.12+。Go/Node 只用于源码开发和构建。

```bash
cp .env.example .env
python3 -m venv .venv
.venv/bin/python -m pip install -r pdf_parser/requirements.txt
npm --prefix web ci
make dev
```

Docker 仅启动 PostgreSQL 与 Redis；本机启动 API、Python 解析器和 Vite。
`scripts/dev/dev.sh` 会把 `.env` 设置传给源码进程，已导出的环境变量优先。
Go API 也会读取根目录 `.env`；直接运行 Python 时，应自行导出所需变量。

| 服务 | 默认地址 | 日志 |
| --- | --- | --- |
| Web | `http://127.0.0.1:5173` | `Logs/dev/web.log` |
| API | `http://127.0.0.1:8080` | `Logs/dev/api.log` |
| 解析器 | `127.0.0.1:50051`，gRPC | `Logs/dev/parser.log` |
| PostgreSQL / Redis | `127.0.0.1:5432` / `127.0.0.1:6379` | `make dev-infra-logs` |

按 `Ctrl+C` 停止三个源码进程，基础设施继续运行。常用命令：

```bash
make dev-start               # 只启动 PostgreSQL / Redis
make dev-app                 # 单独启动 API；安装 Air 时使用自动重载
make dev-parser              # 单独启动解析器
make dev-frontend            # 单独启动前端
make dev-logs SERVICE=parser # 查看解析器日志；省略 SERVICE 查看全部
make dev-status
make dev-stop               # 停止开发基础设施，保留卷
```

开发数据库与 Redis 使用独立的 `postgres-data-dev`、`redis-data-dev` 卷，
不会自动共享完整 Compose 栈的数据。修改源码通常只需重启对应进程。

### 修改端口与局域网访问

修改 API 端口时同步修改前端代理目标：

```bash
HTTP_ADDR=127.0.0.1:8081 VITE_PORT=5174 \
  VITE_API_PROXY_TARGET=http://127.0.0.1:8081 make dev
```

还可调整 `PARSER_GRPC_ADDR`。改开发数据库映射时，`POSTGRES_PORT` / `REDIS_PORT`
应与 `DATABASE_URL` / `REDIS_ADDR` 一致。不要与其他运行方式抢占相同端口。

局域网使用可设置 `VITE_HOST=0.0.0.0`，重启后访问宿主机 IP 的前端端口；
`/api` 由 Vite 代理，API 可以继续只监听本机。当前没有多用户登录权限体系，
只应在可信网络中开放。WSL 的 NAT 地址不一定可从局域网直达，应按实际网络模式设置转发。

原生 Windows 不直接使用 Bash 开发脚本；可在 WSL 中开发，或分别启动各组件。
解压发行包的 Windows 原生启动不依赖 WSL。

## 4. 按用途配置

完整键名与示例值见 [.env.example](../.env.example)。下表中的默认/示例值用于源码与
Compose；Launcher 的端口、路径和凭据由其配置单独管理。修改后重启对应服务。

| 类别 | 关键变量 | 说明 |
| --- | --- | --- |
| 连接 | `DATABASE_URL`、`REDIS_ADDR`、`PARSER_GRPC_ADDR` | 必须与实际服务地址一致；Redis 是异步任务的必要依赖 |
| 文件 | `UPLOAD_DIR`、`RECOGNITION_OUTPUT_DIR` | API 与解析器需访问相同文件；分进程/容器时核对挂载路径 |
| 大小 | `FILE_MAX_UPLOAD_SIZE` | 默认 104857600 字节；解析器和 gRPC 另有大小限制 |
| 网页 | `HTTP_ADDR`、`CORS_ORIGIN`、`VITE_API_PROXY_TARGET` | 区分监听地址、允许来源和前端代理目标 |
| 数据库 | `AUTO_MIGRATE`、`DB_MAX_OPEN_CONNS` | 默认自动迁移；使用外部迁移流程才关闭前者 |
| 日志 | `LOG_LEVEL`、`LOG_PATH` | `debug/info/warn/error`；日志可能含本地路径，分享前检查 |
| MCP | `MCP_ENABLED`、`MCP_ADDR`、`MCP_TOKEN` | 普通 API 默认关闭 MCP；token 非空时验证所有请求 |
| 本地导入 | `MCP_LOCAL_IMPORT_ROOT` | 默认禁用；启用时限制为一个明确的文件目录 |
| 检索 | `PAPER_CATALOG_PROVIDERS` | 示例为 arxiv、semantic_scholar、openalex、crossref |
| 检索凭据 | `SEMANTIC_SCHOLAR_API_KEY`、`OPENALEX_API_KEY`、`CROSSREF_MAILTO` | 按使用的提供方设置；不要写入版本库 |
| 引用检索 | `PAPER_REFERENCE_AUTO_RESOLVE` | 默认 false；提取后仍会匹配库内文档，外部引用检索按需触发 |
| 图片 | `PARSER_IMAGE_DPI` | 默认 144；提高精度会增加渲染时间、内存和文件体积 |

### 提取模式

| `PAPER_EXTRACTION_ENGINE` | 用途 |
| --- | --- |
| `hybrid` | 结合 CRF 与规则；缺少兼容模型时允许规则回退，示例配置使用此模式 |
| `rules` | 仅规则提取，不要求 CRF 权重 |
| `shadow` | 保存规则结果，同时记录 CRF 差异用于评估 |
| `crf` | 要求所需模型有效，否则启动失败 |

`PAPER_CRF_MODEL_DIR` 可指定其他模型目录，通常保持默认。
`PAPER_REFERENCE_CRF_ENABLED=false` 表示参考文献区域仍按规则切分，
不代表关闭标题和引用相关的所有 CRF 模型。

### 并发与资源

`ASYNQ_CONCURRENCY=32` 是 Go 任务 worker 总量，不是同时处理 32 份重型 PDF。
默认 Go 识别/提取准入为 `DOCUMENT_RECOGNITION_CONCURRENCY=4` 和
`DOCUMENT_EXTRACTION_CONCURRENCY=4`；Python 对应为
`PARSER_RECOGNIZE_CONCURRENCY=4`、`PARSER_EXTRACT_CONCURRENCY=4`，
RPC 总预算 `PARSER_MAX_CONCURRENT_RPCS=8`。

保留 `PARSER_EXECUTION_MODE=shared`。重型 worker 按需启动、内部线程并发，空闲后退出；
不要通过无限增加 worker 进程数量来处理排队。线程库缓存可能在任务结束后暂时保留内存，
应结合 worker 是否退出判断，而不是只观察瞬时 RSS。更完整的执行机制见[总体架构](architecture.md)。

## 5. 常见排查

| 现象 | 先检查 |
| --- | --- |
| Redis connection refused | `make dev-status` / `docker compose ps`；端口映射是否与 `REDIS_ADDR` 一致 |
| 任务排队不动 | API 与解析器日志、Redis 连接、解析器准入是否饱和、任务是否已失败待重试 |
| PDF 可读但信息未补全 | 识别与外部查询是独立阶段；查看该论文处理状态，按失败阶段重试 |
| 页面能打开但 API 不通 | Vite/Nginx 代理目标、API 地址；跨域部署还需核对 `CORS_ORIGIN` |
| MCP connection refused / 401 | 是否启用、是否使用实际 `/mcp` 地址、token 是否在客户端进程中生效 |
| 笔记修改冲突 | 重新读取当前内容和 revision 后再编辑，不能用旧内容直接覆盖 |
| Launcher 初始化失败 | 管理页初始化日志、普通用户权限、路径字符、磁盘空间；保留数据后重试 |
| 换端口后仍被占用 | 是否存在另一套开发/容器/Launcher 实例；不要删除锁文件强行启动 |

不必为了改文档触发平台构建。功能开发时，常规检查为 `go test ./...`、
`go build ./...`、`npm --prefix web run build`；专项集成测试应按实际改动选择。
