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

首次使用时创建本地配置，并启动基础设施：

```bash
cp .env.example .env
make dev-start
```

随后在另外三个终端分别运行：

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

常用命令包括 `make dev-status`、`make dev-logs`、`make dev-stop`。修改 Go、Python
或 React 时都不需要 Docker build；分别重启对应本机进程即可。相同入口也可通过
`./scripts/dev.sh start|app|parser|frontend` 使用。

开发 Compose 使用独立的 `postgres-data-dev`、`redis-data-dev` 卷，不会与全容器
模式的 `postgres-data`、`redis-data` 混用。Go API 会自动加载根目录 `.env`；parser
使用其内建的本地默认值（与 `.env.example` 一致），或读取已导出的 `PARSER_GRPC_ADDR`、
`UPLOAD_DIR` 等环境变量。请勿提交 `.env` 或真实凭据。

默认 API 位于 `http://localhost:8080`，Web 位于 `http://localhost:5173`。

服务通过 DI 容器按环境变量选择基础设施：`DB_DRIVER=postgres`（默认）、`DATABASE_URL`、`FILE_STORAGE_DRIVER=local`（默认）、`UPLOAD_DIR`，以及可选的 `FILE_MAX_UPLOAD_SIZE`（字节数）。数据库启动时会执行嵌入式的版本化 PostgreSQL 迁移，SQL 文件位于 `migrations/postgres/`；生产环境若由外部迁移流程管理，可设置 `AUTO_MIGRATE=false`。当前只实现 PostgreSQL 与本地文件存储；选择其他驱动会在启动时返回明确错误。

## 统一扩展模型

- `documents` 只管理源文件和处理生命周期：`original_filename`、`storage_key`、文件类型、DOI、状态与错误信息。
- 每个文档对应一条 `paper_metadata`。其中保存正式标题、摘要、作者、来源、期刊/会议、卷期页码、发布日期与学科 `tags`；标题为空时界面和搜索会回退到 `original_filename`。
- `document_bases` 是命名论文集合，`document_base_files` 是多对多关联。加入、移除或整理 Base 只改变关联记录，不会复制、删除或修改源文档与论文书目信息。
- 文档识别通过 `DocumentRecognizer` 端口接入：`default` 在 Go 本地转换 TXT、Markdown、JSON 与 CSV，`builtin` 调用 `proto/document_parser.proto` 的 Python gRPC `DocumentParser.Recognize`。识别任务由 Asynq 执行；Python 返回 Markdown 与页级 source-map Blocks，Go 负责文件存储、数据库事务和状态更新。
- 解析结果写入 `document_blocks`。每个块包含 `type`、文本、页面尺寸、PyMuPDF 坐标与 Markdown Unicode code-point 区间；网页可据此将块高亮回原 PDF。图片和表格需要独立资产 URL，因此在资产导出实现前不会被持久化为 `image` / `table` Block。
- 阅读端使用 PDF.js（通过 `react-pdf`）渲染 PDF、复制文本，并可切换到解析 Block 选择模式。

## API

