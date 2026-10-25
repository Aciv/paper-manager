# D3 internal validation evidence

Status: Linux archived delivery and Windows 10 native directory/archive acceptance
passed; the final selected-baseline native matrix is pending.
GitHub Actions free allowance is exhausted (maintainer confirmed).
No additional hosted validation runs are authorized or started for this handoff.
The project license remains undecided; this is not a public release approval.

Linux runtime revision: `66a99c66571def5d7f1374161e3ee010b863152e`.
The export tool keeps this revision and independently records validation-tool
hashes. Exported archives and raw reports are local runtime artifacts under
`tmp/desktop-d3/`, not committed binaries or user documents.

## Current artifact test

The 207 MiB Linux tarball and SHA-256 sidecar are under
`tmp/desktop-d3/releases/`. The tarball includes the sealed application, standalone
validation tools and three matching passed JSON reports. The following archive
was checked, extracted and exercised without mounting the application source:

`papers-manager-linux-x86_64-internal-66a99c6-r2.tar.gz`

Environment: Ubuntu 24.04 container, ordinary UID 1000, no network, read-only
controller/resources/archive mounts, disposable writable tmpfs and a dedicated
result directory. Only two generic bootstrap files and test-controller tools were
mounted. The application and its Python/PostgreSQL/Redis/Go executables came from
the extracted tarball, under a Unicode directory. No global application runtimes
or developer tools were installed. Docker is a test isolation tool here, not an
application runtime requirement.

Passed interface scenarios: explicit first setup and retry, no implicit initdb,
mandatory production header/citation CRF load, static UI, authenticated MCP,
upload/Asynq/parse/blocks/reading/notes, note persistence, interrupted 20-page
recognition marked failed on restart and completed after explicit retry, duplicate
instance rejection, occupied-port refusal, corrupt-data preservation, permission
and file-quota failures, single shared workers, extraction-worker crash recovery,
4+4 mixed calls and idle retirement. All data is generated synthetic PDF/text.
A separate 8 MiB tmpfs ENOSPC test also passed; no partial cluster or completion
marker was published (`tmp/desktop-d3/clean-results/disk-full.json`).

Raw latest reports: `tmp/desktop-d3/package-r2-results/`. This second export also
passed with ordinary controller bytecode defaults: the sealed harness suppresses
its own bytecode writes, so verification does not mutate the package inventory.

## Baseline (synthetic fixture, same local host)

Startup: 2.23 seconds. One-page cold upload-to-readable-Markdown: 3.57 seconds.
These are current artifact baselines, not a speedup claim or a real-paper quality
benchmark. c1/c4 denotes client concurrency; worker budgets stay fixed. Each kind
retained one PID during stable batches; after intentional extraction-worker kill,
one replacement worker completed requests and the parent remained available.

| Client concurrency | Cold Python parent MiB | Parent after worker retirement MiB | Recognition jobs/s | 20 mixed tasks, seconds |
| --- | --- | --- | --- | --- |
| 1 | 43.2 | 44.7 | 1.36 | 4.41 |
| 4 | 43.4 | 44.7 | 2.31 | 5.75 |

## Native platform evidence

| Target | Evidence | Remaining gate |
| --- | --- | --- |
| Linux x64 | Current archived delivery passed on Ubuntu 24.04; earlier native Ubuntu 22.04 artifact passed at 0658002 | Final selected-baseline run of the latest packaging/harness when quota is available |
| macOS 14 arm64 | Native artifact passed at 0658002, run 40112951590 | Final latest-revision artifact verification |
| Windows Server 2022 x64 | Native build passed at 0658002; ordinary-user Unicode model load failed | Re-run minimal model-path test for the implemented buffer-backed fix, then final artifact verification |
| Windows 10 x64, build 19045 | Current working-tree directory and exported archive passed full ordinary-user acceptance, private-DLL checks, four native CRF tests and the native Go program-path test | Final selected-baseline matrix remains separate |

The local Windows tests cover real CRF training/reload, Unicode-path prediction,
buffer lifetime and error boundaries. Native CRFsuite raises ValueError for this
filename conversion failure, so the fix now chooses in-memory loading before
calling the filename API and retains the backing bytes. ASCII/Unix loading stays
unchanged. Evidence: tmp/desktop-d3/windows-native/crf-native.json. This closes
the Windows 10 model-path test. Full directory results are under
tmp/desktop-d3/windows-native/native-results/. This does not certify Server 2022.
No current three-platform release is declared supported.

## Windows native directory result

