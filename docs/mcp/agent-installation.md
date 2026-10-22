# papersManager MCP / Skill 安装指南

Status: manual installation guide. The MCP server and skills are implemented;
a one-click installer and downloadable agent package are not yet provided.

This document covers:

- connecting Claude Code, Codex, OpenCode, and DeepSeek Harness to the
  papersManager MCP server;
- installing the two Agent skills;
- loopback, Docker, WSL, and remote-access cases;
- verification and troubleshooting.

The tool contract remains
[`papers-manager-tools.md`](papers-manager-tools.md); the machine-readable
tool list is [`papers-manager-tools.json`](papers-manager-tools.json). This
guide only explains how a host connects to that contract.

## 1. Before installing

### 1.1 Start the MCP adapter

The MCP server is part of the Go API process. It is disabled by default.

```env
MCP_ENABLED=true
MCP_ADDR=127.0.0.1:8090
MCP_TOKEN=
MCP_MAX_REQUEST_BYTES=8388608
```

Start the API normally, for example:

```bash
make dev-app
# or
go run ./cmd/server
```

The endpoint is:

```text
http://127.0.0.1:8090/mcp
```

`MCP_ADDR` is a `host:port`, not a full URL. The listener is the same process
as the HTTP API and shares the same DI container, services, PostgreSQL/Redis
connections, and reading-note revision gates.

### 1.2 Transport support

papersManager currently supports **MCP Streamable HTTP only**.

