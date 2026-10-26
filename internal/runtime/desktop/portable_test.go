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

func TestBundleVerificationThroughParentAlias(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("Windows symlink privileges vary; macOS /var aliases are the target")
	}
	actual := testBundle(t, "1.0.0")
	link := filepath.Join(actual, "python", "python-current")
	if err := os.Symlink("bin/python3", link); err != nil {
		t.Fatal(err)
	}
	sealTestBundle(t, actual, "1.0.0")
	parent := t.TempDir()
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(filepath.Dir(actual), alias); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(alias, filepath.Base(actual))
	if _, _, err := verifyBundle(selected); err != nil {
		t.Fatalf("valid bundle through parent alias rejected: %v", err)
	}
	data := filepath.Join(parent, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBundleConfig(data, selected); err != nil {
		t.Fatalf("portable configuration through parent alias failed: %v", err)
	}
	// Canonicalizing the parent must not permit links outside the bundle.
	outside := filepath.Join(filepath.Dir(actual), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBundle(selected); err == nil {
		t.Fatal("bundle with escaping relative symlink accepted")
	}
}
