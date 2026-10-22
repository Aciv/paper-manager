# Agent skills

Two skills cover the current product loop:

- `papers-manager-library` — ingestion, metadata, Bases, processing state, and
  deletion. It is deliberately thin and safety-oriented.
- `papers-manager-reading` — hierarchical reading, sectioned note generation,
  source mapping, evidence placement, and Base-summary workflow.

`papers-manager-base-review` is intentionally deferred until Phase 3B has a
stable Base-analysis contract. Until then, Base summaries are composed from the
reading skill's existing read tools; there is no hidden `base_summary` tool.

## Runtime dependency

The skills assume the papersManager MCP tools are available. Tool names, input
schemas, response envelopes, bounds, and access rules live in
[`docs/mcp/papers-manager-tools.md`](../docs/mcp/papers-manager-tools.md) and
[`docs/mcp/papers-manager-tools.json`](../docs/mcp/papers-manager-tools.json).

The MCP server is an adapter over the existing application services. It does
not change storage, add Agent identities, or persist derived outlines. All tool
names use the `papers_manager_` capability prefix; hosts may add another
namespace layer. The server exposes Streamable HTTP at `/mcp` only; hosts that
require stdio need a client-side bridge. Per-host installation and skill
locations are in
[`docs/mcp/agent-installation.md`](../docs/mcp/agent-installation.md).

Both skills include a runtime preflight. Missing or unconfigured MCP tools are
reported explicitly and writes stop; scripts, `curl`, or other HTTP fallbacks
must not be used to bypass the MCP boundary. The description-level dependency
clause is only a selection hint, because different Agent hosts do not share an
install-time dependency checker.

Formula policy: use the source Block or PDF page/rectangle index as the formula
source. Do not turn garbled PDF text into guessed LaTeX; keep the formula in the
PDF and link it through an evidence URL. Inline LaTeX rendering is deferred.

## Progressive disclosure

Each `SKILL.md` contains only workflow policy and invariants. Detailed reading
levels, note format, evidence placement, examples, and tool indexes are in the
skill's `references/` directory and are loaded only when needed.

## Current status

The MCP server/tools are implemented in `internal/mcp` and started by the API
process when `MCP_ENABLED=true`; the 19 prefixed tools are registered from the
manifest. The skill source bundles in this directory are usable in a project
that exposes that MCP server. Formal skill packaging/export (installer or
download artifact) is still pending.
