package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUnpackedUninstallPreservesDataAndRejectsActiveRuntime(t *testing.T) {
	program := t.TempDir()
	bundle := filepath.Join(program, "bundle")
	if err := copyTree(testBundle(t, "1.0.0"), bundle, true); err != nil {
		t.Fatal(err)
	}
	entry, native := "paper_manager", "launcher"
	if runtime.GOOS == "windows" {
		entry, native = entry+".exe", native+".exe"
	}
	data, err := os.ReadFile(filepath.Join(bundle, native))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(program, entry), data, 0700); err != nil {
		t.Fatal(err)
	}
	// Portable data and operator settings must survive program removal.
	portable := filepath.Join(program, "data")
	if err := os.Mkdir(portable, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(portable, "runtime.json"), filepath.Join(program, ".launcher_env")} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Custom user data beside the bundle must survive program removal.
	keep := filepath.Join(program, "my-notes.md")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyBundle(bundle); err != nil {
		t.Fatalf("admission lock prevented reading sealed files: %v", err)
	}
	if err := RemoveUnpacked(program); err == nil {
		t.Fatal("removed an active runtime")
	}
	lock.Close()
	if err := RemoveUnpacked(program); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatal("program bundle retained")
	}
	for _, path := range []string{filepath.Join(portable, "runtime.json"), filepath.Join(program, ".launcher_env")} {
		if got, err := os.ReadFile(path); err != nil || string(got) != "keep" {
			t.Fatal("portable configuration removed")
		}
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
		t.Fatal("unrelated user data removed")
	}
}

func TestUnpackedUninstallRefusesModifiedProgram(t *testing.T) {
	program := t.TempDir()
	bundle := filepath.Join(program, "bundle")
	if err := copyTree(testBundle(t, "1.0.0"), bundle, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "user-data.md")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveUnpacked(program); err == nil {
		t.Fatal("removed an unrecognized/modified bundle")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("modified bundle was not preserved")
	}
}
