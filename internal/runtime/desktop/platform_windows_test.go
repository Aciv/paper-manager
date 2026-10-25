//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

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
