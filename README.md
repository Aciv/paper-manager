# Papers Manager

文档上传与阅读服务的可扩展骨架。Go 负责 HTTP API、PostgreSQL 源数据和本地文件存储；Python 解析器通过 gRPC 契约接入。前端是 React + TypeScript。

Go HTTP 层使用 Gin 与 Handler 结构；需要跨表原子性的写入由 Repository 使用 GORM 事务完成。日志使用全局 `slog` 单例：`LOG_LEVEL` 控制最低输出等级，`LOG_PATH` 可选地启用滚动文件日志；未设置时仍输出到标准输出。

业务代码可直接使用项目级日志方法：`logger.Debug(...)`、`logger.Info(...)`、`logger.Warn(...)`、`logger.Error(...)`。`LOG_LEVEL` 会过滤低于当前等级的消息；例如 `LOG_LEVEL=warn` 时不会输出 debug 与 info。

## Docker 一键启动

```bash
docker compose up --build -d
```

若主机通过 HTTP/HTTPS 代理访问网络，先把代理传给 Docker 构建。代理运行在主机的 `127.0.0.1:7890` 时，容器内应使用 `host.docker.internal`，而不是 `127.0.0.1`：

```bash
export HTTP_PROXY=http://host.docker.internal:7890
export HTTPS_PROXY=http://host.docker.internal:7890
export NO_PROXY=localhost,127.0.0.1,postgres,redis,parser,api,web
docker compose up --build -d
```

也可以把这些值写入根目录 `.env`；Compose 会将它们仅传给镜像构建阶段。`host.docker.internal` 已为 Linux Docker 配置为宿主机网关。若你的代理端口或协议不同，替换对应值即可。

如果代理只监听在宿主机 `127.0.0.1`（`host.docker.internal:7890` 返回 `connection refused`），使用 Linux/WSL 的 host 构建网络：

```bash
export BUILD_NETWORK=host
export HTTP_PROXY=http://127.0.0.1:7890
export HTTPS_PROXY=http://127.0.0.1:7890
docker compose up --build -d
```

先在宿主机验证端口和代理本身：`curl -I -x http://127.0.0.1:7890 https://proxy.golang.org`。若该命令也拒绝连接，请检查代理程序是否启动、端口是否确为 `7890`，或开启代理程序的“允许局域网连接 / Bind address 0.0.0.0”选项。

访问 `http://localhost:5173`。Compose 会启动：

- `postgres`：源数据与解析结果；
- `redis`：Asynq 文档识别队列；
- `parser`：`pymupdf4llm` PDF 解析 gRPC worker；
- `api`：Go HTTP API，端口 `8080`；
- `web`：React 生产站点，端口 `5173`，并反向代理 `/api` 到 API。

上传文件保存到宿主机的 `./data/uploads`，API 与 worker 共用该目录；PostgreSQL 数据保存在 Docker 卷 `postgres-data`。停止服务使用 `docker compose down`，保留数据；如需同时删除数据库卷，使用 `docker compose down -v`。

## 快速开发模式

频繁修改代码时，使用混合模式：Docker 只运行 PostgreSQL 与 Redis；Go API、
Python gRPC parser 和 Vite 都从宿主机直接运行。因此，日常源码修改无需重建镜像。

```text
本机 Go API (:8080)  <-  本机 Vite (:5173)
       |                         |
本机 Python parser (:50051)      |
       \_________________________/
                    Docker: PostgreSQL / Redis
```

首次使用时创建本地配置，然后一键启动完整开发栈：

```bash
cp .env.example .env
make dev
```

该命令会等待 PostgreSQL/Redis 健康，然后并行启动 Go API、Python gRPC parser
和 Vite。三路输出会以 `[api]`、`[parser]`、`[web]` 为前缀显示在当前终端，原始
日志同时分别写入 `Logs/dev/api.log`、`Logs/dev/parser.log` 和
`Logs/dev/web.log`。按 `Ctrl+C` 会统一结束三个源码进程，但保留 PostgreSQL/Redis；
随后再次执行 `make dev` 即可快速恢复。

