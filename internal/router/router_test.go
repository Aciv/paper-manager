package router

import (
	"reflect"
	"testing"
)

func TestCORSOrigins(t *testing.T) {
	if got, want := corsOrigins(" https://one.example,https://two.example "), []string{"https://one.example", "https://two.example"}; !reflect.DeepEqual(got, want) {
		t.Errorf("corsOrigins() = %#v, want %#v", got, want)
	}
	if got, want := corsOrigins(" , "), []string{"http://localhost:5173"}; !reflect.DeepEqual(got, want) {
		t.Errorf("corsOrigins() fallback = %#v, want %#v", got, want)
	}
}
