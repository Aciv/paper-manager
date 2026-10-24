// Package server manages the shared Go application's lifecycle. It contains no
// desktop paths or process management and is used by every server entry point.
package server

import (
	"context"
	"fmt"
	"sync"
)

type Runtime struct {
	mu     sync.Mutex
	start  bool
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	run    func(context.Context, func()) error
}

func New() *Runtime {
	return &Runtime{run: runApplication}
}

// Start waits for both HTTP/MCP listeners and background services. A runtime
// instance is single-use; create a new one after shutdown, as the desktop does
// by spawning a fresh server process.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.start {
		r.mu.Unlock()
		return fmt.Errorf("server runtime has already been started")
	}
	r.start = true
	r.done = make(chan struct{})
	operation, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	ready := make(chan struct{})
	var once sync.Once
	go func() {
		defer cancel()
		err := r.run(operation, func() { once.Do(func() { close(ready) }) })
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
	}()
	done := r.done
	r.mu.Unlock()
	select {
	case <-done:
		return r.result()
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) result() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Runtime) Wait(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return r.result()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown cancels admission and waits for HTTP, MCP, housekeeping, Asynq and
// catalog drains before parser connections, Redis clients and DB are closed.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	return r.Wait(ctx)
}
