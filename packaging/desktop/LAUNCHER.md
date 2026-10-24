# D2 Launcher

The Launcher manages private PostgreSQL 17, Redis, Python gRPC and Go server
processes, plus a separate management webpage. The same Go server runtime is
used by development, Docker and Launcher. Python retains shared workers,
internal thread pools, recognition/extraction concurrency 4+4 and RPC budget 8.

This is the lifecycle framework. D3 still assembles the self-contained runtime
directory; these instructions require explicit installed dependency paths.

## Configuration and startup

Build the two Go entry points and the regular frontend:

```sh
go build -o tmp/desktop/bin/launcher ./cmd/launcher
go build -o tmp/desktop/bin/server ./cmd/server
cd web && npm run build
```

Use `.exe` for Windows binary names. Copy the repository's
`.launcher_env.example` to `.launcher_env` beside Launcher, or specify it with
`--env-file /absolute/path/.launcher_env`. Set absolute component paths for the
server binary, Python interpreter, parser source directory, PostgreSQL `bin`,
Redis binary and Vite `dist`. Paths with spaces are supported. PostgreSQL's
Windows binaries may require an ASCII short-path alias; a volume without one
needs an ASCII data directory, with an actionable failure instead of fallback.

```sh
tmp/desktop/bin/launcher --env-file /absolute/path/.launcher_env
```

`--data-dir`, `--install-dir` and `--open-browser` override the corresponding
`LAUNCHER_*` settings. Existing process environment wins over the env file.
Unspecified settings retain saved runtime values or first-run bundle defaults.
The ordinary server's `.env` is not read by Launcher or its child server.
Changing settings requires restarting Launcher; there is no live reload.

Default writable root:

| OS | Root |
| --- | --- |
| Linux | `$XDG_DATA_HOME/papers-manager` or `~/.local/share/papers-manager` |
| macOS | `~/Library/Application Support/papers-manager` |
| Windows | `%AppData%/papers-manager` |

The installation directory is read-only runtime input. The writable root holds
`runtime.json`, the instance lock, private PostgreSQL/Redis data, `uploads`,
recognition output, cache, working directory and bounded rotating logs.
Notes retain the existing FileStorage layout at `uploads/notes/<document_id>/`.
Do not copy a running database directory as a backup. Preserve `runtime.json`
with a consistent data backup: it holds private instance credentials and stable
ports and cannot be regenerated to recover an existing cluster.

Run as an ordinary user, without `sudo` or “Run as administrator”. Root/elevated
startup leaves the management page available with an actionable error.

## Management and lifecycle

The terminal prints the management URL and configuration path. All service
listeners bind to `127.0.0.1`; selected ports persist across restarts. The page
needs no login. Host checks, same-origin writes, no CORS and frame protection
prevent unrelated webpages from issuing management operations.

The page shows initialization/startup/readiness/stopping/stopped/failure states,
per-service readiness, recent logs, start/retry, stop, quit, and the reading UI
link. Closing a tab does not stop services. MCP configuration is copied only on
request and includes the existing MCP token; no Agent configuration is modified.

Startup reserves candidate ports until each process is launched. The underlying
binaries cannot inherit these listeners, so this is not an atomic port handoff:
bind failures or failed readiness abort the cycle and clean owned processes.
An occupied persisted port fails visibly; release it or explicitly change the
corresponding `LAUNCHER_*_PORT`. It never silently attaches to another service.

PostgreSQL initialization uses a temporary sibling and publishes the cluster only
after success. Existing unmarked, wrong-version, incomplete or running clusters
are preserved and rejected. Redis uses AOF plus snapshots, private authentication
and `noeviction`. Ownership fingerprints prevent adopting another instance's data.
No database schema or protobuf change is introduced by D2.

Shutdown closes admission and drains the Go runtime first, then Python, Redis and
PostgreSQL. Parent pipe EOF provides portable Go/Python shutdown; Windows uses
private kill-on-close jobs and Unix uses owned process groups for cleanup.
Short Unix worker paths keep AF_UNIX; long paths use the existing authenticated
loopback TCP transport so deep/Unicode data directories remain usable.
Each service has a shutdown deadline, followed by forced cleanup of its owned
tree. Failed cycles require explicit retry; there is no automatic restart loop.
Stale PostgreSQL PID files after an unclean shutdown require verification; the
Launcher does not delete PID files to force access to a potentially live cluster.

`WEB_DIST_DIR` optionally enables desktop static routing in the server runtime.
API, MCP and health routes keep their existing handlers and 404 behavior.
Only HTML navigation gets SPA fallback; missing assets stay 404. Vite hashed
assets are immutable-cacheable, HTML uses revalidation, and file access stays
inside the web root. Without this setting, API-only development is unchanged.

## Validation

```sh
go test -race ./internal/runtime/... ./internal/router ./cmd/server
go test ./...
go build ./...
python -m unittest discover -s pdf_parser/papers_parser -t pdf_parser -p 'test_*.py'
python scripts/check_desktop_launcher.py \
  --launcher /absolute/path/launcher --server /absolute/path/server \
  --postgres-bin /absolute/path/postgres/bin \
  --redis-server /absolute/path/redis-server --output-dir tmp/desktop-d2
```

The real probe requires an ordinary account and actual PostgreSQL/Redis/Python
dependencies. It tests `.launcher_env`, first start, single-instance refusal,
management boundaries, static resources, MCP, PDF upload→Asynq→shared worker→
reading, persistent restart, API crash cleanup/retry and occupied-port refusal.
It creates only private temporary state and exports sanitized logs and a JSON
report. CI runs native Linux/macOS and a newly created ordinary Windows account.
These tests do not substitute for D3's clean, self-contained artifact acceptance.
