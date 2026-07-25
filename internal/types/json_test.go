package types

import (
	"encoding/json"
	"testing"
)

func TestJSONMarshalsAsRawJSON(t *testing.T) {
	encoded, err := json.Marshal(JSON(`{"read":true}`))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got, want := string(encoded), `{"read":true}`; got != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}
