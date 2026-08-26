// Package file provides durable file storage implementations.
package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"paperMaster/internal/config"
	"paperMaster/internal/logger"
	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
)

const DefaultMaxUploadSize = config.DefaultMaxUploadSize

var (
	ErrEmptyFile           = errors.New("uploaded file is empty")
	ErrFileTooLarge        = errors.New("uploaded file exceeds the size limit")
	ErrUnsupportedFileType = errors.New("unsupported file type")
	ErrInvalidPDF          = errors.New("invalid PDF file")
	ErrInvalidOfficeFile   = errors.New("invalid office file")
	ErrRemoteHTTPStatus    = errors.New("remote file server returned an unsuccessful status")
)

var supportedTypes = map[string]string{
	".txt":      "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".json":     "application/json",
	".csv":      "text/csv",
	".png":      "image/png",
	".pdf":      "application/pdf",
	".doc":      "application/msword",
	".docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":      "application/vnd.ms-excel",
	".xlsx":     "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":      "application/vnd.ms-powerpoint",
	".pptx":     "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// LocalStorage stores uploads beneath BaseDir using opaque keys. Its temporary-file
// and rename sequence prevents readers from observing partially written files.
type LocalStorage struct {
	baseDir    string
	maxSize    int64
	httpClient *http.Client
}

func NewLocalStorage(baseDir string, maxSize int64) (*LocalStorage, error) {
	if strings.TrimSpace(baseDir) == "" {
		err := fmt.Errorf("upload directory is required")
		logger.Error(context.Background(), "create local file storage failed", "error", err)
		return nil, err
	}
	if maxSize <= 0 {
		maxSize = DefaultMaxUploadSize
	}
	absBase, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		logger.Error(context.Background(), "resolve upload directory failed", "directory", baseDir, "error", err)
		return nil, fmt.Errorf("resolve upload directory: %w", err)
	}
	storage := &LocalStorage{baseDir: absBase, maxSize: maxSize,
		httpClient: utils.NewSSRFSafeHTTPClient(utils.DefaultSSRFSafeHTTPClientConfig())}
	if err := storage.CheckConnectivity(context.Background()); err != nil {
		logger.Error(context.Background(), "check local file storage failed", "directory", absBase, "error", err)
		return nil, err
	}
	return storage, nil
}

// ConfigureRemoteClient applies runtime URL-download limits while retaining
// NewLocalStorage's compact constructor for focused storage tests.
func (s *LocalStorage) ConfigureRemoteClient(
	timeout time.Duration,
	maxRedirects int,
	downloads interfaces.RemoteDownloadLimiter,
) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxRedirects <= 0 {
		maxRedirects = 5
	}
	client := utils.NewSSRFSafeHTTPClient(utils.SSRFSafeHTTPClientConfig{
		Timeout: timeout, MaxRedirects: maxRedirects,
	})
	if downloads != nil {
		client.Transport = &downloadLimitedTransport{
			base: client.Transport, downloads: downloads,
		}
	}
	s.httpClient = client
}

// downloadLimitedTransport acquires the limiter for the host of every actual
// HTTP request. Because net/http executes redirects as separate RoundTrip
// calls, a cross-host redirect is constrained by its final host rather than
// only by the user-supplied URL's host.
type downloadLimitedTransport struct {
	base      http.RoundTripper
	downloads interfaces.RemoteDownloadLimiter
}

func (transport *downloadLimitedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	release, err := transport.downloads.Acquire(request.Context(), request.URL.Hostname())
	if err != nil {
		return nil, err
	}
	// actually get from url
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		release()
		return nil, err
	}
	response.Body = &releaseOnClose{ReadCloser: response.Body, release: release}
	return response, nil
}

type releaseOnClose struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (body *releaseOnClose) Close() error {
	err := body.ReadCloser.Close()
	body.once.Do(body.release)
	return err
}

func (s *LocalStorage) CheckConnectivity(_ context.Context) error {
	if err := os.MkdirAll(s.baseDir, 0o750); err != nil {
		logger.Error(context.Background(), "create upload directory failed", "directory", s.baseDir, "error", err)
		return fmt.Errorf("create upload directory: %w", err)
	}
	info, err := os.Stat(s.baseDir)
	if err != nil {
		logger.Error(context.Background(), "stat upload directory failed", "directory", s.baseDir, "error", err)
		return fmt.Errorf("stat upload directory: %w", err)
	}
	if !info.IsDir() {
		err := fmt.Errorf("upload path is not a directory")
		logger.Error(context.Background(), "validate upload directory failed", "directory", s.baseDir, "error", err)
		return err
	}
	return nil
}

