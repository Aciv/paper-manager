# paper_manager

[中文](README.md) | [English](README.en.md)

A local application for reading papers and organizing research. Keep PDFs, metadata, references,
and section notes together; jump from notes to PDF evidence, or connect an MCP-capable agent to
read individual papers and compare papers in a Base collection.

## Features

- **Paper library**: upload PDFs, import supported paper URLs, search titles/authors/tags, and edit metadata.
- **PDF reading**: navigate outlines, select text or parsed blocks, and follow evidence links.
  Hover over a citation index to filter reference candidates; click to navigate within the PDF.
  Selected-text translation is available.
- **Section notes**: read and edit Markdown with H1–H3 navigation, continuous reading within a
  section hierarchy, partial edits, import/export, and links back to PDF evidence.
- **References**: extract bibliographies, enrich them through arXiv, Semantic Scholar, OpenAlex,
  and Crossref, link papers already in the library, and request reference PDFs.
- **Base collections**: group papers, generate bibliography snapshots, and use the reading skill
  to produce comparison and summary reports.
- **Agent integration**: MCP tools for search, reading, notes, collections, and references;
  separate library and reading skills.
- **Processing status**: inspect stages and cancel or retry work.

Extraction and external lookups can miss or misidentify information; check results against the PDF.
Agent reading and summaries require your own MCP-capable client and model. No LLM service is included.

## Choose a startup method

| Method | Suitable for | Requirements |
| --- | --- | --- |
| Unpack and run | Using the application directly | A complete package for your platform; no Docker or development tools |
| Docker Compose | Container deployment | Docker Engine / Docker Desktop with Compose |
| Source development | Changing the code | Go, Node.js, Python, Docker Compose, Make, and Bash |

### Unpack and run

Packages are currently internal candidates; no official release has been published.
See [platform validation](packaging/desktop/VALIDATION.md) for tested revisions and limits.
Full Windows/macOS acceptance of the new root-entry layout remains pending.

1. Obtain the package for your platform and **extract the entire archive**.
2. On Windows, open `paper_manager.exe` at the archive root. On Linux/macOS, run `./paper_manager`.
3. The management page opens in your browser. On first use, select “首次初始化 / 重试”
   (initialize/retry), wait for completion, then select “启动” (start).
4. Reuse the same entry on subsequent launches. Select “退出 Launcher” to exit;
   closing the browser does not stop the services.

Data defaults to `data/` inside the extracted directory; `.launcher_env` lives at its root.
Edit ports and concurrency in the management page while Launcher is running and application services are stopped. Use full ASCII paths on Windows.
Copy the two directories under `skills/` into your Agent client and use MCP connection settings from the management page.
To select a data directory:

```powershell
.\paper_manager.exe --data-dir C:/paper_manager_data
```

See [usage and maintenance](packaging/desktop/USER.md) for default data locations,
update limitations and removal; see [building packages](packaging/desktop/BUILD.md)
for producing your own bundle. Supporting documentation is maintained in Chinese, except existing
packaging reference materials.

### Docker Compose

The root configuration builds from source. An image-only deployment configuration and
local validation are documented in [Docker preparation](packaging/docker/README.md);
container images have not been published yet.

From the repository root:

```bash
docker compose up --build -d
```

Open <http://localhost:5173>. The API uses port `8080` by default.
For custom settings, copy `.env.example` to `.env`; Compose forwards only variables declared in its configuration.

```bash
docker compose logs -f api parser  # Follow logs
docker compose down              # Stop services, retain database volumes
```

Uploaded files default to `data/uploads/`; the database uses a Docker volume.
`docker compose down -v` deletes database volumes: omit `-v` for routine shutdown.
See the [running guide](docs/running.md) for proxies, ports, and storage.

### Source development

These commands target Linux/WSL with Bash. Install Go (version in `go.mod`), Node.js 22+,
Python 3.12+, Docker Compose, and Make, then prepare dependencies:

```bash
cp .env.example .env
python3 -m venv .venv
.venv/bin/python -m pip install -r pdf_parser/requirements.txt
npm --prefix web ci
make dev
```

Docker runs PostgreSQL and Redis; the API, parser, and frontend run on the host.
Open <http://127.0.0.1:5173>. Logs are in `Logs/dev/`.
Press `Ctrl+C` to stop source services, then `make dev-stop` to stop PostgreSQL/Redis while retaining data.
See the [running guide](docs/running.md) for individual services, port overrides, and native Windows notes.

## Key settings

Source/container settings use the root `.env`. The unpacked Launcher uses an optional
`bundle/.launcher_env` and does not read the service `.env`. Keep real credentials out of Git.

| Setting | Purpose |
| --- | --- |
| `DATABASE_URL`, `REDIS_ADDR` | Database and task queue connections for source runs |
| `UPLOAD_DIR` | File storage; API and parser must access the same files |
| `HTTP_ADDR`, `PARSER_GRPC_ADDR` | API bind address and parser connection/listener address |
| `VITE_PORT`, `VITE_API_PROXY_TARGET` | Development frontend port and API proxy target |
| `FILE_MAX_UPLOAD_SIZE` | Single-file upload limit; default 100 MiB |
| `PAPER_EXTRACTION_ENGINE` | Extraction mode; example uses `hybrid`; also `rules`, `shadow`, `crf` |
| `PARSER_IMAGE_DPI` | Parsed image/table preview resolution; default 144 |
| `MCP_ENABLED`, `MCP_ADDR`, `MCP_TOKEN` | MCP access; a configured token requires Bearer authentication |
| `LOG_LEVEL`, `LOG_PATH` | Log level and file location |
| `LAUNCHER_DATA_DIR`, `LAUNCHER_OPEN_BROWSER` | Launcher data directory and browser launch behavior |

See [.env.example](.env.example), [.launcher_env.example](.launcher_env.example), and the
[running guide](docs/running.md) for configuration scope, concurrency, and external lookup settings.

## Connect an agent

Follow the [MCP and skill setup guide](docs/mcp/agent-installation.md).
Source runs require `MCP_ENABLED=true`; for Launcher runs, copy the actual connection settings
from the management page.

- `papers-manager-library`: import, metadata, collections, and references.
- `papers-manager-reading`: layered reading, section notes, evidence, and Base comparison reports.

## Documentation and license

[Documentation index](docs/README.md) · [Architecture](docs/architecture.md) ·
[Reading-note contract](docs/reading-note-agent-contract.md) · [MCP tool contract](docs/mcp/papers-manager-tools.md)

Project-owned code is licensed under [MIT](LICENSE). Bundled dependencies retain their own terms;
see [third-party notices](packaging/desktop/THIRD_PARTY_NOTICES.md).
