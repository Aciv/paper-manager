package desktop

import (
	"context"
	"errors"
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
	ProgressStage     string            `json:"progress_stage,omitempty"`
	ProgressStartedAt *time.Time        `json:"progress_started_at,omitempty"`
	Initialized       bool              `json:"initialized"`
	State             string            `json:"state"`
	Error             string            `json:"error,omitempty"`
	Services          map[string]string `json:"services"`
	AppURL            string            `json:"app_url"`
	MCPURL            string            `json:"mcp_url"`
	CanConfigure      bool              `json:"can_configure"`
}

type managed struct {
	name    string
	process *child
	stop    func(context.Context)
	log     io.Closer
}
type Runtime struct {
	maintenanceProbe bool
	cfg              Config
	mu               sync.Mutex
	state            Snapshot
	cancel           context.CancelFunc
	cycleDone        chan struct{}
	quit             chan struct{}
	quitOnce         sync.Once
	reloadConfig     bool
}

func New(cfg Config) *Runtime {
	cfg = cfg.withDefaults()
	done := make(chan struct{})
	close(done)
	r := &Runtime{
		cfg: cfg,
		state: Snapshot{
			State: "stopped", Services: map[string]string{},
			AppURL: "http://" + address(cfg.Ports.API),
			MCPURL: "http://" + address(cfg.Ports.MCP) + "/mcp",
		},
		cycleDone: done, quit: make(chan struct{}),
	}
	if err := r.checkInitialization(); err == nil {
		r.state.Initialized = true
	} else if errors.Is(err, errSetupRequired) {
		r.state.State = "setup_required"
	}
	return r
}
func (r *Runtime) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state
	s.CanConfigure = r.canConfigureLocked()
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
func (r *Runtime) setService(ctx context.Context, name, state string) {
	r.mu.Lock()
	r.state.Services[name] = state
	r.mu.Unlock()
	message := map[string]string{
		"starting": "服务进程已启动，正在等待就绪", "ready": "服务已就绪", "stopped": "服务已停止",
	}[state]
	logger.Info(ctx, message, "service", name)
}

// Start never attaches to an existing service. Failed cycles need an explicit
// retry; there is no automatic crash/restart loop.
func (r *Runtime) Start() bool {
	return r.begin(false)
}

// Initialize performs explicit first-time setup without starting services.
// The same serialized operation is used by installers and the management page.
func (r *Runtime) Initialize() bool {
	return r.begin(true)
}

