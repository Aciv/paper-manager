package service

import (
	"context"
	"fmt"

	"paperMaster/internal/logger"
	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

// ApplyOperations applies one revision-fenced operation sequence. Only sections
// touched by the sequence are read and rewritten; untouched section files are
// left alone. The manifest is published once, after every operation succeeds.
func (s *noteReadingService) ApplyOperations(
	ctx context.Context, rawID string, request types.ReadingNoteOperations,
) (types.ReadingNoteEditResult, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return types.ReadingNoteEditResult{}, fmt.Errorf("invalid document id: %w", err)
	}
	if request.ExpectedRevision < 1 {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: expected revision must be positive", types.ErrInvalidReadingNote)
	}
	if len(request.Operations) == 0 || len(request.Operations) > types.MaxReadingNoteOperations {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: operations must contain between 1 and %d entries",
			types.ErrInvalidReadingNote, types.MaxReadingNoteOperations)
	}
	gate := s.gate(id)
	if !gate.content.TryRLock() {
		return types.ReadingNoteEditResult{}, types.ErrReadingNoteUnavailable
	}
	defer gate.content.RUnlock()
	gate.manifest.Lock()
	defer gate.manifest.Unlock()
	note, err := s.notes.Get(ctx, id)
	if err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	if note.Revision != request.ExpectedRevision {
		return types.ReadingNoteEditResult{}, types.ErrReadingNoteConflict
	}
	state := newReadingNoteEditState(note)
	for index, operation := range request.Operations {
		if err := state.apply(ctx, s, operation); err != nil {
			return types.ReadingNoteEditResult{}, fmt.Errorf("operation %d (%s): %w", index+1, operation.Op, err)
		}
	}
	if len(state.sections) == 0 {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: a note must retain at least one section; delete the note instead",
			types.ErrInvalidReadingNote)
	}
	for _, section := range state.sections {
		if !section.isNew && !section.dirty {
			continue
		}
		title, err := validateReadingNoteSectionContent(section.entry, section.content)
		if err != nil {
			return types.ReadingNoteEditResult{}, fmt.Errorf("section %s final state: %w", section.entry.ID, err)
		}
		section.entry.Title = title
	}
	state.reorder()
	if !state.changed() {
		return types.ReadingNoteEditResult{
			DocumentID:      id,
			Revision:        note.Revision,
			Directory:       publicDirectory(note.Directory),
			ChangedSections: []types.ReadingNoteSectionEntry{},
		}, nil
	}
	return s.persistReadingNoteEdits(ctx, state, note.Revision)
}

type readingNoteEditSection struct {
	entry         types.ReadingNoteSectionEntry
	originalEntry types.ReadingNoteSectionEntry
	content       string
	original      string
	loaded        bool
	isNew         bool
	dirty         bool
}

type readingNoteEditState struct {
	documentID  uuid.UUID
	sections    []*readingNoteEditSection
	deletedKeys []string
	deletedIDs  []uuid.UUID
}

func newReadingNoteEditState(note types.DocumentReadingNote) *readingNoteEditState {
	state := &readingNoteEditState{documentID: note.DocumentID, sections: make([]*readingNoteEditSection, 0, len(note.Directory.Sections))}
	for _, entry := range note.Directory.Sections {
		state.sections = append(state.sections, &readingNoteEditSection{entry: entry, originalEntry: entry})
	}
	return state
}

func (s *readingNoteEditState) apply(ctx context.Context, service *noteReadingService, operation types.ReadingNoteOperation) error {
	switch operation.Op {
	case types.ReadingNoteOpInsertText:
		index, err := s.requireSection(operation.SectionID)
		if err != nil {
			return err
		}
		content, err := s.loadContent(ctx, service, index)
		if err != nil {
			return err
		}
		updated, err := service.files.ApplyTextEdits(content, []interfaces.TextEdit{{
			Kind: interfaces.TextEditInsert, Offset: operation.Offset, Text: operation.Text,
		}})
		if err != nil {
			return fmt.Errorf("%w: %v", types.ErrInvalidReadingNote, err)
		}
		s.setContent(index, updated)
		return nil
	case types.ReadingNoteOpDeleteText:
		index, err := s.requireSection(operation.SectionID)
		if err != nil {
			return err
		}
		content, err := s.loadContent(ctx, service, index)
		if err != nil {
			return err
		}
		updated, err := service.files.ApplyTextEdits(content, []interfaces.TextEdit{{
			Kind: interfaces.TextEditDelete, Offset: operation.Offset, Length: operation.Length,
		}})
		if err != nil {
			return fmt.Errorf("%w: %v", types.ErrInvalidReadingNote, err)
		}
		s.setContent(index, updated)
		return nil
	case types.ReadingNoteOpAddSection:
		return s.addSection(operation)
	case types.ReadingNoteOpDeleteSection:
		return s.deleteSection(operation.SectionID)
	default:
		return fmt.Errorf("%w: unknown operation %q", types.ErrInvalidReadingNote, operation.Op)
	}
}

