package desktop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"paperMaster/internal/logger"
)

func realChildDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func (m *maintenance) cleanup(ctx context.Context, now time.Time) error {
	if m.record.Active != "" {
		cfg, err := OpenConfig(m.root, m.versionPath(m.record.Active))
		if err != nil {
			return err
		}
		if err := checkPortsClosed(cfg); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(m.root, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
		return fmt.Errorf("database PID/state file present; cleanup deferred")
	}
	var journal maintenanceJournal
	if err := readRecord(maintenanceJournalPath(m.root), &journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Join(m.root, "maintenance", "checkpoints")
	entries, err := os.ReadDir(parent)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	type checkpointItem struct {
		record checkpointRecord
		path   string
	}
	var checkpoints []checkpointItem
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if !safeKey(entry.Name()) || !realChildDirectory(path) {
			continue
		}
		var record checkpointRecord
		if err := readRecord(filepath.Join(path, "checkpoint.json"), &record); err != nil ||
			record.Schema != 1 || record.ID != entry.Name() || record.DataDir != m.root || !record.Complete {
			logger.Warn(ctx, "unknown checkpoint preserved", "checkpoint_id", entry.Name())
			continue
		}
		checkpoints = append(checkpoints, checkpointItem{record, path})
	}
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].record.Created.After(checkpoints[j].record.Created)
	})
	pinnedVersions := map[string]bool{m.record.Active: true}
	for i, item := range checkpoints {
		record := item.record
		keep := i < 2 || record.Protected || record.ID == journal.Checkpoint ||
			now.Sub(record.Created) <= 30*24*time.Hour
		if keep {
			for key := range record.Installation.Versions {
				pinnedVersions[key] = true
			}
			continue
		}
		// Retention and ownership metadata above identify this disposable backup.
		// Do not reread every database/PDF byte just to delete an expired copy.
		if err := os.RemoveAll(item.path); err != nil {
			return err
		}
		logger.Info(ctx, "expired upgrade checkpoint removed", "checkpoint_id", record.ID)
	}
	keys := make([]string, 0, len(m.record.Versions))
	for key := range m.record.Versions {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m.record.Versions[keys[i]].Created.After(m.record.Versions[keys[j]].Created)
	})
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	keptOld := 0
	for _, key := range keys {
		record := m.record.Versions[key]
		if key != m.record.Active && record.Healthy && keptOld < 2 {
			pinnedVersions[key] = true
			keptOld++
		}
		if pinnedVersions[key] || !record.Healthy || now.Sub(record.Created) <= 30*24*time.Hour ||
			within(m.versionPath(key), executable) {
			continue
		}
		hash, err := fileHash(filepath.Join(m.versionPath(key), "bundle.json"))
		if err != nil || hash != record.Manifest {
			logger.Warn(ctx, "unrecognized old program preserved", "version", record.Product.Version)
			continue
		}
		if err := os.RemoveAll(m.versionPath(key)); err != nil {
			return err
		}
		delete(m.record.Versions, key)
		// Persist each deletion so interruption cannot leave an active reference.
		if err := m.save(); err != nil {
			return err
		}
		logger.Info(ctx, "expired program version removed", "version", record.Product.Version)
	}
	for _, stageRoot := range []string{filepath.Join(m.program, "staging"),
		filepath.Join(m.root, "maintenance", "staging")} {
		stages, err := os.ReadDir(stageRoot)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range stages {
			if err := ctx.Err(); err != nil {
				return err
			}
			path := filepath.Join(stageRoot, entry.Name())
			if !safeKey(entry.Name()) || !realChildDirectory(path) {
				continue
			}
			var record stagedResource
			if err := readRecord(filepath.Join(path, "resource.json"), &record); err != nil ||
				record.Schema != 1 || record.DataDir != m.root || record.Updated.IsZero() ||
				now.Sub(record.Updated) <= 24*time.Hour {
				continue
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			logger.Info(ctx, "expired update staging removed", "resource_id", entry.Name())
		}
	}
	return nil
}
