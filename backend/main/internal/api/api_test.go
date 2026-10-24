package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"monitor/backend/main/internal/settings"
)

func newTestServer(t *testing.T) (*Server, *settings.Store) {
	t.Helper()
	store, err := settings.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(nil, nil, store, nil), store
}

func TestGetLLMSettingsRedacts(t *testing.T) {
	s, store := newTestServer(t)
	key := "sk-secret-abcd"
	if _, err := store.Update(settings.Patch{Providers: map[string]settings.ProviderPatch{
		"acme": {APIKey: &key},
	}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/llm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("secret leaked: %s", rec.Body)
	}
	var resp struct {
		Settings settings.View  `json:"settings"`
		Agent    map[string]any `json:"agent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Settings.Model != settings.DefaultModel || len(resp.Settings.Providers) != 1 {
		t.Fatalf("settings: %+v", resp.Settings)
	}
	if resp.Settings.Providers[0].APIKeyHint != "••••abcd" {
		t.Fatalf("hint: %+v", resp.Settings.Providers[0])
	}
}

func TestPutLLMSettingsModelOnly(t *testing.T) {
	s, store := newTestServer(t)
	body := `{"model":"anthropic/claude-sonnet-5-5"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/settings/llm", bytes.NewBufferString(body))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Applied  string        `json:"applied"`
		Settings settings.View `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Applied != "immediate" || resp.Settings.ModelSource != "file" {
		t.Fatalf("response: %+v", resp)
	}
	if got := store.Model(); got != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("stored model = %q", got)
	}
}

func TestPutLLMSettingsValidation(t *testing.T) {
	s, _ := newTestServer(t)
	for _, body := range []string{
		`{"model":"nope"}`,
		`{"providers":{"acme":{"baseURL":"ftp://x"}}}`,
		`{"unknown":true}`,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/settings/llm", bytes.NewBufferString(body))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d (%s)", body, rec.Code, rec.Body)
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["error"] == nil || resp["message"] == nil {
			t.Fatalf("error shape: %s", rec.Body)
		}
	}
}

func TestCatalogUnavailableWithoutHarness(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/llm/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp catalogPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "unavailable" || resp.Current != settings.DefaultModel {
		t.Fatalf("payload: %+v", resp)
	}
	if !strings.Contains(rec.Body.String(), `"providers":[]`) {
		t.Fatalf("providers should be an empty array: %s", rec.Body)
	}
}

func TestCORSPreflight(t *testing.T) {
	t.Setenv("MONITOR_CORS_ORIGIN", "")
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/settings/llm", nil)
	req.Host = "127.0.0.1:9050"
	req.Header.Set("Origin", "http://127.0.0.1:9050")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:9050" {
		t.Fatalf("allow-origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "PUT") {
		t.Fatalf("allow-methods = %q", got)
	}
}

func TestCORSRejectsForeignOrigin(t *testing.T) {
	t.Setenv("MONITOR_CORS_ORIGIN", "")
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/llm", nil)
	req.Host = "127.0.0.1:9050"
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow-origin = %q", got)
	}
}

func TestSortCatalogRanksConnectedAndFeatured(t *testing.T) {
	providers := []catalogProvider{
		{ID: "zzz", Name: "Zzz"},
		{ID: "anthropic", Name: "Anthropic"},
		{ID: "aaa", Name: "Aaa", Models: []catalogModel{{Name: "m"}}},
		{ID: "opencode-go", Name: "OpenCode Go", Connected: true, APIKeySet: true, Models: []catalogModel{{Name: "m"}}},
		{ID: "nvidia", Name: "Nvidia", Connected: true, APIKeySet: true, Models: []catalogModel{{Name: "m"}}},
		{ID: "cerebras", Name: "Cerebras"},
	}
	sortCatalog(providers)
	want := []string{"nvidia", "opencode-go", "anthropic", "cerebras", "aaa", "zzz"}
	for i, id := range want {
		if providers[i].ID != id {
			t.Fatalf("position %d = %s, want %s (%v)", i, providers[i].ID, id, providers)
		}
	}
}
