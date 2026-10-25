package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type checkpointSummary struct {
	ID        string    `json:"id"`
	Created   time.Time `json:"created"`
	Protected bool      `json:"protected"`
	Complete  bool      `json:"complete"`
	Bytes     int64     `json:"bytes"`
	Version   string    `json:"application_version"`
}

func (m *maintenance) printStatus() error {
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(m.root), &journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	var checkpoints []checkpointSummary
	parent := filepath.Join(m.root, "maintenance", "checkpoints")
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if !safeKey(entry.Name()) || !realChildDirectory(path) {
			continue
		}
		var record checkpointRecord
		if err := readRecord(filepath.Join(path, "checkpoint.json"), &record); err != nil ||
			record.DataDir != m.root || record.ID != entry.Name() || record.Schema != 1 {
			continue
		}
		var bytes int64
		for _, file := range record.Files {
			bytes += file.Bytes
		}
		checkpoints = append(checkpoints, checkpointSummary{
			ID: record.ID, Created: record.Created, Protected: record.Protected,
			Complete: record.Complete, Bytes: bytes,
			Version: record.Installation.Versions[record.Installation.Active].Product.Version,
		})
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Installation installation        `json:"installation"`
		Journal      maintenanceJournal  `json:"maintenance"`
		Checkpoints  []checkpointSummary `json:"checkpoints"`
	}{m.record, journal, checkpoints})
}

func (m *maintenance) deleteCheckpoint(ctx context.Context) error {
	if !m.options.ConfirmDelete {
		return fmt.Errorf("checkpoint deletion requires --confirm-delete; automatic cleanup never removes protected backups")
	}
	record, path, err := m.loadCheckpointData(m.options.Checkpoint)
	if err != nil {
		return err
	}
	// No service may still be writing recovery files during explicit removal.
	var cfg Config
	if err := readRecord(filepath.Join(m.root, "runtime.json"), &cfg); err != nil {
		return err
	}
	cfg.DataDir = m.root
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := checkPortsClosed(cfg); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(m.root, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
		return fmt.Errorf("database PID/state file present; deletion deferred")
	}
	recovery := filepath.Join(m.root, "maintenance", "recovery", record.ID)
	if _, err := os.Lstat(recovery); err == nil {
		var owner stagedResource
		if !realChildDirectory(recovery) || readRecord(filepath.Join(recovery, "resource.json"), &owner) != nil ||
			owner.Schema != 1 || owner.DataDir != m.root {
			return fmt.Errorf("unknown recovery directory preserved")
		}
		if err := os.RemoveAll(recovery); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(m.root), &journal); err != nil {
		return err
	}
	if journal.Checkpoint == record.ID {
		if err := m.journal(journal.State, ""); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(path)
}
