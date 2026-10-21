package pdfregion

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types"
)

func TestPythonRendererRendersValidatedTemporaryPNG(t *testing.T) {
	renderer, sourceKey := newTestRenderer(t, 1<<20)
	result, err := renderer.Render(context.Background(), testRequest(sourceKey))
	if err != nil {
		t.Fatalf("render region: %v", err)
	}
	artifact, ok := result.Content.(*temporaryArtifact)
	if !ok {
		t.Fatalf("content type = %T, want temporaryArtifact", result.Content)
	}
	if result.ContentType != "image/png" || result.Width != 2 || result.Height != 1 || result.SizeBytes <= 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, err := io.ReadAll(result.Content)
	if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("read rendered PNG: bytes=%d err=%v", len(data), err)
	}
	path := artifact.path
	if err := result.Content.Close(); err != nil {
		t.Fatalf("close result: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary artifact remains after close: %v", err)
	}
}

func TestPythonRendererRejectsEscapingSourceKey(t *testing.T) {
	renderer, sourceKey := newTestRenderer(t, 1<<20)
	request := testRequest(sourceKey)
	request.SourceStorageKey = "../outside.pdf"
	if _, err := renderer.Render(context.Background(), request); !errors.Is(err, types.ErrInvalidPDFRegionRender) {
		t.Fatalf("error = %v, want ErrInvalidPDFRegionRender", err)
	}
}

func TestPythonRendererRejectsSymlinkOutsideUploadRoot(t *testing.T) {
	renderer, sourceKey := newTestRenderer(t, 1<<20)
	outside := filepath.Join(filepath.Dir(renderer.uploadDirectory), "outside.pdf")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	symlink := filepath.Join(renderer.uploadDirectory, "linked.pdf")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	request := testRequest(sourceKey)
	request.SourceStorageKey = "linked.pdf"
	if _, err := renderer.Render(context.Background(), request); !errors.Is(err, types.ErrInvalidPDFRegionRender) {
		t.Fatalf("error = %v, want ErrInvalidPDFRegionRender", err)
	}
}

func TestPythonRendererWaitingForSlotHonorsCancellation(t *testing.T) {
	renderer, sourceKey := newTestRenderer(t, 1<<20)
	renderer.slots <- struct{}{}
	defer func() { <-renderer.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := renderer.Render(ctx, testRequest(sourceKey)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
}

func TestPythonRendererRejectsOversizedOutput(t *testing.T) {
	renderer, sourceKey := newTestRenderer(t, 8)
	if _, err := renderer.Render(context.Background(), testRequest(sourceKey)); err == nil ||
		!strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("error = %v, want output size error", err)
	}
}

func newTestRenderer(t *testing.T, maxOutputBytes int64) (*PythonRenderer, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for subprocess adapter test")
	}
	root := t.TempDir()
	uploadDirectory := filepath.Join(root, "uploads")
	if err := os.Mkdir(uploadDirectory, 0o700); err != nil {
		t.Fatalf("create upload directory: %v", err)
	}
	sourceKey := "source.pdf"
	if err := os.WriteFile(filepath.Join(uploadDirectory, sourceKey), []byte("test source"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	var encoded bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 1))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	canvas.Set(1, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encode fixture PNG: %v", err)
	}
	scriptPath := filepath.Join(root, "fake_renderer.py")
	script := "import sys\nsys.stdout.buffer.write(bytes.fromhex('" + hex.EncodeToString(encoded.Bytes()) + "'))\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write fake renderer: %v", err)
	}
	renderer, err := NewPythonRenderer(PythonRendererConfig{
		PythonExecutable: python,
		ScriptPath:       scriptPath,
		UploadDirectory:  uploadDirectory,
		TempDirectory:    root,
		MaxDPI:           300,
		MaxPixels:        1_000_000,
		MaxOutputBytes:   maxOutputBytes,
		Concurrency:      1,
	})
	if err != nil {
		t.Fatalf("create renderer: %v", err)
	}
	return renderer, sourceKey
}

func testRequest(sourceKey string) types.PDFRegionRenderRequest {
	return types.PDFRegionRenderRequest{
		SourceStorageKey: sourceKey,
		PageNumber:       1,
		BBoxX0:           0,
		BBoxY0:           0,
		BBoxX1:           100,
		BBoxY1:           100,
		DPI:              144,
	}
}
