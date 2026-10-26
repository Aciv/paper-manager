package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableDefaultsUseDeliveryRootRatherThanWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "bundle.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{filepath.Join(root, "paper_manager"), filepath.Join(bundle, "launcher")} {
		selected, err := BundleDirectory(executable)
		if err != nil || selected != bundle {
			t.Fatalf("select bundle for %s: %s, %v", executable, selected, err)
		}
		data, err := DefaultBundleDataDir(selected)
		if err != nil || data != filepath.Join(root, "data") {
			t.Fatalf("portable data escaped delivery: %s, %v", data, err)
		}
		if DeliveryDirectory(selected) != root {
			t.Fatal("environment file would be inside sealed bundle")
		}
	}
	development := t.TempDir()
	want, err := DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultBundleDataDir(development); err != nil || got != want {
		t.Fatalf("standalone development default changed: %s, %v", got, err)
	}
}

func TestRuntimeFileAliasesDoNotSelectAnotherBuild(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows symlink privileges vary; macOS /var aliases are the target")
	}
	root := t.TempDir()
	actual, alias := filepath.Join(root, "real"), filepath.Join(root, "alias")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	a := Config{Server: actual, Python: actual, ParserDir: actual, PostgresBin: actual, Redis: actual, WebDir: actual}
	b := Config{Server: alias, Python: alias, ParserDir: alias, PostgresBin: alias, Redis: alias, WebDir: alias}
	if !sameRuntimeFiles(a, b) {
		t.Fatal("same runtime spelled through symlink was rejected")
	}
	b.DatabasePassword = "different"
	if sameRuntimeFiles(a, b) {
		t.Fatal("different runtime identity accepted")
	}
}