The local working-tree candidate is labeled `local-windows-d3-validated`, not a
clean commit SHA. It reuses the sealed Linux UI assets and installs locked Windows
wheels offline; Go was compiled natively with private Go 1.26.5 on NTFS. Only
isolated temporary directories and owned child processes were used. No global
Python, Node, PostgreSQL/Redis installation or system service was changed.

Actual native failures closed during this validation:

- CRFsuite's Unicode filename exception is ValueError; retained memory loading
  now avoids that filename API only for non-ASCII Windows paths.
- EDB needs private MSVC DLLs. The builder copies them from locked CPython/NumPy,
  records provenance, and verifies PostgreSQL's loaded DLL paths belong to the
  bundle. Host System32 MSVC DLLs cannot satisfy this probe.
- initdb post-bootstrap SQL failed on a Chinese installation path, even with an
  ASCII data path. initdb/postgres/pg_ctl now use ASCII program-path aliases too.
  Volumes without usable aliases require ASCII installation/data paths explicitly.
- A WSL UNC source path cannot supply native Go file locks; a UNC file proxy also
  lacks a Windows drive letter. Owned NTFS source/module caches resolve both.
  A missing transitive .mod in the test proxy was filled from the existing Linux
  cache, then network fallback was disabled for this local build.

The 205 MiB Windows archive is
tmp/desktop-d3/releases/papers-manager-windows-x86_64-internal.tar.gz, accompanied
by its SHA-256 sidecar and three passed directory reports inside the archive.
The directory probe covered setup/retry, all services, private libraries,
upload/Asynq/parse/notes/MCP, persistence, interrupted-task retry, port/instance
conflicts, corrupt-data preservation, shared workers and worker crash recovery.
The archive was then independently extracted into a new Unicode directory and
exercised using its sealed validation tools, without application source. All three
reports passed under tmp/desktop-d3/windows-native/package-results/. Test services
and synthetic databases were stopped/removed; private build caches remain for
future local validation. No hosted CI runs were used.

Directory baseline: startup 3.50 s; cold one-page upload to Markdown 4.66 s.

| Client concurrency | Cold Python parent MiB | Parent after retirement MiB | Recognition jobs/s | 20 mixed tasks, seconds |
| --- | --- | --- | --- | --- |
| 1 | 33.9 | 36.2 | 1.06 | 6.16 |
| 4 | 34.8 | 36.3 | 1.71 | 5.52 |

The independent archive run observed 4.05 s startup, 5.09 s cold upload to
Markdown, and 36.2/36.9 MiB parent RSS after c1/c4 worker retirement.

These synthetic observations are not a before/after speedup claim. Windows script
tests ran 18 cases: 17 passed and one symlink-creation test was explicitly skipped
because this ordinary account lacks that permission. Windows setup obstruction
and retry passed; Unix chmod/file-quota scenarios remain Unix-specific evidence.

## Validation commands

```bash
.venv/bin/python -m unittest discover -s scripts -p 'test_desktop*.py'
/usr/local/go/bin/go test ./...
/usr/local/go/bin/go build ./...
/usr/local/go/bin/go test -race ./internal/runtime/desktop
cd web && npm run build
```

The 18 focused script tests check archive/harness tampering, wrong or failed report
attachment, overwrite refusal, recursion prevention and safe extraction. They
also check private Windows MSVC assembly and omitted GUI helpers. Existing
runtime evidence additionally includes Python 154 tests after the Windows fix,
Go lifecycle race tests and 34 frontend reading-note tests. The CRF filename
adapter is the only production parser change in this handoff; the shared-worker
architecture and concurrency budgets are unchanged.

Archive validation:

```bash
python scripts/check_desktop_package.py --archive PATH/desktop.tar.gz --verify-only
python scripts/check_desktop_package.py --archive PATH/desktop.tar.gz --output-dir RESULTS
```

The Linux archive exercise used a private controller interpreter inside the
isolated container described above. Windows used a separate private native
controller with psutil, grpcio and protobuf, and only the two stdlib bootstrap
files; application code and the acceptance harness came from the archive. Native
Go also passed `go test ./internal/runtime/desktop -run TestPostgresProgramPath
-count=1`. Exact host invocations are recorded in the local .codex_plan.
Heavy CI is manual, defaults to the minimal Windows CRF
case, and can target one OS. It was syntax-checked locally, not executed remotely.

Public licensing audit, signed installers, upgrades, relocation of existing user
configuration and uninstall/restore workflows remain D4 work.
