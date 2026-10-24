// Package api exposes the HTTP surface: REST handlers, the SSE hub and the
// reverse proxy to the frontend.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor/backend/main/internal/audit"
	"monitor/backend/main/internal/harness"
	"monitor/backend/main/internal/inventory"
	"monitor/backend/main/internal/settings"
)

const maxSettingsBody = 64 << 10

// Server serves the JSON/SSE API under /api.
type Server struct {
	inv      *inventory.Store
	harness  *harness.Manager
	settings *settings.Store
	logger   *slog.Logger

	catMu   sync.Mutex
	catalog *catalogCache
}

// New creates the API server. settings may be nil in tests.
func New(inv *inventory.Store, h *harness.Manager, st *settings.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{inv: inv, harness: h, settings: st, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{pid}", s.handleService)
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	mux.HandleFunc("GET /api/settings/llm", s.handleGetLLMSettings)
	mux.HandleFunc("PUT /api/settings/llm", s.handlePutLLMSettings)
	mux.HandleFunc("POST /api/settings/llm/reload", s.handleReloadAgent)
	mux.HandleFunc("GET /api/llm/models", s.handleLLMModels)
	return corsMiddleware(mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	// MONITOR_CORS_ORIGIN: "-" disables CORS, "" allows same-origin only, any
	// other value is used verbatim (comma-separated origins are not split).
	configured := os.Getenv("MONITOR_CORS_ORIGIN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := ""
		switch {
		case configured == "-":
		case configured == "*":
			allowed = "*"
		case configured != "":
			if origin != "" && origin == configured {
				allowed = origin
			}
		case origin != "" && sameOrigin(origin, r.Host):
			allowed = origin
		}
		if allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
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

func sameOrigin(origin, host string) bool {
	u := strings.TrimSuffix(origin, "/")
	for _, p := range []string{"http://", "https://"} {
		if strings.HasPrefix(u, p) {
			return strings.TrimPrefix(u, p) == host
		}
	}
	return false
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
		done <- s.harness.Run(ctx, body.SessionID, body.Message, "", func(ev harness.Event) {
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

func (s *Server) agentStatus() map[string]any {
	ready := false
	url := ""
	if s.harness != nil {
		ready = s.harness.Ready()
		url = s.harness.ServerURL()
	}
	return map[string]any{"ready": ready, "url": url, "model": s.settings.Model()}
}

func (s *Server) handleGetLLMSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": s.settings.View(),
		"agent":    s.agentStatus(),
	})
}

func (s *Server) handlePutLLMSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	var patch settings.Patch
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	needsRestart := len(patch.Providers) > 0
	applied := "immediate"
	var err error
	if needsRestart && s.harness != nil {
		// Provider credentials are only read when the serve process starts. Hold
		// the chat-exclusion lock across save + restart so a live stream is never
		// torn down and the file never diverges from the running process.
		err = s.harness.WithExclusive(r.Context(), func() error {
			_, uerr := s.settings.Update(patch)
			return uerr
		})
		applied = "restart"
	} else {
		_, err = s.settings.Update(patch)
	}
	if err != nil {
		switch {
		case errors.Is(err, harness.ErrChatInFlight):
			writeError(w, http.StatusConflict, "chat_in_progress", "finish or cancel the current chat, then save")
		case settings.IsValidation(err):
			writeError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		default:
			s.logger.Error("update llm settings", "err", err)
			writeError(w, http.StatusInternalServerError, "save_failed", err.Error())
		}
		return
	}
	s.invalidateCatalog()
	s.logger.Info("llm settings updated", "model", s.settings.Model(), "restart", applied == "restart")
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": s.settings.View(),
		"agent":    s.agentStatus(),
		"applied":  applied,
	})
}

func (s *Server) handleReloadAgent(w http.ResponseWriter, r *http.Request) {
	if s.harness == nil {
		writeError(w, http.StatusServiceUnavailable, "chat_unavailable", "chat is not available")
		return
	}
	if err := s.harness.Restart(r.Context()); err != nil {
		if errors.Is(err, harness.ErrChatInFlight) {
			writeError(w, http.StatusConflict, "chat_in_progress", "finish or cancel the current chat, then reload")
			return
		}
		s.logger.Error("reload agent", "err", err)
		writeError(w, http.StatusInternalServerError, "reload_failed", err.Error())
		return
	}
	s.invalidateCatalog()
	writeJSON(w, http.StatusOK, map[string]any{"agent": s.agentStatus()})
}

type catalogModel struct {
	Ref        string   `json:"ref"`
	Name       string   `json:"name"`
	ProviderID string   `json:"provider_id"`
	ModelID    string   `json:"model_id"`
	Context    int      `json:"context"`
	Output     int      `json:"output"`
	Status     string   `json:"status"`
	Enabled    bool     `json:"enabled"`
	Variants   []string `json:"variants,omitempty"`
	InputCost  float64  `json:"input_cost,omitempty"`
	OutputCost float64  `json:"output_cost,omitempty"`
}

type catalogProvider struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Connected  bool           `json:"connected"`
	APIKeySet  bool           `json:"api_key_set"`
	APIKeyHint string         `json:"api_key_hint,omitempty"`
	KeySource  string         `json:"key_source,omitempty"`
	EnvVar     string         `json:"env_var,omitempty"`
	BaseURL    string         `json:"base_url,omitempty"`
	Models     []catalogModel `json:"models"`
}