默认监听端口为 Web `5173`、API `8080`、parser `50051`。需要避开本机端口时，
可在执行命令前覆盖 `VITE_PORT`、`HTTP_ADDR`、`PARSER_GRPC_ADDR`，并用
`VITE_API_PROXY_TARGET` 指向新的 API 地址。

`make dev` 会在启动源码进程前检查这三个端口；若发现已有进程占用，会立刻退出，
而不会额外启动 Asynq worker 后再等待 API 的监听重试。

### 局域网访问（可选）

Vite 默认只监听 `127.0.0.1`。要让同一局域网的其他设备打开开发页面：

1. 在 `.env` 中设置 `VITE_HOST=0.0.0.0`（或执行 `VITE_HOST=0.0.0.0 make dev`），
   然后重启 `make dev`。
2. 在其他设备访问 `http://<本机局域网 IP>:5173`。

Go API 不需要暴露到局域网：页面里的 `/api` 请求由 Vite 代理到本机
`127.0.0.1:8080`。开发栈没有登录鉴权，`0.0.0.0` 会把论文、上传文件和所有操作
暴露给能访问该端口的人。

WSL2 默认 NAT 模式下，Linux 的 `172.x` 地址不能被局域网设备直接访问。可以在
Windows 管理员 PowerShell 中把 Windows 的局域网 IP 转发到 WSL：

```powershell
wsl hostname -I   # 取得 WSL 的内网 IP
netsh interface portproxy add v4tov4 listenaddress=<Windows 局域网 IP> listenport=5173 connectaddress=<WSL IP> connectport=5173
New-NetFirewallRule -DisplayName "papersManager dev 5173" -Direction Inbound -Protocol TCP -LocalPort 5173 -Action Allow
```

然后访问 `http://<Windows 局域网 IP>:5173`。WSL 重启后 IP 可能变化，需要更新转发
规则；Windows 11 的 mirrored networking 可直接共享主机网络，不需要 portproxy。

如果需要让启动终端保持在后台，可在另一个终端查看最近日志并持续跟踪：

```bash
make dev-logs                 # 三个源码服务
make dev-logs SERVICE=parser  # 只查看 parser
```

仍可使用三个独立终端运行单个服务，便于只重启或调试其中一项：

```bash
make dev-app       # Go API；安装 Air 后自动重载
make dev-parser    # Python gRPC parser
make dev-frontend  # Vite HMR
```

