package desktop

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/logger"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Snapshot struct {
	State    string            `json:"state"`
	Error    string            `json:"error,omitempty"`
	Services map[string]string `json:"services"`
	AppURL   string            `json:"app_url"`
	MCPURL   string            `json:"mcp_url"`
}

type managed struct {
	name    string
	process *child
	stop    func(context.Context)
	log     io.Closer
}
type Runtime struct {
	cfg       Config
	mu        sync.Mutex
	state     Snapshot
	cancel    context.CancelFunc
	cycleDone chan struct{}
	quit      chan struct{}
	quitOnce  sync.Once
}

func New(cfg Config) *Runtime {
	done := make(chan struct{})
	close(done)
	return &Runtime{
		cfg: cfg,
		state: Snapshot{
			State: "stopped", Services: map[string]string{},
			AppURL: "http://" + address(cfg.Ports.API),
			MCPURL: "http://" + address(cfg.Ports.MCP) + "/mcp",
		},
		cycleDone: done, quit: make(chan struct{}),
	}
}
func (r *Runtime) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state
	s.Services = map[string]string{}
	for k, v := range r.state.Services {
		s.Services[k] = v
	}
	return s
}
func (r *Runtime) setState(state, message string) {
	r.mu.Lock()
	r.state.State = state
	r.state.Error = r.cfg.redact(message)
	r.mu.Unlock()
}
func (r *Runtime) setService(name, state string) {
	r.mu.Lock()
	r.state.Services[name] = state
	r.mu.Unlock()
}

// Start never attaches to an existing service. Failed cycles need an explicit
// retry; there is no automatic crash/restart loop.
func (r *Runtime) Start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.quit:
		return false
	default:
	}
	select {
	case <-r.cycleDone:
	default:
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.cycleDone = make(chan struct{})
	r.state.State = "initializing"
	r.state.Error = ""
	r.state.Services = map[string]string{}
	go r.run(ctx, r.cycleDone)
	return true
}
func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.cycleDone:
		return
	default:
	}
	if r.cancel != nil {
		r.state.State = "stopping"
		r.cancel()
	}
}
func (r *Runtime) Wait(ctx context.Context) error {
	r.mu.Lock()
	done := r.cycleDone
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Runtime) Quit()                          { r.quitOnce.Do(func() { close(r.quit) }); r.Stop() }
func (r *Runtime) QuitRequested() <-chan struct{} { return r.quit }

