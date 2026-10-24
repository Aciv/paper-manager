# D3 internal directory bundles

These commands build private validation candidates, not public releases or
signed installers. The maintainer has deferred the project license. See
[third-party notices and unresolved public distribution gates](THIRD_PARTY_NOTICES.md).

Build on each target OS/architecture. Required build tools are Go from go.mod,
Node 22+, npm, Python 3.12+, and (Unix) make, C compiler, bison, flex and Perl.
macOS also uses its SDK, otool, install_name_tool and ad-hoc codesign to relocate
private PostgreSQL dylibs. This ad-hoc signing is not Developer ID/notarization.
Windows uses the hash-pinned EDB ZIP and does not install global services.

```bash
python scripts/build_desktop_bundle.py \
  --output tmp/desktop-candidate --cache tmp/desktop-downloads \
  --revision YOUR_COMMIT_SHA --profile hybrid
```

The output path must not exist. Downloads are content-addressed and verified
against sources.lock.json on every use. Python wheels are fixed by exact versions
and PyPI hashes in requirements-runtime.lock; source builds are forbidden.
Go modules and npm dependencies use their existing repository lock/checksum files.
The build never copies .venv, developer .env files, uploads, notes or credentials.

The default hybrid candidate includes the two authorized header/citation CRF
weights and validates them using fail-closed CRF mode. It cannot silently fall
back to rules to pass packaging. The disabled-by-default large reference weight
is local and optional (`--include-reference-model`). `--profile rules` is an
explicit separate validation profile and has no equivalent quality claim.

The bundle contains launcher/server, web/, pdf_parser/, private python/,
postgres/ and redis/. PostgreSQL includes pg_trgm required by migration 28.
Windows excludes pgAdmin, StackBuilder, their additional interpreter/Electron,
and the unused Redis .NET service wrapper.

Each bundle has a revision/target/profile manifest, per-file SHA-256 hashes,
SHA256SUMS, wheel identities/hashes, source archive lock, Go module identities,
frontend lock and a CycloneDX component inventory (including shipped models).
licenses/ preserves supplied notices. These materials do not constitute a
complete legal audit or a publisher signature.

Validate the artifact from another working directory with an ordinary user:

```bash
python scripts/check_desktop_launcher.py \
  --bundle /absolute/path/to/desktop-candidate --output-dir /tmp/desktop-results
```

The validation controller requires psutil==7.0.0, grpcio==1.82.1 and protobuf==7.35.1.
These are test tools, not dependencies installed into the production environment.
The application receives a controlled PATH and its own private interpreter; the
probe verifies executable origins, mandatory production models, setup failure and
retry, upload/queue/parse/blocks/notes/MCP, persistence, process crash cleanup,
occupied ports, c1/c4 performance, 20 mixed tasks, worker crash recovery and idle
retirement. Each worker kind must retain one shared PID throughout stable loads.
The three target jobs in desktop-d3.yml download a tarball and standalone harness
in a new job without checking out the repository or installing application runtimes.

Current candidate baselines are Ubuntu 22.04 x64, macOS 14 arm64 and Windows
Server 2022 x64. CI on Server 2022 is not proof of Windows 10 UI behavior. A
successful native artifact job is required before describing a target as tested.

Relocation before setup is part of D3 verification. Changing the program location
for already configured user data, installer behavior, signatures, backup/restore,
upgrades and uninstall are D4 work. Existing D2 data requires explicit setup once
to validate the cluster and write initialized.json; it is never rebuilt implicitly.
