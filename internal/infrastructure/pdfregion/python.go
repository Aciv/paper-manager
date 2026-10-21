// Package pdfregion contains framework-level adapters for rendering bounded
// regions of stored PDFs. It is intentionally not wired into the application,
// HTTP router, frontend, dependency injection, or Docker runtime yet.
package pdfregion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

const maxRendererDiagnosticBytes = 8 << 10

// PythonRendererConfig defines bounded process and artifact limits. Runtime
// packaging is deliberately deferred: callers must explicitly select the
// Python executable, helper script, upload root, and writable temporary root.
type PythonRendererConfig struct {
	PythonExecutable string
	ScriptPath       string
	UploadDirectory  string
	TempDirectory    string
	MaxDPI           int
	MaxPixels        int64
	MaxOutputBytes   int64
	Concurrency      int
}

// PythonRenderer starts one short-lived, minimal PyMuPDF process per cache
// miss. It never starts or contacts the long-lived parser gRPC service.
type PythonRenderer struct {
	pythonExecutable string
	scriptPath       string
	uploadDirectory  string
	tempDirectory    string
	maxDPI           int
	maxPixels        int64
	maxOutputBytes   int64
	slots            chan struct{}
}

func NewPythonRenderer(config PythonRendererConfig) (*PythonRenderer, error) {
	pythonExecutable := strings.TrimSpace(config.PythonExecutable)
	if pythonExecutable == "" {
		return nil, errors.New("PDF region renderer Python executable is required")
	}
	resolvedExecutable, err := exec.LookPath(pythonExecutable)
	if err != nil {
		return nil, fmt.Errorf("resolve PDF region renderer Python executable: %w", err)
	}
	scriptPath, err := existingRegularFile(config.ScriptPath, "PDF region renderer script")
	if err != nil {
		return nil, err
	}
	uploadDirectory, err := existingDirectory(config.UploadDirectory, "PDF upload directory")
	if err != nil {
		return nil, err
	}
	tempDirectory := strings.TrimSpace(config.TempDirectory)
	if tempDirectory == "" {
		tempDirectory = os.TempDir()
	}
	tempDirectory, err = existingDirectory(tempDirectory, "PDF region renderer temporary directory")
	if err != nil {
		return nil, err
	}
	if config.MaxDPI <= 0 || config.MaxPixels <= 0 || config.MaxOutputBytes <= 0 || config.Concurrency <= 0 {
		return nil, errors.New("PDF region renderer limits and concurrency must be positive")
	}
	return &PythonRenderer{
		pythonExecutable: resolvedExecutable,
		scriptPath:       scriptPath,
		uploadDirectory:  uploadDirectory,
		tempDirectory:    tempDirectory,
		maxDPI:           config.MaxDPI,
		maxPixels:        config.MaxPixels,
		maxOutputBytes:   config.MaxOutputBytes,
		slots:            make(chan struct{}, config.Concurrency),
	}, nil
}