func (r *Runtime) begin(initialize bool) bool {
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
	if r.reloadConfig {
		if err := r.reloadConfigLocked(); err != nil {
			r.state.State = "failed"
			r.state.Error = err.Error()
			return false
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.cycleDone = make(chan struct{})
	r.state.State = "starting"
	if initialize {
		r.state.State = "initializing"
	}
	r.state.Error = ""
	r.state.Services = map[string]string{}
	if initialize {
		go r.initialize(ctx, r.cycleDone)
	} else {
		go r.run(ctx, r.cycleDone)
	}
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
	logger.Info(ctx, "开始启动应用服务", "startup_timeout", r.cfg.StartupTimeout,
		"tasks", r.cfg.Concurrency.Tasks, "recognition", r.cfg.Concurrency.Recognition,
		"extraction", r.cfg.Concurrency.Extraction, "rpcs", r.cfg.Concurrency.RPCs)
	var owned []managed
	var release []func()
	var failure error
	defer func() {
		r.setState("stopping", "")
		// Independent budgets ensure an unresponsive API cannot consume the time
		// needed for a durable Redis/PostgreSQL shutdown.
		for i := len(owned) - 1; i >= 0; i-- {
			entry := owned[i]
			logger.Info(context.Background(), "正在停止服务", "service", entry.name, "pid", entry.process.cmd.Process.Pid)
			stopCtx, cancel := context.WithTimeout(context.Background(), r.cfg.StopTimeout)
			entry.process.close(stopCtx, func() { entry.stop(stopCtx) })
			cancel()
			entry.log.Close()
			r.setService(context.Background(), entry.name, "stopped")
		}
		for _, closeResource := range release {
			closeResource()
		}
		if errors.Is(failure, errSetupRequired) {
			logger.Info(context.Background(), errSetupRequired.Error())
			r.finishCycle(done, "setup_required", failure.Error())
		} else if failure != nil {
			logger.Error(context.Background(), "应用服务运行失败，请查看对应服务日志后重试", "error", r.cfg.redact(failure.Error()))
			r.finishCycle(done, "failed", failure.Error())
		} else {
			logger.Info(context.Background(), "应用服务已全部停止，管理页仍可用于配置和重新启动")
			r.finishCycle(done, "stopped", "")
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
	if err := r.requireInitialized(startup); err != nil {
		fail(err)
		return
	}
	if err := r.validateFiles(); err != nil {
		fail(err)
		return
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
			fail(fmt.Errorf("%s；请修改端口配置后重试", portFailure(name, port, err)))
			return
		}
		reservations[name] = l
	}
	pgData, err := r.preparePostgres(startup)
	if err != nil {
		fail(err)
		return
	}
	if err := checkIdentity(filepath.Join(r.cfg.DataDir, "redis"), r.cfg.RedisPassword); err != nil {
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
		logger.Info(ctx, "正在启动服务", "service", name, "log_file", log.Filename)
		c, err := startChild(binary, args, env, dir, &redactedWriter{cfg: r.cfg, out: log})
		if err != nil {
			log.Close()
			return nil, fmt.Errorf("start %s: %w", name, err)
		}
		owned = append(owned, managed{name, c, func(stopCtx context.Context) { stop(c, stopCtx) }, log})
		r.setService(ctx, name, "starting")
		return c, nil
	}
	env := r.serviceEnv(nil)
	pgServer, err := r.pgBinary("postgres")
	if err != nil {
		fail(err)
		return
	}
	pgControl, err := r.pgBinary("pg_ctl")
	if err != nil {
		fail(err)
		return
	}
	postgres, err := start(
		"postgres", pgServer, []string{
			"-D", pgData, "-h", "127.0.0.1", "-p", fmt.Sprint(r.cfg.Ports.Postgres),
			"-c", "unix_socket_directories=", "-c", "password_encryption=scram-sha-256",
		}, env, r.cfg.DataDir,
		func(c *child, stopCtx context.Context) {
			args := []string{"-D", pgData, "-m", "fast", "-w", "-t", "30", "stop"}
			_ = runCommand(stopCtx, pgControl, args, env, r.cfg.DataDir, io.Discard)
		},
	)
	if err != nil {
		fail(err)
		return
	}
	if err = r.ready(startup, "等待数据库启动或崩溃恢复完成", postgres, func(check context.Context) error {
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
	r.setService(ctx, "postgres", "ready")
	rc := redis.NewClient(&redis.Options{
		Addr: address(r.cfg.Ports.Redis), Password: r.cfg.RedisPassword,
		MaxRetries: -1, DialTimeout: time.Second,
		ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	release = append(release, func() { _ = rc.Close() })
	redisProcess, err := start(
		// The selected Windows Redis is Cygwin-based. A relative POSIX-style
		// config path works there and on Unix without translating drive letters.
		"redis", r.cfg.Redis, []string{"../redis.conf"}, env, filepath.Join(r.cfg.DataDir, "redis"),
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
	if err = r.ready(startup, "等待任务队列加载已有任务", redisProcess, redisReady); err != nil {
		fail(fmt.Errorf("Redis readiness failed; see redis.log: %w", err))
		return
	}
	r.setService(ctx, "redis", "ready")
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
	if err = r.ready(startup, "等待论文解析服务就绪", parser, func(check context.Context) error {
		if connection.GetState() == connectivity.Ready {
			return nil
		}
		return fmt.Errorf("gRPC transport is not ready")
	}); err != nil {
		fail(fmt.Errorf("parser readiness failed; see parser.log: %w", err))
		return
	}
	r.setService(ctx, "parser", "ready")
	api, err := start(
		"api", r.cfg.Server, nil, r.apiEnv(), filepath.Join(r.cfg.DataDir, "work"), closeInput,
	)
	if err != nil {
		fail(err)
		return
	}
	client := &http.Client{Timeout: time.Second}
	if err = r.ready(startup, "等待论文库就绪（含数据库结构检查及必要升级）", api, func(check context.Context) error {
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
	r.setService(ctx, "api", "ready")
	r.setState("ready", "")
	logger.Info(ctx, "所有服务已就绪，可以打开论文库", "app_url", r.Snapshot().AppURL)
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

func (r *Runtime) ready(ctx context.Context, stage string, c *child, check func(context.Context) error) error {
	return r.runProgress(ctx, stage, func() error {
		return r.waitReady(ctx, c, check)
	})
}

func (r *Runtime) waitReady(ctx context.Context, c *child, check func(context.Context) error) error {
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
