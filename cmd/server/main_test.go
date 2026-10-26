package main

import (
	"io"
	"testing"
	"time"
)

// TestParentShutdownPipe verifies that a standalone server ignores stdin while a child
// server watches the parent pipe and requests shutdown only after parent EOF.
func TestParentShutdownPipe(t *testing.T) {
	if parentShutdown(false, nil) != nil {
		t.Fatal("standalone server unexpectedly watches stdin")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	done := parentShutdown(true, reader)
	select {
	case <-done:
		t.Fatal("server stopped before parent EOF")
	default:
	}
	_ = writer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parent EOF did not request shutdown")
	}
}
