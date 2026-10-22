package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"

	"github.com/google/uuid"
)

func parseNoteAndSectionIDs(rawDocumentID, rawSectionID string) (uuid.UUID, uuid.UUID, error) {
	documentID, err := uuid.Parse(rawDocumentID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid document id: %w", err)
	}
	sectionID, err := uuid.Parse(rawSectionID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: invalid section id", types.ErrInvalidReadingNote)
	}
	return documentID, sectionID, nil
}

func parseOptionalUUID(raw *string) (*uuid.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid section relation id", types.ErrInvalidReadingNote)
	}
	return &id, nil
}

func noteSectionStorageKey(documentID, sectionID uuid.UUID) string {
	return "notes/" + documentID.String() + "/" + sectionID.String() + ".md"
}

func findReadingNoteSection(directory types.ReadingNoteDirectory, id uuid.UUID) (types.ReadingNoteSectionEntry, bool) {
	for _, section := range directory.Sections {
		if section.ID == id {
			return section, true
		}
	}
	return types.ReadingNoteSectionEntry{}, false
}

func publicSection(section types.ReadingNoteSectionEntry) types.ReadingNoteSectionEntry {
	section.StorageKey, section.StoredKey = "", ""
	return section
}

func publicDirectory(directory types.ReadingNoteDirectory) types.ReadingNoteDirectory {
	sections := make([]types.ReadingNoteSectionEntry, len(directory.Sections))
	copy(sections, directory.Sections)
	directory.Sections = sections
	for index := range directory.Sections {
		directory.Sections[index] = publicSection(directory.Sections[index])
	}
	return directory
}

func readingNoteStorageKeys(directory types.ReadingNoteDirectory) []string {
	keys := make([]string, 0, len(directory.Sections))
	for _, section := range directory.Sections {
		if section.StorageKey != "" {
			keys = append(keys, section.StorageKey)
		}
	}
	return keys
}

func (s *noteReadingService) readSectionFile(ctx context.Context, section types.ReadingNoteSectionEntry) (string, error) {
	if _, _, err := parseReadingNoteSectionKey(section.StorageKey); err != nil {
		return "", err
	}
	content, err := s.files.ReadText(ctx, section.StorageKey, types.MaxDocumentReadingNoteBytes)
	if err != nil {
		return "", err
	}
	if len(content) == 0 {
		return "", fmt.Errorf("%w: stored section size is invalid", types.ErrInvalidReadingNote)
	}
	return content, nil
}

func (s *noteReadingService) replaceSectionFile(ctx context.Context, storageKey, content string) (int64, error) {
	if _, _, err := parseReadingNoteSectionKey(storageKey); err != nil {
		return 0, err
	}
	stored, err := s.files.ReplaceText(ctx, storageKey, content, types.MaxDocumentReadingNoteBytes)
	if err != nil {
		return 0, err
	}
	return stored.Size, nil
}

func (s *noteReadingService) deleteSectionKeys(ctx context.Context, keys []string) {
	for _, key := range keys {
		if _, _, err := parseReadingNoteSectionKey(key); err != nil {
			logger.Warn(ctx, "skip invalid reading note section key", "storage_key", key, "error", err)
			continue
		}
		if err := s.files.Delete(ctx, key); err != nil {
			logger.Warn(ctx, "remove unreferenced reading note section failed", "storage_key", key, "error", err)
			continue
		}
	}
}

func parseReadingNoteSectionKey(storageKey string) (uuid.UUID, uuid.UUID, error) {
	parts := strings.Split(filepath.ToSlash(storageKey), "/")
	if len(parts) != 3 || parts[0] != "notes" || filepath.Ext(parts[2]) != ".md" {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: invalid section storage key", types.ErrInvalidReadingNote)
	}
	documentID, err := uuid.Parse(parts[1])
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: invalid section document id", types.ErrInvalidReadingNote)
	}
	sectionID, err := uuid.Parse(strings.TrimSuffix(parts[2], ".md"))
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: invalid section id", types.ErrInvalidReadingNote)
	}
	return documentID, sectionID, nil
}

