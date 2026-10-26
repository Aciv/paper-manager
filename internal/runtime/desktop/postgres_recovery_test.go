package desktop

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This opt-in native test kills the owner, not pg_ctl. It exercises Windows
// kill-on-close jobs, stale PID/WAL recovery, active-instance refusal and data
// preservation after moving a cluster. No existing user database is contacted.
func TestWindowsPostgresOwnerCrashRecovery(t *testing.T) {
	bin := os.Getenv("PAPERS_MANAGER_TEST_POSTGRES_BIN")
	if runtime.GOOS != "windows" || bin == "" {
		t.Skip("requires native Windows and explicitly selected PostgreSQL binaries")
	}
	cfg, err := OpenConfig(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.PostgresBin = bin
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg).postgresData(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var committed bool
	for cycle := 0; cycle < 3; cycle++ {
		if err := SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		log, err := os.Create(filepath.Join(cfg.DataDir, fmt.Sprintf("crash-%d.log", cycle)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { log.Close() })
		cmd := exec.Command(os.Args[0], "-test.run=^TestPostgresCrashOwnerHelper$")
		cmd.Env = childEnv(map[string]string{
			"PM_POSTGRES_CRASH_CONFIG": filepath.Join(cfg.DataDir, "runtime.json"),
		})
		cmd.Stdout, cmd.Stderr = log, log
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); input.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var db *pgx.Conn
		for ctx.Err() == nil {
			db, err = pgx.Connect(ctx, New(cfg).dsn())
			if err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err != nil {
			cancel()
			data, _ := os.ReadFile(log.Name())
			t.Fatalf("recovery %d failed: %v\n%s", cycle, err, data)
		}
		if !committed {
			_, err = db.Exec(ctx, "CREATE TABLE crash_canary(value text); INSERT INTO crash_canary VALUES ('preserved')")
			committed = true
		} else {
			var value string
			err = db.QueryRow(ctx, "SELECT value FROM crash_canary").Scan(&value)
			if err == nil && value != "preserved" {
				err = fmt.Errorf("committed row lost")
			}
		}
		_ = db.Close(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if cycle == 0 {
			// Native lock validation must refuse a second postmaster, even when
			// the Launcher no longer rejects a PID file merely for existing.
			binary, err := New(cfg).pgBinary("postgres")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = runCommand(ctx, binary, []string{"-D", filepath.Join(cfg.DataDir, "postgres")},
				New(cfg).serviceEnv(nil), cfg.DataDir, log)
			cancel()
			if err == nil {
				t.Fatal("second postmaster adopted a live database")
			}
		}
		if cycle < 2 {
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			input.Close()
			deadline := time.Now().Add(10 * time.Second)
			for {
				conn, err := net.DialTimeout("tcp", address(cfg.Ports.Postgres), 100*time.Millisecond)
				if err != nil {
					break
				}
				conn.Close()
				if time.Now().After(deadline) {
					t.Fatal("owned PostgreSQL survived owner termination")
				}
				time.Sleep(25 * time.Millisecond)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "postgres", "postmaster.pid")); err != nil {
				t.Fatalf("crash fixture did not leave a stale PID: %v", err)
			}
		} else {
			input.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("graceful shutdown: %v", err)
			}
			if _, err := os.Stat(filepath.Join(cfg.DataDir, "postgres", "postmaster.pid")); !os.IsNotExist(err) {
				t.Fatal("normal shutdown retained the PID file")
			}
		}
		log.Close()
		if cycle == 1 {
			moved := cfg.DataDir + " moved"
			if err := os.Rename(cfg.DataDir, moved); err != nil {
				t.Fatal(err)
			}
			cfg.DataDir = moved
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
		}
	}
}

func TestPostgresCrashOwnerHelper(t *testing.T) {
	path := os.Getenv("PM_POSTGRES_CRASH_CONFIG")
	if path == "" {
		return
	}
	var cfg Config
	if err := readRecord(path, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Dir(path)
	lock, err := Acquire(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	app := New(cfg)
	data, err := app.preparePostgres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := app.pgBinary("postgres")
	if err != nil {
		t.Fatal(err)
	}
	process, err := startChild(binary, []string{
		"-D", data, "-h", "127.0.0.1", "-p", fmt.Sprint(cfg.Ports.Postgres),
		"-c", "unix_socket_directories=",
	}, app.serviceEnv(nil), cfg.DataDir, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	process.close(ctx, func() {
		control, err := app.pgBinary("pg_ctl")
		if err != nil {
			return
		}
		_ = runCommand(ctx, control, []string{"-D", data, "-m", "fast", "-w", "stop"},
			app.serviceEnv(nil), cfg.DataDir, os.Stdout)
	})
}
