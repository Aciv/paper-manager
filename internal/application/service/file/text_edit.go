package file

import (
	"fmt"
	"unicode/utf8"

	"paperMaster/internal/types/interfaces"
)

// ApplyTextEdits applies primitive edits to UTF-8 text. Offsets and lengths are
// Unicode code-point positions; replace is composed as delete + insert.
func ApplyTextEdits(content string, edits []interfaces.TextEdit) (string, error) {
	result := content
	for index, edit := range edits {
		var err error
		switch edit.Kind {
		case interfaces.TextEditInsert:
			result, err = insertText(result, edit.Offset, edit.Text)
		case interfaces.TextEditDelete:
			result, err = deleteText(result, edit.Offset, edit.Length)
		default:
			err = fmt.Errorf("%w: edit %d has unknown kind %q", interfaces.ErrInvalidTextEdit, index+1, edit.Kind)
		}
		if err != nil {
			return "", fmt.Errorf("edit %d: %w", index+1, err)
		}
	}
	return result, nil
}

func insertText(content string, offset int, text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("%w: insert requires text", interfaces.ErrInvalidTextEdit)
	}
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("%w: inserted text must be UTF-8", interfaces.ErrInvalidTextEdit)
	}
	total := utf8.RuneCountInString(content)
	if offset < 0 || offset > total {
		return "", fmt.Errorf("%w: offset %d is outside the %d code points", interfaces.ErrInvalidTextEdit, offset, total)
	}
	byteOffset, err := runeOffset(content, offset)
	if err != nil {
		return "", err
	}
	return content[:byteOffset] + text + content[byteOffset:], nil
}

func deleteText(content string, offset, length int) (string, error) {
	total := utf8.RuneCountInString(content)
	if offset < 0 || length <= 0 || offset > total || length > total-offset {
		return "", fmt.Errorf("%w: delete range is outside the %d code points", interfaces.ErrInvalidTextEdit, total)
	}
	start, err := runeOffset(content, offset)
	if err != nil {
		return "", err
	}
	end, err := runeOffset(content, offset+length)
	if err != nil {
		return "", err
	}
	return content[:start] + content[end:], nil
}

// runeOffset converts a code-point offset to a byte offset. Iterating the
// string yields the byte index of each rune start, so the boundary never splits
// UTF-8.
func runeOffset(content string, offset int) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("%w: offset cannot be negative", interfaces.ErrInvalidTextEdit)
	}
	if offset == 0 {
		return 0, nil
	}
	runes := 0
	for byteOffset := range content {
		if runes == offset {
			return byteOffset, nil
		}
		runes++
	}
	if runes == offset {
		return len(content), nil
	}
	return 0, fmt.Errorf("%w: offset is outside content", interfaces.ErrInvalidTextEdit)
}
