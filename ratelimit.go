package main

import (
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

const (
	rlMaxAttempts  = 10              // tentatives autorisées par fenêtre
	rlWindow       = time.Minute     // durée de la fenêtre
	rlBlockDur     = 5 * time.Minute // durée du blocage après dépassement
	rlCleanupEvery = 10 * time.Minute
)

type rlEntry struct {
	attempts     int
	windowEnd    time.Time
	blockedUntil time.Time
}

// LoginRateLimiter limite les tentatives de login par IP.
type LoginRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rlEntry
}

func NewLoginRateLimiter() *LoginRateLimiter {
	rl := &LoginRateLimiter{entries: make(map[string]*rlEntry)}
	util.SafeGo("ratelimit.cleanupLoop", rl.cleanupLoop)
	return rl
}

// Allow retourne true si la requête est autorisée, false si elle doit être rejetée.
func (rl *LoginRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	e, ok := rl.entries[ip]
	if !ok {
		e = &rlEntry{}
		rl.entries[ip] = e
	}

	// IP bloquée ?
	if now.Before(e.blockedUntil) {
		return false
	}

	// Fenêtre expirée → remettre à zéro
	if now.After(e.windowEnd) {
		e.attempts = 0
		e.windowEnd = now.Add(rlWindow)
	}

	e.attempts++
	if e.attempts > rlMaxAttempts {
		e.blockedUntil = now.Add(rlBlockDur)
		return false
	}
	return true
}

func (rl *LoginRateLimiter) cleanupLoop() {
	t := time.NewTicker(rlCleanupEvery)
	defer t.Stop()
	for range t.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, e := range rl.entries {
			if now.After(e.blockedUntil) && now.After(e.windowEnd) {
				delete(rl.entries, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// trustProxyHeaders reports whether TRUST_PROXY_HEADERS=true is set — i.e.
// the operator confirms the server sits behind a reverse proxy that
// overwrites X-Forwarded-For/X-Real-IP on every incoming request, so those
// headers can't be forged by the client itself.
func trustProxyHeaders() bool {
	return os.Getenv("TRUST_PROXY_HEADERS") == "true"
}

// remoteIP extrait l'IP de la requête. X-Forwarded-For / X-Real-IP ne sont
// pris en compte que si TRUST_PROXY_HEADERS=true est explicitement défini.
//
// Se fier au header dès que le pair TCP direct est privé/loopback (l'ancien
// comportement) est contournable par n'importe quel client sur le LAN, ou
// derrière un simple bridge Docker : il lui suffit d'envoyer une IP
// aléatoire dans X-Forwarded-For à chaque requête pour rendre le rate
// limiter de login totalement inefficace. Sans opt-in explicite, on ignore
// donc ces headers et on retombe sur l'adresse TCP réelle.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if trustProxyHeaders() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Le maillon le plus à droite est celui ajouté par le reverse
			// proxy de confiance le plus proche — c'est le seul qu'un
			// client externe ne peut pas forger (un proxy correctement
			// configuré ajoute au header au lieu de le remplacer).
			parts := splitComma(xff)
			for i := len(parts) - 1; i >= 0; i-- {
				if candidate := net.ParseIP(trimSpace(parts[i])); candidate != nil {
					return candidate.String()
				}
			}
		}
		if xrip := trimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
			if candidate := net.ParseIP(xrip); candidate != nil {
				return candidate.String()
			}
		}
	}
	return host
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
