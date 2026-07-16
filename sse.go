package main

// ─────────────────────────────────────────────────────────────────────────────
// SSE — Server-Sent Events pour la progression des jobs
//
// GET /api/v1/jobs/stream → text/event-stream
//
// Événements :
//   event: job_update       — état d'un job (pending/downloading/done/failed/skipped)
//   event: job_deleted      — job supprimé (Clear Completed/All — voir jobs_storage.go)
//   event: connected        — snapshot initial de la queue au moment de la connexion
//   event: watchlist_synced — fin de synchronisation d'une watchlist
//   event: watchlist_repaired — fin de réparation d'une watchlist (voir api_admin.go)
//   event: library_rebuild_done — fin du scan/import de bibliothèque (voir api_admin.go)
//   event: retag_incomplete_metadata_done — fin du retag des métadonnées incomplètes (voir api_admin.go)
//   event: server_log       — ligne de log backend (admin only, voir logbuffer.go)
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type JobEvent struct {
	Type string      `json:"type"`
	Job  *Job        `json:"job,omitempty"`
	Data interface{} `json:"data,omitempty"` // payload pour les events non-job (ex: watchlist_synced)
}

// ─────────────────────────────────────────────────────────────────────────────
// SSEHub — fan-out des événements vers tous les clients connectés
// ─────────────────────────────────────────────────────────────────────────────

type SSEHub struct {
	mu   sync.RWMutex
	subs map[chan JobEvent]struct{}
}

func newSSEHub() *SSEHub {
	return &SSEHub{subs: make(map[chan JobEvent]struct{})}
}

// subscribe crée un canal dédié au client et l'enregistre dans le hub.
func (h *SSEHub) subscribe() chan JobEvent {
	ch := make(chan JobEvent, 32) // buffer pour absorber les pics
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// unsubscribe retire le canal du hub et le ferme.
func (h *SSEHub) unsubscribe(ch chan JobEvent) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
	close(ch)
}

// publish diffuse un événement à tous les abonnés.
// Les canaux trop lents sont ignorés (select default) pour ne pas bloquer.
func (h *SSEHub) publish(event JobEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- event:
		default:
			// consommateur trop lent — on skip plutôt que bloquer
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers d'écriture SSE
// ─────────────────────────────────────────────────────────────────────────────

// sendSSEEvent écrit un événement SSE et flush immédiatement.
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
	flusher.Flush()
}

// sendSSEComment écrit une ligne de commentaire SSE et flush. Un commentaire
// (préfixe ":") est le no-op du protocole : les clients l'ignorent totalement,
// donc rien n'atteint onmessage ni aucun handler d'event — c'est exactement ce
// qu'il faut pour un keepalive, qui ne doit rien signifier côté application.
func sendSSEComment(w http.ResponseWriter, flusher http.Flusher, text string) {
	fmt.Fprintf(w, ": %s\n\n", text)
	flusher.Flush()
}

// sseHeartbeatInterval est la période d'émission du keepalive sur un flux
// inactif.
//
// Sans lui, ce flux n'écrit rien tant qu'aucun job ne tourne, et tout reverse
// proxy finit par couper la connexion amont restée muette : nginx le fait à
// proxy_read_timeout, soit 240s dans le proxy.conf par défaut de SWAG. Le
// navigateur reconnecte alors tout seul (EventSource le fait nativement), le
// handler renvoie le snapshot complet des jobs sur 48h, et le cycle recommence
// — toutes les 4 minutes, indéfiniment, à ne rien faire.
//
// 30s laisse une marge large sous les 240s de nginx, et reste sous les seuils
// plus courts qu'on croise ailleurs (Cloudflare coupe vers 100s). Le coût est
// négligeable : quelques octets par client et par minute.
//
// Variable et non constante pour que les tests puissent la réduire.
var sseHeartbeatInterval = 30 * time.Second

// ─────────────────────────────────────────────────────────────────────────────
// Handler SSE — GET /api/v1/jobs/stream
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1JobsStream(w http.ResponseWriter, r *http.Request) {
	// Vérifier que le client accepte les SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeV1Error(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// No "Connection: keep-alive" here: it's an HTTP/1.1 hop-by-hop header,
	// forbidden by the HTTP/2 spec (RFC 9113 §8.2.2) since h2 multiplexes
	// over one connection and has no such semantic. Go's own h2 server
	// strips it automatically, but a reverse proxy in front (nginx,
	// Cloudflare, etc.) forwarding it verbatim to a browser negotiating
	// HTTP/2 can turn it into a protocol violation the browser flags as
	// net::ERR_HTTP2_PROTOCOL_ERROR on this exact kind of long-lived
	// streaming response — better to never set it at all.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no") // désactive le buffering nginx

	// Résoudre le user AVANT le snapshot pour appliquer le filtre dès la connexion
	user := GetUserFromContext(r)

	// Snapshot initial — envoyer les jobs récents (actifs + terminaux des 48h)
	// Filtré par userID pour éviter les fuites de données entre utilisateurs
	cutoff := time.Now().Add(-48 * time.Hour)
	if jobs, err := s.ctr.Jobs.GetAllJobs(); err == nil {
		for i := range jobs {
			j := &jobs[i]
			// Filtre utilisateur : non-admin ne voit que ses propres jobs
			if user != nil && !user.IsAdmin && j.UserID != "" && j.UserID != user.UserID {
				continue
			}
			// Borne temporelle : jobs actifs toujours inclus, terminaux limités à 48h
			if j.Status != StatusPending && j.Status != StatusDownloading {
				if j.UpdatedAt.Before(cutoff) {
					continue
				}
			}
			sendSSEEvent(w, flusher, "job_update", j)
		}
	}

	// S'abonner au hub
	ch := s.ctr.Jobs.hub.subscribe()
	defer s.ctr.Jobs.hub.unsubscribe(ch)

	// Signal de connexion établie
	sendSSEEvent(w, flusher, "connected", map[string]string{"status": "ok"})

	heartbeat := time.NewTicker(sseHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-heartbeat.C:
			// Garde la connexion vivante à travers le reverse proxy quand aucun
			// job ne produit d'événement — voir sseHeartbeatInterval.
			sendSSEComment(w, flusher, "keepalive")
		case event, ok := <-ch:
			if !ok {
				return
			}
			// server_log expose des chemins fichiers et des détails d'erreur
			// potentiellement liés à d'autres utilisateurs — admin only.
			if event.Type == "server_log" {
				if user == nil || !user.IsAdmin {
					continue
				}
			} else if user != nil && !user.IsAdmin && event.Job != nil &&
				// Filtrer par userID si non-admin (uniquement pour les events job)
				event.Job.UserID != "" && event.Job.UserID != user.UserID {
				continue
			}
			// Envoyer Job ou Data selon le type d'événement
			var payload interface{}
			if event.Job != nil {
				payload = event.Job
			} else {
				payload = event.Data
			}
			sendSSEEvent(w, flusher, event.Type, payload)
		case <-r.Context().Done():
			return
		}
	}
}
