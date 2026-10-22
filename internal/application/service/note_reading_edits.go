package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"paperMaster/internal/types"
	"paperMaster/internal/types/interfaces"

	"github.com/google/uuid"
)

const readingNoteMaxReportedMatches = 8

// ApplyEdits applies the semantic edit layer used by agents and automation.
// Every text position is resolved against the section snapshot selected by
// ExpectedRevision; the resolved ranges are then converted into primitive
// delete/insert text operations. Ranges in one section must not overlap or
// share a start position. Structural edits are applied first so text edits
// cannot target a section deleted by the same batch.
func (s *noteReadingService) ApplyEdits(
	ctx context.Context, rawID string, request types.ReadingNoteEdits,
) (types.ReadingNoteEditResult, error) {
	id, err := uuid.Parse(rawID)
	if err != nil {
		return types.ReadingNoteEditResult{}, fmt.Errorf("invalid document id: %w", err)
	}
	if request.ExpectedRevision < 1 {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: expected revision must be positive", types.ErrInvalidReadingNote)
	}
	if len(request.Edits) == 0 || len(request.Edits) > types.MaxReadingNoteEdits {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: edits must contain between 1 and %d entries",
			types.ErrInvalidReadingNote, types.MaxReadingNoteEdits)
	}
	if err := validateReadingNoteInlineEditSizes(request.Edits); err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	note, release, err := s.lockReadingNoteEdit(ctx, id, request.ExpectedRevision)
	if err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	defer release()
	state := newReadingNoteEditState(note)
	if err := s.applyReadingNoteEdits(ctx, state, request.Edits); err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	return s.commitReadingNoteEdits(ctx, note, state)
}

func validateReadingNoteInlineEditSizes(edits []types.ReadingNoteEdit) error {
	total := 0
	for index, edit := range edits {
		for _, field := range []string{edit.OldText, edit.NewText, edit.Content} {
			if len(field) > types.MaxReadingNoteInlineEditFieldBytes {
				return fmt.Errorf("%w: edit %d exceeds the %d-byte inline text limit; upload the whole section instead",
					types.ErrInvalidReadingNote, index+1, types.MaxReadingNoteInlineEditFieldBytes)
			}
			total += len(field)
		}
	}
	if total > types.MaxReadingNoteInlineEditTotalBytes {
		return fmt.Errorf("%w: inline edits exceed the %d-byte request text budget; upload the whole section instead",
			types.ErrInvalidReadingNote, types.MaxReadingNoteInlineEditTotalBytes)
	}
	return nil
}

// ReplaceSection is the raw/file-upload counterpart of the semantic
// replaceSection edit. It intentionally bypasses the inline-edit size budget;
// the whole uploaded Markdown is still validated to contain exactly one
// heading at the target section's level.
func (s *noteReadingService) ReplaceSection(
	ctx context.Context, rawID, rawSectionID string, expectedRevision int64, markdown string,
) (types.ReadingNoteEditResult, error) {
	id, sectionID, err := parseNoteAndSectionIDs(rawID, rawSectionID)
	if err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	if expectedRevision < 1 {
		return types.ReadingNoteEditResult{}, fmt.Errorf("%w: expected revision must be positive", types.ErrInvalidReadingNote)
	}
	if err := types.ValidateReadingNoteMarkdown(markdown); err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	note, release, err := s.lockReadingNoteEdit(ctx, id, expectedRevision)
	if err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	defer release()
	state := newReadingNoteEditState(note)
	if err := s.applyReadingNoteEdits(ctx, state, []types.ReadingNoteEdit{{
		Op: types.ReadingNoteEditOpReplaceSection, SectionID: &sectionID, Content: markdown,
	}}); err != nil {
		return types.ReadingNoteEditResult{}, err
	}
	return s.commitReadingNoteEdits(ctx, note, state)
}

type indexedReadingNoteEdit struct {
	index int
	edit  types.ReadingNoteEdit
}

