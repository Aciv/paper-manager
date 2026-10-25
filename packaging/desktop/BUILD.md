# D3 internal directory bundles

Product naming, application versions, update checkpoints and managed cleanup
follow [RELEASE_POLICY.md](RELEASE_POLICY.md). New bundles use product.json
(`paper_manager`, application 1.0.0) and reject inconsistent web/MCP versions.
This does not change dependency or schema versions, or relabel existing artifacts.

These commands build private validation candidates, not public releases or
signed installers. Project-owned code uses MIT; dependency terms remain separate. See
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

For a local build without Node, `--web-dist PATH` reuses an already built static
directory containing index.html and assets/. If it came from another bundle,
the sibling licenses/web directory is reused too. `--wheel-dir PATH` installs
previously downloaded wheels offline, still checking every pinned version/hash.
These inputs must match the intended revision and target; the manifest records
prebuilt/offline use. The default CI build continues to build the UI and download
locked target wheels. Windows native Go builds require a local drive for source
and module caches; a WSL UNC share does not provide Go's required file locking.
Windows PostgreSQL uses ASCII short-path aliases for both program and data paths.
If a volume disables those aliases, or an alias still contains non-ASCII
characters, setup requires ASCII installation/data paths and reports this before
initialization. The Unicode relocation probe uses a mixed ASCII/Chinese name.

The default hybrid candidate includes the two authorized header/citation CRF
weights and validates them using fail-closed CRF mode. It cannot silently fall
back to rules to pass packaging. The disabled-by-default large reference weight
is local and optional (`--include-reference-model`). `--profile rules` is an
explicit separate validation profile and has no equivalent quality claim.

The bundle contains launcher/server, web/, pdf_parser/, private python/,
postgres/ and redis/. PostgreSQL includes pg_trgm required by migration 28.
Windows excludes pgAdmin, StackBuilder, their additional interpreter/Electron,
and the unused Redis .NET service wrapper.
PostgreSQL receives private MSVC DLLs from the hash-locked CPython/NumPy inputs;
windows-native-libraries.json records their source paths and hashes. Acceptance
checks the loaded PostgreSQL DLL paths, so a host VC++ installation cannot mask
missing bundled runtimes. Their public redistribution terms remain in the D4 audit.

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

## Export and check an internal delivery

Export an already built directory without recompiling any runtime:

```bash
python scripts/package_desktop_bundle.py \
  --bundle tmp/desktop-candidate --output tmp/releases/desktop.tar.gz
```

This writes `desktop.tar.gz` plus `desktop.tar.gz.sha256`. The tarball contains
`bundle/`, `validation/` and `package.json`. It refuses existing output files and
an output inside the runtime directory. `package.json` seals the validation tools
as well as the runtime identity. Optional `--reports-dir PATH` attaches only the
three passed JSON acceptance reports, with the same runtime revision/target/profile;
it does not copy runtime.json, uploads, notes, credentials or arbitrary log files.

```bash
python scripts/check_desktop_package.py --archive tmp/releases/desktop.tar.gz --verify-only
python scripts/check_desktop_package.py --archive tmp/releases/desktop.tar.gz \
  --output-dir /tmp/desktop-package-results
```

The second command extracts into a disposable Unicode directory, verifies the
archive, runtime and harness, then runs the harness from the archive. It requires
an ordinary account and the validation-controller dependencies described above.
Application components come entirely from the extracted bundle. The runtime's
revision and the validation file hashes are recorded separately; exporting a
previously validated runtime does not relabel its application revision.

## Remote validation budget

Both heavy desktop workflows are manual. Neither pushes nor pull requests launch
them. No workflow is invoked by the export or verification commands above.

D3 offers `windows-crf`, `ubuntu-22.04`, `windows-2022`, `macos-14` and `all`.
The default is the small Windows model-path test; `all` is reserved for final
acceptance. Ordinary fixes are checked locally, then with a single relevant
native target only when needed and explicitly authorized.

Before D3 is merged into main, use the existing D0 workflow as the manual entry
on branch `codex_main`, with suite `bundle`. It calls that branch's D3 workflow
from the same commit. Suite `native-all` explicitly selects the original full
D0-D2 matrix. This avoids requiring unvalidated D3 code to be merged merely to
register a new manual workflow. GitHub requires the manually dispatched workflow
to exist on the default branch; same-repository reusable workflows come from the
caller's commit ([manual runs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow),
[workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)).

See [VALIDATION.md](VALIDATION.md) for measured results and remaining native gates.
