package main

import (
	"net/http"
	"testing"
)

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public https", "https://eu-central.monochrome.tf", false},
		{"public http", "http://api.example.com/track", false},
		{"public https with port", "https://api.example.com:8443/x", false},
		{"non-http scheme", "ftp://example.com", true},
		{"file scheme", "file:///etc/passwd", true},
		{"missing host", "https:///path", true},
		{"invalid URL", "http://[::1", true},
		{"loopback IP literal", "http://127.0.0.1/track", true},
		{"loopback IPv6 literal", "http://[::1]/track", true},
		{"localhost hostname", "http://localhost/track", true},
		{"private 10.x literal", "http://10.0.0.5/track", true},
		{"private 192.168.x literal", "http://192.168.1.10/track", true},
		{"link-local / cloud metadata", "http://169.254.169.254/latest/meta-data", true},
		{"unspecified", "http://0.0.0.0/track", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExternalURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestIsSubPath(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		target string
		want   bool
	}{
		{"same path", "/music", "/music", true},
		{"nested one level", "/music", "/music/playlist1", true},
		{"nested deep", "/music", "/music/a/b/c", true},
		{"sibling directory", "/music", "/music-backup", false},
		{"unrelated root", "/music", "/etc", false},
		{"parent of root", "/music", "/", false},
		{"dot-dot escape", "/music", "/music/../etc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSubPath(tt.root, tt.target)
			if got != tt.want {
				t.Errorf("isSubPath(%q, %q) = %v, want %v", tt.root, tt.target, got, tt.want)
			}
		})
	}
}

func TestIsSameOriginRequest(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin header", "app.example.com", "", true},
		{"matching origin", "app.example.com", "https://app.example.com", true},
		{"matching origin with port", "app.example.com:6890", "http://app.example.com:6890", true},
		{"cross-origin attacker site", "app.example.com", "https://evil.example.net", false},
		{"malformed origin", "app.example.com", "not a url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{Header: make(http.Header), Host: tt.host}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			got := isSameOriginRequest(r)
			if got != tt.want {
				t.Errorf("isSameOriginRequest(host=%q, origin=%q) = %v, want %v", tt.host, tt.origin, got, tt.want)
			}
		})
	}
}

func TestRemoteIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	t.Setenv("TRUST_PROXY_HEADERS", "")
	r := makeRequest("192.168.1.50:1234", "203.0.113.9", "")
	if got := remoteIP(r); got != "192.168.1.50" {
		t.Errorf("remoteIP with no TRUST_PROXY_HEADERS = %q, want direct peer IP", got)
	}
}

func TestRemoteIPTrustsForwardedHeadersWhenConfigured(t *testing.T) {
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	r := makeRequest("192.168.1.50:1234", "203.0.113.9, 192.168.1.1", "")
	if got := remoteIP(r); got != "192.168.1.1" {
		t.Errorf("remoteIP with TRUST_PROXY_HEADERS=true = %q, want rightmost X-Forwarded-For entry", got)
	}
}
