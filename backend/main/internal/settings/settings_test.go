package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T, env map[string]string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor", "config.json")
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s.getenv = func(k string) string { return env[k] }
	return s, path
}

func TestDefaults(t *testing.T) {
	s, _ := testStore(t, nil)
	r := s.Resolve()
	if r.Model != DefaultModel || r.ModelSource != "default" {
		t.Fatalf("defaults: %+v", r)
	}
	cfg := s.OpenCodeConfig("prompt")
	if cfg["model"] != DefaultModel {
		t.Fatalf("config model: %+v", cfg["model"])
	}
	if _, ok := cfg["agent"]; ok {
		t.Fatal("deprecated agent key emitted")
	}
	agents, ok := cfg["agents"].(map[string]any)
	if !ok {
		t.Fatalf("agents missing: %+v", cfg)
	}
	monitor, ok := agents["monitor"].(map[string]any)
	if !ok || monitor["system"] != "prompt" {
		t.Fatalf("monitor agent: %+v", agents)
	}
	if _, ok := monitor["permission"]; ok {
		t.Fatal("deprecated permission key emitted")
	}
	rules, ok := monitor["permissions"].([]map[string]string)
	if !ok || len(rules) != 1 || rules[0]["action"] != "shell" {
		t.Fatalf("permissions: %+v", monitor["permissions"])
	}
}

func TestFileEnvPrecedence(t *testing.T) {
	s, path := testStore(t, nil)
	if _, err := s.Update(Patch{
		Model: ptr("anthropic/claude-sonnet-5-5"),
		Providers: map[string]ProviderPatch{
			"anthropic": {APIKey: ptr("sk-ant-secret-1234")},
		},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if r := s.Resolve(); r.Model != "anthropic/claude-sonnet-5-5" || r.ModelSource != "file" {
		t.Fatalf("file model: %+v", r)
	}

	// Reload from disk and apply an env override.
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	s2.getenv = func(k string) string {
		return map[string]string{
			"MONITOR_MODEL":                 "openai/gpt-6.1-sol",
			"MONITOR_LLM_ANTHROPIC_API_KEY": "env-key-9876",
		}[k]
	}
	r := s2.Resolve()
	if r.Model != "openai/gpt-6.1-sol" || r.ModelSource != "env" {
		t.Fatalf("env model: %+v", r)
	}
	p := r.Providers["anthropic"]
	if p.APIKey != "env-key-9876" || p.Source != "env" {
		t.Fatalf("env key: %+v", p)
	}
	cfg := s2.OpenCodeConfig("p")
	providers := cfg["providers"].(map[string]any)
	anth := providers["anthropic"].(map[string]any)
	opts := anth["settings"].(map[string]any)
	if opts["apiKey"] != "env-key-9876" {
		t.Fatalf("env key not injected: %+v", opts)
	}
}

func TestViewRedactsSecrets(t *testing.T) {
	s, _ := testStore(t, map[string]string{"OPENAI_API_KEY": "sk-live-abcd"})
	if _, err := s.Update(Patch{
		Providers: map[string]ProviderPatch{
			"anthropic": {APIKey: ptr("sk-ant-secret-1234")},
			"openai":    {BaseURL: ptr("https://gateway.example/v1")},
		},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	v := s.View()
	if strings.Contains(mustJSON(t, v), "sk-ant-secret") {
		t.Fatalf("secret leaked: %s", mustJSON(t, v))
	}
	byID := map[string]ProviderView{}
	for _, p := range v.Providers {
		byID[p.ID] = p
	}
	if a := byID["anthropic"]; !a.APIKeySet || a.APIKeyHint != "••••1234" || a.Source != "file" {
		t.Fatalf("anthropic view: %+v", a)
	}
	if o := byID["openai"]; !o.APIKeySet || o.Source != "env" || o.EnvVar != "OPENAI_API_KEY" {
		t.Fatalf("openai env view: %+v", o)
	}
}

func TestProviderPatchSemantics(t *testing.T) {
	s, _ := testStore(t, nil)
	if _, err := s.Update(Patch{Providers: map[string]ProviderPatch{
		"acme": {APIKey: ptr("key-1234"), BaseURL: ptr("https://acme.example/v1")},
	}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Omitted key keeps the existing one; empty key clears it.
	if _, err := s.Update(Patch{Providers: map[string]ProviderPatch{
		"acme": {BaseURL: ptr("https://acme.example/v2")},
	}}); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if k := s.Resolve().Providers["acme"].APIKey; k != "key-1234" {
		t.Fatalf("key not kept: %q", k)
	}
	if _, err := s.Update(Patch{Providers: map[string]ProviderPatch{
		"acme": {APIKey: ptr("")},
	}}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if k := s.Resolve().Providers["acme"].APIKey; k != "" {
		t.Fatalf("key not cleared: %q", k)
	}
	if _, err := s.Update(Patch{Providers: map[string]ProviderPatch{"acme": {Remove: true}}}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := s.Resolve().Providers["acme"]; ok {
		t.Fatal("provider not removed")
	}
}

func TestValidation(t *testing.T) {
	s, _ := testStore(t, nil)
	cases := []struct {
		name  string
		patch Patch
	}{
		{"model", Patch{Model: ptr("not-a-model")}},
		{"provider id", Patch{Providers: map[string]ProviderPatch{"bad id": {APIKey: ptr("x")}}}},
		{"base url", Patch{Providers: map[string]ProviderPatch{"acme": {BaseURL: ptr("ftp://x")}}}},
		{"key newline", Patch{Providers: map[string]ProviderPatch{"acme": {APIKey: ptr("a\nb")}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Update(tc.patch)
			if err == nil || !IsValidation(err) {
				t.Fatalf("want validation error, got %v", err)
			}
		})
	}
}

func TestFilePermissions(t *testing.T) {
	s, path := testStore(t, nil)
	if _, err := s.Update(Patch{Model: ptr("openai/gpt-6.1-sol")}); err != nil {
		t.Fatalf("update: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o", dir.Mode().Perm())
	}
}

func TestCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func ptr[T any](v T) *T { return &v }

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
