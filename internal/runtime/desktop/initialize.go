package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"paperMaster/internal/logger"
)

var errSetupRequired = errors.New("first-time setup required; initialize from the management page or use --initialize")

type initialization struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
}

func (r *Runtime) initializationRecord() initialization {
	identity := r.cfg.DatabasePassword + ":" + r.cfg.RedisPassword + ":" + r.cfg.MCPToken
	return initialization{Version: 1, Identity: fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))}
}

func (r *Runtime) checkInitialization() error {
	data, err := os.ReadFile(filepath.Join(r.cfg.DataDir, "initialized.json"))
	if os.IsNotExist(err) {
		return errSetupRequired
	}
	if err != nil {
		return err
	}
	var record initialization
	if json.Unmarshal(data, &record) != nil || record != r.initializationRecord() {
		return fmt.Errorf("invalid initialization marker; restore configuration; data preserved")
	}
	return nil
}

func (r *Runtime) requireInitialized(ctx context.Context) error {
	if err := r.checkInitialization(); err != nil {
		return err
	}
	if _, err := r.preparePostgres(ctx); err != nil {
		return err
	}
	return checkIdentity(filepath.Join(r.cfg.DataDir, "redis"), r.cfg.RedisPassword)
}

func (r *Runtime) validateFiles() error {
	files := []string{
		r.cfg.Server, r.cfg.Python, r.cfg.Redis,
		filepath.Join(r.cfg.ParserDir, "papers_parser", "server.py"),
		filepath.Join(r.cfg.WebDir, "index.html"),
	}
	for _, name := range []string{"initdb", "postgres", "pg_ctl"} {
		path, err := r.pgBinary(name)
		if err != nil {
			return err
		}
		files = append(files, path)
	}
	for _, path := range files {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("missing runtime file %s; install the complete bundle", path)
		}
	}
	return nil
}

func (r *Runtime) initialize(ctx context.Context, done chan struct{}) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.StartupTimeout)
	defer cancel()
	if err := r.initializeData(ctx); err != nil {
		if ctx.Err() != nil {
			r.finishCycle(done, "stopped", "")
		} else {
			logger.Error(ctx, "desktop initialization failed", "error", r.cfg.redact(err.Error()))
			r.finishCycle(done, "failed", err.Error())
		}
		return
	}
	r.mu.Lock()
	r.state.Initialized = true
	r.mu.Unlock()
	logger.Info(ctx, "desktop initialization completed")
	r.finishCycle(done, "stopped", "")
}

func (r *Runtime) initializeData(ctx context.Context) error {
	if err := ordinaryUser(); err != nil {
		return err
	}
	if err := r.cfg.Validate(); err != nil {
		return err
	}
	// Re-running setup validates an existing completed installation. It never
	// repairs a damaged cluster or replaces credentials/ownership markers.
	if err := r.checkInitialization(); err == nil {
		return r.requireInitialized(ctx)
	} else if !errors.Is(err, errSetupRequired) {
		return err
	}
	if err := r.validateFiles(); err != nil {
		return err
	}
	for _, name := range []string{
		"logs", "redis", "uploads", "notes", filepath.Join("cache", "tmp"), "recognition-output", "work",
	} {
		if err := os.MkdirAll(filepath.Join(r.cfg.DataDir, name), 0700); err != nil {
			return err
		}
	}
	if _, err := r.postgresData(ctx, true); err != nil {
		return err
	}
	if err := ensureIdentity(filepath.Join(r.cfg.DataDir, "redis"), r.cfg.RedisPassword); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r.initializationRecord(), "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(r.cfg.DataDir, ".initialization-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(r.cfg.DataDir, "initialized.json"))
}