- There is no native stdio transport.
- The legacy HTTP+SSE transport is not exposed.
- Hosts that only accept stdio must use an external stdio-to-HTTP bridge; see
  [section 2](#2-stdio-only-hosts).

Claude Code, Codex, OpenCode, and DeepSeek Harness can all connect to
Streamable HTTP directly and do not need a bridge.

### 1.3 Token policy

- Loopback (`127.0.0.1`, `::1`, or `localhost`) may run without a token.
- A non-loopback bind **fails at startup** unless `MCP_TOKEN` is set.
- When `MCP_TOKEN` is set, every request must send:

```http
Authorization: Bearer <MCP_TOKEN>
```

Use a client-side environment variable such as
`PAPERS_MANAGER_MCP_TOKEN`; do not commit the token into `.mcp.json`,
`config.toml`, `opencode.json`, or a DSH profile. The client variable may have a
different name from the server's `MCP_TOKEN`, as long as the value matches.

For a local development machine this is sufficient:

```bash
export PAPERS_MANAGER_MCP_TOKEN=dev-token
```

and start the API with:

```bash
MCP_TOKEN=dev-token MCP_ENABLED=true make dev-app
```

### 1.4 Smoke test

The standard MCP handshake is enough to verify the listener, path, and token:

```bash
curl -sS http://127.0.0.1:8090/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"1"}}}'
```

This curl command is a connectivity check only. Agent skills must use MCP tools,
not curl, for paper and note operations.

## 2. stdio-only hosts

If a host cannot connect to Streamable HTTP, run a stdio bridge on the client
side. This keeps papersManager's single execution boundary: the bridge forwards
MCP calls to the same `/mcp` endpoint and does not access the database.

Example with [`mcp-remote`](https://github.com/geelen/mcp-remote):

```bash
npx -y mcp-remote http://127.0.0.1:8090/mcp \
  --header "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}"
```

A typical stdio configuration:

```json
{
  "mcpServers": {
    "papers-manager": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote",
        "http://127.0.0.1:8090/mcp",
        "--header",
        "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}"
      ],
      "env": {
        "PAPERS_MANAGER_MCP_TOKEN": "<set-locally>"
      }
    }
  }
}
```

`mcp-remote` is a third-party client-side bridge. It is not part of the
papersManager MCP contract, and no stdio entrypoint is built into
`cmd/server`.

## 3. Host matrix

| Host | MCP configuration | Skills directory |
| --- | --- | --- |
| Claude Code | `.mcp.json` or `claude mcp add --transport http` | `.claude/skills/` or `~/.claude/skills/` |
| Codex | `~/.codex/config.toml` or `codex mcp add --url` | `.agents/skills/` or `~/.agents/skills/` |
| OpenCode | `opencode.json` → `mcp` | `.opencode/skills/`, `.agents/skills/`, or `.claude/skills/` |
| DeepSeek Harness | MCP Center plugin or `@deepseek-ai/dsh-mcp-client` | `.dsh/skills/`, `~/.dsh/skills/`, or `.agents/skills/` |

For a cross-host project layout, install the skills once under
`.agents/skills/` and symlink them into `.claude/skills/`; see
[section 7](#7-install-the-skills).

## 4. Claude Code

### 4.1 CLI

```bash
claude mcp add --transport http --scope user \
  --header "Authorization: Bearer ${PAPERS_MANAGER_MCP_TOKEN}" \
  papers-manager http://127.0.0.1:8090/mcp
```

All flags must come before the server name. Omit `--header` when no token is
configured.

Use `--scope project` to write a shared `.mcp.json` instead of the user-level
configuration.

### 4.2 `.mcp.json`

Project root:

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

A project-scoped `.mcp.json` is not started silently: Claude Code asks for
approval on first use. Use `/mcp` to approve or inspect it.

### 4.3 Skills

```bash
mkdir -p ~/.claude/skills
cp -R agent-skills/papers-manager-library ~/.claude/skills/
cp -R agent-skills/papers-manager-reading ~/.claude/skills/
```

Or use project-level `.claude/skills/`.

### 4.4 Verify

```bash
claude mcp list
```

Inside a session:

```text
/mcp
```

Both `papers_manager_library_*` and `papers_manager_reading_note_*` tools
should be listed. The first skill use should trigger its runtime preflight.

## 5. Codex

Codex uses TOML, not the Claude Code `.mcp.json` format.

### 5.1 CLI

```bash
codex mcp add papers-manager \
  --url http://127.0.0.1:8090/mcp \
  --bearer-token-env-var PAPERS_MANAGER_MCP_TOKEN
```

### 5.2 `config.toml`

User-level `~/.codex/config.toml`, or `.codex/config.toml` in a trusted
project:

```toml
[mcp_servers.papers-manager]
url = "http://127.0.0.1:8090/mcp"
bearer_token_env_var = "PAPERS_MANAGER_MCP_TOKEN"
enabled = true
tool_timeout_sec = 120
```

Set the environment variable in the shell that launches Codex:

```bash
export PAPERS_MANAGER_MCP_TOKEN=dev-token
```

`tool_timeout_sec = 120` is useful because `library_import_url` and metadata
operations may wait for remote downloads and queues.

### 5.3 Skills

Codex discovers `SKILL.md` folders under `.agents/skills/` (project) or
`~/.agents/skills/` (user):

```bash
mkdir -p .agents/skills
cp -R agent-skills/papers-manager-library .agents/skills/
cp -R agent-skills/papers-manager-reading .agents/skills/
```

Codex loads a skill's name and description first and the full `SKILL.md` only
when it is selected. Keep each skill's `references/` directory next to its
`SKILL.md`.

### 5.4 Verify

```bash
codex mcp list
```

Inside the Codex TUI, `/mcp` shows the active servers. A skill can be invoked
with its `$skill-name` form if the host exposes it; otherwise ask for the
reading/library workflow and let the skill description route it.

## 6. OpenCode

### 6.1 `opencode.json`

Project `opencode.json`, or global
`~/.config/opencode/opencode.json`:

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

OpenCode uses `{env:VAR}` for environment substitution and `timeout` in
milliseconds. Omit the `headers` object when no token is configured.

### 6.2 Skills

OpenCode reads all of these:

```text
.opencode/skills/<name>/SKILL.md
~/.config/opencode/skills/<name>/SKILL.md
.claude/skills/<name>/SKILL.md
.agents/skills/<name>/SKILL.md
```

For a shared project installation, use `.agents/skills/`:

```bash
mkdir -p .agents/skills
cp -R agent-skills/papers-manager-library .agents/skills/
cp -R agent-skills/papers-manager-reading .agents/skills/
```

### 6.3 Verify

```bash
opencode mcp list
opencode mcp debug papers-manager
```

The debug command is the quickest way to separate a configuration error from a
server/login error.

## 7. Install the skills

The two source bundles are:

```text
agent-skills/
  papers-manager-library/
    SKILL.md
    references/
  papers-manager-reading/
    SKILL.md
    references/
```

Install the **whole directory**, not only `SKILL.md`; `references/` is loaded
on demand for reading levels, note standards, evidence rules, and examples.

### 7.1 Shared project installation

```bash
mkdir -p .agents/skills
cp -R agent-skills/papers-manager-library .agents/skills/
cp -R agent-skills/papers-manager-reading .agents/skills/
```

Codex, OpenCode, and DeepSeek Harness read `.agents/skills/` directly.

For Claude Code:

```bash
mkdir -p .claude/skills
ln -sfn "$PWD/.agents/skills/papers-manager-library" .claude/skills/papers-manager-library
ln -sfn "$PWD/.agents/skills/papers-manager-reading" .claude/skills/papers-manager-reading
```

Copy the directories instead of symlinking on platforms or hosts that do not
follow directory symlinks.

### 7.2 Runtime preflight

Both skills declare the papersManager MCP dependency in their descriptions and
perform a runtime preflight:

- missing/unreachable MCP tools are reported explicitly;
- read-only analysis may continue only when read tools exist;
- note writes stop when write tools are missing;
- `curl`, shell scripts, and other HTTP clients must not be used as fallbacks;
- unknown tool names are not guessed.

This matters because a host may discover a skill but not have the MCP server
configured. In that case the correct outcome is an explicit “MCP unavailable”
report, not a silent direct-API workaround.

## 8. DeepSeek Harness

DSH is a developer-preview, plugin-native host. It can consume a normal MCP
server; a papersManager-specific DSH plugin is **not required** for tool
functionality.

### 8.1 Recommended: MCP Center

Install the community MCP manager into the DSH web profile:

```bash
dsh plugin --profile web add dsh-mcp-center
```

Open **Settings → MCP Center** and add a remote server:

```text
name: papers-manager
transport: streamable-http
url: http://127.0.0.1:8090/mcp
Authorization: Bearer <token>
```

The manager exposes tools as `mcp__<server>__<tool>`. Review third-party
plugins before installing them. MCP Center stores bearer tokens in its own
configuration, so treat that file as a secret.

### 8.2 Direct DSH client configuration

DSH profiles can mount the official MCP client package directly, for example
in `$DSH_HOME/profiles/<profile>/cordis.patch.yml`:

```yaml
insert:
  - id: papers-manager-mcp
    name: '@deepseek-ai/dsh-mcp-client'
    config:
      serverName: papers-manager
      transport: streamable-http
      url: http://127.0.0.1:8090/mcp
```

Bearer/custom headers and reload behavior depend on the current DSH preview
version; check the installed `@deepseek-ai/dsh-mcp-client` documentation or use
MCP Center for a UI-managed configuration.

### 8.3 Skills

DSH discovers skills from, in priority order:

```text
<project>/.dsh/skills/<name>/SKILL.md
<project>/.agents/skills/<name>/SKILL.md
~/.dsh/skills/<name>/SKILL.md
~/.agents/skills/<name>/SKILL.md
```

If the shared `.agents/skills/` installation is already present, no extra DSH
installation step is needed.

### 8.4 Should this be a separate DSH plugin?

Only as a distribution wrapper.

A DSH plugin is justified if you want a one-click install, a Settings UI, or
DSH-specific tool approval/UI. Such a plugin should register
`@deepseek-ai/dsh-mcp-client` and the two skill directories, not reimplement
search/read/note tools. Reimplementing the tools would bypass the MCP schema,
revision fencing, and common error envelope.

Until DSH is a primary distribution target, the documented MCP client + skill
roots are the lower-maintenance integration.

## 9. Docker, WSL, and remote hosts

### 9.1 Docker

`MCP_ADDR=127.0.0.1:8090` inside a container is not reachable from the host
through a published port. Publish the port and bind the container listener to
all interfaces:

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

The host-side mapping is restricted to loopback, but the container must bind
`0.0.0.0` for Docker to forward traffic. `MCP_TOKEN` is mandatory in this
configuration.

Agents running on the host then use:

```text
http://127.0.0.1:8090/mcp
```

If an Agent runs in another container, use the API service name
(`http://api:8090/mcp`) in that container network and keep the token enabled.

### 9.2 WSL

If the API runs in WSL and the Agent runs on Windows, `localhost` usually
forwards to WSL automatically. If it does not, use the WSL VM address and set a
token; binding a non-loopback address without `MCP_TOKEN` is rejected.

### 9.3 Remote host

Preferred: keep MCP loopback-only and use an SSH tunnel:

```bash
ssh -L 8090:127.0.0.1:8090 user@server
```

Then connect the Agent to `http://127.0.0.1:8090/mcp` on the local machine.

If an Agent must connect directly over the network, bind a reachable address
and configure a strong `MCP_TOKEN`. There is no per-user ownership model yet;
any client with the token has the same access as a human user.

## 10. Verification and troubleshooting

### 10.1 Common checks

| Symptom | Check |
| --- | --- |
| `401 unauthorized` | Client header value matches `MCP_TOKEN`; token variable loaded in the host process. |
| `connection refused` | API is running, `MCP_ENABLED=true`, and `MCP_ADDR` is exposed/published. |
| `404` | URL must end in `/mcp`. |
| Server exits on startup with “MCP_TOKEN is required” | Non-loopback bind without a token. Set `MCP_TOKEN` or bind `127.0.0.1`. |
| Tools missing in host | Host has been restarted/reloaded; server name and URL are correct; skill preflight reports MCP availability. |
| Tool timeout | Increase the host timeout (`tool_timeout_sec` in Codex, `timeout` in OpenCode, MCP Center timeout in DSH). |
| Skill not discovered | Directory name matches the `name` field; `SKILL.md` is at the root; `references/` is present; host is reading the expected skills root. |
| Docker connection fails | Port publishes to `127.0.0.1:8090`; container `MCP_ADDR=0.0.0.0:8090`; token set. |

### 10.2 Tool-level checks

Ask the host to call:

```text
papers_manager_library_search
```

or inspect the MCP server's `tools/list`. All 19 tools start with the
`papers_manager_` prefix.

Note writes always require `expected_revision`. A `REVISION_CONFLICT` means the
note changed after it was read: reload the manifest/section and re-plan; do not
retry with the stale revision.

## 11. Security notes

- MCP defaults to loopback; do not expose it to a LAN or the internet without a
  token.
- A non-loopback bind without `MCP_TOKEN` fails closed.
- The server has no per-Agent identity or ownership model; tokens are server
  configuration, not user accounts.
- Agents can perform the same operations as the web UI, including deletes and
  metadata replacement. Review tool annotations and host approval settings.
- Keep tokens in environment variables or a host credential store; never commit
  them to `.mcp.json`, `opencode.json`, `config.toml`, or a DSH profile.
- MCP URL import accepts HTTP(S) URLs only; there is no arbitrary local-file
  read or filesystem path input.

## 12. Contract references

- [MCP tool contract](papers-manager-tools.md)
- [MCP tool manifest](papers-manager-tools.json)
- [Skill overview](../../agent-skills/README.md)
- [Reading-note human/Agent contract](../reading-note-agent-contract.md)
- [Reading notes standard](../../agent-skills/papers-manager-reading/references/notes-standard.md)
