// Package desktop owns the desktop application's private services. It does
// not change the configuration or lifecycle of developer/Compose instances.
package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	LauncherFile     string        `json:"-"`
	Version          int           `json:"version"`
	BundleHash       string        `json:"bundle_hash,omitempty"`
	DataDir          string        `json:"-"`
	Server           string        `json:"server"`
	Python           string        `json:"python"`
	ParserDir        string        `json:"parser_dir"`
	PostgresBin      string        `json:"postgres_bin"`
	Redis            string        `json:"redis"`
	WebDir           string        `json:"web_dir"`
	ExtractionEngine string        `json:"extraction_engine"`
	Ports            Ports         `json:"ports"`
	DatabasePassword string        `json:"database_password"`
	RedisPassword    string        `json:"redis_password"`
	MCPToken         string        `json:"mcp_token"`
	StartupTimeout   time.Duration `json:"startup_timeout"`
	StopTimeout      time.Duration `json:"stop_timeout"`
	Concurrency      Concurrency   `json:"concurrency"`
}

type Concurrency struct {
	Tasks       int `json:"tasks"`
	Recognition int `json:"recognition"`
	Extraction  int `json:"extraction"`
	Render      int `json:"render"`
	RPCs        int `json:"rpcs"`
}

func (c Config) withDefaults() Config {
	// Old runtime.json files predate configurable admission limits.
	if c.Concurrency == (Concurrency{}) {
		c.Concurrency = Concurrency{Tasks: 32, Recognition: 4, Extraction: 4, Render: 4, RPCs: 8}
	}
	return c
}

type Ports struct {
	Manager  int `json:"manager"`
	API      int `json:"api"`
	MCP      int `json:"mcp"`
	Postgres int `json:"postgres"`
	Redis    int `json:"redis"`
	Parser   int `json:"parser"`
}

func DefaultDataDir() (string, error) {
	if runtime.GOOS == "linux" {
		if root := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(root) {
			return filepath.Join(root, "papers-manager"), nil
		}
		home, err := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", "papers-manager"), err
	}
	root, err := os.UserConfigDir()
	return filepath.Join(root, "papers-manager"), err
}

func secret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func address(port int) string { return net.JoinHostPort("127.0.0.1", fmt.Sprint(port)) }

// OpenConfig requires the instance lock. Configuration is never implicitly
// replaced: a damaged configuration must not give existing data new credentials.
func OpenConfig(root, install string) (Config, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}
	install, err = filepath.Abs(install)
	if err != nil {
		return Config{}, err
	}
	path := filepath.Join(root, "runtime.json")
	content, err := os.ReadFile(path)
	if err == nil {
		var cfg Config
		if err = json.Unmarshal(content, &cfg); err != nil {
			return cfg, fmt.Errorf("invalid runtime.json; restore configuration from backup: %w", err)
		}
		cfg.DataDir = root
		cfg = cfg.withDefaults()
		return cfg, cfg.Validate()
	}
	if !os.IsNotExist(err) {
		return Config{}, err
	}
	for _, name := range []string{"postgres", "redis", "uploads", "notes"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			return Config{}, fmt.Errorf(
				"runtime.json missing but %s exists; restore configuration instead of reinitializing", name,
			)
		}
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	python := filepath.Join(install, "python", "bin", "python3")
	if runtime.GOOS == "windows" {
		python = filepath.Join(install, "python", "python.exe")
	}
	cfg := Config{
		Version:          1,
		DataDir:          root,
		Server:           filepath.Join(install, "server"+ext),
		Python:           python,
		ParserDir:        filepath.Join(install, "pdf_parser"),
		PostgresBin:      filepath.Join(install, "postgres", "bin"),
		Redis:            filepath.Join(install, "redis", "redis-server"+ext),
		WebDir:           filepath.Join(install, "web"),
		ExtractionEngine: "hybrid",
		DatabasePassword: secret(),
		RedisPassword:    secret(),
		MCPToken:         secret(),
		StartupTimeout:   90 * time.Second,
		StopTimeout:      75 * time.Second,
		Concurrency:      Concurrency{Tasks: 32, Recognition: 4, Extraction: 4, Render: 4, RPCs: 8},
	}
	// New instances use fixed defaults. Conflicts require an explicit user edit.
	cfg.Ports = DefaultLauncherPorts()
	return cfg, SaveConfig(cfg)
}

func SaveConfig(cfg Config) error {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// The instance lock excludes other writers; no crash may truncate the old file.
	temp, err := os.CreateTemp(cfg.DataDir, ".runtime-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(append(data, '\n')); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), filepath.Join(cfg.DataDir, "runtime.json"))
}

func (c Config) Validate() error {
	c = c.withDefaults()
	if c.Version != 1 {
		return fmt.Errorf("unsupported runtime configuration version")
	}
	if c.BundleHash != "" {
		if digest, err := hex.DecodeString(c.BundleHash); err != nil || len(digest) != 32 {
			return fmt.Errorf("invalid runtime bundle fingerprint")
		}
	}
	for _, path := range []string{c.DataDir, c.Server, c.Python, c.ParserDir, c.PostgresBin, c.Redis, c.WebDir} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("runtime paths must be absolute")
		}
	}
	seen := map[int]bool{}
	for _, p := range []int{c.Ports.Manager, c.Ports.API, c.Ports.MCP, c.Ports.Postgres, c.Ports.Redis, c.Ports.Parser} {
		if p < 1 || p > 65535 || seen[p] {
			return fmt.Errorf("runtime ports must be distinct and between 1 and 65535")
		}
		seen[p] = true
	}
	for _, s := range []string{c.DatabasePassword, c.RedisPassword, c.MCPToken} {
		if len(s) != 64 {
			return fmt.Errorf("invalid private credential")
		}
		if _, err := hex.DecodeString(s); err != nil {
			return fmt.Errorf("invalid private credential")
		}
	}
	if c.StartupTimeout < time.Second || c.StopTimeout < time.Second {
		return fmt.Errorf("runtime timeouts must be at least one second")
	}
	for _, limit := range []struct{ value, maximum int }{
		{c.Concurrency.Tasks, 512}, {c.Concurrency.Recognition, 32},
		{c.Concurrency.Extraction, 32}, {c.Concurrency.Render, 32}, {c.Concurrency.RPCs, 128},
	} {
		if limit.value < 1 || limit.value > limit.maximum {
			return fmt.Errorf("invalid concurrency limit")
		}
	}
	switch c.ExtractionEngine {
	case "hybrid", "rules", "crf", "shadow":
	default:
		return fmt.Errorf("invalid extraction engine")
	}
	return nil
}

func (c Config) redact(value string) string {
	for _, s := range []string{c.DatabasePassword, c.RedisPassword, c.MCPToken} {
		if s != "" {
			value = strings.ReplaceAll(value, s, "[redacted]")
		}
	}
	return value
}

// Child processes inherit OS library/locale settings, but never ambient server,
// libpq, Redis or parser configuration from a developer terminal.
func childEnv(values map[string]string) []string {
	keep := map[string]bool{
		"PATH": true, "SYSTEMROOT": true, "WINDIR": true,
		"COMSPEC": true, "PATHEXT": true, "TEMP": true, "TMP": true,
		"TMPDIR": true, "HOME": true, "USERPROFILE": true,
		"LOCALAPPDATA": true, "APPDATA": true, "LANG": true, "LC_ALL": true,
		"LD_LIBRARY_PATH": true, "DYLD_LIBRARY_PATH": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if keep[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
