package file

import (
	"errors"
	"testing"

	"paperMaster/internal/types/interfaces"
)

func TestApplyTextEditsInsertDeleteAndReplaceComposition(t *testing.T) {
	content := "# 标题\n甲乙丙\n"
	heading := 5 // len("# 标题\n") in code points
	updated, err := ApplyTextEdits(content, []interfaces.TextEdit{
		{Kind: interfaces.TextEditInsert, Offset: heading, Text: "X"},
		{Kind: interfaces.TextEditDelete, Offset: heading + 1, Length: 2},
	})
	if err != nil || updated != "# 标题\nX丙\n" {
		t.Fatalf("apply = %q, error=%v", updated, err)
	}
	replaced, err := ApplyTextEdits(content, []interfaces.TextEdit{
		{Kind: interfaces.TextEditDelete, Offset: heading, Length: 3},
		{Kind: interfaces.TextEditInsert, Offset: heading, Text: "new"},
	})
	if err != nil || replaced != "# 标题\nnew\n" {
		t.Fatalf("replace = %q, error=%v", replaced, err)
	}
}

func TestApplyTextEditsRejectsInvalidRangesAndUTF8Splits(t *testing.T) {
	for _, edits := range [][]interfaces.TextEdit{
		{{Kind: interfaces.TextEditInsert, Offset: 99, Text: "x"}},
		{{Kind: interfaces.TextEditInsert, Offset: 0, Text: ""}},
		{{Kind: interfaces.TextEditInsert, Offset: 0, Text: string([]byte{0xff})}},
		{{Kind: interfaces.TextEditDelete, Offset: 0, Length: 0}},
		{{Kind: interfaces.TextEditDelete, Offset: 0, Length: 99}},
		{{Kind: "unknown"}},
	} {
		if _, err := ApplyTextEdits("甲乙\n", edits); !errors.Is(err, interfaces.ErrInvalidTextEdit) {
			t.Fatalf("edits %#v error = %v", edits, err)
		}
	}
}

func TestApplyTextEditsUsesCodePointsRatherThanBytes(t *testing.T) {
	updated, err := ApplyTextEdits("甲乙丙", []interfaces.TextEdit{{Kind: interfaces.TextEditInsert, Offset: 1, Text: "X"}})
	if err != nil || updated != "甲X乙丙" {
		t.Fatalf("insert = %q, error=%v", updated, err)
	}
	deleted, err := ApplyTextEdits("甲乙丙", []interfaces.TextEdit{{Kind: interfaces.TextEditDelete, Offset: 1, Length: 1}})
	if err != nil || deleted != "甲丙" {
		t.Fatalf("delete = %q, error=%v", deleted, err)
	}
}
