package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestUnpackedBundleRefusesSilentVersionChangeAndMissingOldBundle(t *testing.T) {
	data := t.TempDir()
	original := testBundle(t, "1.0.0")
	cfg, err := OpenBundleConfig(data, original)
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
	if _, err := OpenBundleConfig(data, testBundle(t, "1.0.1")); err == nil {
		t.Fatal("another build silently reused the existing database")
	}
	copyPath := filepath.Join(t.TempDir(), "copy")
	if err := copyTree(original, copyPath, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, original+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBundleConfig(data, copyPath); err == nil {
		t.Fatal("missing original runtime was guessed instead of checked")
	}
	after, err := os.ReadFile(filepath.Join(data, "runtime.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("rejected runtime switch modified existing identity")
	}
}