`make dev-app` 会优先使用 [Air](https://github.com/air-verse/air)。可选安装：

```bash
go install github.com/air-verse/air@latest
```

首次运行 parser 前，创建 Python 虚拟环境并安装依赖：

```bash
python3 -m venv .venv
.venv/bin/pip install -r python/requirements.txt
```

Parser 默认仍可只使用规则。若要启用已训练的论文提取 CRF，在 `.env` 设置
`PAPER_EXTRACTION_ENGINE=hybrid`（缺失或不兼容模型自动退回规则）、`shadow`
（仅记录累计差异，写入仍使用规则结果）或 `crf`（模型问题即启动失败）；模型目录由
`PAPER_CRF_MODEL_DIR` 指定。Compose 默认
将 `model_train/artifacts` 以只读方式挂载到 parser。单独评估本地 PDF 的识别与
提取耗时可运行：

```bash
.venv/bin/python scripts/benchmark_paper_extraction_pdf.py path/to/paper.pdf \
  --engine hybrid --output /tmp/paper-extraction.json
```

常用命令包括 `make dev-status`、`make dev-infra-logs`、`make dev-stop`。修改 Go、Python
或 React 时都不需要 Docker build；分别重启对应本机进程即可。相同入口也可通过
`./scripts/dev.sh all|start|app|parser|frontend` 使用。

开发 Compose 使用独立的 `postgres-data-dev`、`redis-data-dev` 卷，不会与全容器
模式的 `postgres-data`、`redis-data` 混用。Go API 会自动加载根目录 `.env`；parser
使用其内建的本地默认值（与 `.env.example` 一致），或读取已导出的 `PARSER_GRPC_ADDR`、
`UPLOAD_DIR` 等环境变量。请勿提交 `.env` 或真实凭据。

默认 API 位于 `http://localhost:8080`，Web 位于 `http://localhost:5173`。

服务通过 DI 容器按环境变量选择基础设施：`DB_DRIVER=postgres`（默认）、`DATABASE_URL`、`FILE_STORAGE_DRIVER=local`（默认）、`UPLOAD_DIR`，以及可选的 `FILE_MAX_UPLOAD_SIZE`（字节数）。数据库启动时会执行嵌入式的版本化 PostgreSQL 迁移，SQL 文件位于 `migrations/postgres/`；生产环境若由外部迁移流程管理，可设置 `AUTO_MIGRATE=false`。当前只实现 PostgreSQL 与本地文件存储；选择其他驱动会在启动时返回明确错误。

文档结构读取使用可删除的运行时 JSON 缓存；默认位于
`./data/runtime/document-structures`（不属于 FileStorage）。可通过
`DOCUMENT_STRUCTURE_CACHE_DIR`、`DOCUMENT_STRUCTURE_CACHE_ENTRIES` 和
`DOCUMENT_STRUCTURE_CACHE_MAX_BYTES` 调整目录、条目数和单文件上限。
缓存不是真值，删除后会从 PostgreSQL 中保存的 outline 重建，不会重新打开 PDF。
结构响应不复制完整 Blocks；PDF 阅读器继续通过
`GET /api/document/:id/blocks?page=N` 按页读取并用 ordinal 关联目录位置。
每个文档的可修订阅读笔记不超过 10 MiB，按 H1-H3 拆成
`notes/{document_id}/{section_id}.md`；数据库只记录章节目录、单调 revision 和时间戳，
不保存正文。整份 Markdown 重导入期间读接口明确返回暂不可用，不会读到半完成状态；
普通修改使用 Plan B 的 section 内 `insertText`/`deleteText` 原语和 section
`addSection`/`deleteSection`，替换由 delete + insert 组合。写入与删除继续使用
revision 乐观锁。笔记中的内部证据链接使用
`papers-manager://self/evidence?v=1&page=2&rect=0.1,0.2,0.3,0.4`
或 `papers-manager://document/{uuid}/evidence?...`，坐标是未旋转 PDF 页面中以
左上角为原点的归一化区域，页面旋转由客户端显示时转换。证据链接是普通 Markdown
内容，由前端、Agent 或 MCP 自行解析；Go 后端不识别、不索引也不解析它。

远程 PDF 可通过 `POST /api/document/import-url` 导入，请求体为 `{"url":"https://arxiv.org/abs/2401.01234","doi":"可选","filename":"可选.pdf"}`。服务会区分论文落地页和实际 PDF URL，逐跳执行 SSRF 校验并流式写入；普通出版社落地页不会被猜测或抓取，当前内建转换仅支持 arXiv 和显式 `.pdf` URL。引用 PDF 通过 `POST /api/references/{referenceID}/download-pdf` 按单条引用显式获取。

生产限流、重试、队列、状态排查和端到端测试方式见
[`OPERATIONS.md`](OPERATIONS.md)。

## 统一扩展模型

- `documents` 只管理源文件和处理生命周期：`original_filename`、`storage_key`、文件类型、DOI、状态与错误信息。
- 每个文档对应一条 `paper_metadata`。其中保存正式标题、摘要、作者、来源、期刊/会议、卷期页码、发布日期与学科 `tags`；标题为空时界面和搜索会回退到 `original_filename`。
- `document_bases` 是命名论文集合，`document_base_files` 是多对多关联。加入、移除或整理 Base 只改变关联记录，不会复制、删除或修改源文档与论文书目信息。
- 文档识别通过 `DocumentRecognizer` 端口接入：`default` 在 Go 本地转换 TXT、Markdown、JSON 与 CSV，`builtin` 调用 `proto/document_parser.proto` 的 Python gRPC `DocumentParser.Recognize`。识别任务由 Asynq 执行；Python 返回 Markdown 与页级 source-map Blocks，Go 负责文件存储、数据库事务和状态更新。
- 解析结果写入 `document_blocks`。每个块包含 `type`、文本、页面尺寸、PyMuPDF 坐标与 Markdown Unicode code-point 区间；网页可据此将块高亮回原 PDF。图片和表格需要独立资产 URL，因此在资产导出实现前不会被持久化为 `image` / `table` Block。
- 阅读端使用 PDF.js（通过 `react-pdf`）渲染 PDF、复制文本，并可切换到解析 Block 选择模式。

## API

- `POST /api/document/upload`：multipart 字段 `file`，可选 `doi`；支持 txt、Markdown、JSON、CSV、PDF、Word、Excel 和 PowerPoint 文件，单文件最大 100 MiB。正式标题和标签通过后续 `PATCH /api/paper/{documentID}` 更新。
- `GET /api/document/search`：支持可选 `id`、`doi`、`status` 与 `page`；无筛选条件时按创建时间倒序返回全部文档，每页 10 条。
- `POST /api/document/import-url`：验证并流式导入显式 PDF、arXiv 页面或受支持的 DOI URL，然后提交正常文档处理流程。
- `GET /api/document/{id}/content`：以内联方式流式返回原始文件，供 PDF 阅读器使用。
- `GET /api/document/{id}/blocks?page=N`：读取某一 PDF 页的解析 Block；省略 `page` 返回全部 Block。
- `GET /api/document/{id}/markdown`：以 `text/markdown` 返回当前识别生成的论文 Markdown（有大小上限），用于阅读或下游处理。
- `GET|PUT|DELETE /api/document/{id}/reading-note`：`GET` 只返回 section 目录和 revision；`PUT` 按 `expectedRevision` 整体导入/替换 Markdown；`DELETE` 删除整份笔记。
- `GET /api/document/{id}/reading-note/sections/{sectionId}`：只读取指定 section 的 Markdown；阅读目录不返回正文。
- `GET /api/document/{id}/reading-note/export`：显式拼接全部分 section 并下载整篇 Markdown，这是唯一会加载全篇正文的读取路径。
- `POST /api/document/{id}/reading-note/operations`：按 `expectedRevision` 原子执行操作序列：`insertText`、`deleteText`、`addSection`、`deleteSection`。文本操作使用 section ID 和 section 内 Unicode code point 偏移；`replace` 由 `deleteText` + `insertText` 组合，不单独设原语。文本操作不能改变 section 的 H1-H3 标题结构。
- `GET /api/document/{id}/structure`：返回持久化论文目录的可重建紧凑结构，不包含完整 Blocks；阅读笔记 section 由 `/reading-note` 独立读取。
- 笔记 Markdown 中的 `papers-manager://` 证据链接由前端、Agent 或 MCP 自行解析；Go 后端不索引、不持久化、也不解析笔记证据。
- `PATCH /api/document/{id}`：仅更新 `originalFilename`、`doi`。旧的请求字段 `filename` 暂作为输入别名兼容。
- `GET /api/paper/{documentID}`、`PATCH /api/paper/{documentID}`：获取或更新独立的论文书目信息；标签通过 `paperMetadata.tags` 数组维护。
- `GET /api/paper/search`：支持 `title`、`author`（作者姓名模糊匹配）、重复的 `tag` 参数、`tagMode=all|any` 及 `page`；标签查询使用 PostgreSQL JSONB GIN 索引。
- `GET|POST|PUT /api/paper/{documentID}/citations`、`DELETE /api/paper/{documentID}/citations/{referenceID}`：读取、新增、整体替换或删除本论文的引用关系；`GET /api/paper/{documentID}/cited-by` 查询反向引用，`POST /api/paper/{documentID}/citations/resolve` 可将一个原始引用显式指向库内论文，`POST /api/references/{referenceID}/resolve` 可关联单条引用。
- `GET /api/document/{id}/processing`：返回文档、论文信息、引用查询与引用 PDF 获取的持久化 span，以及紧凑引用进度；它是唯一处理状态读取接口。
- `POST /api/paper/{documentID}/info/{extraction|catalog|reference}/retry`：分别重试元数据提取、源论文库查询或引用论文查询，不会隐式重跑其他阶段。
- `POST /api/references/{referenceID}/resolve`：把单条引用关联到已有文档；`POST /api/references/{referenceID}/rematch`：只按 DOI/title 重新匹配当前库内 available 文档，不调用外部 provider，也不创建文档；`POST /api/references/{referenceID}/download-pdf`：为该引用显式发起 PDF 获取。
- `DELETE /api/document/{id}`：删除文档元数据及其已存储的原始文件。
- `POST /api/document/bases`、`GET /api/document/bases`、`GET|PATCH|DELETE /api/document/bases/{baseID}`：管理文档 Base。
- `PUT|DELETE /api/document/bases/{baseID}/documents/{documentID}`：加入或移除 Base 成员；`GET /api/document/bases/{baseID}/documents?page=1` 分页列出成员。

Python worker 位于 [`python/`](python/README.md)。启动它和 Redis 后，PDF 上传会经 Asynq 与 gRPC 异步解析：`pymupdf4llm` 生成按页 Markdown 和布局块，Go API 将其写入 PostgreSQL 并将文档状态更新为 `ready`。

## 论文信息异步流程

上传后文档独立推进 `markdown` → `metadata_extraction` → `source_lookup` / `reference_lookup` 四个阶段，每个阶段有自己的 generation fence、取消和重试入口。解析成功后文档即可为 `ready`，后续引用查询失败不会把文档改回 `failed`。`ExtractMetadata` 写入引用后会立即按 DOI/title 查询本地 available 文档；命中时直接填上 `cited_document_id`，不发起外部请求。

每条引用是 `paper_citations` 中的一行，归属于引用它的论文；同一篇被引论文出现在两篇论文中时是两条独立记录，各自解析、各自指向相同的 `cited_document_id`。`paper:reference_query_enqueue` 分页读取本论文尚未解析的行，把它们提交给进程内的 catalog dispatcher。dispatcher 使用共享优先级队列：论文自身查询永远优先；每个 provider 一个 worker，引用查询受全局 `PAPER_REFERENCE_CATALOG_CONCURRENCY`（默认 2）和单文档 1 的准入限制，因此不会占满全部 provider。一次 provider 失败或无匹配会换下一个 provider，整轮都没有可用响应才失败；论文自身查询在 exact DOI/arXiv 或分数达到 `PAPER_CATALOG_SOURCE_STOP_SCORE`（默认 0.9）时提前结束，否则最多尝试每个 provider 一次。

查询结果通过 `paper:query_apply` 任务持久化；每个结果都带 generation，写入前按分数与当前数据库记录比较，因此并发/乱序完成不会用低分候选覆盖高分候选。dispatcher 的队列与在途状态在内存中：进程重启时，启动清理会把中断的 `source_lookup`/`reference_lookup`/引用解析行标为失败，由用户手动重试。`PAPER_REFERENCE_CATALOG_MAX_PER_DOCUMENT` 仍限制一次 dispatch 提交的最大引用数。

引用 PDF 获取由用户对单条引用显式触发：`POST /api/references/{referenceID}/download-pdf` 向隔离的 `download` 队列投递任务。任务重新解析最新 URL、流式下载、按 DOI/SHA256 去重，成功后才创建文档并关联该引用。`FILE_MAX_UPLOAD_SIZE` 同时是远程响应字节上限。任务重试遵循 `Retry-After` 或 Asynq 默认指数退避。
