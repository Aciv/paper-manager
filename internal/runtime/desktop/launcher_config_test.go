package desktop

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestLauncherConfigurationRetainsLegacyPortsAndReloadsRootEdits(t *testing.T) {
	cfg, err := OpenConfig(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ports != DefaultLauncherPorts() {
		t.Fatalf("new configuration did not use fixed defaults: %+v", cfg.Ports)
	}
	cfg.Ports = Ports{Manager: 33450, API: 33451, MCP: 33452, Postgres: 33453, Redis: 33454, Parser: 33455}
	path := filepath.Join(t.TempDir(), "launcher.json")
	updated, err := ApplyLauncherConfiguration(cfg, path)
	if err != nil || updated.Ports != cfg.Ports {
		t.Fatalf("legacy ports changed: %v", err)
	}
	if err := writeRecord(path, launcherConfiguration{Ports: DefaultLauncherPorts()}); err != nil {
		t.Fatal(err)
	}
	updated, err = ApplyLauncherConfiguration(cfg, path)
	if err != nil || updated.Ports != DefaultLauncherPorts() || updated.MCPToken != cfg.MCPToken {
		t.Fatalf("root settings not applied with original identity: %v", err)
	}
	if err := SaveConfig(updated); err != nil {
		t.Fatal(err)
	}
	r := New(updated)
	r.EnableConfigReload()
	settings := r.Settings().Settings
	settings.Ports.API = 28460
	if err := r.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var root launcherConfiguration
	if err := json.Unmarshal(data, &root); err != nil || root.Ports.API != 28460 {
		t.Fatalf("UI edit not saved in root file: %v", err)
	}
	root.Ports.API = 28461
	if err := writeRecord(path, root); err != nil {
		t.Fatal(err)
	}
	if err := r.ReloadConfiguration(); err != nil || r.configSnapshot().Ports.API != 28461 {
		t.Fatalf("root edit not reloaded: %v", err)
	}
	for _, invalid := range []string{
		`{}`, `{"ports":{"manager":0}}`, `{"ports":{}} {}`, `{"port":28450}`, `null`,
	} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyLauncherConfiguration(cfg, path); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("invalid root config accepted: %s, %v", invalid, err)
		}
	}
}

func TestLauncherPortCheckReportsAllConflictsWithoutReassignment(t *testing.T) {
	cfg, err := OpenConfig(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Test fixtures use OS-assigned ports so a running user installation cannot
	// interfere with this test. Production never performs this allocation.
	var fixtures []net.Listener
	for _, port := range []*int{
		&cfg.Ports.Manager, &cfg.Ports.API, &cfg.Ports.MCP,
		&cfg.Ports.Postgres, &cfg.Ports.Redis, &cfg.Ports.Parser,
	} {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		*port = probe.Addr().(*net.TCPAddr).Port
		fixtures = append(fixtures, probe)
	}
	for _, fixture := range fixtures {
		_ = fixture.Close()
	}
	cfg.LauncherFile = filepath.Join(t.TempDir(), "launcher.json")
	first, err := net.Listen("tcp", address(cfg.Ports.API))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := net.Listen("tcp", address(cfg.Ports.Redis))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	before := cfg.Ports
	listener, err := ListenLauncher(cfg)
	if err == nil || listener != nil || cfg.Ports != before {
		t.Fatal("conflicts did not fail without reassignment")
	}
	for _, text := range []string{"论文服务", "Redis", "已被其他程序占用", cfg.LauncherFile} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("missing diagnostic %s: %v", text, err)
		}
	}
	probe, err := net.Listen("tcp", address(cfg.Ports.Manager))
	if err != nil {
		t.Fatalf("failed check leaked management listener: %v", err)
	}
	_ = probe.Close()
	_ = first.Close()
	_ = second.Close()
	listener, err = ListenLauncher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if probe, err := net.Listen("tcp", address(cfg.Ports.Manager)); err == nil {
		_ = probe.Close()
		t.Fatal("management listener was not retained")
	}
	if !strings.Contains(portFailure("管理页面", 28450, syscall.EACCES), "系统禁止使用") {
		t.Fatal("access denial incorrectly described as occupied")
	}
	if runtime.GOOS == "windows" &&
		!strings.Contains(portFailure("管理页面", 28450, syscall.Errno(10013)), "系统禁止使用") {
		t.Fatal("Winsock access denial incorrectly described as occupied")
	}
}