func (s *LocalStorage) Save(ctx context.Context, upload interfaces.FileUpload) (interfaces.StoredFile, error) {
	if upload.Content == nil {
		err := fmt.Errorf("file content is required")
		logger.Error(ctx, "uploaded file content is missing", "error", err)
		return interfaces.StoredFile{}, err
	}
	filename, err := utils.SafeFileName(upload.Filename)
	if err != nil {
		logger.Error(ctx, "validate uploaded file name failed", "filename", upload.Filename, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("invalid filename: %w", err)
	}
	extension := strings.ToLower(filepath.Ext(filename))
	contentType, ok := supportedTypes[extension]
	if !ok {
		logger.Error(ctx, "uploaded file type is unsupported", "filename", filename, "extension", extension)
		return interfaces.StoredFile{}, fmt.Errorf("%w: %s", ErrUnsupportedFileType, extension)
	}
	directory, err := s.directoryForCategory(upload.Category)
	if err != nil {
		logger.Error(ctx, "resolve storage category failed", "category", upload.Category, "error", err)
		return interfaces.StoredFile{}, err
	}

	// for check, but stream can not back, so multi Reader
	sample, err := io.ReadAll(io.LimitReader(upload.Content, 512))
	if err != nil {
		logger.Error(ctx, "read uploaded file header failed", "filename", filename, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("read file header: %w", err)
	}
	if len(sample) == 0 {
		logger.Error(ctx, "uploaded file is empty", "filename", filename)
		return interfaces.StoredFile{}, ErrEmptyFile
	}
	if extension == ".pdf" && !bytes.HasPrefix(sample, []byte("%PDF-")) {
		logger.Error(ctx, "uploaded PDF header is invalid", "filename", filename)
		return interfaces.StoredFile{}, ErrInvalidPDF
	}
	if isZIPOfficeExtension(extension) && !bytes.HasPrefix(sample, []byte("PK\x03\x04")) {
		logger.Error(ctx, "uploaded Office ZIP header is invalid", "filename", filename)
		return interfaces.StoredFile{}, ErrInvalidOfficeFile
	}
	if isLegacyOfficeExtension(extension) && !bytes.HasPrefix(sample, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		logger.Error(ctx, "uploaded legacy Office header is invalid", "filename", filename)
		return interfaces.StoredFile{}, ErrInvalidOfficeFile
	}
	// The extension controls the allow-list because Office containers are commonly
	// reported as application/zip or octet-stream by MIME sniffers.

	temporary, err := os.CreateTemp(s.baseDir, ".upload-*")
	if err != nil {
		logger.Error(ctx, "create temporary upload file failed", "filename", filename, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("create temporary upload: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	limited := io.LimitReader(contextReader{
		ctx:    ctx,
		reader: io.MultiReader(bytes.NewReader(sample), upload.Content),
	}, s.maxSize+1)
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(temporary, hash), limited)
	if syncErr := temporary.Sync(); syncErr != nil && copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := temporary.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		logger.Error(ctx, "write uploaded file failed", "filename", filename, "error", copyErr)
		return interfaces.StoredFile{}, fmt.Errorf("write upload: %w", copyErr)
	}
	if size > s.maxSize {
		logger.Error(ctx, "uploaded file exceeds size limit", "filename", filename, "size", size, "limit", s.maxSize)
		return interfaces.StoredFile{}, ErrFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		logger.Error(ctx, "upload request context cancelled", "filename", filename, "error", err)
		return interfaces.StoredFile{}, err
	}

	if err := os.MkdirAll(directory, 0o750); err != nil {
		logger.Error(ctx, "create storage category directory failed", "category", upload.Category, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("create storage category directory: %w", err)
	}
	key := filepath.ToSlash(filepath.Join(string(upload.Category), uuid.NewString()+extension))
	if upload.Category == "" {
		key = uuid.NewString() + extension
	}
	destination := filepath.Join(s.baseDir, filepath.FromSlash(key))
	if err := os.Rename(temporaryPath, destination); err != nil {
		logger.Error(ctx, "finalize uploaded file failed", "filename", filename, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("finalize upload: %w", err)
	}
	// The parser runs as a separate unprivileged container. The upload
	// directory itself is private, but completed files must be readable by
	// that parser account after the atomic rename.
	if err := os.Chmod(destination, 0o644); err != nil {
		logger.Error(ctx, "set stored file permissions failed", "storage_key", key, "error", err)
		_ = os.Remove(destination)
		return interfaces.StoredFile{}, fmt.Errorf("set upload permissions: %w", err)
	}
	logger.Info(ctx, "uploaded file stored", "filename", filename, "storage_key", key, "size", size)
	return interfaces.StoredFile{Key: key, Filename: filename, ContentType: contentType, Size: size, SHA256: fmt.Sprintf("%x", hash.Sum(nil))}, nil
}

func isZIPOfficeExtension(extension string) bool {
	return extension == ".docx" || extension == ".xlsx" || extension == ".pptx"
}

func isLegacyOfficeExtension(extension string) bool {
	return extension == ".doc" || extension == ".xls" || extension == ".ppt"
}

// SaveMultipart adapts a multipart HTTP upload to the streaming FileStorage interface.
// The storage backend receives only a reader and remains usable by gRPC jobs and tests.
func SaveMultipart(ctx context.Context, storage interfaces.FileStorage, file *multipart.FileHeader) (interfaces.StoredFile, error) {
	if file == nil {
		return interfaces.StoredFile{}, fmt.Errorf("uploaded file is required")
	}
	input, err := file.Open()
	if err != nil {
		logger.Error(ctx, "open multipart upload failed", "filename", file.Filename, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("open uploaded file: %w", err)
	}
	defer input.Close()
	category := interfaces.StorageCategory("")
	if strings.EqualFold(filepath.Ext(file.Filename), ".pdf") {
		category = interfaces.StorageCategoryPDF
	}
	return storage.Save(ctx, interfaces.FileUpload{
		Filename:    file.Filename,
		ContentType: file.Header.Get("Content-Type"),
		Content:     input,
		Category:    category,
	})
}

// SaveBytes stores generated data such as captures or export files through the
// same validation, size-limit, and atomic-write path as uploaded documents.
func (s *LocalStorage) SaveBytes(ctx context.Context, filename string, content []byte) (interfaces.StoredFile, error) {
	return s.Save(ctx, interfaces.FileUpload{Filename: filename, Content: bytes.NewReader(content)})
}

// SaveFromURL streams one already-resolved remote PDF through the same
// header, size, hashing, temporary-file, fsync, and atomic-rename path as an
// uploaded file. The initial URL and every redirect are SSRF-validated.
func (s *LocalStorage) SaveFromURL(
	ctx context.Context, request interfaces.SaveFromURLRequest,
) (interfaces.SaveFromURLResult, error) {
	rawURL := strings.TrimSpace(request.URL)
	if err := utils.ValidateURLForSSRF(rawURL); err != nil {
		return interfaces.SaveFromURLResult{}, fmt.Errorf("validate remote file URL: %w", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return interfaces.SaveFromURLResult{}, fmt.Errorf("invalid remote file URL")
	}
	filename := strings.TrimSpace(request.Filename)
	if filename == "" {
		filename = filepath.Base(parsed.Path)
	}
	if !strings.EqualFold(filepath.Ext(filename), ".pdf") {
		filename += ".pdf"
	}
	client := s.httpClient
	if client == nil {
		client = utils.NewSSRFSafeHTTPClient(utils.DefaultSSRFSafeHTTPClientConfig())
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return interfaces.SaveFromURLResult{}, fmt.Errorf("create remote file request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/pdf,application/octet-stream;q=0.8")
	response, err := client.Do(httpRequest)
	if err != nil {
		return interfaces.SaveFromURLResult{}, fmt.Errorf("download remote PDF: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return interfaces.SaveFromURLResult{}, &RemoteHTTPError{
			StatusCode: response.StatusCode,
			RetryAfter: parseHTTPRetryAfter(response.Header.Get("Retry-After")),
			FinalURL:   response.Request.URL.String(),
		}
	}
	if response.ContentLength > s.maxSize {
		return interfaces.SaveFromURLResult{}, ErrFileTooLarge
	}
	stored, err := s.Save(ctx, interfaces.FileUpload{
		Filename: filename, ContentType: response.Header.Get("Content-Type"),
		Content: response.Body, Category: request.Category,
	})
	if err != nil {
		return interfaces.SaveFromURLResult{}, err
	}
	return interfaces.SaveFromURLResult{File: stored, FinalURL: response.Request.URL.String()}, nil
}

// RemoteHTTPError exposes bounded retry metadata without retaining or leaking
// response bodies. Deterministic 4xx responses remain terminal.
type RemoteHTTPError struct {
	StatusCode int
	RetryAfter time.Duration
	FinalURL   string
}

func (e *RemoteHTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", ErrRemoteHTTPStatus, e.StatusCode)
}
func (e *RemoteHTTPError) Unwrap() error { return ErrRemoteHTTPStatus }
func (e *RemoteHTTPError) TaskRetryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}
func (e *RemoteHTTPError) TaskRetryAfter() time.Duration { return e.RetryAfter }

func parseHTTPRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(time.Now()) {
		return time.Until(when)
	}
	return 0
}

// Copy creates an independent object. It never exposes or accepts filesystem paths.
func (s *LocalStorage) Copy(ctx context.Context, source interfaces.StoredFile) (interfaces.StoredFile, error) {
	input, err := s.Open(ctx, source.Key)
	if err != nil {
		logger.Error(ctx, "open file copy source failed", "storage_key", source.Key, "error", err)
		return interfaces.StoredFile{}, fmt.Errorf("open copy source: %w", err)
	}
	defer input.Close()

	filename := source.Filename
	if filename == "" {
		filename = source.Key
	}
	return s.Save(ctx, interfaces.FileUpload{
		Filename:    filename,
		ContentType: source.ContentType,
		Content:     input,
	})
}

func (s *LocalStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.pathForKey(key)
	if err != nil {
		logger.Error(ctx, "resolve stored file path failed", "storage_key", key, "error", err)
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		logger.Error(ctx, "open stored file failed", "storage_key", key, "error", err)
		return nil, fmt.Errorf("open stored file: %w", err)
	}
	return file, nil
}

func (s *LocalStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.pathForKey(key)
	if err != nil {
		logger.Error(ctx, "resolve stored file deletion path failed", "storage_key", key, "error", err)
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Info(ctx, "stored file already absent", "storage_key", key)
			return nil
		}
		logger.Error(ctx, "delete stored file failed", "storage_key", key, "error", err)
		return fmt.Errorf("delete stored file: %w", err)
	}
	logger.Info(ctx, "stored file deleted", "storage_key", key)
	return nil
}

func (s *LocalStorage) pathForKey(key string) (string, error) {
	key = filepath.ToSlash(strings.TrimSpace(key))
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return "", fmt.Errorf("invalid storage key")
	}
	parts := strings.Split(key, "/")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid storage key")
	}
	for _, part := range parts {
		safePart, err := utils.SafeFileName(part)
		if err != nil || safePart != part {
			return "", fmt.Errorf("invalid storage key")
		}
	}
	if len(parts) == 2 && !isStorageCategory(interfaces.StorageCategory(parts[0])) {
		return "", fmt.Errorf("invalid storage key category")
	}
	return utils.SafePathUnderBase(s.baseDir, filepath.Join(s.baseDir, filepath.FromSlash(key)))
}

func (s *LocalStorage) directoryForCategory(category interfaces.StorageCategory) (string, error) {
	if category == "" {
		return s.baseDir, nil
	}
	if !isStorageCategory(category) {
		return "", fmt.Errorf("unsupported storage category %q", category)
	}
	return utils.SafePathUnderBase(s.baseDir, filepath.Join(s.baseDir, string(category)))
}

func isStorageCategory(category interfaces.StorageCategory) bool {
	switch category {
	case interfaces.StorageCategoryPDF, interfaces.StorageCategoryMarkdown, interfaces.StorageCategoryImage:
		return true
	default:
		return false
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

var _ interfaces.FileStorage = (*LocalStorage)(nil)
