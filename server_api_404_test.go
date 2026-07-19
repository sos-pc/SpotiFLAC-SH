package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An unknown /api/ path used to reach the SPA fallback and answer 200 with
// index.html. A client that mistyped an endpoint got "success" and an HTML
// page, and could only discover the truth by parsing the body — which is how
// several probes in this session were misread as working routes.
//
// The SPA fallback itself must survive: /watchlists and any other client-side
// route still has to serve index.html, since the server owns no such path.
func TestUnknownAPIPathIs404JSONWhileSPAFallbackSurvives(t *testing.T) {
	s := &Server{mux: http.NewServeMux()}
	s.registerRoutes()

	t.Run("route API inconnue", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/nope", nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type = %q, want JSON", ct)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not JSON (%v): %s", err, rec.Body.String())
		}
		if !strings.Contains(body["error"], "/api/v1/admin/nope") {
			t.Errorf("error %q does not name the path asked for", body["error"])
		}
	})

	t.Run("l'ancien nom de route ne réussit plus", func(t *testing.T) {
		// The rename in fb80bc1 looked unverifiable in prod because the old
		// path answered 200 — it was the SPA, not a surviving handler.
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/db/library/reconcile", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("une route client garde le repli SPA", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/watchlists", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 — the SPA fallback was broken", rec.Code)
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "<!doctype html") {
			t.Error("a client-side route no longer serves index.html")
		}
	})
}
