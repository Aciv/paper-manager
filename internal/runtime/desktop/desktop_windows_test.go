//go:build windows

package desktop

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Consolidated from entrypoints_windows_test.go.
// ---------------------------------------------------------------------------

// The copied test executable acts only as the uninstaller's external helper.
// Handle its CLI before the testing package parses flags. No services run here.
func init() {
	if os.Getenv("PAPERS_UNINSTALL_SCRIPT_TEST_HELPER") != "1" {
		return
	}
	if err := checkUninstallScriptHelper(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func checkUninstallScriptHelper() error {
	flags := flag.NewFlagSet("uninstall-helper", flag.ContinueOnError)
	operation := flags.String("maintenance", "", "")
	data := flags.String("data-dir", "", "")
	program := flags.String("program-dir", "", "")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	programRoot, err := filepath.EvalSymlinks(*program)
	if err != nil {
		return err
	}
	expectedProgram, err := filepath.EvalSymlinks(os.Getenv("PAPERS_UNINSTALL_TEST_PROGRAM"))
	if err != nil {
		return err
	}
	if *operation != "uninstall" || !strings.EqualFold(programRoot, expectedProgram) {
		return fmt.Errorf("helper received incorrect maintenance arguments")
	}
	dataRoot, err := filepath.EvalSymlinks(*data)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	stage, err := filepath.EvalSymlinks(filepath.Dir(executable))
	if err != nil {
		return err
	}
	if !within(filepath.Join(dataRoot, "maintenance", "staging"), stage) {
		return fmt.Errorf("helper was placed outside owned maintenance staging")
	}
	var owner stagedResource
	if err := readRecord(filepath.Join(stage, "resource.json"), &owner); err != nil {
		return err
	}
	if owner.Schema != 1 || owner.DataDir != dataRoot || owner.Updated.IsZero() {
		return fmt.Errorf("helper ownership record is invalid")
	}
	return os.WriteFile(os.Getenv("PAPERS_UNINSTALL_TEST_MARKER"), []byte(stage), 0600)
}

// TestWindowsUninstallerIgnoresUnwritableSystemTemp verifies that the generated
// uninstaller runs its external helper from owned maintenance staging even when TEMP is
// a regular file, then cleans up the helper and preserves the TEMP object.
func TestWindowsUninstallerIgnoresUnwritableSystemTemp(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data with space")
	program := filepath.Join(root, "program with space")
	staging := filepath.Join(data, "maintenance", "staging")
	version := filepath.Join(program, "versions", "fixture")
	for _, path := range []string{staging, version} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := filepath.EvalSymlinks(data)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "launcher.exe"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	m := maintenance{root: data, program: program, record: installation{Active: "fixture"}}
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if err := m.writeUninstaller(); err != nil {
		t.Fatal(err)
	}
	blockedTemp := filepath.Join(root, "blocked-temp")
	if err := os.WriteFile(blockedTemp, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(data, "helper-ran")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	powershell := filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", filepath.Join(program, "uninstall.ps1"))
	cmd.Env = append(os.Environ(), "TEMP="+blockedTemp, "TMP="+blockedTemp,
		"PAPERS_UNINSTALL_SCRIPT_TEST_HELPER=1", "PAPERS_UNINSTALL_TEST_PROGRAM="+program,
		"PAPERS_UNINSTALL_TEST_MARKER="+marker)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("uninstaller failed with blocked TEMP: %v: %s", err, strings.TrimSpace(string(output)))
	}
	stage, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("external helper did not execute: %v", err)
	}
	if _, err := os.Stat(string(stage)); !os.IsNotExist(err) {
		t.Fatalf("external helper staging was not cleaned: %v", err)
	}
	content, err := os.ReadFile(blockedTemp)
	if err != nil || string(content) != "preserve" {
		t.Fatal("uninstaller changed the blocked system TEMP object")
	}
}

// ---------------------------------------------------------------------------
// Consolidated from platform_windows_test.go.
// ---------------------------------------------------------------------------

// TestPostgresProgramPath verifies that PostgreSQL binaries under a Unicode install path
// are exposed through an ASCII alias to the same file, with no silent fallback when the
// alias target is missing.
func TestPostgresProgramPath(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "papers 安装 阅读", "postgres", "bin")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(directory, "initdb.exe")
	if err := os.WriteFile(original, []byte("path fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{cfg: Config{PostgresBin: directory}}
	path, err := r.pgBinary("initdb")
	if err != nil {
		t.Fatal(err)
	}
	for _, character := range path {
		if character > 127 {
			t.Fatalf("PostgreSQL program path is not ASCII: %q", path)
		}
	}
	want, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(path)
	if err != nil || !os.SameFile(want, got) {
		t.Fatalf("program alias points to another file: %v", err)
	}
	r.cfg.PostgresBin = filepath.Join(directory, "不存在")
	if _, err := r.pgBinary("initdb"); err == nil {
		t.Fatal("unavailable short path silently fell back to a Unicode path")
	}
}
