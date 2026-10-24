// Command monitord is the monitor front door.
//
// It serves the JSON/SSE API under /api and reverse-proxies everything else to
// the frontend dev server (development) or the built SvelteKit node server
// (production), so a single port exposes the whole app.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"monitor/backend/main/internal/api"
	"monitor/backend/main/internal/harness"
	"monitor/backend/main/internal/inventory"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9050", "address to listen on")
	proxy := flag.String("proxy", "http://127.0.0.1:9051", "frontend server to proxy non-API requests to")
	poll := flag.Duration("poll", 10*time.Second, "inventory poll interval")
	root := flag.String("root", ".", "monitor repo root (for prompts/ops)")
	ocBin := flag.String("opencode", "opencode", "opencode binary")
	ocPort := flag.Int("oc-port", 9060, "dedicated opencode serve port")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	inv := inventory.NewStore(ctx, *poll, logger)
	harnessMgr := harness.New(*ocBin, "127.0.0.1", *ocPort, filepath.Join(*root, "ops", "opencode", "monitor-system-prompt.md"), logger)
	harnessMgr.Start(ctx)
	harnessMgr.WaitReady()
	logger.Info("opencode serve ready", "url", harnessMgr.ServerURL())
	apiServer := api.New(inv, harnessMgr, logger)

	upstream, err := url.Parse(*proxy)
	if err != nil {
		logger.Error("invalid proxy target", "proxy", *proxy, "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
	})
	mux.Handle("/api/", apiServer.Handler())
	mux.Handle("/", newProxy(upstream, logger))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(logger, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("monitord listening", "addr", *addr, "frontend", upstream.String(), "poll", *poll)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	harnessMgr.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	logger.Info("monitord stopped")
}

func newProxy(upstream *url.URL, logger *slog.Logger) http.Handler {
	p := httputil.NewSingleHostReverseProxy(upstream)
	// WebSocket/SSE-friendly defaults; Vite HMR relies on these through the proxy.
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("proxy error", "path", r.URL.Path, "err", err)
		http.Error(w, "frontend unavailable", http.StatusBadGateway)
	}
	return p
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Debug("request", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
