# paper_manager

[中文](README.md) | [English](README.en.md)

面向快速阅读论文和组织研究资料的本地应用。集中管理 PDF、论文信息、引用关系和章节笔记，
在阅读时回到原文证据，也可通过 MCP 与 Agent 协作整理单篇论文、比较一个 Base 中的多篇论文。

## 能做什么

- **论文库**：上传 PDF、导入受支持的论文 URL，搜索标题、作者和标签，整理元数据。
- **PDF 阅读**：查看目录、选择文本或解析块、定位证据；悬停引用索引时筛选右侧候选引用，
  点击仍跳转 PDF 原文。提供选中文本翻译。
- **章节笔记**：阅读和编辑 Markdown 笔记；支持 H1–H3 目录、章节内连续阅读、局部修改、
  导入导出，以及跳回 PDF 的证据链接。
- **引用管理**：提取参考文献，通过 arXiv、Semantic Scholar、OpenAlex、Crossref 补充信息，
  关联库内论文，并按需获取引用 PDF。
- **Base 集合**：把多篇论文组织为研究集合，生成参考文献快照；借助阅读技能输出比较与总结报告。
- **Agent 协作**：通过 MCP 搜索、读论文、写笔记、管理 Base 和引用；提供入库与阅读两套技能。
- **处理进度**：查看解析和信息查询的阶段状态，按需取消或重试。

解析和外部检索可能遗漏或误识别，引用与笔记应结合 PDF 原文核对。
Agent 阅读和总结需要自行配置支持 MCP 的客户端及模型，应用不会自动提供 LLM 服务。

## 选择启动方式

| 方式 | 适合谁 | 本机需要 |
| --- | --- | --- |
| 解压运行包 | 希望直接使用应用 | 对应平台的完整发行包；无需 Docker 或开发环境 |
| Docker Compose | 希望容器化运行 | Docker Engine / Docker Desktop 和 Compose |
| 源码开发 | 需要修改代码 | Go、Node.js、Python、Docker Compose、Make 和 Bash |

### 解压运行

当前为内部候选包，尚未发布正式 Release；已有候选包的验证范围见
[平台验收记录](packaging/desktop/VALIDATION.md)。新布局的完整 Windows/macOS 验收仍待完成。

1. 获取对应平台的包并**完整解压**，保留所有子目录。
2. Windows 双击根目录 `paper_manager.exe`；Linux/macOS 在终端运行 `./paper_manager`。
3. 浏览器打开管理页。首次点击“首次初始化 / 重试”，完成后点击“启动”。
4. 以后从同一入口启动即可；退出时使用管理页的“退出 Launcher”。关闭浏览器不会停止服务。

数据默认保存在解压目录的 `data/`，启动配置 `.launcher_env` 位于根目录。建议使用纯英文路径。
Launcher 运行、其他服务停止时，可在管理页“配置”修改端口和并发。需要指定其他数据位置时：

```powershell
.\paper_manager.exe --data-dir C:/paper_manager_data
```

默认数据位置、更新限制和卸载见
[解压运行与维护](packaging/desktop/USER.md)。自行制作包见[构建说明](packaging/desktop/BUILD.md)。

### Docker Compose

在仓库根目录执行：

```bash
docker compose up --build -d
```

打开 <http://localhost:5173>；API 默认使用 `8080` 端口。
修改设置时可先复制 `.env.example` 为 `.env`，Compose 仅传入其配置中声明的变量。

```bash
docker compose logs -f api parser  # 查看日志
docker compose down              # 停止服务，保留数据库卷
```

论文文件默认保存在 `data/uploads/`，数据库保存在 Docker 卷。
`docker compose down -v` 会删除数据库卷，日常停止不要加 `-v`。
代理设置、端口冲突和数据目录说明见[运行指南](docs/running.md)。

### 源码开发

以下命令适用于 Linux/WSL 的 Bash 环境。先准备 Go（版本见 `go.mod`）、Node.js 22+、
Python 3.12+、Docker Compose 和 Make，再安装依赖：

```bash
cp .env.example .env
python3 -m venv .venv
.venv/bin/python -m pip install -r pdf_parser/requirements.txt
npm --prefix web ci
make dev
```

Docker 运行 PostgreSQL 和 Redis；本机运行 API、解析器与前端。
打开 <http://127.0.0.1:5173>。日志位于 `Logs/dev/`。
按 `Ctrl+C` 停止源码服务，`make dev-stop` 停止数据库与 Redis，保留卷。
独立启动、端口覆盖和原生 Windows 的注意事项见[运行指南](docs/running.md)。

## 关键配置

源码/容器使用根目录 `.env`；解压运行的 Launcher 使用可选的 `bundle/.launcher_env`，
不读取普通服务的 `.env`。不要把两套配置混用，也不要提交真实凭据。

| 配置 | 用途 |
| --- | --- |
| `DATABASE_URL`、`REDIS_ADDR` | 源码运行时的数据库与任务队列连接 |
| `UPLOAD_DIR` | 文件存储目录；API 与解析器必须指向同一份文件 |
| `HTTP_ADDR`、`PARSER_GRPC_ADDR` | API 监听地址、解析器连接/监听地址 |
| `VITE_PORT`、`VITE_API_PROXY_TARGET` | 开发前端端口及 API 代理目标 |
| `FILE_MAX_UPLOAD_SIZE` | 单文件上传上限，默认 100 MiB |
| `PAPER_EXTRACTION_ENGINE` | 提取模式，示例配置使用 `hybrid`；可选 `rules`、`shadow`、`crf` |
| `PARSER_IMAGE_DPI` | 解析图片/表格预览精度，默认 144 |
| `MCP_ENABLED`、`MCP_ADDR`、`MCP_TOKEN` | 启用和配置 MCP；设置 token 后请求须携带 Bearer token |
| `LOG_LEVEL`、`LOG_PATH` | 日志级别和文件位置 |
| `LAUNCHER_DATA_DIR`、`LAUNCHER_OPEN_BROWSER` | Launcher 数据目录和是否自动打开管理页 |

完整示例见 [.env.example](.env.example)、[.launcher_env.example](.launcher_env.example)，
并发、外部查询、配置生效范围见[运行指南](docs/running.md)。

## 连接 Agent

运行服务后，按 [MCP 与技能接入指南](docs/mcp/agent-installation.md) 配置客户端。
源码模式需设置 `MCP_ENABLED=true`；Launcher 模式从管理页获取实际连接地址与配置。

- `papers-manager-library`：入库、信息整理、Base 与引用管理。
- `papers-manager-reading`：分层阅读、章节笔记、证据定位和 Base 比较报告。

## 文档与许可

[中文文档导航](docs/README.md) · [总体架构](docs/architecture.md) ·
[阅读笔记契约](docs/reading-note-agent-contract.md) · [MCP 工具契约](docs/mcp/papers-manager-tools.md)

项目自有代码采用 [MIT](LICENSE)。捆绑的第三方组件遵循各自许可，详见
[第三方依赖说明](packaging/desktop/THIRD_PARTY_NOTICES.md)。
