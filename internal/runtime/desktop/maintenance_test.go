package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func sealTestBundle(t *testing.T, root, version string) {
	t.Helper()
	files, err := fileInventory(root, true)
	if err != nil {
		t.Fatal(err)
	}
	target := runtime.GOOS + "-x86_64"
	if runtime.GOOS == "darwin" {
		target = "macos-arm64"
	}
	record := bundleManifest{Schema: 1, Distribution: "internal-validation-only", Target: target,
		Profile: "rules", Application: Product{"papers-manager", "paper_manager", version},
		Compatibility: Compatibility{17, 1, 1}, Files: files}
	if err := writeRecord(filepath.Join(root, "bundle.json"), record); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var checks strings.Builder
	for _, key := range keys {
		if files[key].SHA256 != "" {
			fmt.Fprintf(&checks, "%s  %s\n", files[key].SHA256, key)
		}
	}
	hash, err := fileHash(filepath.Join(root, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&checks, "%s  bundle.json\n", hash)
	if err := os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(checks.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func testBundle(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	ext, python := "", "python/bin/python3"
	if runtime.GOOS == "windows" {
		ext, python = ".exe", "python/python.exe"
	}
	for _, name := range []string{"launcher" + ext, "server" + ext, python, "redis/redis-server" + ext,
		"postgres/bin/postgres" + ext, "postgres/bin/initdb" + ext, "postgres/bin/pg_ctl" + ext,
		"postgres/bin/pg_controldata" + ext, "web/index.html", "pdf_parser/papers_parser/server.py"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture "+version), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sealTestBundle(t, root, version)
	return root
}

func TestBundleChecksumIndexAllowsPathOrderingButRejectsDuplicates(t *testing.T) {
	root := testBundle(t, "1.0.0")
	path := filepath.Join(root, "SHA256SUMS")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBundle(root); err != nil {
		t.Fatalf("valid differently ordered checksum list rejected: %v", err)
	}
	lines = append(lines, lines[0])
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBundle(root); err == nil {
		t.Fatal("duplicate checksum entry accepted")
	}
}

func fakeMaintenanceHooks(t *testing.T) maintenanceHooks {
	t.Helper()
	return maintenanceHooks{
		initialize: func(_ context.Context, cfg Config) error {
			for _, name := range []string{"postgres", "redis", "uploads", "notes"} {
				if err := os.MkdirAll(filepath.Join(cfg.DataDir, name), 0700); err != nil {
					return err
				}
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "postgres", "schema")); os.IsNotExist(err) {
				if err := os.WriteFile(filepath.Join(cfg.DataDir, "postgres", "schema"), []byte("initial"), 0600); err != nil {
					return err
				}
			}
			return writeRecord(filepath.Join(cfg.DataDir, "initialized.json"), New(cfg).initializationRecord())
		},
		probe: func(_ context.Context, cfg Config) error {
			return os.WriteFile(filepath.Join(cfg.DataDir, "postgres", "schema"), []byte(cfg.Server), 0600)
		},
		cold: func(_ context.Context, cfg Config) error { return checkPortsClosed(cfg) },
	}
}

func readInstallation(t *testing.T, program string) installation {
	t.Helper()
	var record installation
	if err := readRecord(filepath.Join(program, "installation.json"), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestManagedUpgradeRestoresAllDurableDataOnCandidateFailure(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	program := filepath.Join(t.TempDir(), "program with space")
	options := MaintenanceOptions{Operation: "install", DataDir: root, ProgramDir: program,
		SourceBundle: testBundle(t, "1.0.0")}
	hooks := fakeMaintenanceHooks(t)
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatal(err)
	}
	original, err := OpenConfig(root, program)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"notes/chapter.md", "uploads/original.pdf"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("preserve "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	options.SourceBundle = testBundle(t, "1.0.1")
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatal(err)
	}
	before := readInstallation(t, program)
	upgraded, _ := OpenConfig(root, program)
	if upgraded.Ports != original.Ports || upgraded.DatabasePassword != original.DatabasePassword ||
		upgraded.MCPToken != original.MCPToken || upgraded.RedisPassword != original.RedisPassword {
		t.Fatal("upgrade changed ports or credentials")
	}
	schema, _ := os.ReadFile(filepath.Join(root, "postgres", "schema"))
	options.SourceBundle = testBundle(t, "1.0.2")
	failing := hooks
	failing.probe = func(_ context.Context, cfg Config) error {
		for _, name := range []string{"postgres/schema", "notes/chapter.md", "uploads/original.pdf"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("failed migration"), 0600); err != nil {
				return err
			}
		}
		return fmt.Errorf("injected candidate health failure")
	}
	if err := runMaintenance(ctx, options, failing); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("failed candidate was not recovered: %v", err)
	}
	after := readInstallation(t, program)
	if after.Active != before.Active {
		t.Fatal("failed update switched the active program")
	}
	got, _ := os.ReadFile(filepath.Join(root, "postgres", "schema"))
	if string(got) != string(schema) {
		t.Fatal("database migration was not restored")
	}
	for _, name := range []string{"notes/chapter.md", "uploads/original.pdf"} {
		got, _ := os.ReadFile(filepath.Join(root, name))
		if string(got) != "preserve "+name {
			t.Fatalf("durable resource lost: %s", name)
		}
	}
	if err := CheckMaintenanceReady(root); err != nil {
		t.Fatal(err)
	}
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(root), &journal); err != nil || journal.Checkpoint == "" {
		t.Fatal("recovery checkpoint not recorded")
	}
	var checkpoint checkpointRecord
	if err := readRecord(filepath.Join(root, "maintenance/checkpoints", journal.Checkpoint, "checkpoint.json"),
		&checkpoint); err != nil || !checkpoint.Protected {
		t.Fatal("failed update checkpoint was not protected")
	}
	options.Operation, options.Checkpoint = "restore", journal.Checkpoint
	if err := runMaintenance(ctx, options, hooks); err == nil {
		t.Fatal("restore accepted without explicit confirmation")
	}
	options.ConfirmRestore = true
	if err := os.WriteFile(filepath.Join(root, "maintenance/checkpoints", journal.Checkpoint,
		"data/notes/chapter.md"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runMaintenance(ctx, options, hooks); err == nil {
		t.Fatal("corrupt checkpoint restored")
	}
}

func TestManagedFirstSetupRetryAndOwnershipBoundaries(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	program := filepath.Join(t.TempDir(), "program")
	options := MaintenanceOptions{Operation: "install", DataDir: root, ProgramDir: program,
		SourceBundle: testBundle(t, "1.0.0")}
	hooks := fakeMaintenanceHooks(t)
	failing := hooks
	failing.probe = func(context.Context, Config) error { return fmt.Errorf("first setup failed") }
	if err := runMaintenance(ctx, options, failing); err == nil {
		t.Fatal("first health failure accepted")
	}
	if err := CheckMaintenanceReady(root); err == nil {
		t.Fatal("normal startup allowed with interrupted setup")
	}
	cfg, _ := OpenConfig(root, program)
	options.Operation = "retry"
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatal(err)
	}
	retried, _ := OpenConfig(root, program)
	if retried != cfg {
		t.Fatal("setup retry changed configuration")
	}
	options.Operation = "install"
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatalf("identical installer is not idempotent: %v", err)
	}
	originalSource := options.SourceBundle
	options.SourceBundle = testBundle(t, "1.0.0")
	if err := os.WriteFile(filepath.Join(options.SourceBundle, "web/index.html"), []byte("different build"), 0600); err != nil {
		t.Fatal(err)
	}
	sealTestBundle(t, options.SourceBundle, "1.0.0")
	if err := runMaintenance(ctx, options, hooks); err == nil {
		t.Fatal("different build with same version accepted")
	}
	options.SourceBundle = originalSource
	lock, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMaintenance(ctx, options, hooks); err == nil {
		t.Fatal("maintenance ignored active Launcher lock")
	}
	lock.Close()
	options.Operation = "uninstall"
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime.json")); err != nil {
		t.Fatal("uninstall removed user configuration")
	}
	options.Operation = "install"
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatalf("exact version reinstall failed: %v", err)
	}
	options.ProgramDir = filepath.Join(root, "nested-program")
	if err := runMaintenance(ctx, options, hooks); err == nil {
		t.Fatal("nested program/data directories accepted")
	}
}