func (s *readingNoteEditState) requireSection(id *uuid.UUID) (int, error) {
	if id == nil {
		return 0, fmt.Errorf("%w: sectionId is required", types.ErrInvalidReadingNote)
	}
	for index := range s.sections {
		if s.sections[index].entry.ID == *id {
			return index, nil
		}
	}
	return 0, fmt.Errorf("%w: section does not exist", types.ErrInvalidReadingNote)
}

func (s *readingNoteEditState) loadContent(ctx context.Context, service *noteReadingService, index int) (string, error) {
	section := s.sections[index]
	if section.loaded {
		return section.content, nil
	}
	content, err := service.readSectionFile(ctx, section.entry)
	if err != nil {
		return "", err
	}
	section.content, section.original, section.loaded = content, content, true
	return content, nil
}

func (s *readingNoteEditState) setContent(index int, content string) {
	section := s.sections[index]
	section.content = content
	if !section.isNew && content == section.original {
		section.entry = section.originalEntry
		section.dirty = false
		return
	}
	section.dirty = true
}

func (s *readingNoteEditState) addSection(operation types.ReadingNoteOperation) error {
	parsed, err := splitReadingNoteMarkdown(operation.Markdown)
	if err != nil {
		return err
	}
	if len(parsed) != 1 || parsed[0].entry.Level == 0 {
		return fmt.Errorf("%w: addSection requires exactly one H1-H3 section", types.ErrInvalidReadingNote)
	}
	entries := s.entries()
	insertAt, err := readingNoteInsertionIndex(entries, operation.ParentID, operation.AfterID, parsed[0].entry.Level)
	if err != nil {
		return err
	}
	if len(s.sections)+1 > types.MaxReadingNoteHeadings {
		return fmt.Errorf("%w: too many note sections", types.ErrInvalidReadingNote)
	}
	section := &readingNoteEditSection{
		entry: parsed[0].entry, content: parsed[0].content,
		loaded: true, isNew: true, dirty: true,
	}
	s.sections = append(s.sections, nil)
	copy(s.sections[insertAt+1:], s.sections[insertAt:])
	s.sections[insertAt] = section
	s.reorder()
	return nil
}

func (s *readingNoteEditState) deleteSection(id *uuid.UUID) error {
	index, err := s.requireSection(id)
	if err != nil {
		return err
	}
	first, last := index, index+1
	// Preamble (level 0) is not a parent in the heading tree; deleting it must
	// not take every heading with it.
	if s.sections[index].entry.Level > 0 {
		for last < len(s.sections) && s.sections[last].entry.Level > s.sections[index].entry.Level {
			last++
		}
	}
	for _, section := range s.sections[first:last] {
		if section.isNew {
			continue
		}
		if section.entry.StorageKey != "" {
			s.deletedKeys = append(s.deletedKeys, section.entry.StorageKey)
		}
		s.deletedIDs = append(s.deletedIDs, section.entry.ID)
	}
	s.sections = append(s.sections[:first], s.sections[last:]...)
	s.reorder()
	return nil
}

func (s *readingNoteEditState) entries() []types.ReadingNoteSectionEntry {
	entries := make([]types.ReadingNoteSectionEntry, len(s.sections))
	for index := range s.sections {
		entries[index] = s.sections[index].entry
	}
	return entries
}

func (s *readingNoteEditState) reorder() {
	for index := range s.sections {
		s.sections[index].entry.Order = index
	}
}

func (s *readingNoteEditState) changed() bool {
	if len(s.deletedKeys) > 0 {
		return true
	}
	for _, section := range s.sections {
		if section.isNew || section.dirty {
			return true
		}
	}
	return false
}

