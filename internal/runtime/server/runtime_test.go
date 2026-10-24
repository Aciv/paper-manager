package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeLifecycle(t *testing.T) {
	stopped := make(chan struct{})
	app := &Runtime{run: func(ctx context.Context, ready func()) error {
		ready()
		<-ctx.Done()
		close(stopped)
		return nil
	}}
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err == nil {
		t.Fatal("duplicate start accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Shutdown returned before cleanup")
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeStartupFailureAndShutdownDeadline(t *testing.T) {
	want := errors.New("startup failed")
	failed := &Runtime{run: func(context.Context, func()) error { return want }}
	if err := failed.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("startup error not propagated: %v", err)
	}
	finish := make(chan struct{})
	blocked := &Runtime{run: func(ctx context.Context, ready func()) error {
		ready()
		<-finish
		return nil
	}}
	if err := blocked.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := blocked.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown deadline ignored: %v", err)
	}
	close(finish)
	if err := blocked.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
