package doicitation

import (
	"errors"
	"strings"
	"unicode"
)

// inspectSingleBibTeXEntry performs bounded structural validation without
// rewriting provider text and returns the one observed citation key.
func inspectSingleBibTeXEntry(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value[0] != '@' {
		return "", errors.New("BibTeX entry must start with @")
	}
	i := 1
	for i < len(value) && ((value[i] >= 'A' && value[i] <= 'Z') || (value[i] >= 'a' && value[i] <= 'z')) {
		i++
	}
	entryType := strings.ToLower(value[1:i])
	if entryType == "" || entryType == "comment" || entryType == "preamble" || entryType == "string" {
		return "", errors.New("BibTeX response is not one bibliographic entry")
	}
	for i < len(value) && unicode.IsSpace(rune(value[i])) {
		i++
	}
	if i >= len(value) || (value[i] != '{' && value[i] != '(') {
		return "", errors.New("BibTeX entry has no body")
	}
	opener := value[i]
	closer := byte('}')
	if opener == '(' {
		closer = ')'
	}
	i++
	keyStart := i
	for i < len(value) && value[i] != ',' {
		if value[i] == opener || value[i] == closer || value[i] == '\n' || value[i] == '\r' {
			return "", errors.New("BibTeX entry has an invalid key")
		}
		i++
	}
	if i >= len(value) {
		return "", errors.New("BibTeX entry key is not terminated")
	}
	key := strings.TrimSpace(value[keyStart:i])
	if key == "" {
		return "", errors.New("BibTeX entry key is empty")
	}
	depth, inQuote, escaped := 1, false, false
	for i++; i < len(value); i++ {
		character := value[i]
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if character == opener {
			depth++
		} else if character == closer {
			depth--
			if depth == 0 {
				if strings.TrimSpace(value[i+1:]) != "" {
					return "", errors.New("BibTeX response contains trailing content or multiple entries")
				}
				return key, nil
			}
		}
	}
	return "", errors.New("BibTeX entry is not closed")
}
