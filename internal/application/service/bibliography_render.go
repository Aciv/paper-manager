package service

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"paperMaster/internal/types"
)

var bibliographyNonKeyCharacter = regexp.MustCompile(`[^a-z0-9]+`)

func bibliographyMissingLocalFields(metadata types.PaperMetadata) []string {
	missing := make([]string, 0, 4)
	if strings.TrimSpace(metadata.Title) == "" {
		missing = append(missing, "title")
	}
	if len(metadata.Authors) == 0 {
		missing = append(missing, "authors")
	}
	if metadata.PublishedAt == nil {
		missing = append(missing, "year")
	}
	if strings.TrimSpace(metadata.Venue) == "" {
		missing = append(missing, "venue")
	}
	return missing
}

func bibliographyLocalKeyBase(metadata types.PaperMetadata) string {
	author := "paper"
	if len(metadata.Authors) > 0 {
		parts := strings.Fields(metadata.Authors[0].Name)
		if len(parts) > 0 {
			author = parts[len(parts)-1]
		}
	}
	year := "nd"
	if metadata.PublishedAt != nil {
		year = fmt.Sprintf("%04d", metadata.PublishedAt.Year())
	}
	titleWord := "work"
	if parts := strings.Fields(metadata.Title); len(parts) > 0 {
		for _, part := range parts {
			if normalized := bibliographyKeyPart(part); normalized != "" {
				titleWord = normalized
				break
			}
		}
	}
	key := bibliographyKeyPart(author) + year + titleWord
	if key == "" || key[0] >= '0' && key[0] <= '9' {
		key = "paper" + key
	}
	return key
}

func bibliographyKeyPart(value string) string {
	decomposed, _, err := transform.String(transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	})), strings.ToLower(value))
	if err != nil {
		decomposed = strings.ToLower(value)
	}
	return bibliographyNonKeyCharacter.ReplaceAllString(decomposed, "")
}

func renderLocalBibliographyEntry(key, doi string, metadata types.PaperMetadata) (string, error) {
	if strings.TrimSpace(metadata.Title) == "" {
		return "", errors.New("local bibliography entry requires a title")
	}
	fields := make([]string, 0, 9)
	if len(metadata.Authors) > 0 {
		authors := make([]string, 0, len(metadata.Authors))
		for _, author := range metadata.Authors {
			if name := strings.TrimSpace(author.Name); name != "" {
				authors = append(authors, escapeBibliographyValue(name))
			}
		}
		if len(authors) > 0 {
			fields = append(fields, bibliographyField("author", strings.Join(authors, " and ")))
		}
	}
	fields = append(fields, bibliographyField("title", escapeBibliographyValue(strings.TrimSpace(metadata.Title))))
	if metadata.PublishedAt != nil {
		fields = append(fields, bibliographyField("year", fmt.Sprintf("%04d", metadata.PublishedAt.Year())))
	}
	if value := strings.TrimSpace(metadata.Venue); value != "" {
		fields = append(fields, bibliographyField("howpublished", escapeBibliographyValue(value)))
	}
	for _, item := range []struct{ name, value string }{
		{"volume", metadata.Volume}, {"number", metadata.Issue}, {"pages", metadata.Pages},
		{"doi", doi}, {"url", metadata.SourceURL},
	} {
		if value := strings.TrimSpace(item.value); value != "" {
			fields = append(fields, bibliographyField(item.name, escapeBibliographyValue(value)))
		}
	}
	return "@misc{" + key + ",\n  " + strings.Join(fields, ",\n  ") + "\n}", nil
}

func bibliographyField(name, value string) string { return name + " = {" + value + "}" }

func escapeBibliographyValue(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch character {
		case '\\':
			builder.WriteString(`\textbackslash{}`)
		case '{', '}', '#', '$', '%', '&', '_':
			builder.WriteByte('\\')
			builder.WriteRune(character)
		case '~':
			builder.WriteString(`\textasciitilde{}`)
		case '^':
			builder.WriteString(`\textasciicircum{}`)
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func bibliographyFilename(name string) string {
	stem := bibliographyKeyPart(name)
	if stem == "" {
		stem = "bibliography"
	}
	if len(stem) > 80 {
		stem = stem[:80]
	}
	return stem + ".bib"
}
