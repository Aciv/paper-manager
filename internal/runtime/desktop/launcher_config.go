package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
)

// DefaultLauncherPorts are stable user-facing addresses, never a scanning range.
func DefaultLauncherPorts() Ports {
	return Ports{Manager: 28450, API: 28451, MCP: 28452, Postgres: 28453, Redis: 28454, Parser: 28455}
}

type launcherConfiguration struct {
	Ports Ports `json:"ports"`
}

// ApplyLauncherConfiguration makes the root file authoritative. If absent,
// publish the current ports so existing installations retain their addresses.
func ApplyLauncherConfiguration(cfg Config, path string) (Config, error) {
	cfg.LauncherFile = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, saveLauncherConfiguration(cfg)
	}
	if err != nil {
		return cfg, fmt.Errorf("无法读取端口配置 %s: %w", path, err)
	}
	var settings launcherConfiguration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return cfg, fmt.Errorf("请修正端口配置 %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("端口配置 %s 必须只包含一个 JSON 对象", path)
	}
	cfg.Ports = settings.Ports
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("端口配置 %s 无效：六个端口必须不同且在 1–65535 之间: %w", path, err)
	}
	return cfg, nil
}

func saveLauncherConfiguration(cfg Config) error {
	if cfg.LauncherFile == "" {
		return nil
	}
	return writeRecord(cfg.LauncherFile, launcherConfiguration{Ports: cfg.Ports})
}

// ListenLauncher checks every configured port before starting any service and
// keeps the actual management listener. Service ports are checked again by the
// runtime immediately before it starts the child processes.
func ListenLauncher(cfg Config) (net.Listener, error) {
	var manager net.Listener
	var issues []string
	var held []net.Listener
	for _, service := range []struct {
		name string
		port int
	}{
		{"管理页面", cfg.Ports.Manager}, {"论文服务", cfg.Ports.API}, {"MCP", cfg.Ports.MCP},
		{"PostgreSQL", cfg.Ports.Postgres}, {"Redis", cfg.Ports.Redis}, {"PDF 解析", cfg.Ports.Parser},
	} {
		listener, err := net.Listen("tcp", address(service.port))
		if err != nil {
			issues = append(issues, portFailure(service.name, service.port, err))
			continue
		}
		if service.port == cfg.Ports.Manager {
			manager = listener
		} else {
			held = append(held, listener)
		}
	}
	for _, listener := range held {
		_ = listener.Close()
	}
	if len(issues) == 0 {
		return manager, nil
	}
	if manager != nil {
		_ = manager.Close()
	}
	return nil, fmt.Errorf("无法启动，以下端口不可用：\n%s\n请修改 %s 中的端口后重新启动；不会自动更换端口。",
		strings.Join(issues, "\n"), cfg.LauncherFile)
}

func portFailure(name string, port int, err error) string {
	reason := "无法绑定"
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) ||
		(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10013))) {
		reason = "系统禁止使用（可能处于系统保留范围）"
	} else if errors.Is(err, syscall.EADDRINUSE) ||
		(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10048))) {
		reason = "已被其他程序占用"
	}
	return fmt.Sprintf("%s %d：%s (%v)", name, port, reason, err)
}
