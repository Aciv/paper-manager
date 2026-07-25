package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvLoadsUnsetValuesAndPreservesProcessEnvironment(t *testing.T) {
	const (
		loadedKey   = "PAPERS_MANAGER_DOTENV_TEST_LOADED"
		existingKey = "PAPERS_MANAGER_DOTENV_TEST_EXISTING"
		quotedKey   = "PAPERS_MANAGER_DOTENV_TEST_QUOTED"
	)
	unsetEnvForTest(t, loadedKey)
	unsetEnvForTest(t, quotedKey)
	t.Setenv(existingKey, "from-process")

	path := filepath.Join(t.TempDir(), ".env")
	content := "# local configuration\n" +
		loadedKey + "=from-file\n" +
		existingKey + "=must-not-replace\n" +
		quotedKey + "=\"quoted value\" # comment\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write dotenv fixture: %v", err)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}
	if got := os.Getenv(loadedKey); got != "from-file" {
		t.Errorf("loaded value = %q, want %q", got, "from-file")
	}
	if got := os.Getenv(existingKey); got != "from-process" {
		t.Errorf("existing value = %q, want process value", got)
	}
	if got := os.Getenv(quotedKey); got != "quoted value" {
		t.Errorf("quoted value = %q, want %q", got, "quoted value")
	}
}

func TestLoadDotEnvRejectsMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("NOT_A_SETTING\n"), 0o600); err != nil {
		t.Fatalf("write dotenv fixture: %v", err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("LoadDotEnv() error = nil, want malformed-line error")
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	previous, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(key, previous)
			return
		}
		_ = os.Unsetenv(key)
	})
}
