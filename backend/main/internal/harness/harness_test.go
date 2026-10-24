package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"monitor/backend/main/internal/settings"
)

func TestBuildConfigUsesStore(t *testing.T) {
	dir := t.TempDir()
	store, err := settings.Load(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := "anthropic/claude-sonnet-5-5"
	key := "sk-ant-1234"
	if _, err := store.Update(settings.Patch{
		Model: &model,
		Providers: map[string]settings.ProviderPatch{
			"anthropic": {APIKey: &key},
		},
	}); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(prompt, []byte("be nice"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New("opencode", "127.0.0.1", 1, prompt, store, nil)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(m.buildConfig()), &cfg); err != nil {
		t.Fatalf("config json: %v", err)
	}
	if cfg["model"] != model {
		t.Fatalf("model = %v", cfg["model"])
	}
	providers := cfg["providers"].(map[string]any)
	anth := providers["anthropic"].(map[string]any)
	opts := anth["settings"].(map[string]any)
	if opts["apiKey"] != key {
		t.Fatalf("key not injected: %+v", opts)
	}
	agents := cfg["agents"].(map[string]any)
	monitor := agents["monitor"].(map[string]any)
	if monitor["system"] != "be nice" {
		t.Fatalf("prompt = %v", monitor["system"])
	}
}

func TestRunPassesModel(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	bin := filepath.Join(dir, "fake-opencode")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho '{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"hi\"}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store, _ := settings.Load(filepath.Join(dir, "config.json"))
	m := New(bin, "127.0.0.1", 1, filepath.Join(dir, "nope.md"), store, nil)
	m.password = "pw"

	var events []Event
	err := m.Run(context.Background(), "ses_test", "hello", "acme/model#high", func(ev Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model acme/model#high") {
		t.Fatalf("missing model flag: %q", joined)
	}
	if !strings.Contains(joined, "--agent monitor") || !strings.Contains(joined, "hello") {
		t.Fatalf("bad args: %q", joined)
	}
	if len(events) != 1 || events[0].Text != "hi" {
		t.Fatalf("events: %+v", events)
	}
}

func TestAPIRequestAndDiscovery(t *testing.T) {
	password := "secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "opencode" || p != password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/provider":
			_, _ = w.Write([]byte(`{"data":[{"id":"acme","name":"Acme","activation":"auto"}]}`))
		case "/api/integration":
			_, _ = w.Write([]byte(`{"data":[{"id":"acme","name":"Acme","methods":[{"type":"key"},{"type":"env","names":["ACME_API_KEY"]}],"connections":[{"type":"env","name":"ACME_API_KEY"}]},{"id":"bigco","name":"Big Co","methods":[{"type":"key"}]}]}`))
		case "/api/model":
			_, _ = w.Write([]byte(`{"data":[{"id":"m1","modelID":"m1","providerID":"acme","name":"M1","status":"active","enabled":true,"variants":[{"id":"high"}],"limit":{"context":1000,"output":100},"cost":[{"input":1,"output":2}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}

	m := New("opencode", u.Hostname(), port, "", nil, nil)
	m.password = password

	providers, err := m.Providers(context.Background())
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "acme" {
		t.Fatalf("providers: %+v", providers)
	}
	models, err := m.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 1 || models[0].Limit.Context != 1000 || models[0].Variants[0].ID != "high" {
		t.Fatalf("models: %+v", models)
	}
	integrations, err := m.Integrations(context.Background())
	if err != nil {
		t.Fatalf("integrations: %v", err)
	}
	if len(integrations) != 2 {
		t.Fatalf("integrations: %+v", integrations)
	}
	if integrations[0].ID != "acme" || integrations[0].Name != "Acme" ||
		len(integrations[0].Methods) != 2 || integrations[0].Methods[1].Names[0] != "ACME_API_KEY" ||
		len(integrations[0].Connections) != 1 || integrations[0].Connections[0].Type != "env" ||
		integrations[0].Connections[0].Name != "ACME_API_KEY" {
		t.Fatalf("acme integration: %+v", integrations[0])
	}
	if integrations[1].ID != "bigco" || len(integrations[1].Connections) != 0 {
		t.Fatalf("bigco integration: %+v", integrations[1])
	}
}

func TestWithExclusiveRejectsWhileChatting(t *testing.T) {
	m := New("opencode", "127.0.0.1", 1, "", nil, nil)
	m.runMu.RLock()
	defer m.runMu.RUnlock()
	if err := m.WithExclusive(context.Background(), nil); err != ErrChatInFlight {
		t.Fatalf("err = %v", err)
	}
}

func TestReadyStateSignalsOnce(t *testing.T) {
	st := newReadyState()
	if st.isReady() {
		t.Fatal("ready before signal")
	}
	st.signal(true)
	st.signal(false) // must not panic or overwrite ok
	if !st.isReady() {
		t.Fatal("not ready after signal")
	}
	select {
	case <-st.ch:
	case <-time.After(time.Second):
		t.Fatal("channel not closed")
	}
}
