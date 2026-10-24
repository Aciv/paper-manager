package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	root := filepath.Join(t.TempDir(), "运行数据 with space")
	lock, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	cfg, err := OpenConfig(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPrivateConfigAndInstanceLock(t *testing.T) {
	cfg := testConfig(t)
	if lock, err := Acquire(cfg.DataDir); err == nil {
		lock.Close()
		t.Fatal("second instance acquired the same directory")
	}
	reloaded, err := OpenConfig(cfg.DataDir, t.TempDir())
	if err != nil || reloaded != cfg {
		t.Fatalf("configuration/ports/credentials changed on restart: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(cfg.DataDir, "runtime.json"))
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credentials permission: %v", info.Mode())
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "runtime.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfig(cfg.DataDir, t.TempDir()); err == nil {
		t.Fatal("damaged configuration was silently replaced")
	}
	if err := os.Remove(filepath.Join(cfg.DataDir, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.DataDir, "postgres"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfig(cfg.DataDir, t.TempDir()); err == nil {
		t.Fatal("existing data received replacement credentials")
	}
}

func TestExistingPostgresNeverReinitialized(t *testing.T) {
	cfg := testConfig(t)
	directory := filepath.Join(cfg.DataDir, "postgres")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(directory, "keep-me")
	_ = os.WriteFile(sentinel, []byte("valuable data"), 0600)
	app := New(cfg)
	if _, err := app.preparePostgres(context.Background()); err == nil {
		t.Fatal("partial cluster accepted")
	}
	_ = os.WriteFile(filepath.Join(directory, "PG_VERSION"), []byte("17\n"), 0600)
	_ = os.WriteFile(filepath.Join(directory, "postmaster.pid"), []byte("123\n"), 0600)
	if _, err := app.preparePostgres(context.Background()); err == nil {
		t.Fatal("existing process PID ignored")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "valuable data" {
		t.Fatal("existing database contents changed")
	}
}

func TestPrivateDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	if err := ensureIdentity(root, "first-instance"); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(root, "first-instance"); err != nil {
		t.Fatal(err)
	}
	if err := ensureIdentity(root, "second-instance"); err == nil {
		t.Fatal("another instance adopted persisted data")
	}
	foreign := t.TempDir()
	_ = os.WriteFile(filepath.Join(foreign, "PG_VERSION"), []byte("17"), 0600)
	if err := ensureIdentity(foreign, "first-instance"); err == nil {
		t.Fatal("unmarked existing database directory was adopted")
	}
}

func TestManagementBoundariesAndFailureVisibility(t *testing.T) {
	cfg := testConfig(t)
	app := New(cfg)
	handler := app.Handler()
	origin := app.ManagementURL()
	request := func(method, path, host, source string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, origin+path, nil)
		req.Host = host
		if source != "" {
			req.Header.Set("Origin", source)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	for _, tc := range []struct {
		method, path, host, source string
		want                       int
	}{
		{"GET", "/", address(cfg.Ports.Manager), "", 200},
		{"GET", "/status", address(cfg.Ports.Manager), "", 200},
		{"GET", "/status", "evil.example", "", 403},
		{"GET", "/status", address(cfg.Ports.Manager), "https://evil.example", 403},
		{"POST", "/stop", address(cfg.Ports.Manager), "", 403},
		{"POST", "/stop", address(cfg.Ports.Manager), origin, 202},
		{"GET", "/logs?service=../runtime", address(cfg.Ports.Manager), "", 400},
	} {
		result := request(tc.method, tc.path, tc.host, tc.source)
		if result.Code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, result.Code, tc.want)
		}
	}
	if app.Snapshot().State != "stopped" {
		t.Fatal("idempotent stop changed completed state")
	}
	if !app.Start() {
		t.Fatal("start rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if state := app.Snapshot(); state.State != "failed" || state.Error == "" {
		t.Fatalf("missing runtime failure not visible: %+v", state)
	}
	result := request("GET", "/status", address(cfg.Ports.Manager), "")
	if result.Code != 200 || !strings.Contains(result.Body.String(), "failed") {
		t.Fatal("management page unavailable after service failure")
	}
	if strings.Contains(request("GET", "/", address(cfg.Ports.Manager), "").Body.String(), cfg.MCPToken) {
		t.Fatal("MCP token leaked into HTML")
	}
	app.Quit()
	if app.Start() {
		t.Fatal("start accepted after Launcher quit")
	}
}

func TestChildEnvironmentAndLogRedaction(t *testing.T) {
	t.Setenv("DATABASE_URL", "ambient-db")
	t.Setenv("REDIS_PASSWORD", "ambient-redis")
	t.Setenv("PYTHONPATH", "ambient-python")
	env := strings.Join(childEnv(map[string]string{"REDIS_PASSWORD": "private-redis"}), "\n")
	for _, forbidden := range []string{"ambient-db", "ambient-redis", "ambient-python"} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("inherited unrelated runtime environment: %s", forbidden)
		}
	}
	cfg := testConfig(t)
	var output bytes.Buffer
	writer := &redactedWriter{cfg: cfg, out: &output}
	_, _ = writer.Write([]byte("password=" + cfg.RedisPassword[:20]))
	_, _ = writer.Write([]byte(cfg.RedisPassword[20:] + "\n"))
	if strings.Contains(output.String(), cfg.RedisPassword) || !strings.Contains(output.String(), "redacted") {
		t.Fatal("split credential was not redacted")
	}
	_, _ = writer.Write(bytes.Repeat([]byte("x"), 100<<10))
	if len(writer.pending) > 64<<10 {
		t.Fatal("diagnostic buffer grew without bound")
	}
}

func TestLauncherEnvironmentContract(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv("LAUNCHER_PYTHON", filepath.Join(t.TempDir(), "python"))
	t.Setenv("LAUNCHER_STARTUP_TIMEOUT", "2m")
	t.Setenv("LAUNCHER_EXTRACTION_ENGINE", "rules")
	override, err := ApplyEnvironment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if override.Python == cfg.Python || override.StartupTimeout != 2*time.Minute ||
		override.ExtractionEngine != "rules" || override.DatabasePassword != cfg.DatabasePassword {
		t.Fatal("launcher settings/identity precedence violated")
	}
	t.Setenv("LAUNCHER_HTTP_PORT", "70000")
	if _, err := ApplyEnvironment(cfg); err == nil {
		t.Fatal("invalid port accepted")
	}
	t.Setenv("LAUNCHER_HTTP_PORT", "")
	t.Setenv("LAUNCHER_PYTHON", "relative/python")
	if _, err := ApplyEnvironment(cfg); err == nil {
		t.Fatal("cwd-dependent runtime path accepted")
	}
}

func TestManagedProcessGracefulAndForcedCleanup(t *testing.T) {
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "forced"}[forced], func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ready.json")
			env := childEnv(map[string]string{
				"PM_RUNTIME_HELPER": "parent", "PM_RUNTIME_MARKER": marker,
			})
			process, err := startChild(
				os.Args[0], []string{"-test.run=^TestRuntimeProcessHelper$"}, env, t.TempDir(), io.Discard,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				process.close(ctx, func() {})
			}()
			var port string
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(marker)
				if err == nil && json.Unmarshal(data, &port) == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if port == "" {
				t.Fatal("grandchild did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			process.close(ctx, func() {
				if !forced {
					process.stdin.Close()
				}
			})
			process.close(ctx, func() { t.Fatal("duplicate close was not idempotent") })
			// Killing a process tree is asynchronous on both supported APIs.
			// Wait for OS teardown, rather than asserting before SIGKILL runs.
			deadline = time.Now().Add(2 * time.Second)
			for {
				connection, err := net.DialTimeout("tcp", port, 100*time.Millisecond)
				if err != nil {
					break
				}
				connection.Close()
				if time.Now().After(deadline) {
					t.Fatal("owned grandchild survived shutdown")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestRuntimeProcessHelper(t *testing.T) {
	role := os.Getenv("PM_RUNTIME_HELPER")
	if role == "" {
		return
	}
	if role == "parent" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeProcessHelper$")
		cmd.Env = childEnv(map[string]string{
			"PM_RUNTIME_HELPER": "grandchild", "PM_RUNTIME_MARKER": os.Getenv("PM_RUNTIME_MARKER"),
		})
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	data, _ := json.Marshal(listener.Addr().String())
	if err := os.WriteFile(os.Getenv("PM_RUNTIME_MARKER"), data, 0600); err != nil {
		os.Exit(4)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(5)
		}
		connection.Close()
	}
}