func (s *noteReadingService) applyReadingNoteEdits(
	ctx context.Context, state *readingNoteEditState, edits []types.ReadingNoteEdit,
) error {
	textEdits := make([]indexedReadingNoteEdit, 0, len(edits))
	for index, edit := range edits {
		switch edit.Op {
		case types.ReadingNoteEditOpAddSection:
			if err := state.addSection(types.ReadingNoteOperation{
				ParentID: edit.ParentID,
				AfterID:  edit.AfterID,
				Markdown: edit.Content,
			}); err != nil {
				return fmt.Errorf("edit %d (%s): %w", index+1, edit.Op, err)
			}
		case types.ReadingNoteEditOpDeleteSection:
			if err := state.deleteSection(edit.SectionID); err != nil {
				return fmt.Errorf("edit %d (%s): %w", index+1, edit.Op, err)
			}
		case types.ReadingNoteEditOpReplaceText,
			types.ReadingNoteEditOpReplaceRange,
			types.ReadingNoteEditOpInsertAt,
			types.ReadingNoteEditOpAppendText,
			types.ReadingNoteEditOpReplaceSection:
			textEdits = append(textEdits, indexedReadingNoteEdit{index: index, edit: edit})
		default:
			return fmt.Errorf("%w: edit %d has unknown op %q", types.ErrInvalidReadingNote, index+1, edit.Op)
		}
	}

	groups := make(map[int][]indexedReadingNoteEdit)
	for _, item := range textEdits {
		if item.edit.SectionID == nil {
			return fmt.Errorf("%w: edit %d (%s) requires sectionId", types.ErrInvalidReadingNote, item.index+1, item.edit.Op)
		}
		sectionIndex, err := state.requireSection(item.edit.SectionID)
		if err != nil {
			return fmt.Errorf("edit %d (%s): %w", item.index+1, item.edit.Op, err)
		}
		groups[sectionIndex] = append(groups[sectionIndex], item)
	}

	for sectionIndex := range state.sections {
		items := groups[sectionIndex]
		if len(items) == 0 {
			continue
		}
		if err := s.applyReadingNoteSectionEdits(ctx, state, sectionIndex, items); err != nil {
			return err
		}
	}
	return nil
}

func (s *noteReadingService) applyReadingNoteSectionEdits(
	ctx context.Context, state *readingNoteEditState, sectionIndex int, items []indexedReadingNoteEdit,
) error {
	content, err := state.loadContent(ctx, s, sectionIndex)
	if err != nil {
		return err
	}
	entry := state.sections[sectionIndex].entry
	lines := readingNoteLines(content)
	totalRunes := utf8.RuneCountInString(content)
	ranges := make([]readingNoteTextRange, 0, len(items))
	for _, item := range items {
		if item.edit.Op == types.ReadingNoteEditOpReplaceSection && len(items) != 1 {
			return fmt.Errorf("%w: edit %d (replaceSection) must be the only edit for section %s",
				types.ErrInvalidReadingNote, item.index+1, entry.ID)
		}
		textRange, err := readingNoteTextRangeForEdit(content, lines, totalRunes, entry, item.edit)
		if err != nil {
			return fmt.Errorf("edit %d (%s): %w", item.index+1, item.edit.Op, err)
		}
		ranges = append(ranges, textRange)
	}
	ranges, err = validateReadingNoteTextRanges(ranges)
	if err != nil {
		return fmt.Errorf("section %s: %w", entry.ID, err)
	}
	primitive := make([]interfaces.TextEdit, 0, len(ranges)*2)
	for _, textRange := range ranges {
		if textRange.end > textRange.start {
			primitive = append(primitive, interfaces.TextEdit{
				Kind: interfaces.TextEditDelete, Offset: textRange.start, Length: textRange.end - textRange.start,
			})
		}
		if textRange.text != "" {
			primitive = append(primitive, interfaces.TextEdit{
				Kind: interfaces.TextEditInsert, Offset: textRange.start, Text: textRange.text,
			})
		}
	}
	if len(primitive) == 0 {
		return nil
	}
	updated, err := s.files.ApplyTextEdits(content, primitive)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidReadingNote, err)
	}
	state.setContent(sectionIndex, updated)
	return nil
}

