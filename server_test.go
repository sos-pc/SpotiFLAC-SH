package main

import (
	"net/http"
	"testing"
)

func makeRequest(remoteAddr, xForwardedFor, xRealIP string) *http.Request {
	r := &http.Request{Header: make(http.Header), RemoteAddr: remoteAddr}
	if xForwardedFor != "" { r.Header.Set("X-Forwarded-For", xForwardedFor) }
	if xRealIP != ""       { r.Header.Set("X-Real-IP", xRealIP) }
	return r
}

func TestIsLocalIP(t *testing.T) {
	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		want          bool
	}{
		{"loopback IPv4",           "127.0.0.1:1234",    "", "",             true},
		{"loopback IPv6",           "[::1]:1234",         "", "",             true},
		{"LAN 192.168.x",           "192.168.1.50:5678", "", "",             true},
		{"LAN 10.x",                "10.0.0.5:1234",     "", "",             true},
		{"Docker bridge 172.17.x",  "172.17.0.1:1234",   "", "",             true},
		{"IP publique refusée",     "8.8.8.8:1234",      "", "",             false},
		{"X-Forwarded-For → refus", "192.168.1.50:1234", "8.8.8.8", "",     false},
		{"X-Real-IP → refus",       "127.0.0.1:1234",    "", "203.0.113.1", false},
		{"IP invalide",             "invalid",            "", "",             false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLocalIP(makeRequest(tt.remoteAddr, tt.xForwardedFor, tt.xRealIP))
			if got != tt.want {
				t.Errorf("isLocalIP(%q, fwd=%q, real=%q) = %v, want %v",
					tt.remoteAddr, tt.xForwardedFor, tt.xRealIP, got, tt.want)
			}
		})
	}
}
