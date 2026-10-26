package desktop

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"paperMaster/internal/logger"
)

// MaintenanceOptions are offline operations. The existing Launcher must exit;
// the same data-directory lock excludes concurrent services and maintenance.
type MaintenanceOptions struct {
	Operation      string
	DataDir        string
	ProgramDir     string
	SourceBundle   string
	Checkpoint     string
	ConfirmRestore bool
	ShortcutDir    string
	ConfirmDelete  bool
}

type installedVersion struct {
	Product       Product       `json:"product"`
	Compatibility Compatibility `json:"compatibility"`
	Manifest      string        `json:"manifest"`
	Created       time.Time     `json:"created"`
	Healthy       bool          `json:"healthy"`
}

type installation struct {
	Schema      int                         `json:"schema_version"`
	DataDir     string                      `json:"data_dir"`
	Active      string                      `json:"active"`
	Versions    map[string]installedVersion `json:"versions"`
	Retired     *installedVersion           `json:"retired,omitempty"`
	Shortcuts   []shortcutRecord            `json:"shortcuts,omitempty"`
	ShortcutDir string                      `json:"shortcut_dir,omitempty"`
}

type maintenanceJournal struct {
	Schema     int       `json:"schema_version"`
	State      string    `json:"state"`
	Checkpoint string    `json:"checkpoint,omitempty"`
	Updated    time.Time `json:"updated"`
}

type maintenance struct {
	options MaintenanceOptions
	root    string
	program string
	record  installation
}

type maintenanceHooks struct {
	initialize func(context.Context, Config) error
	probe      func(context.Context, Config) error
	cold       func(context.Context, Config) error
}

func parseVersion(value string) ([3]int, error) {
	var result [3]int
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, fmt.Errorf("application version must be MAJOR.MINOR.PATCH")
	}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || strconv.Itoa(number) != part {
			return result, fmt.Errorf("invalid application version")
		}
		result[i] = number
	}
	return result, nil
}

