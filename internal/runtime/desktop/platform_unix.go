//go:build !windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

type InstanceLock struct{ file *os.File }

func Acquire(root string) (*InstanceLock, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "launcher.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another Launcher owns this data directory")
	}
	return &InstanceLock{f}, nil
}
func (l *InstanceLock) Close() error { return l.file.Close() }
func prepareProcess(cmd *exec.Cmd) (func(), func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return func() {}, func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}, nil
}
func ownProcess(*exec.Cmd) error { return nil }
func ordinaryUser() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("PostgreSQL cannot run as root; " +
			"start Launcher as an ordinary user with its own writable data directory")
	}
	return nil
}
func nativePath(path string) (string, error) { return path, nil }
