package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// Include all durable application resources. Caches/logs/work are excluded.
// Uploads are deliberately copied too: restoring an old database cannot depend
// on a PDF or block image which a newer version/user has since removed.
var checkpointEntries = []string{
	"runtime.json", "initialized.json", "postgres", "redis", "uploads", "notes", "recognition-output",
}

type checkpointRecord struct {
	Schema       int                   `json:"schema_version"`
	ID           string                `json:"id"`
	Created      time.Time             `json:"created"`
	Protected    bool                  `json:"protected"`
	Complete     bool                  `json:"complete"`
	DataDir      string                `json:"data_dir"`
	Installation installation          `json:"installation"`
	Present      []string              `json:"present"`
	Files        map[string]bundleFile `json:"files"`
}

func checkColdRuntime(ctx context.Context, cfg Config) error {
	if err := checkPortsClosed(cfg); err != nil {
		return err
	}
	var probe Ports
	lease := filepath.Join(cfg.DataDir, "maintenance", "probe-ports.json")
	if err := readRecord(lease, &probe); err == nil {
		candidate := cfg
		candidate.Ports = probe
		if err := checkPortsClosed(candidate); err != nil {
			return fmt.Errorf("candidate services remain active; stop them before recovery")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	app := New(cfg)
	data, err := app.preparePostgres(ctx)
	if err != nil {
		return err
	}
	binary, err := app.pgBinary("pg_controldata")
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, binary, "-D", data)
	command.Env = app.serviceEnv(map[string]string{"LC_ALL": "C", "LANG": "C"})
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect private PostgreSQL shutdown state: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		key, value, _ := strings.Cut(line, ":")
		if key == "Database cluster state" && strings.TrimSpace(value) == "shut down" {
			return nil
		}
	}
	return fmt.Errorf("PostgreSQL is not cleanly shut down; recover the existing runtime before maintenance")
}