func readingNoteInsertionIndex(sections []types.ReadingNoteSectionEntry, parentID, afterID *uuid.UUID, level int) (int, error) {
	findIndex := func(id uuid.UUID) int {
		for index := range sections {
			if sections[index].ID == id {
				return index
			}
		}
		return -1
	}
	if parentID != nil {
		index := findIndex(*parentID)
		if index < 0 || sections[index].Level == 0 || level <= sections[index].Level {
			return 0, fmt.Errorf("%w: invalid parent section", types.ErrInvalidReadingNote)
		}
	}
	if afterID != nil {
		index := findIndex(*afterID)
		if index < 0 {
			return 0, fmt.Errorf("%w: after section does not exist", types.ErrInvalidReadingNote)
		}
		after := sections[index]
		if (parentID == nil) != (after.ParentID == nil) || (parentID != nil && *after.ParentID != *parentID) {
			return 0, fmt.Errorf("%w: after section must be a sibling", types.ErrInvalidReadingNote)
		}
		insertAt := index + 1
		for insertAt < len(sections) && sections[insertAt].Level > after.Level {
			insertAt++
		}
		return insertAt, nil
	}
	if parentID == nil {
		return len(sections), nil
	}
	parent := sections[findIndex(*parentID)]
	insertAt := findIndex(*parentID) + 1
	for insertAt < len(sections) && sections[insertAt].Level > parent.Level {
		insertAt++
	}
	return insertAt, nil
}

type readingNoteGate struct {
	content  sync.RWMutex
	manifest sync.Mutex
}

type splitReadingNoteSection struct {
	entry   types.ReadingNoteSectionEntry
	content string
}

// New manifests start with gaps so later sibling/child insertions can pick a
// midpoint without renumbering existing sections. Older manifests with dense
// 0,1,2 orders remain valid; float midpoints fill those gaps too.
const readingNoteOrderStep = 1024.0

func splitReadingNoteMarkdown(markdown string) ([]splitReadingNoteSection, error) {
	if err := types.ValidateReadingNoteMarkdown(markdown); err != nil {
		return nil, err
	}
	type heading struct {
		start int
		level int
		title string
	}
	var headings []heading
	inFence := false
	offset := 0
	for _, line := range strings.SplitAfter(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			offset += len(line)
			continue
		}
		if !inFence {
			left := strings.TrimLeft(line, " \t")
			level := 0
			for level < len(left) && level < 3 && left[level] == '#' {
				level++
			}
			if level > 0 && len(left) > level && (left[level] == ' ' || left[level] == '\t') {
				title := strings.TrimSpace(strings.TrimRight(left[level+1:], "# \t\r\n"))
				if title != "" {
					headings = append(headings, heading{start: offset, level: level, title: title})
				}
			}
		}
		offset += len(line)
	}
	if len(headings) > types.MaxReadingNoteHeadings {
		return nil, fmt.Errorf("%w: too many H1-H3 sections", types.ErrInvalidReadingNote)
	}
	sections := make([]splitReadingNoteSection, 0, len(headings)+1)
	stack := make([]int, 0, 3)
	if len(headings) == 0 || headings[0].start > 0 {
		end := len(markdown)
		if len(headings) > 0 {
			end = headings[0].start
		}
		if strings.TrimSpace(markdown[:end]) != "" {
			id := uuid.New()
			sections = append(sections, splitReadingNoteSection{entry: types.ReadingNoteSectionEntry{
				ID: id, Order: 0, Level: 0, Title: "Preamble", ContentRevision: 1,
			}, content: markdown[:end]})
		}
	}
	for index, item := range headings {
		end := len(markdown)
		if index+1 < len(headings) {
			end = headings[index+1].start
		}
		for len(stack) > 0 && sections[stack[len(stack)-1]].entry.Level >= item.level {
			stack = stack[:len(stack)-1]
		}
		id := uuid.New()
		entry := types.ReadingNoteSectionEntry{ID: id, Order: float64(len(sections)) * readingNoteOrderStep, Level: item.level,
			Title: item.title, ContentRevision: 1}
		if len(stack) > 0 {
			parent := sections[stack[len(stack)-1]].entry.ID
			entry.ParentID = &parent
		}
		sections = append(sections, splitReadingNoteSection{entry: entry, content: markdown[item.start:end]})
		stack = append(stack, len(sections)-1)
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("%w: note has no content", types.ErrInvalidReadingNote)
	}
	if len(sections) > types.MaxReadingNoteHeadings {
		return nil, fmt.Errorf("%w: too many note sections", types.ErrInvalidReadingNote)
	}
	return sections, nil
}

func validateReadingNoteSectionContent(entry types.ReadingNoteSectionEntry, content string) (string, error) {
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("%w: section must be UTF-8", types.ErrInvalidReadingNote)
	}
	sections, err := splitReadingNoteMarkdown(content)
	if err != nil {
		return "", err
	}
	if len(sections) != 1 || sections[0].entry.Level != entry.Level || sections[0].content != content {
		return "", fmt.Errorf("%w: section edit must preserve exactly one H1-H3 heading and its level", types.ErrInvalidReadingNote)
	}
	return sections[0].entry.Title, nil
}
