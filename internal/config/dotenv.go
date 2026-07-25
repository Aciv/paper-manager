package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv applies values from a dotenv file when the corresponding process
// environment variable is not already set. The file is optional so containers
// and production deployments can rely solely on their injected environment.
// Existing environment variables always win over local developer defaults.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dotenv file %q: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Permit long connection strings or allow lists without silently truncating
	// a valid configuration line at Scanner's small default token limit.
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			return fmt.Errorf("parse dotenv file %q line %d: expected KEY=VALUE", path, lineNumber)
		}
		key := strings.TrimSpace(line[:separator])
		if !validEnvKey(key) {
			return fmt.Errorf("parse dotenv file %q line %d: invalid variable name %q", path, lineNumber, key)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(line[separator+1:]))
		if err != nil {
			return fmt.Errorf("parse dotenv file %q line %d: %w", path, lineNumber, err)
		}

		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set environment variable %q from %q: %w", key, path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read dotenv file %q: %w", path, err)
	}
	return nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range key {
		if char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func parseDotEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		end := strings.IndexByte(value[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		end++
		if rest := strings.TrimSpace(value[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", fmt.Errorf("unexpected characters after quoted value")
		}
		return value[1:end], nil
	}
	if value[0] == '"' {
		end := 1
		for end < len(value) {
			if value[end] == '"' && value[end-1] != '\\' {
				break
			}
			end++
		}
		if end == len(value) {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		if rest := strings.TrimSpace(value[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", fmt.Errorf("unexpected characters after quoted value")
		}
		parsed, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", fmt.Errorf("invalid double-quoted value: %w", err)
		}
		return parsed, nil
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimSpace(value), nil
}