func (m *maintenance) createCheckpoint() (string, error) {
	id := time.Now().UTC().Format("20060102t150405") + "-" + secret()[:12]
	parent := filepath.Join(m.root, "maintenance", "checkpoints")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	staging := filepath.Join(m.root, "maintenance", "staging")
	stage, err := os.MkdirTemp(staging, "checkpoint-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := writeRecord(filepath.Join(stage, "resource.json"), stagedResource{
		Schema: 1, DataDir: m.root, Created: time.Now().UTC(), Updated: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	payload := filepath.Join(stage, "checkpoint")
	if err := os.Mkdir(payload, 0700); err != nil {
		return "", err
	}
	data := filepath.Join(payload, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		return "", err
	}
	record := checkpointRecord{Schema: 1, ID: id, Created: time.Now().UTC(), Protected: true,
		Complete: true, DataDir: m.root, Installation: installation{
			Schema: 1, DataDir: m.root, Active: m.record.Active,
			Versions: map[string]installedVersion{m.record.Active: m.record.Versions[m.record.Active]},
		}}
	for _, name := range checkpointEntries {
		source := filepath.Join(m.root, name)
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		if err := copyTree(source, filepath.Join(data, name), false); err != nil {
			return "", fmt.Errorf("checkpoint %s: %w", name, err)
		}
		record.Present = append(record.Present, name)
	}
	record.Files, err = fileInventory(data, false)
	if err != nil {
		return "", err
	}
	if err := writeRecord(filepath.Join(payload, "checkpoint.json"), record); err != nil {
		return "", err
	}
	if err := os.Rename(payload, filepath.Join(parent, id)); err != nil {
		return "", err
	}
	return id, nil
}

func (m *maintenance) loadCheckpointData(id string) (checkpointRecord, string, error) {
	var record checkpointRecord
	if !safeKey(id) {
		return record, "", fmt.Errorf("invalid checkpoint id")
	}
	path := filepath.Join(m.root, "maintenance", "checkpoints", id)
	if !realChildDirectory(path) || !realChildDirectory(filepath.Join(path, "data")) {
		return record, "", fmt.Errorf("checkpoint must contain real directories")
	}
	if err := readRecord(filepath.Join(path, "checkpoint.json"), &record); err != nil {
		return record, "", err
	}
	if record.Schema != 1 || record.ID != id || !record.Complete || record.DataDir != m.root ||
		record.Installation.DataDir != m.root || record.Installation.Active == "" {
		return record, "", fmt.Errorf("checkpoint identity or completion marker invalid")
	}
	present := map[string]bool{}
	for _, name := range record.Present {
		valid := false
		for _, allowed := range checkpointEntries {
			valid = valid || name == allowed
		}
		if !valid || present[name] {
			return record, "", fmt.Errorf("invalid checkpoint resource list")
		}
		present[name] = true
	}
	if !present["runtime.json"] || !present["initialized.json"] || !present["postgres"] || !present["redis"] {
		return record, "", fmt.Errorf("checkpoint lacks core resources")
	}
	files, err := fileInventory(filepath.Join(path, "data"), false)
	if err != nil || !reflect.DeepEqual(files, record.Files) {
		return record, "", fmt.Errorf("checkpoint integrity check failed")
	}
	if !safeKey(record.Installation.Active) {
		return record, "", fmt.Errorf("invalid checkpoint program reference")
	}
	version, ok := record.Installation.Versions[record.Installation.Active]
	if !ok || !version.Healthy {
		return record, "", fmt.Errorf("checkpoint has no healthy program reference")
	}
	return record, path, nil
}

func (m *maintenance) loadCheckpoint(id string) (checkpointRecord, string, error) {
	record, path, err := m.loadCheckpointData(id)
	if err != nil {
		return record, "", err
	}
	version := record.Installation.Versions[record.Installation.Active]
	_, hash, err := verifyBundle(m.versionPath(record.Installation.Active))
	if err != nil || hash != version.Manifest {
		return record, "", fmt.Errorf("checkpoint program is missing or modified")
	}
	return record, path, nil
}

func (m *maintenance) restore(ctx context.Context, id string, hooks maintenanceHooks) error {
	if err := m.ensureCheckpointProgram(id); err != nil {
		return err
	}
	record, checkpoint, err := m.loadCheckpoint(id)
	if err != nil {
		return err
	}
	var cfg Config
	if err := readRecord(filepath.Join(checkpoint, "data", "runtime.json"), &cfg); err != nil {
		return err
	}
	cfg.DataDir = m.root
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Restoring may resume after a crash with a missing runtime.json. Check the
	// persisted old ports/PID without requiring an intact current cluster first.
	if err := checkPortsClosed(cfg); err != nil {
		return err
	}
	var lease Ports
	if err := readRecord(filepath.Join(m.root, "maintenance", "probe-ports.json"), &lease); err == nil {
		probe := cfg
		probe.Ports = lease
		if err := checkPortsClosed(probe); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.root, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
		return fmt.Errorf("existing PostgreSQL PID/state needs manual recovery; checkpoint preserved")
	}
	if err := m.journal("restoring", id); err != nil {
		return err
	}
	// Preserve the first failed/current copy across retries, never delete it.
	recovery := filepath.Join(m.root, "maintenance", "recovery", id)
	if err := os.MkdirAll(recovery, 0700); err != nil {
		return err
	}
	if !realChildDirectory(recovery) {
		return fmt.Errorf("recovery directory is not a real directory")
	}
	if entries, err := os.ReadDir(recovery); err != nil {
		return err
	} else if len(entries) != 0 {
		var owner stagedResource
		if readRecord(filepath.Join(recovery, "resource.json"), &owner) != nil ||
			owner.Schema != 1 || owner.DataDir != m.root {
			return fmt.Errorf("unknown recovery resources preserved")
		}
	}
	if err := writeRecord(filepath.Join(recovery, "resource.json"), stagedResource{
		Schema: 1, DataDir: m.root, Created: time.Now().UTC(), Updated: time.Now().UTC(),
	}); err != nil {
		return err
	}
	for _, name := range checkpointEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := filepath.Join(m.root, name)
		previous := filepath.Join(recovery, name)
		if _, err := os.Lstat(current); err == nil {
			if _, err := os.Lstat(previous); os.IsNotExist(err) {
				if err := os.Rename(current, previous); err != nil {
					return err
				}
			} else {
				// This operation's partially restored copy, guarded by journal/lock.
				if err := os.RemoveAll(current); err != nil {
					return err
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		for _, present := range record.Present {
			if present == name {
				if err := copyTree(filepath.Join(checkpoint, "data", name), current, false); err != nil {
					return err
				}
			}
		}
	}
	m.record.Active = record.Installation.Active
	for key, version := range record.Installation.Versions {
		m.record.Versions[key] = version
	}
	if err := m.save(); err != nil {
		return err
	}
	if err := m.writeEntrypoint(ctx); err != nil {
		return err
	}
	if err := hooks.cold(ctx, cfg); err != nil {
		return err
	}
	return m.journal("recovered", id)
}

func (m *maintenance) ensureCheckpointProgram(id string) error {
	record, _, err := m.loadCheckpointData(id)
	if err != nil {
		return err
	}
	key := record.Installation.Active
	expected := record.Installation.Versions[key]
	if _, digest, err := verifyBundle(m.versionPath(key)); err == nil && digest == expected.Manifest {
		return nil
	}
	if _, err := os.Lstat(m.versionPath(key)); !os.IsNotExist(err) {
		return fmt.Errorf("checkpoint program exists but was modified; preserve it for inspection")
	}
	source, err := filepath.Abs(m.options.SourceBundle)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if within(m.root, source) || within(m.program, source) {
		return fmt.Errorf("provide the original external archive directory via --source-bundle")
	}
	_, digest, err := verifyBundle(source)
	if err != nil || digest != expected.Manifest {
		return fmt.Errorf("checkpoint needs its exact original program; provide it via --source-bundle")
	}
	stage, err := os.MkdirTemp(filepath.Join(m.program, "staging"), "restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := writeRecord(filepath.Join(stage, "resource.json"), stagedResource{
		Schema: 1, DataDir: m.root, Created: time.Now().UTC(), Updated: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if err := copyTree(source, filepath.Join(stage, "bundle"), true); err != nil {
		return err
	}
	_, copied, err := verifyBundle(filepath.Join(stage, "bundle"))
	if err != nil || copied != digest {
		return fmt.Errorf("checkpoint program changed during copy")
	}
	if err := os.Rename(filepath.Join(stage, "bundle"), m.versionPath(key)); err != nil {
		return err
	}
	m.record.Versions[key] = expected
	return m.save()
}
