package desktop

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
)

var errConfigBusy = errors.New("stop application services before editing configuration")

type Settings struct {
	Ports            Ports       `json:"ports"`
	Concurrency      Concurrency `json:"concurrency"`
	ExtractionEngine string      `json:"extraction_engine"`
}

type SettingsView struct {
	Settings
	Editable bool   `json:"editable"`
	File     string `json:"file"`
}

// EnableConfigReload applies edits made to the private JSON file before the
// next explicit initialization/start. Maintenance probes use their isolated
// in-memory configuration and do not opt into this behavior.
func (r *Runtime) EnableConfigReload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloadConfig = true
}

func (r *Runtime) configSnapshot() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg
}

func (r *Runtime) canConfigureLocked() bool {
	select {
	case <-r.quit:
		return false
	default:
	}
	select {
	case <-r.cycleDone:
		return true
	default:
		return false
	}
}

func (r *Runtime) Settings() SettingsView {
	r.mu.Lock()
	defer r.mu.Unlock()
	return SettingsView{
		Settings: Settings{r.cfg.Ports, r.cfg.Concurrency, r.cfg.ExtractionEngine},
		Editable: r.canConfigureLocked(), File: filepath.Join(r.cfg.DataDir, "runtime.json"),
	}
}

func (r *Runtime) UpdateSettings(settings Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.canConfigureLocked() {
		return errConfigBusy
	}
	if settings.Concurrency == (Concurrency{}) {
		return fmt.Errorf("concurrency limits must be positive")
	}
	cfg, err := r.readSavedConfigLocked()
	if err != nil {
		return err
	}
	cfg.Ports, cfg.Concurrency = settings.Ports, settings.Concurrency
	cfg.ExtractionEngine = settings.ExtractionEngine
	if err := r.checkLiveCompatibility(cfg); err != nil {
		return err
	}
	if err := SaveConfig(cfg); err != nil {
		return err
	}
	r.applyConfigLocked(cfg)
	return nil
}

func (r *Runtime) ReloadConfiguration() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.canConfigureLocked() {
		return errConfigBusy
	}
	return r.reloadConfigLocked()
}

func (r *Runtime) readSavedConfigLocked() (Config, error) {
	var cfg Config
	if err := readRecord(filepath.Join(r.cfg.DataDir, "runtime.json"), &cfg); err != nil {
		return cfg, fmt.Errorf("cannot read runtime.json; correct the JSON file before starting")
	}
	cfg.DataDir = r.cfg.DataDir
	cfg = cfg.withDefaults()
	if err := r.checkLiveCompatibility(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (r *Runtime) checkLiveCompatibility(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	unchanged := cfg
	unchanged.Ports.API, unchanged.Ports.MCP = r.cfg.Ports.API, r.cfg.Ports.MCP
	unchanged.Ports.Postgres, unchanged.Ports.Redis = r.cfg.Ports.Postgres, r.cfg.Ports.Redis
	unchanged.Ports.Parser = r.cfg.Ports.Parser
	unchanged.Concurrency = r.cfg.Concurrency
	unchanged.ExtractionEngine = r.cfg.ExtractionEngine
	unchanged.StartupTimeout, unchanged.StopTimeout = r.cfg.StartupTimeout, r.cfg.StopTimeout
	if unchanged != r.cfg {
		return fmt.Errorf("management port, credentials or component paths changed; restart Launcher to apply")
	}
	return nil
}

func (r *Runtime) reloadConfigLocked() error {
	cfg, err := r.readSavedConfigLocked()
	if err != nil {
		return err
	}
	r.applyConfigLocked(cfg)
	return nil
}

func (r *Runtime) applyConfigLocked(cfg Config) {
	r.cfg = cfg
	r.state.AppURL = "http://" + address(cfg.Ports.API)
	r.state.MCPURL = "http://" + address(cfg.Ports.MCP) + "/mcp"
}

// Publish a terminal state and release admission in one critical section. A
// browser observing "stopped" must never race a still-open cycleDone channel.
func (r *Runtime) finishCycle(done chan struct{}, state, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.State = state
	r.state.Error = r.cfg.redact(message)
	close(done)
}

func (r *Runtime) OpenConfiguration(ctx context.Context) error {
	r.mu.Lock()
	if !r.canConfigureLocked() {
		r.mu.Unlock()
		return errConfigBusy
	}
	path := filepath.Join(r.cfg.DataDir, "runtime.json")
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("notepad.exe", path)
	case "darwin":
		command = exec.Command("open", "-t", path)
	default:
		command = exec.Command("xdg-open", path)
	}
	// Editors can remain open for minutes. Wait asynchronously so their lifetime
	// is not coupled to the HTTP request or its cancellation.
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot open local editor; open the displayed runtime.json path manually")
	}
	go func() { _ = command.Wait() }()
	return nil
}
