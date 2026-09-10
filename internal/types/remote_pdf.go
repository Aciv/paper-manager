package types

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var (
	// ErrInvalidRemotePDFRequest means the caller supplied a malformed URL or
	// DOI before any remote connection was attempted.
	ErrInvalidRemotePDFRequest = errors.New("invalid remote PDF request")
	// ErrRemotePDFUnavailable means the supplied landing/source information
	// cannot be converted into a trusted downloadable PDF URL.
	ErrRemotePDFUnavailable = errors.New("remote PDF is unavailable")
)

var remoteDOIPattern = regexp.MustCompile(`(?i)10\.[0-9]{4,9}/[-._;()/:a-z0-9]+`)

// NormalizeDOI returns the stable lowercase DOI key used by documents and
// catalog lookups. It accepts plain values, doi: values, and DOI URLs.
func NormalizeDOI(value string) string {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil {
		host := strings.ToLower(parsed.Hostname())
		if host == "doi.org" || host == "dx.doi.org" {
			// get path, encoded
			value = strings.TrimPrefix(parsed.EscapedPath(), "/")
			// decode path
			if decoded, decodeErr := url.PathUnescape(value); decodeErr == nil {
				value = decoded
			}
		}
	}
	value = strings.TrimSpace(value)
	if len(value) >= 4 && strings.EqualFold(value[:4], "doi:") {
		value = strings.TrimSpace(value[4:])
	}
	match := remoteDOIPattern.FindString(value)
	if match == "" {
		return ""
	}
	return strings.ToLower(strings.TrimRight(match, " \t\r\n.,;:"))
}

// ExtractDOIFromURL extracts a DOI only from doi.org/dx.doi.org URLs or an
// explicit doi: value. It does not mistake a publisher path ending in .pdf
// for a DOI and never invents one from an arXiv identifier.
func ExtractDOIFromURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= 4 && strings.EqualFold(trimmed[:4], "doi:") {
		return NormalizeDOI(trimmed)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "doi.org" && host != "dx.doi.org" {
		return ""
	}
	return NormalizeDOI(trimmed)
}

// PaperURLResolveRequest is provider-neutral input for resolving one landing
// page or explicit PDF URL. ExplicitDOI wins over a DOI found in SourceURL.
type PaperURLResolveRequest struct {
	SourceURL   string
	ExplicitDOI string
}

// ResolvedPaperURL keeps the human-facing landing page separate from the
// byte-producing URL passed to FileStorage.SaveFromURL.
type ResolvedPaperURL struct {
	LandingURL  string
	DownloadURL string
	DOI         string
	Filename    string
}

// DocumentURLImportCommand is the HTTP/application command for importing one
// PDF from an explicit source URL.
type DocumentURLImportCommand struct {
	URL      string `json:"url"`
	DOI      string `json:"doi,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// Validate rejects incomplete or malformed URL import commands before any
// network request is made.
func (command DocumentURLImportCommand) Validate() error {
	parsed, err := url.Parse(strings.TrimSpace(command.URL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: url must be an absolute HTTP or HTTPS URL", ErrInvalidRemotePDFRequest)
	}
	if command.DOI != "" && NormalizeDOI(command.DOI) == "" {
		return fmt.Errorf("%w: doi is invalid", ErrInvalidRemotePDFRequest)
	}
	return nil
}

// SuggestedPDFFilename derives a safe metadata filename from a resolved URL.
func SuggestedPDFFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil {
		name := path.Base(parsed.Path)
		if strings.HasSuffix(strings.ToLower(name), ".pdf") && name != ".pdf" {
			return name
		}
	}
	return "paper.pdf"
}

// DocumentRemoteImportPayload is emitted only by an explicit user request to
// download one bibliographic placeholder. Reference parsing never creates it.
type DocumentRemoteImportPayload struct {
	RequestID  string `json:"request_id,omitempty"`
	DocumentID string `json:"document_id"`
	Generation int64  `json:"generation"`
}

func (payload DocumentRemoteImportPayload) Validate() error {
	id, err := uuid.Parse(strings.TrimSpace(payload.DocumentID))
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("document id is required")
	}
	if payload.Generation <= 0 {
		return fmt.Errorf("generation must be positive")
	}
	return nil
}