type readingNoteTextRange struct {
	start int
	end   int
	text  string
}

type readingNoteLine struct {
	startByte int
	endByte   int
	nextByte  int
	startRune int
	runeCount int
}

func readingNoteLineCount(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

// readingNoteLines reports lines like an editor: a trailing newline terminates
// the last line instead of creating a phantom empty line.
func readingNoteLines(content string) []readingNoteLine {
	if content == "" {
		return nil
	}
	lines := make([]readingNoteLine, 0, strings.Count(content, "\n")+1)
	byteOffset, runeOffset := 0, 0
	for byteOffset < len(content) {
		newline := strings.IndexByte(content[byteOffset:], '\n')
		contentEnd, nextByte := len(content), len(content)
		if newline >= 0 {
			contentEnd = byteOffset + newline
			nextByte = contentEnd + 1
		}
		runeCount := utf8.RuneCountInString(content[byteOffset:contentEnd])
		lines = append(lines, readingNoteLine{
			startByte: byteOffset,
			endByte:   contentEnd,
			nextByte:  nextByte,
			startRune: runeOffset,
			runeCount: runeCount,
		})
		runeOffset += runeCount
		if newline >= 0 {
			runeOffset++
		}
		byteOffset = nextByte
	}
	return lines
}

func readingNoteTextRangeForEdit(
	content string, lines []readingNoteLine, totalRunes int,
	entry types.ReadingNoteSectionEntry, edit types.ReadingNoteEdit,
) (readingNoteTextRange, error) {
	switch edit.Op {
	case types.ReadingNoteEditOpReplaceText:
		if edit.OldText == "" {
			return readingNoteTextRange{}, fmt.Errorf("%w: replaceText requires oldText", types.ErrInvalidReadingNote)
		}
		windowStart, windowEnd, err := readingNoteTextSearchWindow(content, lines, edit.LineStart, edit.LineEnd)
		if err != nil {
			return readingNoteTextRange{}, err
		}
		matches := readingNoteTextMatchStarts(content, windowStart, windowEnd, edit.OldText)
		if len(matches) == 0 {
			return readingNoteTextRange{}, fmt.Errorf("%w: oldText was not found in %s",
				types.ErrInvalidReadingNote, readingNoteTextScope(edit))
		}
		if len(matches) > 1 {
			return readingNoteTextRange{}, fmt.Errorf("%w: oldText matched multiple times (%s) in %s; narrow lineStart/lineEnd or use replaceRange",
				types.ErrInvalidReadingNote, readingNoteMatchLines(lines, matches), readingNoteTextScope(edit))
		}
		start, err := readingNoteRuneOffsetAtByte(content, lines, matches[0])
		if err != nil {
			return readingNoteTextRange{}, err
		}
		end := start + utf8.RuneCountInString(edit.OldText)
		if end > totalRunes {
			return readingNoteTextRange{}, fmt.Errorf("%w: oldText range exceeds the section", types.ErrInvalidReadingNote)
		}
		return readingNoteTextRange{start: start, end: end, text: edit.NewText}, nil
	case types.ReadingNoteEditOpReplaceRange:
		if edit.Start == nil || edit.End == nil {
			return readingNoteTextRange{}, fmt.Errorf("%w: replaceRange requires start and end", types.ErrInvalidReadingNote)
		}
		if edit.End.Line < edit.Start.Line {
			return readingNoteTextRange{}, fmt.Errorf("%w: replaceRange end is before start", types.ErrInvalidReadingNote)
		}
		start, err := readingNotePositionOffset(lines, totalRunes, *edit.Start, false)
		if err != nil {
			return readingNoteTextRange{}, err
		}
		end, err := readingNotePositionOffset(lines, totalRunes, *edit.End, true)
		if err != nil {
			return readingNoteTextRange{}, err
		}
		if start > end {
			return readingNoteTextRange{}, fmt.Errorf("%w: replaceRange end is before start", types.ErrInvalidReadingNote)
		}
		if start == end && edit.Content == "" {
			return readingNoteTextRange{}, fmt.Errorf("%w: replaceRange with an empty range requires content", types.ErrInvalidReadingNote)
		}
		return readingNoteTextRange{start: start, end: end, text: edit.Content}, nil
	case types.ReadingNoteEditOpInsertAt:
		if edit.Position == nil {
			return readingNoteTextRange{}, fmt.Errorf("%w: insertAt requires position", types.ErrInvalidReadingNote)
		}
		offset, err := readingNotePositionOffset(lines, totalRunes, *edit.Position, false)
		if err != nil {
			return readingNoteTextRange{}, err
		}
		if edit.Content == "" {
			return readingNoteTextRange{}, fmt.Errorf("%w: insertAt requires content", types.ErrInvalidReadingNote)
		}
		return readingNoteTextRange{start: offset, end: offset, text: edit.Content}, nil
	case types.ReadingNoteEditOpAppendText:
		if edit.Content == "" {
			return readingNoteTextRange{}, fmt.Errorf("%w: appendText requires content", types.ErrInvalidReadingNote)
		}
		return readingNoteTextRange{start: totalRunes, end: totalRunes, text: edit.Content}, nil
	case types.ReadingNoteEditOpReplaceSection:
		if _, err := validateReadingNoteSectionContent(entry, edit.Content); err != nil {
			return readingNoteTextRange{}, err
		}
		return readingNoteTextRange{start: 0, end: totalRunes, text: edit.Content}, nil
	default:
		return readingNoteTextRange{}, fmt.Errorf("%w: unknown edit op %q", types.ErrInvalidReadingNote, edit.Op)
	}
}

func readingNoteTextScope(edit types.ReadingNoteEdit) string {
	if edit.LineStart > 0 {
		return fmt.Sprintf("lines %d-%d", edit.LineStart, edit.LineEnd)
	}
	return "the section"
}

func readingNoteTextSearchWindow(content string, lines []readingNoteLine, startLine, endLine int) (int, int, error) {
	if startLine == 0 && endLine == 0 {
		return 0, len(content), nil
	}
	if startLine == 0 || endLine == 0 {
		return 0, 0, fmt.Errorf("%w: lineStart and lineEnd must be provided together", types.ErrInvalidReadingNote)
	}
	if startLine < 1 || endLine < startLine || endLine > len(lines) {
		return 0, 0, fmt.Errorf("%w: line range %d-%d is outside the section", types.ErrInvalidReadingNote, startLine, endLine)
	}
	start := lines[startLine-1].startByte
	end := len(content)
	if endLine < len(lines) {
		end = lines[endLine].startByte
	}
	return start, end, nil
}

func readingNoteTextMatchStarts(content string, startByte, endByte int, oldText string) []int {
	if oldText == "" || startByte < 0 || endByte > len(content) || startByte >= endByte {
		return nil
	}
	window := content[startByte:endByte]
	matches := make([]int, 0, 2)
	for offset := 0; offset <= len(window)-len(oldText); {
		index := strings.Index(window[offset:], oldText)
		if index < 0 {
			break
		}
		matches = append(matches, startByte+offset+index)
		if len(matches) > readingNoteMaxReportedMatches {
			break
		}
		// Advance by one byte, not by len(oldText), so overlapping occurrences
		// are reported as ambiguous instead of being silently skipped.
		offset += index + 1
	}
	return matches
}

func readingNoteMatchLines(lines []readingNoteLine, matches []int) string {
	limit := len(matches)
	if limit > readingNoteMaxReportedMatches {
		limit = readingNoteMaxReportedMatches
	}
	numbers := make([]string, 0, limit)
	for _, match := range matches[:limit] {
		numbers = append(numbers, fmt.Sprint(readingNoteLineNumberAtByte(lines, match)))
	}
	if len(matches) > limit {
		numbers = append(numbers, "...")
	}
	return strings.Join(numbers, ", ")
}

func readingNoteLineNumberAtByte(lines []readingNoteLine, byteOffset int) int {
	for index, line := range lines {
		if byteOffset <= line.endByte {
			return index + 1
		}
		if byteOffset < line.nextByte {
			return index + 1
		}
	}
	return len(lines)
}

func readingNoteRuneOffsetAtByte(content string, lines []readingNoteLine, byteOffset int) (int, error) {
	if byteOffset < 0 || byteOffset > len(content) {
		return 0, fmt.Errorf("%w: byte position is outside the section", types.ErrInvalidReadingNote)
	}
	if byteOffset == len(content) {
		if len(lines) == 0 {
			return 0, nil
		}
		last := lines[len(lines)-1]
		offset := last.startRune + last.runeCount
		if last.nextByte > last.endByte {
			offset++
		}
		return offset, nil
	}
	for _, line := range lines {
		if byteOffset >= line.startByte && byteOffset <= line.endByte {
			return line.startRune + utf8.RuneCountInString(content[line.startByte:byteOffset]), nil
		}
		if byteOffset == line.nextByte && line.nextByte > line.endByte {
			return line.startRune + line.runeCount + 1, nil
		}
	}
	return 0, fmt.Errorf("%w: byte position is not on a line boundary", types.ErrInvalidReadingNote)
}

func readingNotePositionOffset(
	lines []readingNoteLine, totalRunes int, position types.ReadingNotePosition, end bool,
) (int, error) {
	if position.Line < 1 {
		return 0, fmt.Errorf("%w: line must be positive", types.ErrInvalidReadingNote)
	}
	if position.Line == len(lines)+1 {
		if position.Column != 0 && position.Column != 1 {
			return 0, fmt.Errorf("%w: end-of-section position only accepts column 1", types.ErrInvalidReadingNote)
		}
		return totalRunes, nil
	}
	if position.Line > len(lines) {
		return 0, fmt.Errorf("%w: line %d is outside the section", types.ErrInvalidReadingNote, position.Line)
	}
	line := lines[position.Line-1]
	if position.Column == 0 {
		if end {
			return line.startRune + line.runeCount, nil
		}
		return line.startRune, nil
	}
	if position.Column < 1 || position.Column > line.runeCount+1 {
		return 0, fmt.Errorf("%w: column %d is outside line %d", types.ErrInvalidReadingNote, position.Column, position.Line)
	}
	return line.startRune + position.Column - 1, nil
}

func validateReadingNoteTextRanges(ranges []readingNoteTextRange) ([]readingNoteTextRange, error) {
	for _, textRange := range ranges {
		if textRange.start < 0 || textRange.end < textRange.start {
			return nil, fmt.Errorf("%w: edit range is invalid", types.ErrInvalidReadingNote)
		}
	}
	sort.Slice(ranges, func(left, right int) bool {
		if ranges[left].start == ranges[right].start {
			return ranges[left].end < ranges[right].end
		}
		return ranges[left].start < ranges[right].start
	})
	for index := 1; index < len(ranges); index++ {
		previous, current := ranges[index-1], ranges[index]
		if current.start == previous.start {
			return nil, fmt.Errorf("%w: edits share start offset %d in one section", types.ErrInvalidReadingNote, current.start)
		}
		if current.start < previous.end {
			return nil, fmt.Errorf("%w: edits overlap in one section", types.ErrInvalidReadingNote)
		}
	}
	for left, right := 0, len(ranges)-1; left < right; left, right = left+1, right-1 {
		ranges[left], ranges[right] = ranges[right], ranges[left]
	}
	return ranges, nil
}
