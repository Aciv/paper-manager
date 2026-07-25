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

## 本地开发（可选）

仍可分别运行 Python worker、Go API 和 Vite 前端，具体命令见 [`python/README.md`](python/README.md)。

首次本地运行时，从示例生成未提交的开发配置：

```bash
cp .env.example .env
go run ./cmd/server
```

服务启动时自动加载根目录 `.env`。文件缺失会被忽略，且已经由 shell、Docker 或部署平台提供的环境变量优先于 `.env` 中的值。请勿将 `.env` 或真实凭据提交到仓库。

默认 API 位于 `http://localhost:8080`，Web 位于 `http://localhost:5173`。

服务通过 DI 容器按环境变量选择基础设施：`DB_DRIVER=postgres`（默认）、`DATABASE_URL`、`FILE_STORAGE_DRIVER=local`（默认）、`UPLOAD_DIR`，以及可选的 `FILE_MAX_UPLOAD_SIZE`（字节数）。数据库启动时会执行嵌入式的版本化 PostgreSQL 迁移，SQL 文件位于 `migrations/postgres/`；生产环境若由外部迁移流程管理，可设置 `AUTO_MIGRATE=false`。当前只实现 PostgreSQL 与本地文件存储；选择其他驱动会在启动时返回明确错误。

## 统一扩展模型

- `documents` 只管理源文件和处理生命周期：`original_filename`、`storage_key`、文件类型、DOI、状态与错误信息。
- 每个文档对应一条 `paper_metadata`。其中保存正式标题、摘要、作者、来源、期刊/会议、卷期页码、发布日期与学科 `tags`；标题为空时界面和搜索会回退到 `original_filename`。
- `document_bases` 是命名论文集合，`document_base_files` 是多对多关联。加入、移除或整理 Base 只改变关联记录，不会复制、删除或修改源文档与论文书目信息。
- 文档识别通过 `DocumentRecognizer` 端口接入：`default` 在 Go 本地转换 TXT、Markdown、JSON 与 CSV，`builtin` 调用 `proto/document_parser.proto` 的 Python gRPC `DocumentParser.Recognize`。识别任务由 Asynq 执行；Python 返回 Markdown 与页级 source-map Blocks，Go 负责文件存储、数据库事务和状态更新。
- 解析结果写入 `document_blocks`。每个块包含 `type`、文本、页面尺寸、PyMuPDF 坐标、Markdown Unicode code-point 区间和 Markdown SHA-256；网页可据此将块高亮回原 PDF。图片和表格需要独立资产 URL，因此在资产导出实现前不会被持久化为 `image` / `table` Block。
- 阅读端使用 PDF.js（通过 `react-pdf`）渲染 PDF、复制文本，并可切换到解析 Block 选择模式。

## API

- `POST /api/document/upload`：multipart 字段 `file`，可选 `doi`、`title` 与重复 `tags` 字段；支持 txt、Markdown、JSON、CSV、PDF、Word、Excel 和 PowerPoint 文件，单文件最大 100 MiB。
- `GET /api/document/search`：仅支持可选 `id`、`doi` 与 `page`；无筛选条件时按创建时间倒序返回全部源文档，每页 10 条。
- `GET /api/document/{id}/content`：以内联方式流式返回原始文件，供 PDF 阅读器使用。
- `GET /api/document/{id}/blocks?page=N`：读取某一 PDF 页的解析 Block；省略 `page` 返回全部 Block。
- `PATCH /api/document/{id}`：仅更新 `originalFilename`、`doi`。旧的请求字段 `filename` 暂作为输入别名兼容。
- `GET /api/paper/{documentID}`、`PATCH /api/paper/{documentID}`：获取或更新独立的论文书目信息；标签通过 `paperMetadata.tags` 数组维护。
- `GET /api/paper/search`：支持 `title`、`author`（作者姓名模糊匹配）、重复的 `tag` 参数、`tagMode=all|any` 及 `page`；标签查询使用 PostgreSQL JSONB GIN 索引。
- `DELETE /api/document/{id}`：删除文档元数据及其已存储的原始文件。
- `POST /api/document/bases`、`GET /api/document/bases`、`GET|PATCH|DELETE /api/document/bases/{baseID}`：管理文档 Base。
- `PUT|DELETE /api/document/bases/{baseID}/documents/{documentID}`：加入或移除 Base 成员；`GET /api/document/bases/{baseID}/documents?page=1` 分页列出成员。

Python worker 位于 [`python/`](python/README.md)。启动它和 Redis 后，PDF 上传会经 Asynq 与 gRPC 异步解析：`pymupdf4llm` 生成按页 Markdown 和布局块，Go API 将其写入 PostgreSQL 并将文档状态更新为 `ready`。
