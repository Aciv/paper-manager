package desktop

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (r *Runtime) pgBinary(name string) (string, error) {
	// EDB's Windows binaries also interpolate their installation/share path into
	// bootstrap SQL. Normalizing only the data directory leaves Unicode failures.
	bin, err := nativePath(r.cfg.PostgresBin)
	if err != nil {
		return "", fmt.Errorf("PostgreSQL installation directory: %w", err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(bin, name), nil
}
func (r *Runtime) dsn() string {
	u := &url.URL{
		Scheme: "postgres", User: url.UserPassword("papers_local", r.cfg.DatabasePassword),
		Host: address(r.cfg.Ports.Postgres), Path: "/postgres",
	}
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("connect_timeout", "2")
	u.RawQuery = q.Encode()
	return u.String()
}

func (r *Runtime) preparePostgres(ctx context.Context) (string, error) {
	return r.postgresData(ctx, false)
}

func (r *Runtime) postgresData(ctx context.Context, initialize bool) (string, error) {
	root, err := nativePath(r.cfg.DataDir)
	if err != nil {
		return "", err
	}
	data := filepath.Join(root, "postgres")
	if _, err = os.Stat(data); os.IsNotExist(err) {
		if !initialize {
			return "", fmt.Errorf("private PostgreSQL is not initialized; run first-time setup")
		}
		binary, e := r.pgBinary("initdb")
		if e != nil {
			return "", e
		}
		// Initialize in a disposable sibling. Failed initdb never turns a partial
		// cluster into an apparently valid existing cluster on retry.
		temp, e := os.MkdirTemp(root, ".postgres-init-")
		if e != nil {
			return "", e
		}
		defer os.RemoveAll(temp)
		password := filepath.Join(temp, "password")
		if e = os.WriteFile(password, []byte(r.cfg.DatabasePassword+"\n"), 0600); e != nil {
			return "", e
		}
		log, e := os.OpenFile(filepath.Join(r.cfg.DataDir, "logs", "initdb.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		defer log.Close()
		stage := filepath.Join(temp, "cluster")
		args := []string{
			"-D", stage, "-U", "papers_local", "--pwfile", password,
			"--auth=scram-sha-256", "--encoding=UTF8", "--no-locale",
		}
		e = runCommand(
			ctx, binary, args, r.serviceEnv(nil), root,
			&redactedWriter{cfg: r.cfg, out: log},
		)
		if e != nil {
			return "", fmt.Errorf("initialize private PostgreSQL: %w", e)
		}
		identity := fmt.Sprintf("%x\n", sha256.Sum256([]byte(r.cfg.DatabasePassword)))
		if e = os.WriteFile(filepath.Join(stage, ".papers-manager-instance"), []byte(identity), 0600); e != nil {
			return "", e
		}
		if e = os.Rename(stage, data); e != nil {
			return "", e
		}
	} else if err != nil {
		return "", err
	}
	version, err := os.ReadFile(filepath.Join(data, "PG_VERSION"))
	if err != nil || strings.TrimSpace(string(version)) != "17" {
		return "", fmt.Errorf("existing PostgreSQL directory is incomplete or not version 17; " +
			"restore/upgrade explicitly; data was preserved")
	}
	for _, name := range []string{"global/pg_control", "postgresql.conf", "pg_hba.conf"} {
		info, err := os.Stat(filepath.Join(data, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", fmt.Errorf("existing PostgreSQL cluster is incomplete; data preserved")
		}
	}
	if err := checkIdentity(data, r.cfg.DatabasePassword); err != nil {
		return "", fmt.Errorf("private PostgreSQL directory: %w", err)
	}
	// A PID file also survives power loss or forced termination. PostgreSQL owns
	// the live-process/lock checks and WAL recovery; let it distinguish a stale
	// lock from an active instance. Never unlink the PID file in the Launcher.
	// Identity checks above and port reservations still precede process startup.
	return data, nil
}

// An existing nonempty directory without this instance's marker is never
// adopted merely because its port/version looks compatible.
func ensureIdentity(directory, credential string) error {
	path := filepath.Join(directory, ".papers-manager-instance")
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte(credential)))
	data, err := os.ReadFile(path)
	if err == nil {
		if string(data) != want {
			return fmt.Errorf("directory belongs to a different instance; data preserved")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("existing directory has no ownership marker; refusing to adopt it")
	}
	return os.WriteFile(path, []byte(want), 0600)
}

// checkIdentity validates without creating an ownership claim during startup.
func checkIdentity(directory, credential string) error {
	data, err := os.ReadFile(filepath.Join(directory, ".papers-manager-instance"))
	if err != nil {
		return fmt.Errorf("ownership marker unavailable; data preserved: %w", err)
	}
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte(credential)))
	if string(data) != want {
		return fmt.Errorf("directory belongs to a different instance; data preserved")
	}
	return nil
}
