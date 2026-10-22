package mcp

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

const sectionDescriptionMaxRunes = 512

var (
	sectionMetaOpen = regexp.MustCompile(`^<!--\s*papers-manager:section-meta\s+v=(\d+)\s*(.*)$`)
	sectionHeading  = regexp.MustCompile(`^ {0,3}#{1,3}[ \t]+\S`)
)

// parseSectionMeta is an MCP-adapter consumer parser. It deliberately does not
// live in the note service or database: section-meta is ordinary Markdown, and
// only consumers such as this adapter and the frontend interpret it.
func parseSectionMeta(markdown string) (string, []string) {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	index := 0
	for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
		index++
	}
	if index < len(lines) && sectionHeading.MatchString(lines[index]) {
		index++
	}
	for index < len(lines) && strings.TrimSpace(lines[index]) == "" {
		index++
	}
	if index >= len(lines) {
		return "", nil
	}
	opened := sectionMetaOpen.FindStringSubmatch(strings.TrimSpace(lines[index]))
	if opened == nil {
		return "", nil
	}
	version := opened[1]
	payload := opened[2]
	diagnostics := []string{}
	if version != "1" {
		return "", []string{"unsupported section metadata version"}
	}

	// Accept both a one-line marker and the normal multi-line form.
	if closeAt := strings.Index(payload, "-->"); closeAt >= 0 {
		payload = payload[:closeAt]
	} else {
		parts := make([]string, 0, 4)
		if strings.TrimSpace(payload) != "" {
			parts = append(parts, payload)
		}
		closed := false
		for index++; index < len(lines); index++ {
			line := lines[index]
			if closeAt := strings.Index(line, "-->"); closeAt >= 0 {
				if strings.TrimSpace(line[:closeAt]) != "" {
					parts = append(parts, line[:closeAt])
				}
				closed = true
				break
			}
			parts = append(parts, line)
		}
		if !closed {
			return "", []string{"section metadata block is not closed"}
		}
		payload = strings.Join(parts, "\n")
	}

	if strings.TrimSpace(payload) == "" {
		return "", []string{"section metadata has no JSON payload"}
	}
	var decoded struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return "", []string{"section metadata JSON is invalid"}
	}
	description := strings.Join(strings.Fields(decoded.Description), " ")
	if utf8.RuneCountInString(description) > sectionDescriptionMaxRunes {
		diagnostics = append(diagnostics, "section description exceeds the 512-character limit")
		description = string([]rune(description)[:sectionDescriptionMaxRunes])
	}
	return description, diagnostics
}
