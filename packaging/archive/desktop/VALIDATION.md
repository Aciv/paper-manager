# D3 internal validation evidence

## Unpacked delivery change, 2026-10-25

Current source defaults to a root native `paper_manager` / `paper_manager.exe`
entry, Windows ZIP and Unix tar.gz. The accepted CI artifacts below predate this
layout and must not be described as tests of the new entry. No Actions run was
triggered for this change.

Local checks passed: full Go tests/build, desktop race tests, web production build,
28 desktop-tool tests and actionlint. A Windows-native Go test executable passed
the new runtime discovery, identical-copy rebinding, identity preservation and
different-build/missing-original refusal tests on the local Windows host.

The current Linux launcher/server and parser/UI source were overlaid on sealed
native dependencies using the explicitly test-only fixture builder. The exported
archive passed the full ordinary-user, offline, read-only-container harness,
including root-entry discovery without `--install-dir`, initialization/retry,
upload/queue/parse/notes, restart persistence, process failures and c1/c4 loads.
Startup was 2.218 s; synthetic one-page cold upload-to-read was 4.075 s.
Shared workers, crash recovery and idle retirement passed. These are host-local
measurements, not a cross-platform performance comparison or release build.
Evidence: `tmp/desktop-d4/unpacked-results/`; archive:
`tmp/desktop-d4/releases/unpacked-local.tar.gz`.
The same archive also passed managed-install setup/reinstall, a synthetic version
upgrade preserving parsed data, real failed SQL/file rollback, controlled cleanup,
uninstall preserving data and exact-version reinstall. Report: `upgrade/upgrade.json`.
Latest Windows ZIP and macOS root-entry full native acceptance remain unrun.

## Previously accepted managed-install candidates

