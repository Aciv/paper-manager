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
python scripts/desktop/build_desktop_bundle.py \
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
Managed Windows installation requires ASCII program/data paths and rejects
non-ASCII paths before maintenance. The lower-level PostgreSQL runtime can use
ASCII short-path aliases for existing direct-launch configurations, but managed
installation does not depend on 8.3 aliases being enabled. The Unicode relocation
probe records the explicit refusal and then exercises ASCII installation when
aliases are unavailable. Shortcuts are optional and disabled by default.

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
python scripts/desktop/check_desktop_launcher.py \
  --bundle /absolute/path/to/desktop-candidate --output-dir /tmp/desktop-results
```

The validation controller requires psutil==7.0.0, grpcio==1.82.1 and protobuf==7.35.1.
These are test tools, not dependencies installed into the production environment.
The application receives a controlled PATH and its own private interpreter; the
probe verifies executable origins, mandatory production models, setup failure and
retry, upload/queue/parse/blocks/notes/MCP, persistence, process crash cleanup,
occupied ports, c1/c4 performance, 20 mixed tasks, worker crash recovery and idle
retirement. Each worker kind must retain one shared PID throughout stable loads.
The three target jobs in desktop-d3.yml download an archive and standalone harness
in a new job without checking out the repository or installing application runtimes.

Current candidate baselines are Ubuntu 22.04 x64, macOS 14 arm64 and Windows
Server 2022 x64. CI on Server 2022 is not proof of Windows 10 UI behavior. A
successful native artifact job is required before describing a target as tested.

Relocation before setup is part of D3 verification. Changing the program location
for already configured user data, installer behavior, signatures, backup/restore,
upgrades and uninstall are D4 work. Existing D2 data requires explicit setup once
to validate the cluster and write initialized.json; it is never rebuilt implicitly.

## Export and check an internal delivery

Export a directory built with the current Launcher without recompiling runtimes:

```bash
python scripts/desktop/package_desktop_bundle.py \
  --bundle tmp/desktop-candidate --output tmp/releases/desktop.tar.gz
```

Use `--output tmp/releases/desktop.zip` for Windows; Unix uses tar.gz.
Default user archives contain exactly four root entries: the native program,
`bundle/`, a short operations-only `README.md`, and the target platform's
`Uninstall.cmd`, `Uninstall.command` or `Uninstall.sh`.
No installer, validation harness, reports or root-level metadata/license copies
are shipped. Required notices and runtime integrity metadata stay inside bundle/.
The archive's `.sha256` and `.manifest.json` sidecars remain outside it for tooling.
A fresh build with `unpacked_uninstall: 1` is required for native uninstall support.

Verify a user archive from the repository (tools are not shipped to the user):

```bash
python scripts/desktop/check_desktop_package.py \
  --archive tmp/releases/desktop.tar.gz --verify-only
```

For real runtime validation, additionally pass `--output-dir PATH` and
`--harness-dir scripts/desktop`. Managed install/upgrade regression archives
are explicitly internal and retain their historical tools:

```bash
python scripts/desktop/package_desktop_bundle.py --audience validation \
  --bundle tmp/desktop-candidate --output tmp/releases/desktop-validation.tar.gz
```

`--reports-dir`, `--failure-probe` and `--layout maintenance` are accepted only
with `--audience validation`. The CI workflow explicitly selects this internal
mode for its existing managed upgrade/rollback checks; it is not the user package.
The same platform build also exports and verifies the minimal user archive and
uploads it as `paper_manager-<runner>`, retained for seven days. Download these
artifacts only after the full workflow, including native acceptance, succeeds.
No second component build is performed and no GitHub Release is published.
Existing archives remain verifiable. User archives do not install global services;
removal keeps user data and requires the application to have exited.

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
