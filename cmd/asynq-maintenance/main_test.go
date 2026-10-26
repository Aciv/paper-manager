package main

import "testing"

// TestLegacyTaskTypesIncludesRetiredCatalogNames verifies that maintenance targets the
// retired catalog task types for purge while leaving all active pipeline task types untouched.
func TestLegacyTaskTypesIncludesRetiredCatalogNames(t *testing.T) {
	for _, taskType := range []string{
		"paper:catalog_resolve",
		"paper:reference_dispatch",
		"paper:catalog_result",
	} {
		if _, exists := legacyTaskTypes[taskType]; !exists {
			t.Fatalf("legacy task type %q is not registered for maintenance", taskType)
		}
	}
	for _, taskType := range []string{
		"document:recognize",
		"paper:metadata_extract",
		"paper:source_query_enqueue",
		"paper:reference_query_enqueue",
		"paper:query_apply",
		"document:remote_import",
	} {
		if _, exists := legacyTaskTypes[taskType]; exists {
			t.Fatalf("active task type %q must not be purged", taskType)
		}
	}
}
