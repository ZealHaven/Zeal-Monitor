// Package api exposes the HTTP surface: REST handlers, the SSE hub and the
// reverse proxy to the frontend.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"monitor/backend/main/internal/audit"
	"monitor/backend/main/internal/harness"
	"monitor/backend/main/internal/inventory"
)

// Server serves the JSON/SSE API under /api.
type Server struct {
	inv     *inventory.Store
	harness *harness.Manager
	logger  *slog.Logger
}

func New(inv *inventory.Store, h *harness.Manager, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{inv: inv, harness: h, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{pid}", s.handleService)
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	return corsMiddleware(mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	// Configurable for local-first use. Defaults to "*" so the SvelteKit SSR
	// server can call the API directly; set MONITOR_CORS_ORIGIN to restrict it.
	origin := os.Getenv("MONITOR_CORS_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "-" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"services":   s.inv.Snapshot(),
		"scanned_at": s.inv.ScannedAt(),
	})
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		http.Error(w, "invalid pid", http.StatusBadRequest)
		return
	}
	svc, ok := s.inv.Find(pid)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	services := s.inv.Scan(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"services":   services,
		"scanned_at": s.inv.ScannedAt(),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering

	ch := s.inv.Subscribe()
	defer s.inv.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case services := <-ch:
			data, err := json.Marshal(services)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if s.harness == nil {
		http.Error(w, "chat unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		http.Error(w, "empty message", http.StatusBadRequest)
		return
	}
	if body.SessionID == "" {
		body.SessionID = newSessionID()
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// send session id first
	fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{"type": "session", "session_id": body.SessionID}))
	flusher.Flush()

	ctx := r.Context()
	events := make(chan harness.Event, 64)
	done := make(chan error, 1)
	go func() {
		done <- s.harness.Run(ctx, body.SessionID, body.Message, func(ev harness.Event) {
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		})
		close(events)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// process finished; wait for exit err
				err := <-done
				if err != nil {
					fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{"type": "error", "error": err.Error()}))
				}
				fmt.Fprintf(w, "data: %s\n\n", mustMarshal(map[string]any{"type": "done", "session_id": body.SessionID}))
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", mustMarshal(ev))
			flusher.Flush()
		}
	}
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			n = i
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": audit.Read(n)})
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ses_" + hex.EncodeToString(b)
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
