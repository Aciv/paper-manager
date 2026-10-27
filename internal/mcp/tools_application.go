package mcp

import (
	"context"
	"encoding/json"
)

type applicationInfoResult struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	WebAvailable bool     `json:"web_available"`
	WebBaseURL   string   `json:"web_base_url,omitempty"`
	LibraryURL   string   `json:"library_url,omitempty"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
}

// applicationInfo exposes only the advertised Web location, never config files,
// listener internals, paths or credentials. It needs no database/file requests.
func (s *ToolServer) applicationInfo(_ context.Context, raw json.RawMessage) (any, error) {
	var input struct{}
	if err := decodeToolArgs(raw, &input); err != nil {
		return nil, err
	}
	result := applicationInfoResult{Name: "paper_manager", Version: serverVersion}
	if s.cfg.WebBaseURL == "" {
		result.Diagnostics = []string{"web_base_url_not_configured"}
		return result, nil
	}
	result.WebAvailable = true
	result.WebBaseURL = s.cfg.WebBaseURL
	result.LibraryURL = s.cfg.WebBaseURL + "#/library"
	return result, nil
}