func (s *readingNoteEditState) directory() types.ReadingNoteDirectory {
	s.reorder()
	directory := types.ReadingNoteDirectory{SchemaVersion: 1, Sections: make([]types.ReadingNoteSectionEntry, len(s.sections))}
	for index := range s.sections {
		directory.Sections[index] = s.sections[index].entry
	}
	return directory
}

type readingNoteRestore struct {
	entry   types.ReadingNoteSectionEntry
	old     string
	written string
}

func (s *noteReadingService) persistReadingNoteEdits(
	ctx context.Context, state *readingNoteEditState, expectedRevision int64,
) (types.ReadingNoteEditResult, error) {
	var projected int64
	for _, section := range state.sections {
		if section.isNew || section.dirty {
			projected += int64(len(section.content))
		} else {
			projected += section.entry.SizeBytes
		}
	}
	if projected <= 0 || projected > types.MaxDocumentReadingNoteBytes {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: note exceeds the %d-byte limit", types.ErrInvalidReadingNote, types.MaxDocumentReadingNoteBytes)
	}
	var newKeys []string
	var restores []readingNoteRestore
	changed := make([]types.ReadingNoteSectionEntry, 0)
	for _, section := range state.sections {
		if !section.isNew && !section.dirty {
			continue
		}
		key := noteSectionStorageKey(state.documentID, section.entry.ID)
		size, err := s.replaceSectionFile(ctx, key, section.content)
		if err != nil {
			s.rollbackReadingNoteEdits(context.WithoutCancel(ctx), newKeys, restores)
			return types.ReadingNoteEditResult{}, err
		}
		section.entry.StorageKey, section.entry.SizeBytes = key, size
		if section.isNew {
			section.entry.ContentRevision = 1
			newKeys = append(newKeys, key)
		} else {
			section.entry.ContentRevision++
			restores = append(restores, readingNoteRestore{entry: section.entry, old: section.original, written: section.content})
		}
		changed = append(changed, publicSection(section.entry))
	}
	directory := state.directory()
	var total int64
	for _, section := range directory.Sections {
		total += section.SizeBytes
	}
	candidate := types.DocumentReadingNote{DocumentID: state.documentID, SizeBytes: total}
	if err := candidate.SetDirectory(directory); err != nil {
		s.rollbackReadingNoteEdits(context.WithoutCancel(ctx), newKeys, restores)
		return types.ReadingNoteEditResult{}, err
	}
	persisted, err := s.notes.Put(ctx, candidate, expectedRevision)
	if err != nil {
		s.rollbackReadingNoteEdits(context.WithoutCancel(ctx), newKeys, restores)
		return types.ReadingNoteEditResult{}, err
	}
	s.deleteSectionKeys(context.WithoutCancel(ctx), state.deletedKeys)
	return types.ReadingNoteEditResult{
		DocumentID:        state.documentID,
		Revision:          persisted.Revision,
		Directory:         publicDirectory(persisted.Directory),
		ChangedSections:   changed,
		DeletedSectionIDs: state.deletedIDs,
	}, nil
}

func (s *noteReadingService) rollbackReadingNoteEdits(ctx context.Context, newKeys []string, restores []readingNoteRestore) {
	s.deleteSectionKeys(ctx, newKeys)
	for _, restore := range restores {
		s.restoreReadingNoteSection(ctx, restore)
	}
}

func (s *noteReadingService) restoreReadingNoteSection(ctx context.Context, restore readingNoteRestore) {
	current, err := s.readSectionFile(ctx, restore.entry)
	if err != nil {
		if _, writeErr := s.replaceSectionFile(ctx, restore.entry.StorageKey, restore.old); writeErr != nil {
			logger.Warn(ctx, "restore reading note section failed", "storage_key", restore.entry.StorageKey, "error", writeErr)
		}
		return
	}
	if current != restore.written {
		logger.Warn(ctx, "reading note section changed after failed edit; leaving newer content",
			"storage_key", restore.entry.StorageKey)
		return
	}
	if _, err := s.replaceSectionFile(ctx, restore.entry.StorageKey, restore.old); err != nil {
		logger.Warn(ctx, "restore reading note section failed", "storage_key", restore.entry.StorageKey, "error", err)
	}
}
