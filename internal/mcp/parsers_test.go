package mcp

import (
	"strings"
	"testing"
)

func TestParseSectionMeta(t *testing.T) {
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"对应原文 §3\"}\n-->\n\n正文"
	description, diagnostics := parseSectionMeta(markdown)
	if len(diagnostics) != 0 || description != "对应原文 §3" {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

func TestParseSectionMetaIgnoresBodyMarker(t *testing.T) {
	markdown := "# 方法\n\n正文\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"late\"}\n-->"
	description, diagnostics := parseSectionMeta(markdown)
	if description != "" || len(diagnostics) != 0 {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

func TestParseSectionMetaReportsMalformedJSON(t *testing.T) {
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{not-json}\n-->"
	description, diagnostics := parseSectionMeta(markdown)
	if description != "" || len(diagnostics) == 0 {
		t.Fatalf("parseSectionMeta() = %q, %v", description, diagnostics)
	}
}

func TestParseSectionMetaBoundsDescription(t *testing.T) {
	description := strings.Repeat("x", sectionDescriptionMaxRunes+1)
	markdown := "# 方法\n\n<!-- papers-manager:section-meta v=1\n{\"description\":\"" + description + "\"}\n-->"
	parsed, diagnostics := parseSectionMeta(markdown)
	if len([]rune(parsed)) != sectionDescriptionMaxRunes || len(diagnostics) == 0 {
		t.Fatalf("parseSectionMeta() length=%d diagnostics=%v", len([]rune(parsed)), diagnostics)
	}
}

func TestParseEvidenceHref(t *testing.T) {
	target, diagnostics := parseEvidenceHref("papers-manager://self/evidence?v=1&page=3&rect=0.1,0.2,0.8,0.9&block=00000000-0000-4000-8000-000000000000&section=3")
	if len(diagnostics) != 0 || target.Page != 3 || len(target.Rects) != 1 || target.Section != "3" {
		t.Fatalf("parseEvidenceHref() = %+v, %v", target, diagnostics)
	}
}

func TestParseEvidenceHrefRejectsMalformedTargets(t *testing.T) {
	for _, href := range []string{
		"https://example.com/evidence?v=1&page=1",
		"papers-manager://self/evidence?v=2&page=1",
		"papers-manager://self/evidence?v=1&page=0",
		"papers-manager://document/not-a-uuid/evidence?v=1&page=1",
		"papers-manager://self/evidence?v=1&page=1&rect=0.5,0.5,0.1,0.2",
	} {
		_, diagnostics := parseEvidenceHref(href)
		if len(diagnostics) == 0 {
			t.Errorf("parseEvidenceHref(%q) accepted malformed input", href)
		}
	}
}
