package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from bundle_test.go.
// ---------------------------------------------------------------------------

// TestBundleDirectoryUsesExecutableLocation verifies that the runtime locates an
// unpacked bundle next to its executable and otherwise uses the executable's own directory.
func TestBundleDirectoryUsesExecutableLocation(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "bundle.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{filepath.Join(root, "paper_manager"), filepath.Join(bundle, "launcher")} {
		got, err := BundleDirectory(executable)
		if err != nil || got != bundle {
			t.Fatalf("runtime directory for %s: %s, %v", executable, got, err)
		}
	}
	standalone := t.TempDir()
	got, err := BundleDirectory(filepath.Join(standalone, "launcher"))
	if err != nil || got != standalone {
		t.Fatalf("source/maintenance launcher directory changed: %s, %v", got, err)
	}
}

// TestUnpackedBundleConfigRetainsIdentityAndAllowsIdenticalCopy verifies that an
// identical copied bundle keeps ports, credentials, timeouts, and extraction settings
// while still rebinding the server entry to the copy.
func TestUnpackedBundleConfigRetainsIdentityAndAllowsIdenticalCopy(t *testing.T) {
	data := t.TempDir()
	original := testBundle(t, "1.0.0")
	cfg, err := OpenBundleConfig(data, original)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExtractionEngine != "rules" {
		t.Fatal("rules bundle unexpectedly selected heavy CRF extraction")
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copied bundle")
	if err := copyTree(original, copyPath, true); err != nil {
		t.Fatal(err)
	}
	rebound, err := OpenBundleConfig(data, copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if rebound != configForBundle(cfg, copyPath, cfg.ExtractionEngine) {
		t.Fatal("copy changed ports, credentials, timeouts or extraction settings")
	}
	if rebound.Server == cfg.Server {
		t.Fatal("new entry would silently run the old bundle")
	}
}

func TestIncompletePortableLayoutIsNotTreatedAsSourceLauncher(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bundle"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := BundleDirectory(filepath.Join(root, "paper_manager")); err == nil {
		t.Fatal("incomplete portable package fell back to old source component paths")
	}
}

// A complete data directory can be copied to another compatible release even
// when the previous runtime is still installed or no longer exists.
func TestCopiedDataUsesCurrentBundleAndRetainsIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, removeOld := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%v/removeOld=%v", legacy, removeOld), func(t *testing.T) {
				original := testBundle(t, "1.0.0")
				data := t.TempDir()
				cfg, err := OpenBundleConfig(data, original)
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					cfg.BundleHash = ""
				}
				cfg.Concurrency.Tasks = 12
				if err := SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
				if err := writeRecord(filepath.Join(data, "initialized.json"), New(cfg).initializationRecord()); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"uploads/paper.pdf", "notes/document/section.md", "postgres/keep-data"} {
					path := filepath.Join(data, filepath.FromSlash(name))
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("preserve "+name), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := fileInventory(data, false)
				if err != nil {
					t.Fatal(err)
				}
				copied := filepath.Join(t.TempDir(), "copied data")
				if err := copyTree(data, copied, false); err != nil {
					t.Fatal(err)
				}
				if removeOld {
					if err := os.RemoveAll(original); err != nil {
						t.Fatal(err)
					}
				}
				selected := testBundle(t, "1.0.1")
				reopened, err := OpenBundleConfig(copied, selected)
				if err != nil {
					t.Fatal(err)
				}
				want := configForBundle(cfg, selected, cfg.ExtractionEngine)
				want.DataDir = copied
				if reopened != want {
					t.Fatal("copy changed credentials, ports or user settings, or selected old components")
				}
				if err := SaveConfig(reopened); err != nil {
					t.Fatal(err)
				}
				if err := New(reopened).checkInitialization(); err != nil {
					t.Fatalf("copy no longer recognizes initialized data: %v", err)
				}
				after, err := fileInventory(copied, false)
				if err != nil {
					t.Fatal(err)
				}
				delete(before, "runtime.json")
				delete(after, "runtime.json")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("rebinding modified copied database, notes, papers or initialization marker")
				}
			})
		}
	}
}

