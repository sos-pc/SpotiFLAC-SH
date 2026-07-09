package main

import (
	"fmt"
	"net"
	"net/url"
)

// ValidateExternalURL rejects URLs that could be used for SSRF against the
// server's own network: non-http(s) schemes, missing host, or a host that
// resolves to a loopback / private / link-local / unspecified address.
// Used to vet both operator-supplied proxy URLs (PUT /api/v1/apis/proxies)
// and proxy URLs pulled from the third-party discovery feed
// (tidal-uptime.geeked.wtf) before either is used as the base of an
// outbound request made by the server.
//
// This is a save-time / ingest-time check, not a request-time one — it does
// not protect against DNS rebinding (a hostname resolving to a public IP
// now and a private one at request time). Full protection would require
// validating the resolved IP at dial time.
func ValidateExternalURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL %q: scheme must be http or https", raw)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL %q: missing host", raw)
	}

	if ip := net.ParseIP(host); ip != nil {
		if isDisallowedIP(ip) {
			return fmt.Errorf("URL %q: host resolves to a disallowed address (%s)", raw, ip)
		}
		return nil
	}

	// Hostname — resolve and check every returned address. Best-effort: if
	// the lookup itself fails (transient DNS hiccup, no network at
	// validation time), don't hard-fail here — the outbound request will
	// simply fail on its own later. We only block hosts we can affirmatively
	// see resolve to something unsafe.
	ips, lookupErr := net.LookupIP(host)
	if lookupErr != nil {
		return nil
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("URL %q: host %q resolves to a disallowed address (%s)", raw, host, ip)
		}
	}
	return nil
}

func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
