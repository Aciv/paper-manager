package types

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDocumentImportOptionsDecode(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		valid bool
	}{
		{"defaults", `{}`, true},
		{"date_only", `{"metadata":{"publishedAt":"2024-01-02"}}`, true},
		{"invalid_date", `{"metadata":{"publishedAt":"2024-02-30"}}`, false},
		{"skip", `{"processing":{"source_lookup":"skip"}}`, true},
		{"metadata", `{"metadata":{"title":"Paper","authors":[{"name":"A"}],"doi":"10.1234/example"}}`, true},
		{"explicit_clear", `{"metadata":{"title":"","authors":[],"tags":[]}}`, true},
		{"unknown_option", `{"trusted":true}`, false},
		{"unsupported_stage", `{"processing":{"markdown":"skip"}}`, false},
		{"invalid_mode", `{"processing":{"source_lookup":"disabled"}}`, false},
		{"wrong_mode_type", `{"processing":{"source_lookup":true}}`, false},
		{"null_processing", `{"processing":null}`, false},
		{"unknown_metadata", `{"metadata":{"publisher":"X"}}`, false},
		{"client_provenance", `{"metadata":{"metadataSource":"catalog"}}`, false},
		{"trusted_metadata", `{"metadata":{"trusted":true}}`, false},
		{"invalid_duplicate_policy", `{"duplicate_metadata":"overwrite_all"}`, false},
		{"multiple_objects", `{} {}`, false},
		{
			"oversized_metadata",
			`{"metadata":{"abstract":"` + strings.Repeat("x", MaxDocumentImportMetadataBytes) + `"}}`,
			false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var options DocumentImportOptions
			err := json.Unmarshal([]byte(test.input), &options)
			if (err == nil) != test.valid {
				t.Fatalf("decode error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestDocumentImportDefaultsAndEnums(t *testing.T) {
	mode, err := SourceLookupMode("").Normalized()
	if err != nil || mode != SourceLookupAuto {
		t.Fatalf("default mode = %q, %v", mode, err)
	}
	policy, err := DuplicateMetadataPolicy("").Normalized()
	if err != nil || policy != DuplicateMetadataFillMissing {
		t.Fatalf("default duplicate policy = %q, %v", policy, err)
	}
	if _, err := SourceLookupMode("manual").Normalized(); !errors.Is(err, ErrInvalidDocumentImport) {
		t.Fatalf("invalid mode error = %v", err)
	}
	for _, policy := range []DuplicateMetadataPolicy{
		DuplicateMetadataNone, DuplicateMetadataFillMissing, DuplicateMetadataReplace,
	} {
		if actual, err := policy.Normalized(); err != nil || actual != policy {
			t.Fatalf("policy = %q, %v", actual, err)
		}
	}
}