type catalogPayload struct {
	Providers []catalogProvider `json:"providers"`
	Current   string            `json:"current"`
	Source    string            `json:"source"`
	Error     string            `json:"error,omitempty"`
}

type catalogCache struct {
	at      time.Time
	payload catalogPayload
}

// featuredProviders are surfaced ahead of the long tail of integrations so the
// common choices stay easy to find in the provider pickers.
var featuredProviders = map[string]bool{
	"anthropic":       true,
	"openai":          true,
	"google":          true,
	"google-vertex":   true,
	"openrouter":      true,
	"groq":            true,
	"mistral":         true,
	"xai":             true,
	"deepseek":        true,
	"cohere":          true,
	"cerebras":        true,
	"together":        true,
	"fireworks":       true,
	"perplexity":      true,
	"opencode":        true,
	"opencode-go":     true,
	"zai-coding-plan": true,
	"nvidia":          true,
	"omniroute":       true,
	"ollama":          true,
}

func providerRank(p catalogProvider) int {
	switch {
	case p.Connected && len(p.Models) > 0:
		return 0
	case p.Connected:
		return 1
	case featuredProviders[p.ID]:
		return 2
	case len(p.Models) > 0:
		return 3
	default:
		return 4
	}
}

func sortCatalog(providers []catalogProvider) {
	sort.SliceStable(providers, func(i, j int) bool {
		ri, rj := providerRank(providers[i]), providerRank(providers[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(providers[i].Name) < strings.ToLower(providers[j].Name)
	})
}

func (s *Server) invalidateCatalog() {
	s.catMu.Lock()
	s.catalog = nil
	s.catMu.Unlock()
}

func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	s.catMu.Lock()
	cached := s.catalog
	s.catMu.Unlock()
	if cached != nil && time.Since(cached.at) < time.Minute {
		writeJSON(w, http.StatusOK, cached.payload)
		return
	}

	payload := s.buildCatalog(r)
	if payload.Source == "opencode" {
		s.catMu.Lock()
		s.catalog = &catalogCache{at: time.Now(), payload: payload}
		s.catMu.Unlock()
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) buildCatalog(r *http.Request) catalogPayload {
	payload := catalogPayload{Current: s.settings.Model(), Source: "unavailable", Providers: []catalogProvider{}}
	if s.harness == nil {
		payload.Error = "chat is not available"
		return s.decorateCatalog(payload)
	}
	if !s.harness.Ready() {
		payload.Error = "agent is starting"
		return s.decorateCatalog(payload)
	}

	// Models are the source of truth for what can actually run, but they only
	// cover providers that are already authenticated. The integration endpoint
	// lists every known provider (including unconnected ones) and tells us how
	// each one is authenticated. /api/provider is unreliable across OpenCode
	// versions (it can be empty even when models exist), so it is only an
	// enrichment source.
	meta := map[string]harness.ProviderInfo{}
	if providers, err := s.harness.Providers(r.Context()); err == nil {
		for _, p := range providers {
			meta[p.ID] = p
		}
	}
	integrations := map[string]harness.IntegrationInfo{}
	if list, err := s.harness.Integrations(r.Context()); err == nil {
		for _, it := range list {
			integrations[it.ID] = it
		}
	} else {
		s.logger.Debug("list integrations", "err", err)
	}
	models, err := s.harness.Models(r.Context())
	if err != nil {
		payload.Error = err.Error()
		return s.decorateCatalog(payload)
	}

	byProvider := map[string][]catalogModel{}
	for _, m := range models {
		cm := catalogModel{
			Ref:        m.ProviderID + "/" + m.ID,
			Name:       m.Name,
			ProviderID: m.ProviderID,
			ModelID:    m.ID,
			Context:    m.Limit.Context,
			Output:     m.Limit.Output,
			Status:     m.Status,
			Enabled:    m.Enabled,
		}
		if cm.Name == "" {
			cm.Name = m.ID
		}
		for _, v := range m.Variants {
			cm.Variants = append(cm.Variants, v.ID)
		}
		if len(m.Cost) > 0 {
			cm.InputCost = m.Cost[0].Input
			cm.OutputCost = m.Cost[0].Output
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], cm)
	}

	ids := make([]string, 0, len(byProvider)+len(meta)+len(integrations))
	seen := map[string]bool{}
	addID := func(id string) {
		if id == "" || seen[id] {
			return
		}
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range byProvider {
		addID(id)
	}
	for id := range meta {
		addID(id)
	}
	for id := range integrations {
		addID(id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		p := meta[id]
		it := integrations[id]
		cp := catalogProvider{
			ID:        id,
			Name:      p.Name,
			Connected: len(byProvider[id]) > 0 || (p.Activation != "" && p.Activation != "disabled"),
			Models:    byProvider[id],
		}
		if cp.Name == "" {
			cp.Name = it.Name
		}
		if cp.Name == "" {
			cp.Name = id
		}
		if cp.Models == nil {
			cp.Models = []catalogModel{}
		}
		sort.Slice(cp.Models, func(i, j int) bool { return cp.Models[i].Name < cp.Models[j].Name })

		// An active connection means the provider is usable even when we did
		// not receive an enabled provider record. Credential connections are
		// owned by OpenCode (auth login), env connections by the environment.
		for _, c := range it.Connections {
			switch c.Type {
			case "credential":
				cp.Connected = true
				if !cp.APIKeySet {
					cp.APIKeySet = true
					cp.KeySource = "opencode"
				}
			case "env":
				cp.Connected = true
				if !cp.APIKeySet {
					cp.APIKeySet = true
					cp.KeySource = "env"
					cp.EnvVar = c.Name
				}
			}
		}

		payload.Providers = append(payload.Providers, cp)
	}
	payload.Source = "opencode"
	return s.decorateCatalog(payload)
}

// decorateCatalog merges redacted credential state from settings and adds
// configured providers that the running serve does not know about yet.
func (s *Server) decorateCatalog(payload catalogPayload) catalogPayload {
	view := s.settings.View()
	byID := map[string]settings.ProviderView{}
	for _, p := range view.Providers {
		byID[p.ID] = p
	}
	seen := map[string]bool{}
	for i := range payload.Providers {
		cp := &payload.Providers[i]
		seen[cp.ID] = true
		if p, ok := byID[cp.ID]; ok {
			// A Monitor-managed key takes precedence over OpenCode credentials
			// and environment variables. A keyless entry must not erase a
			// credential that OpenCode still reports as connected.
			if p.APIKeySet {
				cp.APIKeySet = true
				cp.APIKeyHint = p.APIKeyHint
				cp.KeySource = p.Source
				cp.EnvVar = p.EnvVar
				cp.Connected = true
			}
			if p.BaseURL != "" {
				cp.BaseURL = p.BaseURL
			}
			delete(byID, cp.ID)
			continue
		}
		if cp.APIKeySet {
			continue
		}
		if name, value := s.settings.ProviderEnv(cp.ID); value != "" {
			cp.APIKeySet = true
			cp.APIKeyHint = settings.Hint(value)
			cp.KeySource = "env"
			cp.EnvVar = name
			cp.Connected = true
		}
	}
	for id, p := range byID {
		name := p.Name
		if name == "" {
			name = id
		}
		payload.Providers = append(payload.Providers, catalogProvider{
			ID:         id,
			Name:       name,
			Connected:  true,
			APIKeySet:  p.APIKeySet,
			APIKeyHint: p.APIKeyHint,
			KeySource:  p.Source,
			EnvVar:     p.EnvVar,
			BaseURL:    p.BaseURL,
			Models:     []catalogModel{},
		})
	}
	sortCatalog(payload.Providers)
	return payload
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

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}
