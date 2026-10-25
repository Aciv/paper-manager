# Internal desktop validation bundle

For private validation only. No public redistribution approval is implied.
Read THIRD_PARTY_NOTICES.md before sharing. The bundle includes private CPython,
PostgreSQL 17, Redis, parser source, Go entry points and prebuilt web resources.
Users do not need Go, Node, Python, pip, Redis or PostgreSQL installed.

The outer archive provides the native `paper_manager` (`paper_manager.exe` on
Windows) entry. Unpack the entire archive and run that executable; no installer
or system Python is needed. It locates this nested bundle beside itself.
Install.sh/Install.command/Install.cmd remain optional managed-install helpers.
Read the outer README.md for data locations and maintenance limitations.

Run as an ordinary user. Keep this program directory separate from writable
application data. When using this raw inner directory, open `launcher`
(`launcher.exe` on Windows). Choose “首次初始化 / 重试” in the management page.
Initialization creates a private database and exits the setup operation;
then choose “启动”. Closing the browser leaves the processes running. Use “退出
Launcher” to stop owned services. A machine without browser integration can open
the printed loopback management URL manually.

Installers can invoke the same setup explicitly:

```
launcher --data-dir /absolute/user/data --initialize
launcher --data-dir /absolute/user/data --open-browser=false
```

`--configure` only creates/validates runtime.json. It does not initialize a DB.
`--initialize` is idempotent for healthy initialized data; it never rebuilds a
corrupted cluster. Do not run as root/administrator. Keep runtime.json private;
it contains the database credentials and MCP token. Back it up with user data.

`bundle.json` records the native target, revision, extraction profile and exact
files. `SHA256SUMS` also seals the manifest. Checksums detect accidental changes;
they are not a publisher signature. The rules validation profile excludes CRF
weights explicitly. Hybrid builds require pinned header/citation weights; --include-reference-model optionally adds the large local weight.
The core parser/reading path works offline; external metadata providers require
network access and may report their own failure independently.

The system baseline is the corresponding CI runner, not every OS release.
This is a directory bundle with optional unsigned installation helpers. Managed
installations support offline upgrades, checkpoints, restoration and uninstall.
Unpacked launches do not silently adopt another build's existing database.
Do not relocate an active runtime or delete database/PID files to force startup.
Signing is optional for these candidates; native acceptance is recorded separately.
