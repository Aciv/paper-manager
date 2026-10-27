package config

import "testing"

func TestResolveWebBaseURL(t *testing.T) {
	for _, test := range []struct {
		name, raw, address, dist, want string
	}{
		{"explicit prefix", "https://papers.example/app", "127.0.0.1:8080", "", "https://papers.example/app/"},
		{"escaped prefix", "https://papers.example/a%20b", "", "", "https://papers.example/a%20b/"},
		{"desktop", "", "127.0.0.1:28453", "web", "http://127.0.0.1:28453/"},
		{"wildcard static", "", "0.0.0.0:12345", "web", "http://127.0.0.1:12345/"},
		{"ipv6 static", "", "[::]:12345", "web", "http://[::1]:12345/"},
		{"API only", "", "127.0.0.1:8080", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveWebBaseURL(test.raw, test.address, test.dist)
			if err != nil || got != test.want {
				t.Fatalf("got %q %v, want %q", got, err, test.want)
			}
		})
	}
	for _, invalid := range []string{
		"localhost:5173", "file:///tmp/report.md", "http://0.0.0.0:8080/", "http://host:0/",
		"http://host:99999/", "https://user:secret@host/", "https://host/?token=secret", "https://host/#/library",
	} {
		if _, err := resolveWebBaseURL(invalid, "", ""); err == nil {
			t.Fatal("invalid Web base URL was accepted")
		}
	}
}

func TestLoadWebBaseURL(t *testing.T) {
	t.Setenv("WEB_BASE_URL", "https://papers.example/library-root")
	cfg, err := Load()
	if err != nil || cfg.WebBaseURL != "https://papers.example/library-root/" {
		t.Fatalf("Web URL load: %q %v", cfg.WebBaseURL, err)
	}
	t.Setenv("WEB_BASE_URL", "https://user:secret@host/")
	if _, err := Load(); err == nil {
		t.Fatal("invalid advertised URL did not fail startup")
	}
}