- `POST /api/document/upload`：multipart 字段 `file`，可选 `doi`、`title` 与重复 `tags` 字段；支持 txt、Markdown、JSON、CSV、PDF、Word、Excel 和 PowerPoint 文件，单文件最大 100 MiB。
- `GET /api/document/search`：仅支持可选 `id`、`doi` 与 `page`；无筛选条件时按创建时间倒序返回全部源文档，每页 10 条。
- `GET /api/document/{id}/content`：以内联方式流式返回原始文件，供 PDF 阅读器使用。
- `GET /api/document/{id}/blocks?page=N`：读取某一 PDF 页的解析 Block；省略 `page` 返回全部 Block。
- `PATCH /api/document/{id}`：仅更新 `originalFilename`、`doi`。旧的请求字段 `filename` 暂作为输入别名兼容。
- `GET /api/paper/{documentID}`、`PATCH /api/paper/{documentID}`：获取或更新独立的论文书目信息；标签通过 `paperMetadata.tags` 数组维护。
- `GET /api/paper/search`：支持 `title`、`author`（作者姓名模糊匹配）、重复的 `tag` 参数、`tagMode=all|any` 及 `page`；标签查询使用 PostgreSQL JSONB GIN 索引。
- `GET|POST|PUT /api/paper/{documentID}/citations`、`DELETE /api/paper/{documentID}/citations/{citedDocumentID}`：读取、新增、整体替换或删除本论文的引用关系；`GET /api/paper/{documentID}/cited-by` 查询反向引用，`POST /api/paper/{documentID}/citations/resolve` 可将一个原始引用显式指向库内论文。
- `GET /api/paper/{documentID}/info`：返回论文元数据提取和论文库查询的当前可观察状态，独立于 `documents.status`。`POST /api/paper/{documentID}/info/extraction/retry` 与 `.../catalog/retry` 仅提交当前安全输入对应的 Asynq 重试任务。
- `DELETE /api/document/{id}`：删除文档元数据及其已存储的原始文件。
- `POST /api/document/bases`、`GET /api/document/bases`、`GET|PATCH|DELETE /api/document/bases/{baseID}`：管理文档 Base。
- `PUT|DELETE /api/document/bases/{baseID}/documents/{documentID}`：加入或移除 Base 成员；`GET /api/document/bases/{baseID}/documents?page=1` 分页列出成员。

Python worker 位于 [`python/`](python/README.md)。启动它和 Redis 后，PDF 上传会经 Asynq 与 gRPC 异步解析：`pymupdf4llm` 生成按页 Markdown 和布局块，Go API 将其写入 PostgreSQL 并将文档状态更新为 `ready`。

## 论文信息异步流程

文档解析成功后，API 在 `default` 队列提交 `paper:metadata_extract`：worker 从持久化 Markdown 和 Blocks 调用 Python 的 `PaperMetadataExtractor.Extract`，再将标题、作者、摘要和原始参考文献写入 `paper_metadata` / `paper_citations`。提取成功且存在 DOI 或标题时，worker 在 `low` 队列提交 `paper:catalog_resolve`，依次查询 `PAPER_CATALOG_PROVIDERS`（默认 arXiv、Semantic Scholar、OpenAlex、Crossref）。论文信息任务失败不会把已解析文档从 `ready` 改成 `failed`；重试与死信由 Asynq 负责。

源论文查询完成后，最多 `PAPER_REFERENCE_CATALOG_MAX_PER_DOCUMENT` 条去重后的未知引用会作为同一个 `paper:catalog_resolve` 任务的 `reference` scope 投入低优先级队列；它们不会阻塞源论文信息。`PAPER_REFERENCE_CATALOG_CONCURRENCY` 与 `PAPER_REFERENCE_CATALOG_PER_DOCUMENT_CONCURRENCY` 是全局和单篇文献的分布式任务租约上限。`REDIS_ADDR` 启用时这些租约、provider 请求间隔、并发租约和 `Retry-After` 冷却在所有 worker 间共享；Lite 模式退化为进程内保护。`PAPER_CATALOG_<PROVIDER>_TIMEOUT`、`_SPACING` 与 `_CONCURRENCY` 可分别配置 provider（默认值见 `.env.example`）。队列和任务超时仍由 `PAPER_EXTRACTION_TASK_TIMEOUT`、`PAPER_EXTRACTION_TASK_MAX_RETRY`、`PAPER_CATALOG_TASK_TIMEOUT`、`PAPER_CATALOG_TASK_MAX_RETRY` 配置。Semantic Scholar 的 `SEMANTIC_SCHOLAR_API_KEY`、OpenAlex 的 `OPENALEX_API_KEY` 与 Crossref 的 `CROSSREF_MAILTO` 均可选；不要提交真实值。变更 `proto/document_parser.proto` 后，按 [`python/README.md`](python/README.md) 中的 Python 与 Go `grpc_tools.protoc` 命令重新生成两个语言的 stub，绝不手改生成文件。