func TestBundleReplacementInPlaceRetainsConfiguration(t *testing.T) {
	data := t.TempDir()
	bundle := testBundle(t, "1.0.0")
	cfg, err := OpenBundleConfig(data, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(testBundle(t, "1.0.1"), bundle, true); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenBundleConfig(data, bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := cfg
	want.BundleHash = reopened.BundleHash
	if reopened != want || reopened.BundleHash == cfg.BundleHash {
		t.Fatal("replacement did not refresh only the runtime fingerprint")
	}
}

func TestInvalidCurrentBundlePreservesExistingConfiguration(t *testing.T) {
	for _, invalid := range []string{"modified", "incompatible", "malformed", "wrong-platform", "missing-manifest"} {
		t.Run(invalid, func(t *testing.T) {
			data := t.TempDir()
			cfg, err := OpenBundleConfig(data, testBundle(t, "1.0.0"))
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(data, "runtime.json"))
			if err != nil {
				t.Fatal(err)
			}
			selected := testBundle(t, "1.0.1")
			var manifest bundleManifest
			if err := readRecord(filepath.Join(selected, "bundle.json"), &manifest); err != nil {
				t.Fatal(err)
			}
			switch invalid {
			case "modified":
				err = os.WriteFile(filepath.Join(selected, "web", "index.html"), []byte("partial update"), 0600)
			case "incompatible":
				manifest.Compatibility.DataFormat = 2
				err = writeRecord(filepath.Join(selected, "bundle.json"), manifest)
			case "malformed":
				err = os.WriteFile(filepath.Join(selected, "bundle.json"), []byte("{"), 0600)
			case "wrong-platform":
				manifest.Target = "unsupported-target"
				err = writeRecord(filepath.Join(selected, "bundle.json"), manifest)
			case "missing-manifest":
				err = os.Remove(filepath.Join(selected, "bundle.json"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := OpenBundleConfig(data, selected); err == nil {
				t.Fatal("invalid current package was accepted")
			}
			after, err := os.ReadFile(filepath.Join(data, "runtime.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("invalid replacement changed persisted configuration")
			}
		})
	}
}

func TestPortableBundleRenameRetainsDataAndSettings(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "original program")
			bundle := filepath.Join(program, "bundle")
			if err := os.MkdirAll(program, 0700); err != nil {
				t.Fatal(err)
			}
			if err := copyTree(testBundle(t, "1.0.0"), bundle, true); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(program, "data")
			if err := os.MkdirAll(data, 0700); err != nil {
				t.Fatal(err)
			}
			cfg, err := OpenBundleConfig(data, bundle)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				cfg.BundleHash = ""
			}
			if err := SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			moved := program + " renamed"
			if err := os.Rename(program, moved); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenBundleConfig(filepath.Join(moved, "data"), filepath.Join(moved, "bundle"))
			if err != nil {
				t.Fatal(err)
			}
			want := configForBundle(cfg, filepath.Join(moved, "bundle"), cfg.ExtractionEngine)
			want.DataDir = filepath.Join(moved, "data")
			if reopened != want || reopened.BundleHash == "" {
				t.Fatal("rename lost runtime identity, credentials or settings")
			}
			if err := SaveConfig(reopened); err != nil {
				t.Fatal(err)
			}
			other := testBundle(t, "1.0.1")
			if _, err := OpenBundleConfig(reopened.DataDir, other); err != nil {
				t.Fatalf("compatible replacement after rename was rejected: %v", err)
			}
		})
	}
}

