package desktop

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type child struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	err      error
	killTree func()
	release  func()
	once     sync.Once
}

func startChild(binary string, args, env []string, dir string, log io.Writer) (*child, error) {
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cleanup, kill, err := prepareProcess(cmd)
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		cleanup()
		return nil, err
	}
	if err = ownProcess(cmd); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		stdin.Close()
		cleanup()
		return nil, err
	}
	c := &child{cmd: cmd, stdin: stdin, done: make(chan struct{}), killTree: kill, release: cleanup}
	go func() { c.err = cmd.Wait(); close(c.done) }()
	return c, nil
}

func (c *child) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
func (c *child) close(ctx context.Context, graceful func()) {
	c.once.Do(func() {
		if c.alive() {
			graceful()
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			c.killTree()
			_ = c.cmd.Process.Kill()
			<-c.done
		}
		// Also kill owned descendants after a leader has exited unexpectedly.
		c.killTree()
		c.stdin.Close()
		c.release()
	})
}

func runCommand(ctx context.Context, binary string, args, env []string, dir string, log io.Writer) error {
	c, err := startChild(binary, args, env, dir, log)
	if err != nil {
		return err
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	c.close(ctx, func() {})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.err != nil {
		return fmt.Errorf("%s exited unsuccessfully; see service log", filepath.Base(binary))
	}
	return nil
}
