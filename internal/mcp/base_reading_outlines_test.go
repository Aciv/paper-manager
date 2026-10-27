package mcp

import (
	"context"
	"strings"
	"testing"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"
)

type outlineBatchBaseService struct {
	interfaces.DocumentBaseService
	calls  int
	query  types.BaseReadingOutlinesQuery
	status string
}

func (s *outlineBatchBaseService) ListBaseReadingOutlines(
	_ context.Context, _ string, query types.BaseReadingOutlinesQuery,
) (types.BaseReadingOutlinesPage, error) {
	s.calls++
	s.query = query
	return types.BaseReadingOutlinesPage{Scope: "page", Status: s.status, Page: query.Page,
		HasMore: true, Items: []types.BaseReadingOutlineItem{}}, nil
}

func TestBaseReadingOutlinesToolPreservesBatchStates(t *testing.T) {
	server := newTestServer(t)
	bases := &outlineBatchBaseService{}
	server.bases = bases
	client := connectTestClient(t, server)
	name := "papers_manager_base_reading_outlines"
	args := map[string]any{"base_id": "4f13ef47-81b2-4a76-b3bf-97182cfcc868", "page": 2,
		"page_size": 20, "outline_type": "both", "section_limit": 64}
	for _, status := range []string{"succeeded", "partial", "failed"} {
		bases.status = status
		result := callTool(t, client, name, args)
		if result.IsError || !strings.Contains(resultText(t, result), `"status":"`+status+`"`) ||
			!strings.Contains(resultText(t, result), `"has_more":true`) {
			t.Fatalf("batch status hidden: %s", resultText(t, result))
		}
	}
	if bases.calls != 3 || bases.query.Page != 2 || bases.query.OutlineType != "both" {
		t.Fatalf("not one batch service call: %+v", bases)
	}
	before := bases.calls
	args["page_size"] = 51
	result := callTool(t, client, name, args)
	if !result.IsError || bases.calls != before {
		t.Fatal("oversized page reached batch service")
	}
	if server.registry[name].budget != budgetRead {
		t.Fatal("outline metadata did not use ordinary read budget")
	}
}
