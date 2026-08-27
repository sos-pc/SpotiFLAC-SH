package util

// ChromeUserAgent is the browser User-Agent every outbound request sends to an
// endpoint that expects a browser — Spotify's web player APIs, Apple Music's
// public genre lookup, the provider clients, the image uploader.
//
// It lives in `util` rather than in `providerutil`, where it started, because
// providerutil imports backend/meta: a package in meta that needs the string
// (genre_apple.go) cannot import providerutil without an import cycle. util
// imports nothing internal at all, so every caller can reach it.
//
// One string, and the reason to keep it one: it is a value that ROTATES. Real
// Chrome moves a major version every few weeks, and a User-Agent that drifts
// far behind is what bot detection notices. Bumping it has to be a single edit
// or it becomes a hunt — the string was written out ten more times across
// spotify/, meta/ and the uploader while a shared constant already existed two
// packages away, so the last bump reached four call sites and missed the rest.
//
// Chrome 146 as of 2026-08-27, matching what upstream spotbye/SpotiFLAC sends
// (backend/http_headers.go). They are the closest thing to a live reference for
// this: same endpoints, same scraping surface, and they bump it when it starts
// mattering.
const ChromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