func newerVersion(candidate, current string) bool {
	a, errA := parseVersion(candidate)
	b, errB := parseVersion(current)
	if errA != nil || errB != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func maintenanceJournalPath(root string) string {
	return filepath.Join(root, "maintenance", "journal.json")
}

// CheckMaintenanceReady prevents a normal start after an interrupted switch.
// It never repairs state, removes locks or guesses whether a migration completed.
func CheckMaintenanceReady(root string) error {
	var journal maintenanceJournal
	err := readRecord(maintenanceJournalPath(root), &journal)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || journal.Schema != 1 {
		return fmt.Errorf("invalid maintenance journal; preserve data and restore explicitly")
	}
	if journal.State != "complete" && journal.State != "recovered" {
		if journal.State == "uninstalling" {
			return fmt.Errorf("uninstall interrupted; finish --maintenance uninstall before starting")
		}
		if journal.Checkpoint == "" {
			return fmt.Errorf("first setup interrupted; use --maintenance retry")
		}
		return fmt.Errorf("maintenance interrupted; use --maintenance restore --checkpoint-id %s", journal.Checkpoint)
	}
	return nil
}

func RunMaintenance(ctx context.Context, options MaintenanceOptions) error {
	if err := ordinaryUser(); err != nil {
		return err
	}
	hooks := maintenanceHooks{
		initialize: func(ctx context.Context, cfg Config) error { return New(cfg).initializeData(ctx) },
		probe:      probeInstalledRuntime,
		cold:       checkColdRuntime,
	}
	return runMaintenance(ctx, options, hooks)
}

func runMaintenance(ctx context.Context, options MaintenanceOptions, hooks maintenanceHooks) error {
	if !filepath.IsAbs(options.DataDir) || !filepath.IsAbs(options.ProgramDir) {
		return fmt.Errorf("maintenance requires absolute data-dir and program-dir")
	}
	if within(filepath.Clean(options.DataDir), filepath.Clean(options.ProgramDir)) ||
		within(filepath.Clean(options.ProgramDir), filepath.Clean(options.DataDir)) {
		return fmt.Errorf("program and data directories must be separate, non-nested directories")
	}
	root, err := realDirectory(options.DataDir)
	if err != nil {
		return err
	}
	program, err := realDirectory(options.ProgramDir)
	if err != nil {
		return err
	}
	if within(root, program) || within(program, root) {
		return fmt.Errorf("program and data directories must be separate, non-nested directories")
	}
	dataLock, err := Acquire(root)
	if err != nil {
		return fmt.Errorf("exit the current Launcher before maintenance: %w", err)
	}
	defer dataLock.Close()
	programLock, err := Acquire(program)
	if err != nil {
		return err
	}
	defer programLock.Close()
	m := maintenance{options: options, root: root, program: program}
	if err := m.openInstallation(); err != nil {
		return err
	}
	if options.ShortcutDir != "" {
		if !filepath.IsAbs(options.ShortcutDir) {
			return fmt.Errorf("shortcut-dir must be absolute")
		}
		m.record.ShortcutDir = options.ShortcutDir
	}
	for _, path := range []string{
		filepath.Join(root, "maintenance"), filepath.Join(root, "maintenance", "checkpoints"),
		filepath.Join(root, "maintenance", "recovery"), filepath.Join(program, "versions"),
		filepath.Join(root, "maintenance", "staging"),
		filepath.Join(program, "staging"),
	} {
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		if !realChildDirectory(path) {
			return fmt.Errorf("managed maintenance directory is not a real directory")
		}
	}
	logger.Info(ctx, "desktop maintenance started", "operation", options.Operation)
	if options.Operation != "restore" && options.Operation != "retry" &&
		options.Operation != "uninstall" && options.Operation != "status" {
		if err := CheckMaintenanceReady(root); err != nil {
			return err
		}
	}
	switch options.Operation {
	case "install":
		err = m.install(ctx, hooks)
	case "retry":
		err = m.retryFirstSetup(ctx, hooks)
	case "restore":
		if !options.ConfirmRestore {
			return fmt.Errorf("restore replaces managed user data; add --confirm-restore after reviewing the checkpoint")
		}
		err = m.restore(ctx, options.Checkpoint, hooks)
	case "cleanup":
		err = m.cleanup(ctx, time.Now())
	case "uninstall":
		err = m.uninstall(ctx)
	case "status":
		err = m.printStatus()
	case "delete-checkpoint":
		err = m.deleteCheckpoint(ctx)
	default:
		err = fmt.Errorf("unknown maintenance operation")
	}
	if err != nil {
		logger.Error(ctx, "desktop maintenance failed", "operation", options.Operation, "error", err)
		return err
	}
	if options.Operation == "install" || options.Operation == "retry" {
		if err := m.cleanup(ctx, time.Now()); err != nil {
			logger.Warn(ctx, "post-install cleanup deferred", "error", err)
		}
	}
	logger.Info(ctx, "desktop maintenance completed", "operation", options.Operation)
	return nil
}

func (m *maintenance) openInstallation() error {
	path := filepath.Join(m.program, "installation.json")
	if err := readRecord(path, &m.record); err == nil {
		if m.record.Schema != 1 || m.record.DataDir != m.root || m.record.Versions == nil {
			return fmt.Errorf("installation belongs to another data directory or is damaged")
		}
		for key := range m.record.Versions {
			if !safeKey(key) {
				return fmt.Errorf("invalid installed version key")
			}
		}
		if m.record.Active != "" {
			if _, ok := m.record.Versions[m.record.Active]; !ok {
				return fmt.Errorf("active version record missing")
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if m.options.Operation != "install" {
		return fmt.Errorf("no managed installation at program-dir")
	}
	entries, err := os.ReadDir(m.program)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "launcher.lock" {
			return fmt.Errorf("refusing nonempty program directory without ownership record")
		}
	}
	if _, err := os.Stat(filepath.Join(m.root, "runtime.json")); !os.IsNotExist(err) {
		return fmt.Errorf("existing unmanaged runtime requires explicit migration; data preserved")
	}
	dataEntries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range dataEntries {
		if entry.Name() != "launcher.lock" {
			return fmt.Errorf("refusing nonempty unmanaged data directory; choose a new private directory")
		}
	}
	m.record = installation{Schema: 1, DataDir: m.root, Versions: map[string]installedVersion{}}
	return writeRecord(path, m.record)
}

func safeKey(key string) bool {
	if key == "" || key == "." || key == ".." {
		return false
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func (m *maintenance) save() error {
	return writeRecord(filepath.Join(m.program, "installation.json"), m.record)
}

func (m *maintenance) journal(state, checkpoint string) error {
	if err := os.MkdirAll(filepath.Join(m.root, "maintenance"), 0700); err != nil {
		return err
	}
	return writeRecord(maintenanceJournalPath(m.root), maintenanceJournal{
		Schema: 1, State: state, Checkpoint: checkpoint, Updated: time.Now().UTC(),
	})
}

func (m *maintenance) versionPath(key string) string {
	return filepath.Join(m.program, "versions", key)
}

func configForBundle(cfg Config, bundle string, profile string) Config {
	// Persist the sealed runtime identity independently of its absolute path so
	// a portable directory can move without permitting an unnoticed upgrade.
	if hash, err := fileHash(filepath.Join(bundle, "bundle.json")); err == nil {
		cfg.BundleHash = hash
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	cfg.Server = filepath.Join(bundle, "server"+ext)
	cfg.Python = filepath.Join(bundle, "python", "bin", "python3")
	if runtime.GOOS == "windows" {
		cfg.Python = filepath.Join(bundle, "python", "python.exe")
	}
	cfg.ParserDir = filepath.Join(bundle, "pdf_parser")
	cfg.PostgresBin = filepath.Join(bundle, "postgres", "bin")
	cfg.Redis = filepath.Join(bundle, "redis", "redis-server"+ext)
	cfg.WebDir = filepath.Join(bundle, "web")
	cfg.ExtractionEngine = profile
	return cfg
}

func checkPortsClosed(cfg Config) error {
	for _, port := range []int{cfg.Ports.Manager, cfg.Ports.API, cfg.Ports.MCP,
		cfg.Ports.Postgres, cfg.Ports.Redis, cfg.Ports.Parser} {
		listener, err := net.Listen("tcp", address(port))
		if err != nil {
			return fmt.Errorf("a persisted service port is occupied; stop services before maintenance")
		}
		listener.Close()
	}
	return nil
}

func probeInstalledRuntime(ctx context.Context, cfg Config) (result error) {
	// Use unpublished, distinct ports. Existing browser/MCP clients cannot send
	// writes to the candidate during migration/probe/automatic failure recovery.
	used := map[int]bool{}
	for _, port := range []int{cfg.Ports.Manager, cfg.Ports.API, cfg.Ports.MCP,
		cfg.Ports.Postgres, cfg.Ports.Redis, cfg.Ports.Parser} {
		used[port] = true
	}
	for _, port := range []*int{&cfg.Ports.Manager, &cfg.Ports.API, &cfg.Ports.MCP,
		&cfg.Ports.Postgres, &cfg.Ports.Redis, &cfg.Ports.Parser} {
		for {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			candidate := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			if !used[candidate] {
				*port, used[candidate] = candidate, true
				break
			}
		}
	}
	lease := filepath.Join(cfg.DataDir, "maintenance", "probe-ports.json")
	if err := writeRecord(lease, cfg.Ports); err != nil {
		return err
	}
	app := New(cfg)
	app.maintenanceProbe = true
	app.Start()
	defer func() {
		app.Quit()
		stop, cancel := context.WithTimeout(context.Background(), 4*cfg.StopTimeout+time.Second)
		defer cancel()
		if err := app.Wait(stop); err != nil {
			result = fmt.Errorf("candidate services did not stop: %w", err)
		} else {
			_ = os.Remove(lease)
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state := app.Snapshot()
		if state.State == "ready" {
			return nil
		}
		if state.State == "failed" {
			return fmt.Errorf("candidate health check failed: %s", state.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
