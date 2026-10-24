package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"paperMaster/internal/types/interfaces"
	"paperMaster/internal/utils"

	"github.com/google/uuid"
)

func TestLocalStorageSaveAndOpen(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	stored, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "../notes.json",
		Content:  strings.NewReader(`{"title":"paper"}`),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if stored.Filename != "notes.json" || stored.ContentType != "application/json" || stored.Size == 0 {
		t.Fatalf("unexpected stored file: %+v", stored)
	}
	wantHash := sha256.Sum256([]byte(`{"title":"paper"}`))
	if stored.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("stored SHA256 = %q, want %x", stored.SHA256, wantHash)
	}

	opened, err := storage.Open(context.Background(), stored.Key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer opened.Close()
	content, err := io.ReadAll(opened)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, want := string(content), `{"title":"paper"}`; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	ranged, err := storage.OpenRange(context.Background(), stored.Key, 2, 5)
	if err != nil {
		t.Fatalf("open range: %v", err)
	}
	defer ranged.Close()
	rangeContent, err := io.ReadAll(ranged)
	if err != nil || string(rangeContent) != `title` {
		t.Fatalf("range content = %q, error=%v", rangeContent, err)
	}
}

func TestLocalStorageRejectsInvalidUploads(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		content  string
		maxSize  int64
		wantErr  error
	}{
		{name: "unsupported extension", filename: "script.exe", content: "data", maxSize: 10, wantErr: ErrUnsupportedFileType},
		{name: "invalid PDF", filename: "paper.pdf", content: "not a PDF", maxSize: 10, wantErr: ErrInvalidPDF},
		{name: "invalid modern office file", filename: "paper.docx", content: "not a zip", maxSize: 20, wantErr: ErrInvalidOfficeFile},
		{name: "invalid legacy office file", filename: "paper.xls", content: "not a compound file", maxSize: 30, wantErr: ErrInvalidOfficeFile},
		{name: "too large", filename: "notes.txt", content: "12345", maxSize: 4, wantErr: ErrFileTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage, err := NewLocalStorage(t.TempDir(), test.maxSize)
			if err != nil {
				t.Fatalf("create storage: %v", err)
			}
			_, err = storage.Save(context.Background(), interfaces.FileUpload{
				Filename: test.filename,
				Content:  strings.NewReader(test.content),
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestLocalStorageSaveBytesAndCopy(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	source, err := storage.SaveBytes(context.Background(), "notes.txt", []byte("original"))
	if err != nil {
		t.Fatalf("save bytes: %v", err)
	}
	copy, err := storage.Copy(context.Background(), source)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copy.Key == source.Key || copy.Filename != source.Filename {
		t.Fatalf("unexpected copy: source=%+v copy=%+v", source, copy)
	}
	if err := storage.Delete(context.Background(), source.Key); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	reader, err := storage.Open(context.Background(), copy.Key)
	if err != nil {
		t.Fatalf("open copied file: %v", err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if got, want := string(content), "original"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestLocalStorageSaveFromURLStreamsRedirectedPDF(t *testing.T) {
	allowLocalHTTPServer(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/paper.pdf", http.StatusFound)
			return
		}
		writer.Header().Set("Content-Type", "application/pdf")
		_, _ = writer.Write([]byte("%PDF-1.7\nremote"))
	}))
	defer server.Close()
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := storage.SaveFromURL(context.Background(), interfaces.SaveFromURLRequest{
		URL: server.URL + "/start", Filename: "remote.pdf", Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatalf("SaveFromURL() error = %v", err)
	}
	if !strings.HasSuffix(result.FinalURL, "/paper.pdf") || result.File.SHA256 == "" || result.File.Size == 0 {
		t.Fatalf("result = %#v", result)
	}
}

type recordingDownloadLimiter struct {
	hosts []string
}

func (limiter *recordingDownloadLimiter) Acquire(_ context.Context, host string) (func(), error) {
	limiter.hosts = append(limiter.hosts, host)
	return func() {}, nil
}

func (*recordingDownloadLimiter) Cooldown(context.Context, string, time.Duration) error {
	return nil
}

func TestLocalStorageSaveFromURLLimitsEveryRedirectRequest(t *testing.T) {
	allowLocalHTTPServer(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(writer, request, "/paper.pdf", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("%PDF-1.7\nremote"))
	}))
	defer server.Close()
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	limiter := &recordingDownloadLimiter{}
	storage.ConfigureRemoteClient(time.Second, 3, limiter)
	if _, err := storage.SaveFromURL(context.Background(), interfaces.SaveFromURLRequest{
		URL: server.URL + "/start", Filename: "paper.pdf",
	}); err != nil {
		t.Fatalf("SaveFromURL() error = %v", err)
	}
	if len(limiter.hosts) != 2 || limiter.hosts[0] != "127.0.0.1" || limiter.hosts[1] != "127.0.0.1" {
		t.Fatalf("limited hosts = %#v, want one lease per redirect request", limiter.hosts)
	}
}

func TestLocalStorageSaveFromURLRejectsOversizeAndInvalidPDF(t *testing.T) {
	allowLocalHTTPServer(t)
	for _, test := range []struct {
		name string
		body string
		max  int64
		want error
	}{
		{name: "oversize", body: "%PDF-1.7\ncontent larger than limit", max: 8, want: ErrFileTooLarge},
		{name: "invalid header", body: "<html>not a pdf</html>", max: 1024, want: ErrInvalidPDF},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/pdf")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			storage, err := NewLocalStorage(t.TempDir(), test.max)
			if err != nil {
				t.Fatal(err)
			}
			_, err = storage.SaveFromURL(context.Background(), interfaces.SaveFromURLRequest{URL: server.URL + "/paper.pdf", Filename: "paper.pdf"})
			if !errors.Is(err, test.want) {
				t.Fatalf("SaveFromURL() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLocalStorageSaveFromURLRevalidatesRedirectTarget(t *testing.T) {
	allowLocalHTTPServer(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer server.Close()
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.SaveFromURL(context.Background(), interfaces.SaveFromURLRequest{URL: server.URL + "/paper.pdf", Filename: "paper.pdf"})
	if err == nil || !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("SaveFromURL() error = %v, want blocked redirect", err)
	}
}

func allowLocalHTTPServer(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

func TestLocalStoragePlacesCategorizedFilesUnderTypeDirectory(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	stored, err := storage.Save(context.Background(), interfaces.FileUpload{
		Filename: "paper.pdf", Content: strings.NewReader("%PDF-1.7\n"), Category: interfaces.StorageCategoryPDF,
	})
	if err != nil {
		t.Fatalf("save categorized file: %v", err)
	}
	if !strings.HasPrefix(stored.Key, "pdf/") {
		t.Fatalf("storage key = %q, want pdf/ prefix", stored.Key)
	}
	reader, err := storage.Open(context.Background(), stored.Key)
	if err != nil {
		t.Fatalf("open categorized file: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stored file: %v", err)
	}
}

func TestLocalStorageReadTextRejectsInvalidUTF8(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	key := "notes/" + uuid.NewString() + "/" + uuid.NewString() + ".md"
	if _, err := storage.replaceObject(context.Background(), key, "section.md", "text/plain",
		bytes.NewReader([]byte{0xff}), 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ReadText(context.Background(), key, 1024); err == nil {
		t.Fatal("invalid UTF-8 text was accepted")
	}
}

func TestLocalStorageReplaceTextPublishesAndAtomicallyReplacesObject(t *testing.T) {
	storage, err := NewLocalStorage(t.TempDir(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	key := "notes/6a046cb1-a8b9-41b8-93e5-613e74493364/f6640d4e-f214-4293-b07b-e3048442b6f6.md"
	for _, content := range []string{"# First\nold", "# First\nnew content"} {
		stored, writeErr := storage.ReplaceText(context.Background(), key, content, 1024)
		if writeErr != nil || stored.Key != key || stored.Size != int64(len(content)) {
			t.Fatalf("stored=%#v error=%v", stored, writeErr)
		}
	}
	content, err := storage.ReadText(context.Background(), key, 1024)
	if err != nil || content != "# First\nnew content" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	if _, err := storage.ReplaceText(context.Background(), "../outside.md", "x", 1024); err == nil {
		t.Fatal("path traversal key was accepted")
	}
}