func TestPostgresStalePIDIsPreservedForNativeRecovery(t *testing.T) {
	cfg := testConfig(t)
	// Path encoding is covered independently; this fixture tests lock handling.
	cfg.DataDir = t.TempDir()
	dir := filepath.Join(cfg.DataDir, "postgres")
	for name, content := range map[string]string{
		"PG_VERSION": "17\n", "global/pg_control": "control",
		"postgresql.conf": "config", "pg_hba.conf": "auth",
		"postmaster.pid": "123\n", ".papers-manager-instance": fmt.Sprintf("%x\n", sha256.Sum256([]byte(cfg.DatabasePassword))),
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(cfg).preparePostgres(context.Background()); err != nil {
		t.Fatalf("native recovery was blocked: %v", err)
	}
	if pid, err := os.ReadFile(filepath.Join(dir, "postmaster.pid")); err != nil || string(pid) != "123\n" {
		t.Fatal("Launcher altered the database lock")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from maintenance_test.go.
// ---------------------------------------------------------------------------

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

// TestBundleChecksumIndexAllowsPathOrderingButRejectsDuplicates verifies that bundle
// verification accepts checksum entries in any path order but rejects duplicate entries.
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

// TestManagedInstallPreservesSuccessWhenOptionalShortcutFails verifies that a managed
// install remains healthy and ready when shortcut creation fails, without altering an
// existing object at the shortcut destination.
func TestManagedInstallPreservesSuccessWhenOptionalShortcutFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	program := filepath.Join(t.TempDir(), "program")
	shortcutPath := filepath.Join(t.TempDir(), "shortcut-obstruction")
	if err := os.WriteFile(shortcutPath, []byte("preserve this file"), 0600); err != nil {
		t.Fatal(err)
	}
	options := MaintenanceOptions{
		Operation: "install", DataDir: root, ProgramDir: program,
		SourceBundle: testBundle(t, "1.0.0"), ShortcutDir: shortcutPath,
	}
	if err := runMaintenance(context.Background(), options, fakeMaintenanceHooks(t)); err != nil {
		t.Fatalf("optional shortcut failure aborted installation: %v", err)
	}
	record := readInstallation(t, program)
	if record.Active == "" || !record.Versions[record.Active].Healthy || len(record.Shortcuts) != 0 {
		t.Fatal("shortcut failure changed the healthy installation state")
	}
	content, err := os.ReadFile(shortcutPath)
	if err != nil || string(content) != "preserve this file" {
		t.Fatal("shortcut failure changed an existing object")
	}
	if err := CheckMaintenanceReady(root); err != nil {
		t.Fatalf("shortcut failure left maintenance incomplete: %v", err)
	}
}

// TestManagedUpgradeRestoresAllDurableDataOnCandidateFailure verifies that a failed
// candidate upgrade rolls back to the previous active version and restores the database
// schema plus durable notes and uploads while protecting its recovery checkpoint.
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

// TestManagedFirstSetupRetryAndOwnershipBoundaries verifies that interrupted first
// setup can be retried without changing configuration and that installs enforce build,
// launcher-lock, uninstall, and program/data directory ownership boundaries.
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
	changedPage := filepath.Join(options.SourceBundle, "web/index.html")
	if err := os.WriteFile(changedPage, []byte("different build"), 0600); err != nil {
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

// TestManagedCleanupKeepsRecoveryReferencesAndUnknownFiles verifies that cleanup keeps
// the active version, the most recent recovery checkpoints, and unknown or manual files
// while removing expired owned staging.
func TestManagedCleanupKeepsRecoveryReferencesAndUnknownFiles(t *testing.T) {
	ctx := context.Background()
	parentDir := t.TempDir()
	if runtime.GOOS != "windows" {
		// Exercise canonical ownership paths even on Linux, where TempDir
		// normally has no aliases. macOS /var and Windows short names do.
		alias := filepath.Join(t.TempDir(), "data-parent-alias")
		if err := os.Symlink(parentDir, alias); err != nil {
			t.Fatal(err)
		}
		parentDir = alias
	}
	root := filepath.Join(parentDir, "data")
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
				Schema: 1, DataDir: record.DataDir,
				Created: now.Add(-48 * time.Hour), Updated: now.Add(-48 * time.Hour),
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

// ---------------------------------------------------------------------------
// Consolidated from runtime_test.go.
// ---------------------------------------------------------------------------

func testConfig(t *testing.T) Config {
	t.Helper()
	root := filepath.Join(t.TempDir(), "运行数据 with space")
	lock, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	cfg, err := OpenConfig(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestPrivateConfigAndInstanceLock verifies that a data directory admits one instance,
// keeps private configuration stable across restarts, and refuses to replace damaged
// configuration or issue new credentials over existing data.
func TestPrivateConfigAndInstanceLock(t *testing.T) {
	cfg := testConfig(t)
	if lock, err := Acquire(cfg.DataDir); err == nil {
		lock.Close()
		t.Fatal("second instance acquired the same directory")
	}
	reloaded, err := OpenConfig(cfg.DataDir, t.TempDir())
	if err != nil || reloaded != cfg {
		t.Fatalf("configuration/ports/credentials changed on restart: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(cfg.DataDir, "runtime.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credentials permission: %v", info.Mode())
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "runtime.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfig(cfg.DataDir, t.TempDir()); err == nil {
		t.Fatal("damaged configuration was silently replaced")
	}
	if err := os.Remove(filepath.Join(cfg.DataDir, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.DataDir, "postgres"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfig(cfg.DataDir, t.TempDir()); err == nil {
		t.Fatal("existing data received replacement credentials")
	}
}

// TestExistingPostgresNeverReinitialized verifies that startup refuses to initialize a
// partial or already running PostgreSQL cluster and leaves existing database contents intact.
func TestExistingPostgresNeverReinitialized(t *testing.T) {
	cfg := testConfig(t)
	directory := filepath.Join(cfg.DataDir, "postgres")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory, "keep-me")
	_ = os.WriteFile(sentinel, []byte("valuable data"), 0600)
	app := New(cfg)
	if _, err := app.preparePostgres(context.Background()); err == nil {
		t.Fatal("partial cluster accepted")
	}
	_ = os.WriteFile(filepath.Join(directory, "PG_VERSION"), []byte("17\n"), 0600)
	_ = os.WriteFile(filepath.Join(directory, "postmaster.pid"), []byte("123\n"), 0600)
	if _, err := app.preparePostgres(context.Background()); err == nil {
		t.Fatal("existing process PID ignored")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "valuable data" {
		t.Fatal("existing database contents changed")
	}
}

// TestPrivateDirectoryIdentity verifies that a private data directory records a single
// owning instance and is not adopted by another instance or from an unmarked existing database.
func TestPrivateDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	if err := ensureIdentity(root, "first-instance"); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(root, "first-instance"); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(root, "second-instance"); err == nil {
		t.Fatal("another instance adopted persisted data")
	}
	foreign := t.TempDir()
	_ = os.WriteFile(filepath.Join(foreign, "PG_VERSION"), []byte("17"), 0600)
	if err := ensureIdentity(foreign, "first-instance"); err == nil {
		t.Fatal("unmarked existing database directory was adopted")
	}
}

// TestManagementBoundariesAndFailureVisibility verifies that the management API enforces
// host and origin checks, surfaces runtime failures without leaking the MCP token, and
// refuses to restart after the Launcher has quit.
func TestManagementBoundariesAndFailureVisibility(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg)
	handler := app.Handler()
	origin := app.ManagementURL()
	request := func(method, path, host, source string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, origin+path, nil)
		req.Host = host
		if source != "" {
			req.Header.Set("Origin", source)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	for _, tc := range []struct {
		method, path, host, source string
		want                       int
	}{
		{"GET", "/", address(cfg.Ports.Manager), "", 200},
		{"GET", "/status", address(cfg.Ports.Manager), "", 200},
		{"GET", "/status", "evil.example", "", 403},
		{"GET", "/status", address(cfg.Ports.Manager), "https://evil.example", 403},
		{"POST", "/stop", address(cfg.Ports.Manager), "", 403},
		{"POST", "/stop", address(cfg.Ports.Manager), origin, 202},
		{"GET", "/logs?service=../runtime", address(cfg.Ports.Manager), "", 400},
	} {
		result := request(tc.method, tc.path, tc.host, tc.source)
		if result.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, result.Code, tc.want)
		}
	}
	if app.Snapshot().State != "setup_required" {
		t.Fatal("idempotent stop changed completed state")
	}
	if !app.Start() {
		t.Fatal("start rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if state := app.Snapshot(); (state.State != "failed" && state.State != "setup_required") || state.Error == "" {
		t.Fatalf("missing runtime failure not visible: %+v", state)
	}
	result := request("GET", "/status", address(cfg.Ports.Manager), "")
	if result.Code != 200 || (!strings.Contains(result.Body.String(), "failed") && !strings.Contains(result.Body.String(), "setup_required")) {
		t.Fatal("management page unavailable after service failure")
	}
	if strings.Contains(request("GET", "/", address(cfg.Ports.Manager), "").Body.String(), cfg.MCPToken) {
		t.Fatal("MCP token leaked into HTML")
	}
	app.Quit()
	if app.Start() {
		t.Fatal("start accepted after Launcher quit")
	}
}

// TestChildEnvironmentAndLogRedaction verifies that child processes receive only the
// intended credential environment and that streamed logs redact split secrets while
// keeping buffered diagnostic output bounded.
func TestChildEnvironmentAndLogRedaction(t *testing.T) {
	t.Setenv("DATABASE_URL", "ambient-db")
	t.Setenv("REDIS_PASSWORD", "ambient-redis")
	t.Setenv("PYTHONPATH", "ambient-python")
	env := strings.Join(childEnv(map[string]string{"REDIS_PASSWORD": "private-redis"}), "\n")
	for _, forbidden := range []string{"ambient-db", "ambient-redis", "ambient-python"} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("inherited unrelated runtime environment: %s", forbidden)
		}
	}
	cfg := testConfig(t)
	var output bytes.Buffer
	writer := &redactedWriter{cfg: cfg, out: &output}
	_, _ = writer.Write([]byte("password=" + cfg.RedisPassword[:20]))
	_, _ = writer.Write([]byte(cfg.RedisPassword[20:] + "\n"))
	if strings.Contains(output.String(), cfg.RedisPassword) || !strings.Contains(output.String(), "redacted") {
		t.Fatal("split credential was not redacted")
	}
	_, _ = writer.Write(bytes.Repeat([]byte("x"), 100<<10))
	if len(writer.pending) > 64<<10 {
		t.Fatal("diagnostic buffer grew without bound")
	}
}

// TestLauncherEnvironmentContract verifies that LAUNCHER_* overrides change only runtime
// settings without replacing persisted credentials, and that invalid ports and
// cwd-dependent runtime paths are rejected.
func TestLauncherEnvironmentContract(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("LAUNCHER_PYTHON", filepath.Join(t.TempDir(), "python"))
	t.Setenv("LAUNCHER_STARTUP_TIMEOUT", "2m")
	t.Setenv("LAUNCHER_EXTRACTION_ENGINE", "rules")
	override, err := ApplyEnvironment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if override.Python == cfg.Python || override.StartupTimeout != 2*time.Minute ||
		override.ExtractionEngine != "rules" || override.DatabasePassword != cfg.DatabasePassword {
		t.Fatal("launcher settings/identity precedence violated")
	}
	t.Setenv("LAUNCHER_HTTP_PORT", "70000")
	if _, err := ApplyEnvironment(cfg); err == nil {
		t.Fatal("invalid port accepted")
	}
	t.Setenv("LAUNCHER_HTTP_PORT", "")
	t.Setenv("LAUNCHER_PYTHON", "relative/python")
	if _, err := ApplyEnvironment(cfg); err == nil {
		t.Fatal("cwd-dependent runtime path accepted")
	}
}

// TestMCPReadinessRejectsUnrelatedHTTPService verifies that MCP readiness authenticates
// with the instance token and accepts only an MCP server-info response rather than any
// generic healthy HTTP endpoint.
func TestMCPReadinessRejectsUnrelatedHTTPService(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg)
	for _, tc := range []struct {
		body string
		code int
		want bool
	}{
		{`{"status":"ok"}`, 200, false},
		{`{"error":"unauthorized"}`, 401, false},
		{`{"result":{"serverInfo":{"name":"papers-manager"}}}`, 200, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+cfg.MCPToken {
				t.Error("MCP readiness did not use this instance's credential")
			}
			w.WriteHeader(tc.code)
			_, _ = io.WriteString(w, tc.body)
		}))
		app.state.MCPURL = server.URL
		err := app.mcpReady(context.Background(), server.Client())
		server.Close()
		if (err == nil) != tc.want {
			t.Errorf("response %s: %v", tc.body, err)
		}
	}
}

// TestManagedProcessGracefulAndForcedCleanup verifies that both graceful and forced
// shutdown terminate a managed process tree's grandchild listener and that repeated
// close calls are idempotent.
func TestManagedProcessGracefulAndForcedCleanup(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "forced"}[forced], func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ready.json")
			env := childEnv(map[string]string{
				"PM_RUNTIME_HELPER": "parent", "PM_RUNTIME_MARKER": marker,
			})
			process, err := startChild(
				os.Args[0], []string{"-test.run=^TestRuntimeProcessHelper$"}, env, t.TempDir(), io.Discard,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				process.close(ctx, func() {})
			}()
			var port string
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(marker)
				if err == nil && json.Unmarshal(data, &port) == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if port == "" {
				t.Fatal("grandchild did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			process.close(ctx, func() {
				if !forced {
					process.stdin.Close()
				}
			})
			process.close(ctx, func() { t.Fatal("duplicate close was not idempotent") })
			// Killing a process tree is asynchronous on both supported APIs.
			// Wait for OS teardown, rather than asserting before SIGKILL runs.
			deadline = time.Now().Add(2 * time.Second)
			for {
				connection, err := net.DialTimeout("tcp", port, 100*time.Millisecond)
				if err != nil {
					break
				}
				connection.Close()
				if time.Now().After(deadline) {
					t.Fatal("owned grandchild survived shutdown")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// TestRuntimeProcessHelper acts as the subprocess fixture for managed-process tests,
// spawning a grandchild in the parent role and serving a marker-advertised TCP listener
// in the grandchild role.
func TestRuntimeProcessHelper(t *testing.T) {
	role := os.Getenv("PM_RUNTIME_HELPER")
	if role == "" {
		return
	}
	if role == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeProcessHelper$")
		cmd.Env = childEnv(map[string]string{
			"PM_RUNTIME_HELPER": "grandchild", "PM_RUNTIME_MARKER": os.Getenv("PM_RUNTIME_MARKER"),
		})
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	data, _ := json.Marshal(listener.Addr().String())
	if err := os.WriteFile(os.Getenv("PM_RUNTIME_MARKER"), data, 0600); err != nil {
		os.Exit(4)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(5)
		}
		connection.Close()
	}
}

// TestNormalStartupNeverInitializesPostgres verifies that a fresh runtime starts in
// setup-required mode and that ordinary startup neither initializes nor creates the
// PostgreSQL directory.
func TestNormalStartupNeverInitializesPostgres(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg)
	if app.Snapshot().Initialized || app.Snapshot().State != "setup_required" {
		t.Fatal("new install did not request explicit setup")
	}
	if _, err := app.preparePostgres(context.Background()); err == nil {
		t.Fatal("startup created a database without explicit setup")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "postgres")); !os.IsNotExist(err) {
		t.Fatal("normal startup modified the database directory")
	}
}

// TestInitializationMarkerAndOwnershipValidation verifies that initialization markers
// must identify this instance and that ownership validation rejects a missing database
// without creating an ownership claim.
func TestInitializationMarkerAndOwnershipValidation(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg)
	marker := filepath.Join(cfg.DataDir, "initialized.json")
	if err := os.WriteFile(marker, []byte(`{"version":1,"identity":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.checkInitialization(); err == nil {
		t.Fatal("foreign initialization marker accepted")
	}
	data, _ := json.Marshal(app.initializationRecord())
	if err := os.WriteFile(marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.requireInitialized(context.Background()); err == nil {
		t.Fatal("marker hid a missing database")
	}
	directory := filepath.Join(cfg.DataDir, "postgres")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkIdentity(directory, cfg.DatabasePassword); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err := os.Stat(filepath.Join(directory, ".papers-manager-instance")); !os.IsNotExist(err) {
		t.Fatal("validation created an ownership claim")
	}
}
