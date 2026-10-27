package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// resolveWebBaseURL uses an advertised URL or the known same-process static
// listener. API-only processes must not infer a frontend from CORS/MCP ports.
func resolveWebBaseURL(raw, httpAddress, webDirectory string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && strings.TrimSpace(webDirectory) != "" {
		host, port, err := net.SplitHostPort(httpAddress)
		if err != nil {
			return "", fmt.Errorf("resolve static Web address: %w", err)
		}
		switch host {
		case "", "0.0.0.0", "*":
			host = "127.0.0.1"
		case "::":
			host = "::1"
		}
		raw = "http://" + net.JoinHostPort(host, port) + "/"
	}
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("WEB_BASE_URL must be an absolute HTTP(S) URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.Hostname() == "0.0.0.0" || parsed.Hostname() == "::" || parsed.Hostname() == "*" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("WEB_BASE_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", fmt.Errorf("WEB_BASE_URL has an invalid port")
		}
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
		// Keep an escaped deployment prefix consistent with the appended slash.
		if parsed.RawPath != "" {
			parsed.RawPath += "/"
		}
	}
	return parsed.String(), nil
}
