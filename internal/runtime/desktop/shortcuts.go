package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"paperMaster/internal/logger"
)

type shortcutRecord struct {
	Path  string                `json:"path"`
	Hash  string                `json:"sha256,omitempty"`
	Files map[string]bundleFile `json:"files,omitempty"`
}

func shortcutUnchanged(record shortcutRecord) bool {
	if !filepath.IsAbs(record.Path) {
		return false
	}
	info, err := os.Lstat(record.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if info.IsDir() && record.Files != nil {
		files, err := fileInventory(record.Path, false)
		return err == nil && reflect.DeepEqual(files, record.Files)
	}
	if info.Mode().IsRegular() && record.Hash != "" {
		hash, err := fileHash(record.Path)
		return err == nil && hash == record.Hash
	}
	return false
}

func desktopArgument(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%")
	return "\"" + replacer.Replace(value) + "\""
}

func (m *maintenance) writeShortcuts(ctx context.Context) error {
	if m.record.ShortcutDir == "" {
		return nil
	}
	if err := os.MkdirAll(m.record.ShortcutDir, 0700); err != nil {
		return err
	}
	name := "paper_manager.desktop"
	if runtime.GOOS == "windows" {
		name = "paper_manager.lnk"
	} else if runtime.GOOS == "darwin" {
		name = "paper_manager.app"
	}
	path := filepath.Join(m.record.ShortcutDir, name)
	if _, err := os.Lstat(path); err == nil {
		owned := false
		for _, shortcut := range m.record.Shortcuts {
			owned = owned || (shortcut.Path == path && shortcutUnchanged(shortcut))
		}
		if !owned {
			return fmt.Errorf("existing shortcut was not created by this installation or was edited; preserved")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if runtime.GOOS == "windows" {
		payload, err := json.Marshal(map[string]string{
			"path": path, "entry": filepath.Join(m.program, "paper_manager.ps1"), "working": m.program,
		})
		if err != nil {
			return err
		}
		code := "$p = $env:PAPER_MANAGER_SHORTCUT | ConvertFrom-Json; " +
			"$s = (New-Object -ComObject WScript.Shell).CreateShortcut($p.path); " +
			"$s.TargetPath = Join-Path $env:SystemRoot 'System32\\WindowsPowerShell\\v1.0\\powershell.exe'; " +
			"$s.Arguments = '-NoProfile -ExecutionPolicy Bypass -File \"' + $p.entry + '\"'; " +
			"$s.WorkingDirectory = $p.working; $s.Description = 'paper_manager'; $s.Save()"
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		powershell := filepath.Join(os.Getenv("SYSTEMROOT"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		cmd := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command", code)
		cmd.Env = append(os.Environ(), "PAPER_MANAGER_SHORTCUT="+string(payload))
		if err := cmd.Run(); err != nil {
			return err
		}
	} else if runtime.GOOS == "darwin" {
		macOS := filepath.Join(path, "Contents", "MacOS")
		if err := os.MkdirAll(macOS, 0700); err != nil {
			return err
		}
		version := m.record.Versions[m.record.Active].Product.Version
		plist := "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict>" +
			"<key>CFBundleName</key><string>paper_manager</string>" +
			"<key>CFBundleIdentifier</key><string>org.paper-manager.desktop</string>" +
			"<key>CFBundleExecutable</key><string>paper_manager</string>" +
			"<key>CFBundlePackageType</key><string>APPL</string>" +
			"<key>CFBundleShortVersionString</key><string>" + version + "</string>" +
			"<key>CFBundleVersion</key><string>" + version + "</string></dict></plist>\n"
		if err := os.WriteFile(filepath.Join(path, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
			return err
		}
		script := "#!/bin/sh\nexec " + shellLiteral(filepath.Join(m.program, "paper_manager")) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(macOS, "paper_manager"), []byte(script), 0700); err != nil {
			return err
		}
	} else {
		content := "[Desktop Entry]\nType=Application\nName=paper_manager\nTerminal=false\nCategories=Office;\nExec=" +
			desktopArgument(filepath.Join(m.program, "paper_manager")) + "\n"
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			return err
		}
	}
	entry := shortcutRecord{Path: path}
	if runtime.GOOS == "darwin" {
		files, err := fileInventory(path, false)
		if err != nil {
			return err
		}
		entry.Files = files
	} else {
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		entry.Hash = hash
	}
	var records []shortcutRecord
	for _, old := range m.record.Shortcuts {
		if old.Path != path {
			records = append(records, old)
		}
	}
	m.record.Shortcuts = append(records, entry)
	return m.save()
}

func (m *maintenance) removeShortcuts(ctx context.Context) error {
	var preserved []shortcutRecord
	for _, shortcut := range m.record.Shortcuts {
		if shortcutUnchanged(shortcut) {
			if err := os.RemoveAll(shortcut.Path); err != nil {
				return err
			}
		} else {
			logger.Warn(ctx, "edited or absent shortcut preserved", "path", shortcut.Path)
			preserved = append(preserved, shortcut)
		}
	}
	m.record.Shortcuts = preserved
	return m.save()
}
