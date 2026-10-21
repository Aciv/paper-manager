package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

type noteReadingService struct {
	notes interfaces.NoteReadingRepository
	files interfaces.TextFileEditor
	gates sync.Map
}

func NewNoteReadingService(
	notes interfaces.NoteReadingRepository,
	files interfaces.TextFileEditor,
) interfaces.NoteReadingService {
	return &noteReadingService{notes: notes, files: files}
}

func (s *noteReadingService) Export(ctx context.Context, rawID string) (types.ReadingNoteExport, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return types.ReadingNoteExport{}, fmt.Errorf("invalid document id: %w", err)
	}
	gate := s.gate(id)
	if !gate.content.TryRLock() {
		return types.ReadingNoteExport{}, types.ErrReadingNoteUnavailable
	}
	defer gate.content.RUnlock()
	note, err := s.notes.Get(ctx, id)
	if err != nil {
		return types.ReadingNoteExport{}, err
	}
	var markdown strings.Builder
	for _, section := range note.Directory.Sections {
		if markdown.Len() > 0 && !strings.HasSuffix(markdown.String(), "\n") {
			markdown.WriteByte('\n')
		}
		content, readErr := s.readSectionFile(ctx, section)
		if readErr != nil {
			return types.ReadingNoteExport{}, readErr
		}
		markdown.WriteString(content)
	}
	return types.ReadingNoteExport{DocumentID: id, Revision: note.Revision, Markdown: markdown.String()}, nil
}

func (s *noteReadingService) gate(id uuid.UUID) *readingNoteGate {
	value, _ := s.gates.LoadOrStore(id, &readingNoteGate{})
	return value.(*readingNoteGate)
}

func (s *noteReadingService) ReadSection(ctx context.Context, rawID, rawSectionID string) (types.ReadingNoteSection, error) {
	id, sectionID, err := parseNoteAndSectionIDs(rawID, rawSectionID)
	if err != nil {
		return types.ReadingNoteSection{}, err
	}
	gate := s.gate(id)
	if !gate.content.TryRLock() {
		return types.ReadingNoteSection{}, types.ErrReadingNoteUnavailable
	}
	defer gate.content.RUnlock()
	note, err := s.notes.Get(ctx, id)
	if err != nil {
		return types.ReadingNoteSection{}, err
	}
	section, ok := findReadingNoteSection(note.Directory, sectionID)
	if !ok {
		return types.ReadingNoteSection{}, sql.ErrNoRows
	}
	content, err := s.readSectionFile(ctx, section)
	if err != nil {
		return types.ReadingNoteSection{}, err
	}
	return types.ReadingNoteSection{DocumentID: id, Revision: note.Revision, Section: publicSection(section), Markdown: content}, nil
}

func (s *noteReadingService) GetManifest(ctx context.Context, rawID string) (types.DocumentReadingNote, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return types.DocumentReadingNote{}, fmt.Errorf("invalid document id: %w", err)
	}
	gate := s.gate(id)
	if !gate.content.TryRLock() {
		return types.DocumentReadingNote{}, types.ErrReadingNoteUnavailable
	}
	defer gate.content.RUnlock()
	note, err := s.notes.Get(ctx, id)
	if err == nil {
		note.Directory = publicDirectory(note.Directory)
	}
	return note, err
}

func (s *noteReadingService) Put(ctx context.Context, rawID string, expectedRevision int64, markdown string) (types.DocumentReadingNote, error) {

	id, err := uuid.Parse(rawID)
	if err != nil {
		return types.DocumentReadingNote{}, fmt.Errorf("invalid document id: %w", err)
	}
	if expectedRevision < 0 {
		return types.DocumentReadingNote{}, fmt.Errorf("%w: expected revision cannot be negative", types.ErrInvalidReadingNote)
	}
	if err := types.ValidateReadingNoteMarkdown(markdown); err != nil {
		return types.DocumentReadingNote{}, err
	}
	sections, err := splitReadingNoteMarkdown(markdown)
	if err != nil {
		return types.DocumentReadingNote{}, err
	}
	gate := s.gate(id)
	gate.content.Lock()
	defer gate.content.Unlock()
	var old types.DocumentReadingNote
	old, oldErr := s.notes.Get(ctx, id)
	if oldErr != nil && !errors.Is(oldErr, sql.ErrNoRows) {
		return types.DocumentReadingNote{}, oldErr
	}
	written := make([]string, 0, len(sections))
	directory := types.ReadingNoteDirectory{SchemaVersion: 1, Sections: make([]types.ReadingNoteSectionEntry, 0, len(sections))}
	var total int64
	for _, section := range sections {
		key := noteSectionStorageKey(id, section.entry.ID)
		size, writeErr := s.replaceSectionFile(ctx, key, section.content)
		if writeErr != nil {
			s.deleteSectionKeys(context.WithoutCancel(ctx), written)
			return types.DocumentReadingNote{}, writeErr
		}
		section.entry.StorageKey, section.entry.SizeBytes = key, size
		directory.Sections = append(directory.Sections, section.entry)
		written = append(written, key)
		total += size
	}
	candidate := types.DocumentReadingNote{DocumentID: id, SizeBytes: total}
	if err := candidate.SetDirectory(directory); err != nil {
		s.deleteSectionKeys(context.WithoutCancel(ctx), written)
		return types.DocumentReadingNote{}, err
	}
	persisted, err := s.notes.Put(ctx, candidate, expectedRevision)
	if err != nil {
		s.deleteSectionKeys(context.WithoutCancel(ctx), written)
		return types.DocumentReadingNote{}, err
	}
	if oldErr == nil {
		s.deleteSectionKeys(context.WithoutCancel(ctx), readingNoteStorageKeys(old.Directory))
	}
	persisted.Directory = publicDirectory(persisted.Directory)
	return persisted, nil
}

func (s *noteReadingService) Delete(ctx context.Context, rawID string, expectedRevision int64) error {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return fmt.Errorf("invalid document id: %w", err)
	}
	if expectedRevision < 1 {
		return fmt.Errorf("%w: expected revision must be positive", types.ErrInvalidReadingNote)
	}
	gate := s.gate(id)
	gate.content.Lock()
	defer gate.content.Unlock()
	deleted, err := s.notes.Delete(ctx, id, expectedRevision)
	if err != nil {
		return err
	}
	s.deleteSectionKeys(context.WithoutCancel(ctx), readingNoteStorageKeys(deleted.Directory))
	return nil
}