func (r *Runtime) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	var owned []managed
	var release []func()
	var failure error
	defer func() {
		r.setState("stopping", "")
		// Independent budgets ensure an unresponsive API cannot consume the time
		// needed for a durable Redis/PostgreSQL shutdown.
		for i := len(owned) - 1; i >= 0; i-- {
			entry := owned[i]
			stopCtx, cancel := context.WithTimeout(context.Background(), r.cfg.StopTimeout)
			entry.process.close(stopCtx, func() { entry.stop(stopCtx) })
			cancel()
			entry.log.Close()
			r.setService(entry.name, "stopped")
		}
		for _, closeResource := range release {
			closeResource()
		}
		if failure != nil {
			r.setState("failed", failure.Error())
			logger.Error(context.Background(), "local runtime failed", "error", r.cfg.redact(failure.Error()))
		} else {
			r.setState("stopped", "")
		}
	}()
	fail := func(err error) {
		if ctx.Err() == nil {
			failure = err
		}
	}
	startup, cancel := context.WithTimeout(ctx, r.cfg.StartupTimeout)
	defer cancel()
	if err := ordinaryUser(); err != nil {
		fail(err)
		return
	}
	if err := r.cfg.Validate(); err != nil {
		fail(err)
		return
	}
	directories := []string{
		"logs", "redis", "uploads", filepath.Join("cache", "tmp"), "recognition-output", "work",
	}
	for _, name := range directories {
		if err := os.MkdirAll(filepath.Join(r.cfg.DataDir, name), 0700); err != nil {
			fail(err)
			return
		}
	}
	files := []string{
		r.cfg.Server, r.cfg.Python, r.cfg.Redis,
		filepath.Join(r.cfg.ParserDir, "papers_parser", "server.py"),
		filepath.Join(r.cfg.WebDir, "index.html"),
		r.pgBinary("initdb"), r.pgBinary("postgres"), r.pgBinary("pg_ctl"),
	}
	for _, path := range files {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			fail(fmt.Errorf("missing runtime file %s; configure runtime.json or install the complete bundle", path))
			return
		}
	}
	// Hold all ports until the corresponding process starts. Releasing a socket
	// is not atomic handoff to these binaries: bind/readiness failures still fail
	// the cycle, rather than silently connecting to a pre-existing instance.
	reservations := map[string]net.Listener{}
	defer func() {
		for _, l := range reservations {
			l.Close()
		}
	}()
	ports := map[string]int{
		"postgres": r.cfg.Ports.Postgres, "redis": r.cfg.Ports.Redis,
		"parser": r.cfg.Ports.Parser, "api": r.cfg.Ports.API, "mcp": r.cfg.Ports.MCP,
	}
	for name, port := range ports {
		l, err := net.Listen("tcp", address(port))
		if err != nil {
			fail(fmt.Errorf("%s port %d is occupied; release it or change the stopped runtime's configuration", name, port))
			return
		}
		reservations[name] = l
	}
	pgData, err := r.preparePostgres(startup)
	if err != nil {
		fail(err)
		return
	}
	if err := ensureIdentity(filepath.Join(r.cfg.DataDir, "redis"), r.cfg.RedisPassword); err != nil {
		fail(fmt.Errorf("private Redis directory: %w", err))
		return
	}
	redisConfig := filepath.Join(r.cfg.DataDir, "redis.conf")
	redisText := fmt.Sprintf(
		"bind 127.0.0.1\nprotected-mode yes\nport %d\nrequirepass %s\n"+
			"daemonize no\nappendonly yes\nappendfsync everysec\n"+
			"save 900 1\nmaxmemory-policy noeviction\ndir .\n",
		r.cfg.Ports.Redis, r.cfg.RedisPassword,
	)
	if err = os.WriteFile(redisConfig, []byte(redisText), 0600); err != nil {
		fail(err)
		return
	}
	r.setState("starting", "")
	start := func(
		name, binary string, args, env []string, dir string,
		stop func(*child, context.Context),
	) (*child, error) {
		if err := startup.Err(); err != nil {
			return nil, err
		}
		log := &lumberjack.Logger{Filename: filepath.Join(r.cfg.DataDir, "logs", name+".log"), MaxSize: 2, MaxBackups: 3}
		if l := reservations[name]; l != nil {
			l.Close()
			delete(reservations, name)
		}
		if name == "api" {
			reservations["mcp"].Close()
			delete(reservations, "mcp")
		}
		c, err := startChild(binary, args, env, dir, &redactedWriter{cfg: r.cfg, out: log})
		if err != nil {
			log.Close()
			return nil, fmt.Errorf("start %s: %w", name, err)
		}
		owned = append(owned, managed{name, c, func(stopCtx context.Context) { stop(c, stopCtx) }, log})
		r.setService(name, "starting")
		return c, nil
	}
	env := r.serviceEnv(nil)
	postgres, err := start(
		"postgres", r.pgBinary("postgres"), []string{
			"-D", pgData, "-h", "127.0.0.1", "-p", fmt.Sprint(r.cfg.Ports.Postgres),
			"-c", "unix_socket_directories=", "-c", "password_encryption=scram-sha-256",
		}, env, r.cfg.DataDir,
		func(c *child, stopCtx context.Context) {
			args := []string{"-D", pgData, "-m", "fast", "-w", "-t", "30", "stop"}
			_ = runCommand(stopCtx, r.pgBinary("pg_ctl"), args, env, r.cfg.DataDir, io.Discard)
		},
	)
	if err != nil {
		fail(err)
		return
	}
	if err = r.ready(startup, postgres, func(check context.Context) error {
		db, e := pgx.Connect(check, r.dsn())
		if e != nil {
			return e
		}
		defer db.Close(check)
		return db.Ping(check)
	}); err != nil {
		fail(fmt.Errorf("PostgreSQL readiness failed; see postgres.log: %w", err))
		return
	}
	r.setService("postgres", "ready")
	rc := redis.NewClient(&redis.Options{
		Addr: address(r.cfg.Ports.Redis), Password: r.cfg.RedisPassword,
		MaxRetries: -1, DialTimeout: time.Second,
		ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	release = append(release, func() { _ = rc.Close() })
	redisProcess, err := start(
		"redis", r.cfg.Redis, []string{redisConfig}, env, filepath.Join(r.cfg.DataDir, "redis"),
		func(c *child, stopCtx context.Context) {
			_ = rc.Shutdown(stopCtx).Err()
		},
	)
	if err != nil {
		rc.Close()
		fail(err)
		return
	}
	redisReady := func(check context.Context) error { return rc.Ping(check).Err() }
	if err = r.ready(startup, redisProcess, redisReady); err != nil {
		fail(fmt.Errorf("Redis readiness failed; see redis.log: %w", err))
		return
	}
	r.setService("redis", "ready")
	parser, err := start(
		"parser", r.cfg.Python, r.parserArgs(), r.parserEnv(),
		filepath.Join(r.cfg.DataDir, "work"), closeInput,
	)
	if err != nil {
		fail(err)
		return
	}
	connection, err := grpc.NewClient(
		address(r.cfg.Ports.Parser), grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		fail(err)
		return
	}
	defer connection.Close()
	connection.Connect()
	if err = r.ready(startup, parser, func(check context.Context) error {
		if connection.GetState() == connectivity.Ready {
			return nil
		}
		return fmt.Errorf("gRPC transport is not ready")
	}); err != nil {
		fail(fmt.Errorf("parser readiness failed; see parser.log: %w", err))
		return
	}
	r.setService("parser", "ready")
	api, err := start(
		"api", r.cfg.Server, nil, r.apiEnv(), filepath.Join(r.cfg.DataDir, "work"), closeInput,
	)
	if err != nil {
		fail(err)
		return
	}
	client := &http.Client{Timeout: time.Second}
	if err = r.ready(startup, api, func(check context.Context) error {
		req, _ := http.NewRequestWithContext(check, "GET", r.Snapshot().AppURL+"/health", nil)
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("health returned %d", resp.StatusCode)
		}
		return r.mcpReady(check, client)
	}); err != nil {
		fail(fmt.Errorf("API readiness failed; see api.log: %w", err))
		return
	}
	for _, entry := range owned {
		if !entry.process.alive() {
			fail(fmt.Errorf("%s exited during startup; see %s.log", entry.name, entry.name))
			return
		}
	}
	r.setService("api", "ready")
	r.setState("ready", "")
	logger.Info(ctx, "local runtime ready", "api_address", address(r.cfg.Ports.API))
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, entry := range owned {
				if !entry.process.alive() {
					fail(fmt.Errorf("%s exited unexpectedly; see %s.log and retry explicitly", entry.name, entry.name))
					return
				}
			}
		}
	}
}

func (r *Runtime) ready(ctx context.Context, c *child, check func(context.Context) error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !c.alive() {
			return fmt.Errorf("owned process exited")
		}
		probe, cancel := context.WithTimeout(ctx, time.Second)
		err := check(probe)
		cancel()
		if err == nil {
			select {
			case <-c.done:
				return fmt.Errorf("owned process exited")
			case <-time.After(100 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return fmt.Errorf("owned process exited")
		case <-ticker.C:
		}
	}
}

type redactedWriter struct {
	mu      sync.Mutex
	cfg     Config
	out     io.Writer
	pending string
}

func (w *redactedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := w.pending[:i+1]
		w.pending = w.pending[i+1:]
		if _, err := io.WriteString(w.out, w.cfg.redact(line)); err != nil {
			return 0, err
		}
	}
	// Long binary/no-newline diagnostics are discarded, never allowed unbounded
	// buffering or partial-secret disclosure. Ordinary service logs are lines.
	if len(w.pending) > 64<<10 {
		w.pending = ""
		_, _ = io.WriteString(w.out, "[oversized diagnostic omitted]\n")
	}
	return len(p), nil
}
