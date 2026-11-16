package config

import (
	"strings"
	"testing"
)

func TestFactoryURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		allow     bool
	}{
		{"HTTPS", "https://factory.example.com", true},
		{"HTTP port and base path", "http://factory.example.com:8080/base", true},
		{"IPv6 authority", "https://[2001:db8::1]:8443/base", true},
		{"encoded path", "https://factory.example.com/base%20path", true},
		{"missing authority", "https:///", false},
		{"empty hostname with port", "https://:443", false},
		{"path whitespace", "https://factory.example.com/a b", false},
		{"path backtick", "https://factory.example.com/`id`", false},
		{"path dollar", "https://factory.example.com/$(id)", false},
		{"path backslash", "https://factory.example.com/\\id", false},
		{"invalid escape", "https://factory.example.com/%xx", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := map[string]any{"version": "v1.35", "talos": map[string]any{"imageFactoryURL": tc.url}}
			_, err := Resolve(nil, pool)
			if (err == nil) != tc.allow {
				t.Fatalf("URL %q: error=%v, want allowed=%v", tc.url, err, tc.allow)
			}
			if err != nil && !strings.Contains(err.Error(), "imageFactoryURL") {
				t.Fatalf("wrong validation error: %v", err)
			}
			pool["osImage"] = map[string]any{"builtin": map[string]any{}}
			if _, err := Resolve(nil, pool); err != nil {
				t.Fatalf("unused factory URL rejected on a builtin source: %v", err)
			}
		})
	}
}