// Render invokes the configured helper with fixed argv, validates its bounded
// PNG output, and returns a self-deleting temporary artifact. It does not save
// a cache entry or mutate the source PDF.
func (renderer *PythonRenderer) Render(
	ctx context.Context,
	request types.PDFRegionRenderRequest,
) (*types.RenderedPDFRegion, error) {
	if renderer == nil {
		return nil, errors.New("PDF region renderer is not configured")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.DPI > renderer.maxDPI {
		return nil, fmt.Errorf("%w: DPI exceeds renderer limit", types.ErrInvalidPDFRegionRender)
	}
	sourcePath, err := renderer.resolveSource(request.SourceStorageKey)
	if err != nil {
		return nil, err
	}
	select {
	case renderer.slots <- struct{}{}:
		defer func() { <-renderer.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	temporary, err := os.CreateTemp(renderer.tempDirectory, "pdf-region-*.png")
	if err != nil {
		return nil, fmt.Errorf("create PDF region temporary artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	arguments := []string{
		renderer.scriptPath,
		"--source", sourcePath,
		"--page-number", strconv.Itoa(request.PageNumber),
		"--bbox-x0", strconv.FormatFloat(request.BBoxX0, 'g', -1, 64),
		"--bbox-y0", strconv.FormatFloat(request.BBoxY0, 'g', -1, 64),
		"--bbox-x1", strconv.FormatFloat(request.BBoxX1, 'g', -1, 64),
		"--bbox-y1", strconv.FormatFloat(request.BBoxY1, 'g', -1, 64),
		"--dpi", strconv.Itoa(request.DPI),
		"--max-dpi", strconv.Itoa(renderer.maxDPI),
		"--max-pixels", strconv.FormatInt(renderer.maxPixels, 10),
	}
	// ********************************************
	// use stdout to get
	// why not use file writer direct?
	// ********************************************
	command := exec.CommandContext(ctx, renderer.pythonExecutable, arguments...)
	var diagnostics boundedBuffer
	diagnostics.limit = maxRendererDiagnosticBytes
	command.Stderr = &diagnostics
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open PDF region renderer output: %w", err)
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start PDF region renderer: %w", err)
	}
	written, copyErr := io.Copy(temporary, io.LimitReader(stdout, renderer.maxOutputBytes+1))
	if written > renderer.maxOutputBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, errors.New("PDF region renderer output exceeds size limit")
	}
	if copyErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("copy PDF region renderer output: %w", copyErr)
	}
	if err := command.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		message := strings.TrimSpace(diagnostics.String())
		if message == "" {
			return nil, fmt.Errorf("PDF region renderer failed: %w", err)
		}
		return nil, fmt.Errorf("PDF region renderer failed: %s: %w", message, err)
	}
	if written == 0 {
		return nil, errors.New("PDF region renderer returned an empty image")
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind PDF region temporary artifact: %w", err)
	}
	imageConfig, err := png.DecodeConfig(temporary)
	if err != nil {
		return nil, fmt.Errorf("validate PDF region PNG: %w", err)
	}
	if imageConfig.Width <= 0 || imageConfig.Height <= 0 ||
		int64(imageConfig.Width)*int64(imageConfig.Height) > renderer.maxPixels {
		return nil, errors.New("PDF region renderer returned invalid image dimensions")
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind validated PDF region artifact: %w", err)
	}
	keepTemporary = true
	return &types.RenderedPDFRegion{
		Content:     &temporaryArtifact{File: temporary, path: temporaryPath},
		ContentType: "image/png",
		Filename: fmt.Sprintf(
			"pdf-region-page-%d-%ddpi.png",
			request.PageNumber,
			request.DPI,
		),
		SizeBytes: written,
		Width:     imageConfig.Width,
		Height:    imageConfig.Height,
	}, nil
}

func (renderer *PythonRenderer) resolveSource(storageKey string) (string, error) {
	storageKey = strings.TrimSpace(storageKey)
	if filepath.IsAbs(storageKey) {
		return "", fmt.Errorf("%w: source storage key must be relative", types.ErrInvalidPDFRegionRender)
	}
	cleanKey := filepath.Clean(filepath.FromSlash(storageKey))
	if cleanKey == "." || cleanKey == ".." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: source storage key escapes upload directory", types.ErrInvalidPDFRegionRender)
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(renderer.uploadDirectory, cleanKey))
	if err != nil {
		return "", fmt.Errorf("resolve stored PDF source: %w", err)
	}
	if !pathWithin(renderer.uploadDirectory, candidate) {
		return "", fmt.Errorf("%w: resolved source escapes upload directory", types.ErrInvalidPDFRegionRender)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("inspect stored PDF source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("stored PDF source is not a regular file")
	}
	return candidate, nil
}

func existingRegularFile(path, label string) (string, error) {
	resolved, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must be a regular file", label)
	}
	return resolved, nil
}

func existingDirectory(path, label string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve %s symlinks: %w", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s must be a directory", label)
	}
	return resolved, nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := buffer.limit - buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.Buffer.Write(data)
	}
	return originalLength, nil
}

type temporaryArtifact struct {
	*os.File
	path string
	once sync.Once
	err  error
}

func (artifact *temporaryArtifact) Close() error {
	artifact.once.Do(func() {
		artifact.err = errors.Join(artifact.File.Close(), os.Remove(artifact.path))
	})
	return artifact.err
}

var _ interfaces.PDFRegionRenderer = (*PythonRenderer)(nil)
