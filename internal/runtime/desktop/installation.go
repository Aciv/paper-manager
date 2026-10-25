package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"paperMaster/internal/logger"
)

type stagedResource struct {
	Schema  int       `json:"schema_version"`
	DataDir string    `json:"data_dir"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"last_activity"`
}

func (m *maintenance) install(ctx context.Context, hooks maintenanceHooks) error {
	source, err := filepath.Abs(m.options.SourceBundle)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if within(m.program, source) || within(m.root, source) || within(source, m.program) || within(source, m.root) {
		return fmt.Errorf("source bundle must be separate from program and data directories")
	}
	manifest, digest, err := verifyBundle(source)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := m.record.Active
	if previous == "" && m.record.Retired != nil && m.record.Retired.Manifest != digest {
		return fmt.Errorf("reinstall the exact uninstalled version before upgrading existing data")
	}
	if previous != "" {
		current := m.record.Versions[previous]
		if current.Healthy && manifest.Application.Version == current.Product.Version && digest == current.Manifest {
			_, installedHash, err := verifyBundle(m.versionPath(previous))
			if err != nil || installedHash != digest {
				return fmt.Errorf("installed program was modified; repair explicitly")
			}
			cfg, err := OpenConfig(m.root, m.versionPath(previous))
			if err != nil {
				return err
			}
			if err := hooks.cold(ctx, cfg); err != nil {
				return err
			}
			if err := m.save(); err != nil {
				return err
			}
			return m.writeEntrypoint(ctx)
		}
		if !newerVersion(manifest.Application.Version, current.Product.Version) {
			return fmt.Errorf("update requires a newer application version; use checkpoint restore for rollback")
		}
		if current.Compatibility != manifest.Compatibility {
			return fmt.Errorf("data, task payload or PostgreSQL compatibility changed; explicit migration required")
		}
		_, currentHash, err := verifyBundle(m.versionPath(previous))
		if err != nil || currentHash != current.Manifest {
			return fmt.Errorf("installed program was modified; preserve data and repair explicitly")
		}
		cfg, err := OpenConfig(m.root, m.versionPath(previous))
		if err != nil {
			return err
		}
		if cfg != configForBundle(cfg, m.versionPath(previous), cfg.ExtractionEngine) {
			return fmt.Errorf("runtime uses external component paths; explicit migration required")
		}
		if err := hooks.cold(ctx, cfg); err != nil {
			return err
		}
	}
	key := manifest.Application.Version + "-" + digest[:12]
	for _, name := range []string{"versions", "staging"} {
		if err := os.MkdirAll(filepath.Join(m.program, name), 0700); err != nil {
			return err
		}
	}
	id := "stage-" + secret()[:16]
	stage := filepath.Join(m.program, "staging", id)
	if err := os.Mkdir(stage, 0700); err != nil {
		return err
	}
	if err := writeRecord(filepath.Join(stage, "resource.json"), stagedResource{
		Schema: 1, DataDir: m.root, Created: time.Now().UTC(), Updated: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if err := copyTree(source, filepath.Join(stage, "bundle"), true); err != nil {
		return err
	}
	_, copiedHash, err := verifyBundle(filepath.Join(stage, "bundle"))
	if err != nil || copiedHash != digest {
		return fmt.Errorf("staged bundle changed during copy")
	}
	if err := writeRecord(filepath.Join(stage, "resource.json"), stagedResource{
		Schema: 1, DataDir: m.root, Created: time.Now().UTC(), Updated: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if _, err := os.Lstat(m.versionPath(key)); os.IsNotExist(err) {
		if err := os.Rename(filepath.Join(stage, "bundle"), m.versionPath(key)); err != nil {
			return err
		}
	} else {
		_, existing, err := verifyBundle(m.versionPath(key))
		if err != nil || existing != digest {
			return fmt.Errorf("existing target version is modified; preserve it for inspection")
		}
	}
	checkpoint := ""
	if previous != "" {
		checkpoint, err = m.createCheckpoint()
		if err != nil {
			return err
		}
	}
	if err := m.journal("updating", checkpoint); err != nil {
		return err
	}
	m.record.Versions[key] = installedVersion{
		Product: manifest.Application, Compatibility: manifest.Compatibility,
		Manifest: digest, Created: time.Now().UTC(),
	}
	if err := m.save(); err != nil {
		return err
	}
	var cfg Config
	cfg, err = OpenConfig(m.root, m.versionPath(key))
	if err == nil {
		cfg = configForBundle(cfg, m.versionPath(key), manifest.Profile)
		err = SaveConfig(cfg)
	}
	if err == nil && previous == "" {
		if m.record.Retired != nil {
			err = hooks.cold(ctx, cfg)
		}
		if err == nil {
			err = hooks.initialize(ctx, cfg)
		}
	}
	if err == nil {
		err = hooks.probe(ctx, cfg)
	}
	if err != nil {
		if checkpoint != "" {
			recoverCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if restoreErr := m.restore(recoverCtx, checkpoint, hooks); restoreErr != nil {
				return fmt.Errorf("candidate failed; restore checkpoint %s explicitly: %w", checkpoint, restoreErr)
			}
			return fmt.Errorf("candidate failed; previous data/program restored: %w", err)
		}
		return fmt.Errorf("first setup failed; data preserved; inspect logs before retry: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	version := m.record.Versions[key]
	version.Healthy = true
	m.record.Versions[key] = version
	m.record.Active = key
	if err := m.save(); err != nil {
		return err
	}
	if err := m.writeEntrypoint(ctx); err != nil {
		return err
	}
	if checkpoint != "" {
		record, path, err := m.loadCheckpoint(checkpoint)
		if err != nil {
			return err
		}
		record.Protected = false
		if err := writeRecord(filepath.Join(path, "checkpoint.json"), record); err != nil {
			return err
		}
	}
	return m.journal("complete", checkpoint)
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func powershellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (m *maintenance) writeEntrypoint(ctx context.Context) error {
	// The version resolver is outside versions/. It does not overwrite a
	// running executable, and follows the atomic installation record each run.
	var name, content string
	if runtime.GOOS == "windows" {
		name = "paper_manager.ps1"
		content = "$ErrorActionPreference = 'Stop'\n" +
			"$record = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'installation.json') -Raw | ConvertFrom-Json\n" +
			"if ($record.active -notmatch '^[a-z0-9][a-z0-9.-]*$') { throw 'Invalid active version' }\n" +
			"$bundle = Join-Path (Join-Path $PSScriptRoot 'versions') $record.active\n" +
			"& (Join-Path $bundle 'launcher.exe') --data-dir " + powershellLiteral(m.root) +
			" --install-dir $bundle --program-dir $PSScriptRoot --source-bundle $bundle @args\nexit $LASTEXITCODE\n"
	} else {
		name = "paper_manager"
		// No jq or system Python dependency. The active bundle path is written
		// only after a healthy switch, while the instance lock is held.
		content = "#!/bin/sh\nexec " + shellLiteral(filepath.Join(m.versionPath(m.record.Active), "launcher")) +
			" --data-dir " + shellLiteral(m.root) + " --install-dir " + shellLiteral(m.versionPath(m.record.Active)) +
			" --program-dir " + shellLiteral(m.program) + " --source-bundle " + shellLiteral(m.versionPath(m.record.Active)) +
			" \"$@\"\n"
	}
	f, err := os.CreateTemp(m.program, ".entry-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(content)
	if err == nil {
		err = f.Chmod(0700)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), filepath.Join(m.program, name)); err != nil {
		return err
	}
	if err := m.writeUninstaller(); err != nil {
		return err
	}
	if err := m.writeShortcuts(ctx); err != nil {
		logger.Warn(ctx, "desktop shortcut could not be updated; use program entry point", "error", err)
	}
	return nil
}

func (m *maintenance) uninstall(ctx context.Context) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if within(m.program, executable) {
		return fmt.Errorf("run uninstall from an external installer/helper, not the installed executable")
	}
	cfg, err := OpenConfig(m.root, m.program)
	if err != nil {
		return err
	}
	if err := checkPortsClosed(cfg); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.root, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
		return fmt.Errorf("PostgreSQL still has a PID/state file; preserve programs until recovery")
	}
	// Never delete unknown files or user data. The uninstall command must run
	// from an external/downloaded bundle on Windows so executables are closed.
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(m.root), &journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	for key, record := range m.record.Versions {
		if _, err := os.Lstat(m.versionPath(key)); os.IsNotExist(err) && journal.State == "uninstalling" {
			continue
		}
		_, hash, err := verifyBundle(m.versionPath(key))
		if err != nil || hash != record.Manifest {
			return fmt.Errorf("refusing removal of modified version %s", key)
		}
	}
	if err := m.journal("uninstalling", journal.Checkpoint); err != nil {
		return err
	}
	if err := m.removeShortcuts(ctx); err != nil {
		return err
	}
	if m.record.Active != "" {
		retired := m.record.Versions[m.record.Active]
		m.record.Retired = &retired
	}
	m.record.Active = ""
	if err := m.save(); err != nil {
		return err
	}
	for key := range m.record.Versions {
		if err := os.RemoveAll(m.versionPath(key)); err != nil {
			return err
		}
		delete(m.record.Versions, key)
		if err := m.save(); err != nil {
			return err
		}
	}
	for _, name := range []string{"paper_manager", "paper_manager.ps1", "uninstall", "uninstall.ps1"} {
		if err := os.Remove(filepath.Join(m.program, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.journal("complete", journal.Checkpoint)
}

func (m *maintenance) retryFirstSetup(ctx context.Context, hooks maintenanceHooks) error {
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(m.root), &journal); err != nil {
		return err
	}
	if journal.Schema != 1 || journal.State != "updating" || journal.Checkpoint != "" {
		return fmt.Errorf("retry is only for an interrupted first installation; restore an upgrade checkpoint instead")
	}
	cfg, err := OpenConfig(m.root, m.program)
	if err != nil {
		return err
	}
	if err := checkPortsClosed(cfg); err != nil {
		return err
	}
	for key, version := range m.record.Versions {
		if cfg.Server != configForBundle(cfg, m.versionPath(key), cfg.ExtractionEngine).Server {
			continue
		}
		_, hash, err := verifyBundle(m.versionPath(key))
		if err != nil || hash != version.Manifest {
			return fmt.Errorf("first installation bundle is modified")
		}
		if err := hooks.initialize(ctx, cfg); err != nil {
			return err
		}
		if err := hooks.probe(ctx, cfg); err != nil {
			return err
		}
		version.Healthy = true
		m.record.Active, m.record.Versions[key] = key, version
		if err := m.save(); err != nil {
			return err
		}
		if err := m.writeEntrypoint(ctx); err != nil {
			return err
		}
		return m.journal("complete", "")
	}
	return fmt.Errorf("no recorded bundle matches the interrupted configuration")
}
