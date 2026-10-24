package types

import (
	"github.com/google/uuid"
	"testing"
)

func TestRecognitionStorageKeyInputValidation(t *testing.T) {
	for _, key := range []string{"pdf/source.pdf", "pdf/论文 source.pdf"} {
		r := RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "source.pdf", InputMode: RecognitionInputStorageKey, SourceStorageKey: key, SizeBytes: 4}
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		r.Content = []byte("%PDF")
		if err := r.Validate(); err == nil {
			t.Fatal("mixed bytes and key accepted")
		}
		r.Content = nil
		r.InputMode = RecognitionInputBytes
		r.SizeBytes = 0
		if err := r.Validate(); err == nil {
			t.Fatal("bytes mode accepted a key")
		}
	}
	for _, key := range []string{"", "/source.pdf", "../source.pdf", "pdf/../source.pdf", "pdf//source.pdf", "pdf/./source.pdf", `C:\source.pdf`, `pdf\source.pdf`, "pdf/\x00.pdf"} {
		r := RecognitionRequest{DocumentID: uuid.New(), OriginalFilename: "source.pdf", InputMode: RecognitionInputStorageKey, SourceStorageKey: key}
		if err := r.Validate(); err == nil {
			t.Fatalf("invalid key %q accepted", key)
		}
	}
}