func TestManagedCleanupKeepsRecoveryReferencesAndUnknownFiles(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	program := filepath.Join(t.TempDir(), "program")
	options := MaintenanceOptions{Operation: "install", DataDir: root, ProgramDir: program}
	hooks := fakeMaintenanceHooks(t)
	for i := 0; i < 5; i++ {
		options.SourceBundle = testBundle(t, fmt.Sprintf("1.0.%d", i))
		if err := runMaintenance(ctx, options, hooks); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	record := readInstallation(t, program)
	for key, version := range record.Versions {
		version.Created = version.Created.Add(-40 * 24 * time.Hour)
		record.Versions[key] = version
	}
	if err := writeRecord(filepath.Join(program, "installation.json"), record); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "maintenance/checkpoints")
	entries, _ := os.ReadDir(parent)
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name(), "checkpoint.json")
		var cp checkpointRecord
		if err := readRecord(path, &cp); err != nil {
			t.Fatal(err)
		}
		cp.Created = cp.Created.Add(-40 * 24 * time.Hour)
		if err := writeRecord(path, cp); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"old-owned", "unknown"} {
		path := filepath.Join(program, "staging", name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if name == "old-owned" {
			if err := writeRecord(filepath.Join(path, "resource.json"), stagedResource{
				Schema: 1, DataDir: root, Created: now.Add(-48 * time.Hour), Updated: now.Add(-48 * time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "manual-backups"), 0700); err != nil {
		t.Fatal(err)
	}
	options.Operation = "cleanup"
	if err := runMaintenance(ctx, options, hooks); err != nil {
		t.Fatal(err)
	}
	cleaned := readInstallation(t, program)
	if len(cleaned.Versions) != 3 || cleaned.Active != record.Active {
		t.Fatalf("wrong version retention: %d", len(cleaned.Versions))
	}
	remaining, _ := os.ReadDir(parent)
	if len(remaining) != 2 {
		t.Fatalf("wrong checkpoint retention: %d", len(remaining))
	}
	if _, err := os.Stat(filepath.Join(program, "staging/old-owned")); !os.IsNotExist(err) {
		t.Fatal("expired owned staging not removed")
	}
	for _, path := range []string{filepath.Join(program, "staging/unknown"), filepath.Join(root, "manual-backups")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unknown/manual resource removed: %s", path)
		}
	}
}
