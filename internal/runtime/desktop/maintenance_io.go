package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
)

type bundleFile struct {
	SHA256     string `json:"sha256,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	Executable *bool  `json:"executable,omitempty"`
	Symlink    string `json:"symlink,omitempty"`
}

type Product struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Version     string `json:"version"`
}

type Compatibility struct {
	PostgresMajor int `json:"postgres_major"`
	DataFormat    int `json:"data_format"`
	TaskPayload   int `json:"task_payload"`
}

type bundleManifest struct {
	Schema        int                   `json:"schema_version"`
	Distribution  string                `json:"distribution"`
	Target        string                `json:"target"`
	Profile       string                `json:"profile"`
	Application   Product               `json:"application"`
	Compatibility Compatibility         `json:"compatibility"`
	Files         map[string]bundleFile `json:"files"`
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readRecord(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 16<<20 {
		return fmt.Errorf("maintenance record exceeds size limit or cannot be read")
	}
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("maintenance record has trailing data")
	}
	return nil
}

func writeRecord(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("maintenance record exceeds 16 MiB; operation was not published")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".maintenance-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func realDirectory(path string) (string, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func fileInventory(root string, bundle bool) (map[string]bundleFile, error) {
	// Compare resolved link targets against the same physical root. On macOS
	// temporary paths may use /var while EvalSymlinks returns /private/var.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	result := map[string]bundleFile{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		key := filepath.ToSlash(relative)
		if bundle && (key == "bundle.json" || key == "SHA256SUMS") {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if !bundle {
				return fmt.Errorf("checkpoint refuses symlink %s", key)
			}
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil || filepath.IsAbs(link) || !within(root, target) {
				return fmt.Errorf("unsupported bundle symlink %s", key)
			}
			result[key] = bundleFile{Symlink: link}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type %s", key)
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		item := bundleFile{SHA256: hash, Bytes: info.Size()}
		if runtime.GOOS != "windows" {
			executable := info.Mode()&0111 != 0
			item.Executable = &executable
		}
		result[key] = item
		return nil
	})
	return result, err
}

// readBundleManifest identifies a build without reading its component contents.
func readBundleManifest(root string) (bundleManifest, string, error) {
	var record bundleManifest
	if !realChildDirectory(root) {
		return record, "", fmt.Errorf("bundle root must be a real directory")
	}
	if err := readRecord(filepath.Join(root, "bundle.json"), &record); err != nil {
		return record, "", err
	}
	target := runtime.GOOS + "-" + map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	if runtime.GOOS == "darwin" {
		target = "macos-arm64"
	}
	if !(runtime.GOARCH == "amd64" && (runtime.GOOS == "linux" || runtime.GOOS == "windows")) &&
		!(runtime.GOARCH == "arm64" && runtime.GOOS == "darwin") {
		return record, "", fmt.Errorf("unsupported desktop platform")
	}
	if record.Schema != 1 || record.Distribution != "internal-validation-only" || record.Target != target ||
		record.Application.ID != "papers-manager" || record.Application.DisplayName != "paper_manager" ||
		(record.Profile != "hybrid" && record.Profile != "rules") ||
		record.Compatibility != (Compatibility{PostgresMajor: 17, DataFormat: 1, TaskPayload: 1}) {
		return record, "", fmt.Errorf("unsupported product, platform or data/queue compatibility")
	}
	if _, err := parseVersion(record.Application.Version); err != nil {
		return record, "", err
	}
	ext := ""
	python := "python/bin/python3"
	if runtime.GOOS == "windows" {
		ext, python = ".exe", "python/python.exe"
	}
	for _, name := range []string{"launcher" + ext, "server" + ext, python,
		"redis/redis-server" + ext, "postgres/bin/postgres" + ext, "postgres/bin/initdb" + ext,
		"postgres/bin/pg_ctl" + ext, "postgres/bin/pg_controldata" + ext,
		"web/index.html", "pdf_parser/papers_parser/server.py"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return record, "", fmt.Errorf("missing required bundled component %s", name)
		}
	}
	hash, err := fileHash(filepath.Join(root, "bundle.json"))
	if err != nil {
		return record, "", err
	}
	return record, hash, nil
}

// verifyBundle is reserved for explicit installation/recovery verification.
// Ordinary startup reads only the manifest and required entry paths.
func verifyBundle(root string) (bundleManifest, string, error) {
	record, hash, err := readBundleManifest(root)
	if err != nil {
		return record, "", err
	}
	observed, err := fileInventory(root, true)
	if err != nil {
		return record, "", err
	}
	if !reflect.DeepEqual(observed, record.Files) {
		return record, "", fmt.Errorf("bundle differs from sealed file inventory")
	}
	expected := map[string]string{"bundle.json": hash}
	for name, item := range record.Files {
		if item.SHA256 != "" {
			expected[name] = item.SHA256
		}
	}
	index, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		return record, "", fmt.Errorf("bundle checksum index differs")
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(index), "\r\n", "\n"), "\n"), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok || seen[name] || expected[name] == "" || expected[name] != digest {
			return record, "", fmt.Errorf("bundle checksum index differs")
		}
		seen[name] = true
	}
	if len(seen) != len(expected) {
		return record, "", fmt.Errorf("bundle checksum index is incomplete")
	}
	return record, hash, nil
}

// copyTree never follows a link. A verified bundle may contain relative links;
// writable application data must contain regular files/directories only.
func copyTree(source, target string, links bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !links {
			return fmt.Errorf("refusing data symlink")
		}
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(link, target)
	}
	if info.IsDir() {
		if err := os.Mkdir(target, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name()), links); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing special file")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