Status: native candidate runtime and managed lifecycle acceptance passed on all
three selected baselines. Windows completed the full default-without-shortcut
flow at c5feaa4 in [run 40191338426](https://github.com/Aciv/paper-master/actions/runs/40191338426).
Linux/macOS evidence remains fe2506b from run 40181692843; these are distinct
revision-specific candidates, not a claim that every platform ran the same Git SHA.

| Target | Full artifact evidence | Scope |
| --- | --- | --- |
| Ubuntu 22.04 x64 | fe2506b, run 40181692843, passed | Runtime, install, synthetic upgrade, actual rollback, uninstall/reinstall; shortcuts enabled in that snapshot |
| macOS 14 arm64 | fe2506b, run 40181692843, passed | Same full lifecycle; shortcuts enabled in that snapshot |
| Windows Server 2022 x64 | c5feaa4, run 40191338426, passed | Same full lifecycle, default shortcuts off, owned user environment and uninstall staging |
| Windows 10 x64 | Local runtime/archive and focused native checks passed | Additional host evidence, not a substitute for Server 2022 |

[validation-1.0.0.json](validation-1.0.0.json) records exact revisions, report/archive
hashes and measured baselines. Original archives are retained locally under
`tmp/desktop-d4/final-artifacts/`, with CRC and SHA-256 verification. Its index.json
selects the accepted Windows c5feaa4 package; the failed 3f1a58b package is under
history/ and is not a usable delivery. The earlier Unix archives retain their
original installer behavior; current source defaults to optional shortcuts on
all platforms. They are not relabelled as newly built c5feaa4 binaries.

D3 native candidate packaging is verified. D4 core installation/update/recovery/
uninstall implementation and technical acceptance are complete; D4 public-release
preparation remains in progress. Project-owned code is MIT; third-party binary
redistribution review, publisher signing/notarization decisions and real-user
OS/antivirus/sleep-resume checks remain open. No official release was published
by this task. Original failures and local archive evidence are preserved below.

## Historical D3 archive evidence

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
python scripts/desktop/check_desktop_package.py --archive PATH/desktop.tar.gz --verify-only
python scripts/desktop/check_desktop_package.py --archive PATH/desktop.tar.gz --output-dir RESULTS
```

The Linux archive exercise used a private controller interpreter inside the
isolated container described above. Windows used a separate private native
controller with psutil, grpcio and protobuf, and only the two stdlib bootstrap
files; application code and the acceptance harness came from the archive. Native
Go also passed `go test ./internal/runtime/desktop -run TestPostgresProgramPath
-count=1`. Exact host invocations are recorded in the local .codex_plan.
Heavy CI is manual, defaults to the minimal Windows CRF
case, and can target one OS. It was syntax-checked locally, not executed remotely.

Public redistribution review, signing and automatic migration of unmanaged existing
installations remain separate gates. Managed install/upgrade/restore/uninstall
are implemented; see INSTALL.md and the D4 evidence below.


## D4 acceptance snapshot: 2026-10-25

Commit 07ea065 added managed version directories, installer bootstraps, shortcuts,
complete cold checkpoints, offline upgrades, isolated migration probes, restoration,
owned-resource retention and data-preserving uninstall. The application code is
MIT; bundled dependencies retain their own license terms. No signing/notarization
or official release has been performed.

Local Linux ordinary-user, network-disabled, read-only-root archive acceptance
passed at tmp/desktop-d4/linux-results-r3/upgrade.json. It installed the application,
uploaded/parsed a PDF via Asynq, persisted a reading note, upgraded synthetic
metadata from 1.0.0 to 1.0.1, injected an actual failing SQL migration and note/file
mutations in synthetic 1.0.2, verified rollback, uninstalled and reinstalled with
data retained. These synthetic versions use the same application source; they
are not evidence of an actual prior public release or an arbitrary future payload
migration. The reused native dependencies and local working-tree fixture are
labelled in the manifest; this is not the final clean-commit platform build.

Run [40171815263](https://github.com/Aciv/paper-master/actions/runs/40171815263)
built Linux successfully. Windows and macOS both failed the same cleanup fixture:
it wrote an uncanonicalized temporary DataDir into an ownership record, while the
maintenance runtime correctly compared canonical paths. Subsequent artifact jobs
were skipped by the old shared matrix dependency; no platform received final
D3/D4 acceptance from that run. The fixture now uses the installation's recorded
DataDir; Linux additionally exercises a real symlink alias. Each target now owns
a separate reusable build/acceptance workflow, with a fresh runner for acceptance.
Ordinary pushes and pull requests still do not launch heavy builds.

Current checks and the next consolidated matrix will update this evidence;
D3/D4 remain in progress until platform acceptance passes. Public redistribution
review, OS download/signature prompts, antivirus and sleep/resume checks remain
separate from automated application lifecycle tests.


### Consolidated run 40181692843

All three native builds passed. Ubuntu 22.04 and macOS 14 arm64 completed both
artifact probes successfully, including c1/c4 and 20 mixed parser requests,
worker recovery/idle retirement, real failed SQL migration and durable-file
rollback, shortcuts, uninstall and exact-version reinstall. Their JSON reports
were retrieved locally under tmp/desktop-d4/internal-desktop-results-*.zip.
Startup was 2.01 s (Linux) / 2.07 s (macOS), and one-page cold upload-to-reading
was 3.04 s / 2.78 s respectively; these are separate host baselines, not a
cross-platform speed comparison or a before/after performance claim.

Windows Server 2022 loaded both production CRF models, then explicitly rejected
the Unicode PostgreSQL program path: that runner volume has no usable ASCII
short-path alias. This matches the documented limitation, but leaves Windows
artifact acceptance incomplete. The Windows harness now preserves that exact
refusal under unicode-path/ and, only for this specific error, copies the delivery
to an owned ASCII path and runs every lifecycle/upgrade probe there. The result
records path_mode=ascii-fallback; this does not certify arbitrary Unicode paths.
Unknown initialization errors cannot use this fallback. A dedicated
validation/d4-windows-* tag selects only Windows for the final follow-up; already
passed platforms need not be rebuilt for Windows-only validation changes.


### Windows local follow-up

The actual fe2506b Windows archive, using updated validation tools in explicit
ASCII mode, passed the complete runtime/CRF/setup/shared-worker benchmark probe
on Windows 10 as an ordinary user. D4 then passed first install, synthetic upgrade
and real SQL/file rollback, but failed when PowerShell could not immediately
remove its temporary uninstaller EXE. The failing report is retained under
tmp/desktop-d4/windows-ascii-local-r2/upgrade.
This is a real Windows cleanup failure, not an application/queue compatibility pass.

The generated uninstaller now explicitly waits for the helper and retries temporary
cleanup for up to 20 attempts. A remaining helper cleanup problem warns with the
owned path while preserving the actual uninstall exit code. A rebuilt native
launcher passed two focused install/uninstall/reinstall cycles; both preserved
notes/config and removed their temporary helper. Evidence:
tmp/desktop-d4/windows-uninstall-focused/uninstall.json. The fixture is labelled
local-uninstall-fix-fixture, not a clean-commit release. Full Windows Server 2022
artifact acceptance remains pending the single-target follow-up.


### Windows run 40185062937 and installation path policy

[Run 40185062937](https://github.com/Aciv/paper-master/actions/runs/40185062937)
built a21ddb4 successfully. Its Windows runtime, setup retry and c1/c4/20-task
benchmark reports all passed in explicit ASCII mode, preserving the Unicode
PostgreSQL refusal report. Artifact acceptance then failed in the CI wrapper:
PowerShell is case-insensitive, so assigning a Process to `$upgrade` collided
with the typed `[switch]$Upgrade` parameter. No final upgrade report was collected;
this run cannot certify D4 upgrade acceptance.

The process variable is now `$upgradeProcess`. A native PowerShell execution test
runs the actual wrapper with fake account/process operations and real temporary
report files, verifies ASCII fallback and runtime/upgrade report collection, and
confirms that the original variable assignment still fails. This check runs before
Windows packaging; it does not create real users or start application services.

The maintainer accepted the Windows path limitation. The packaged installer now
rejects non-ASCII program/data paths before invoking maintenance, with English-path
examples; no directory is created or existing user file removed on rejection.
It no longer attempts to promise installation through 8.3 aliases. Unix Unicode
installation remains supported. Installer validation is covered by focused Python
tests for ASCII acceptance, non-ASCII rejection before mutation, and Unix behavior.
The lower-level runtime's existing path checks remain as additional error reporting.


### Run 40187549892 and optional shortcuts

[Run 40187549892](https://github.com/Aciv/paper-master/actions/runs/40187549892)
built 184fc37 and passed Windows runtime/setup/parser benchmark checks. The first
managed installation reached healthy services, but its optional WScript/PowerShell
shortcut process failed at the 30-second limit. The installer preserved success;
the then-required shortcut assertion stopped D4 acceptance before upgrade testing.
Raw reports are retained at tmp/desktop-d4/windows-184-results.zip. That run is
not evidence of passed Windows upgrades or rollback.

The maintainer explicitly requested optional shortcuts. The packaged installer
now defaults to no shortcut; --create-shortcut or explicit --shortcut-dir opts in.
The default Windows path never starts a desktop lookup. Requested shortcut lookup
or creation failures warn without failing installation. Go shortcut errors now
include timeout/error output instead of only an exit code. Native COM replacement
was researched but not implemented after this scope decision.

The default D4 probe verifies install/idempotent reinstall without creating an
unsolicited shortcut directory, while retaining every upgrade, real SQL rollback,
uninstall and data-preservation check. --test-shortcuts is a separate explicit
probe option. Focused Python tests cover default/opt-in/custom directory behavior
and optional Windows lookup failure; Go verifies an actual shortcut filesystem
failure cannot change healthy installation state or overwrite an obstruction.
No hosted workflow has been started for this change at this point.


### Run 40189037420: inherited administrator TEMP

The Windows default-without-shortcut run passed install, real PDF/queue/parser,
synthetic upgrade and actual SQL/file rollback. Uninstall failed before starting
its helper: the ordinary credential inherited the administrator runner's TEMP,
pointing to C:/Users/RUNNER~1/AppData. This is captured in uninstaller.log, not a
parser/concurrency failure. That archive is not marked accepted.

The validation wrapper now supplies owned TEMP/TMP/USERPROFILE/HOME/APPDATA/
LOCALAPPDATA directories and restores its parent environment afterward. The
uninstaller places its helper under the application's data/maintenance/staging,
with the same resource.json ownership/timestamp format as managed cleanup. It
therefore does not require a writable system TEMP; normal cleanup removes the
helper, while interrupted helper staging can be handled by existing owned TTL
cleanup. A native regression executes the generated PowerShell uninstaller with
TEMP/TMP deliberately pointing to a blocking file, verifies helper arguments and
ownership, and checks that only its own staging is removed. No database services
or actual account creation are needed by that regression.


### Windows c5feaa4 accepted: run 40191338426

Both native build and independent artifact-test jobs passed. The four retained
reports (launcher.json, setup-failures.json, parser-benchmark.json and
upgrade/upgrade.json) all report passed, is_admin=false where applicable, and
explicit ASCII installation mode. The upgrade report includes real PDF/queue/
parser persistence, synthetic 1.0.1 upgrade, failed 1.0.2 SQL/file mutation rollback,
owned cleanup, data-preserving uninstall and exact-version reinstall. Shortcuts
were not requested. This validates the inherited-TEMP fix without weakening
uninstall/data-preservation assertions. New native regression and actual-wrapper
environment assertions also ran during the build.

Windows baseline startup was 2.422 s and cold one-page upload-to-reading 4.157 s.
These are synthetic host observations, not a before/after speedup or real-paper
quality claim. Windows program/data ASCII restrictions remain explicit.

Successful local source checks after the fix:

```bash
/usr/local/go/bin/go test ./...
/usr/local/go/bin/go build ./...
/usr/local/go/bin/go test -race ./internal/runtime/... ./cmd/launcher
.venv/bin/python -m unittest discover -s scripts -p 'test_desktop*.py'
cd web && npm run build
git diff --check
```

The script suite ran 26 tests. Native Windows additionally executed
TestWindowsUninstallerIgnoresUnwritableSystemTemp and
test_windows_desktop_harness.ps1, including the deliberate old-variable regression.
