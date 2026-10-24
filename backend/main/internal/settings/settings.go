// Package settings persists monitor configuration (currently the chat LLM
// provider/model selection) in a user-owned JSON file.
//
// Resolution order is: compiled defaults, then the config file, then
// environment variables. Secrets are never exposed through View.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// DefaultModel keeps the original hardcoded choice when nothing is configured.
const DefaultModel = "opencode-go/beam-free"

// schemaVersion is the on-disk document version.
const schemaVersion = 1

const (
	maxModelLen  = 256
	maxKeyLen    = 8192
	maxURLLen    = 2048
	maxNameLen   = 128
	maxHeaderLen = 4096
)

var (
	modelRefRE   = regexp.MustCompile(`^[^/#\s]+/[^#\s]+(?:#[^#\s]+)?$`)
	providerIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	modelIDRE    = regexp.MustCompile(`^[^/#\s]+$`)
)

// Model is a custom model definition passed through to OpenCode.
type Model struct {
	Name    string `json:"name,omitempty"`
	ModelID string `json:"modelID,omitempty"`
}

// Provider is a provider entry as stored on disk.
type Provider struct {
	APIKey  string            `json:"apiKey,omitempty"`
	BaseURL string            `json:"baseURL,omitempty"`
	Name    string            `json:"name,omitempty"`
	Package string            `json:"package,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  map[string]Model  `json:"models,omitempty"`
}

// LLM is the persisted LLM section.
type LLM struct {
	Model     string              `json:"model,omitempty"`
	Providers map[string]Provider `json:"providers,omitempty"`
}

type document struct {
	Version int `json:"version"`
	LLM     LLM `json:"llm"`
}

// ResolvedProvider is a provider after file + environment merging.
type ResolvedProvider struct {
	ID      string
	Name    string
	APIKey  string
	BaseURL string
	Package string
	Headers map[string]string
	Models  map[string]Model
	Source  string // "file" or "env"
	EnvVar  string
}

// Resolved is the effective LLM configuration.
type Resolved struct {
	Model       string
	ModelSource string // "default", "file", or "env"
	Providers   map[string]ResolvedProvider
}

// ProviderView is the redacted, API-safe representation of a provider.
type ProviderView struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint,omitempty"`
	Source     string `json:"source,omitempty"`
	EnvVar     string `json:"env_var,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Custom     bool   `json:"custom,omitempty"`
}

// View is the redacted API representation of the settings.
type View struct {
	Path         string         `json:"path"`
	Model        string         `json:"model"`
	ModelSource  string         `json:"model_source"`
	Providers    []ProviderView `json:"providers"`
	EnvOverrides []string       `json:"env_overrides"`
}

// ProviderPatch updates one provider. Nil fields are left unchanged.
type ProviderPatch struct {
	APIKey  *string            `json:"apiKey,omitempty"`
	BaseURL *string            `json:"baseURL,omitempty"`
	Name    *string            `json:"name,omitempty"`
	Package *string            `json:"package,omitempty"`
	Headers *map[string]string `json:"headers,omitempty"`
	Models  *map[string]Model  `json:"models,omitempty"`
	Remove  bool               `json:"remove,omitempty"`
}

// Patch is a partial settings update. A nil Model keeps the current model.
type Patch struct {
	Model     *string                  `json:"model,omitempty"`
	Providers map[string]ProviderPatch `json:"providers,omitempty"`
}

// ValidationError marks a caller-supplied value as invalid.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// IsValidation reports whether err is a settings validation failure.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func invalid(format string, args ...any) error {
	return &ValidationError{Err: fmt.Errorf(format, args...)}
}

// Store holds the loaded settings and serializes reads/writes.
type Store struct {
	mu     sync.RWMutex
	path   string
	doc    document
	getenv func(string) string
}

// DefaultPath returns MONITOR_CONFIG, else the XDG config path.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("MONITOR_CONFIG")); p != "" {
		return p
	}
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return filepath.Join(x, "monitor", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "monitor-config.json"
	}
	return filepath.Join(home, ".config", "monitor", "config.json")
}

// Load reads path. A missing file is not an error.
func Load(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	s := &Store{path: path, getenv: os.Getenv, doc: document{Version: schemaVersion}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read settings: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.doc); err != nil {
		// Do not run with a partially decoded document.
		s.doc = document{Version: schemaVersion}
		return s, fmt.Errorf("parse settings %s: %w", path, err)
	}
	if s.doc.Version == 0 {
		s.doc.Version = schemaVersion
	}
	if s.doc.LLM.Providers == nil {
		s.doc.LLM.Providers = map[string]Provider{}
	}
	return s, nil
}

// Path returns the config file path.
func (s *Store) Path() string { return s.path }

// Model returns the effective model reference.
func (s *Store) Model() string { return s.Resolve().Model }

// Resolve merges defaults, file, and environment.
func (s *Store) Resolve() Resolved {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveLocked()
}

func (s *Store) resolveLocked() Resolved {
	r := Resolved{
		Model:       DefaultModel,
		ModelSource: "default",
		Providers:   map[string]ResolvedProvider{},
	}
	if m := strings.TrimSpace(s.doc.LLM.Model); m != "" {
		r.Model = m
		r.ModelSource = "file"
	}
	for id, p := range s.doc.LLM.Providers {
		r.Providers[id] = ResolvedProvider{
			ID:      id,
			Name:    p.Name,
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Package: p.Package,
			Headers: p.Headers,
			Models:  p.Models,
			Source:  "file",
		}
	}
	if m := strings.TrimSpace(s.getenv("MONITOR_MODEL")); m != "" {
		r.Model = m
		r.ModelSource = "env"
	}
	// MONITOR_LLM_<ID>_API_KEY is monitor-specific, so OpenCode cannot read it.
	// Promote it into the generated config.
	for id, rp := range r.Providers {
		if v := strings.TrimSpace(s.getenv(monitorEnvName(id))); v != "" {
			rp.APIKey = v
			rp.Source = "env"
			rp.EnvVar = monitorEnvName(id)
			r.Providers[id] = rp
		}
	}
	return r
}

// View returns redacted settings for the API.
func (s *Store) View() View {
	r := s.Resolve()
	v := View{
		Path:        s.path,
		Model:       r.Model,
		ModelSource: r.ModelSource,
		Providers:   make([]ProviderView, 0, len(r.Providers)),
	}
	if s.getenv("MONITOR_MODEL") != "" {
		v.EnvOverrides = append(v.EnvOverrides, "MONITOR_MODEL")
	}
	for _, rp := range r.Providers {
		pv := ProviderView{
			ID:        rp.ID,
			Name:      rp.Name,
			APIKeySet: rp.APIKey != "",
			BaseURL:   rp.BaseURL,
			Custom:    rp.Package != "" || len(rp.Models) > 0,
		}
		if pv.APIKeySet {
			pv.APIKeyHint = Hint(rp.APIKey)
			pv.Source = rp.Source
			pv.EnvVar = rp.EnvVar
		} else if name, value := s.ProviderEnv(rp.ID); value != "" {
			pv.APIKeySet = true
			pv.APIKeyHint = Hint(value)
			pv.Source = "env"
			pv.EnvVar = name
		}
		if pv.EnvVar != "" {
			v.EnvOverrides = append(v.EnvOverrides, pv.EnvVar)
		}
		v.Providers = append(v.Providers, pv)
	}
	sort.Slice(v.Providers, func(i, j int) bool { return v.Providers[i].ID < v.Providers[j].ID })
	sort.Strings(v.EnvOverrides)
	return v
}

// ProviderEnv finds a standard environment credential for a provider. OpenCode
// reads these itself; monitor only reports them.
func (s *Store) ProviderEnv(id string) (name, value string) {
	for _, n := range providerEnvNames(id) {
		if v := strings.TrimSpace(s.getenv(n)); v != "" {
			return n, v
		}
	}
	return "", ""
}

// Update validates and persists a patch. The returned Resolved is the new
// effective configuration.
func (s *Store) Update(p Patch) (Resolved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneDocument(s.doc)
	if p.Model != nil {
		m := strings.TrimSpace(*p.Model)
		if m == "" {
			next.LLM.Model = ""
		} else {
			if err := validateModel(m); err != nil {
				return Resolved{}, err
			}
			next.LLM.Model = m
		}
	}
	if next.LLM.Providers == nil {
		next.LLM.Providers = map[string]Provider{}
	}
	if err := applyProviders(next.LLM.Providers, p.Providers); err != nil {
		return Resolved{}, err
	}
	next.Version = schemaVersion
	if err := writeFile(s.path, next); err != nil {
		return Resolved{}, fmt.Errorf("save settings: %w", err)
	}
	s.doc = next
	return s.resolveLocked(), nil
}

func applyProviders(dst map[string]Provider, patches map[string]ProviderPatch) error {
	for id, pp := range patches {
		if !providerIDRE.MatchString(id) {
			return invalid("invalid provider id %q", id)
		}
		if pp.Remove {
			delete(dst, id)
			continue
		}
		p := dst[id]
		if pp.APIKey != nil {
			key := strings.TrimSpace(*pp.APIKey)
			if err := validateKey(key); err != nil {
				return err
			}
			p.APIKey = key
		}
		if pp.BaseURL != nil {
			u := strings.TrimSpace(*pp.BaseURL)
			if err := validateBaseURL(u); err != nil {
				return err
			}
			p.BaseURL = u
		}
		if pp.Name != nil {
			n := strings.TrimSpace(*pp.Name)
			if len(n) > maxNameLen {
				return invalid("provider name is too long")
			}
			p.Name = n
		}
		if pp.Package != nil {
			n := strings.TrimSpace(*pp.Package)
			if len(n) > maxNameLen {
				return invalid("package is too long")
			}
			p.Package = n
		}
		if pp.Headers != nil {
			if err := validateHeaders(*pp.Headers); err != nil {
				return err
			}
			p.Headers = *pp.Headers
		}
		if pp.Models != nil {
			if err := validateModels(*pp.Models); err != nil {
				return err
			}
			p.Models = *pp.Models
		}
		if isEmptyProvider(p) {
			delete(dst, id)
			continue
		}
		dst[id] = p
	}
	return nil
}

func validateModel(model string) error {
	if len(model) > maxModelLen {
		return invalid("model reference is too long")
	}
	if !modelRefRE.MatchString(model) {
		return invalid("model must look like provider/model or provider/model#variant")
	}
	return nil
}

func validateKey(key string) error {
	if len(key) > maxKeyLen {
		return invalid("api key is too long")
	}
	if strings.ContainsAny(key, "\r\n") {
		return invalid("api key must be a single line")
	}
	return nil
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxURLLen {
		return invalid("base URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return invalid("base URL must be an absolute URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return invalid("base URL must use http or https")
	}
	return nil
}

func validateHeaders(h map[string]string) error {
	for k, v := range h {
		if k == "" || len(k) > 256 || len(v) > maxHeaderLen {
			return invalid("invalid header %q", k)
		}
		if strings.ContainsAny(k+v, "\r\n") {
			return invalid("headers must be single-line")
		}
	}
	return nil
}

func validateModels(m map[string]Model) error {
	for id, def := range m {
		if !modelIDRE.MatchString(id) || len(id) > maxModelLen {
			return invalid("invalid model id %q", id)
		}
		if len(def.Name) > maxNameLen || (def.ModelID != "" && !modelIDRE.MatchString(def.ModelID)) {
			return invalid("invalid definition for model %q", id)
		}
	}
	return nil
}

// OpenCodeConfig builds the OPENCODE_CONFIG_CONTENT document for `opencode
// serve`. It uses the OpenCode V2 config shape.
func (s *Store) OpenCodeConfig(prompt string) map[string]any {
	r := s.Resolve()
	cfg := map[string]any{
		"model": r.Model,
		"agents": map[string]any{
			"monitor": map[string]any{
				"description": "Machine ops agent for the monitor dashboard",
				"mode":        "subagent",
				"system":      prompt,
				"permissions": []map[string]string{
					{"action": "shell", "resource": "*", "effect": "allow"},
				},
			},
		},
	}
	providers := map[string]any{}
	ids := make([]string, 0, len(r.Providers))
	for id := range r.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rp := r.Providers[id]
		p := map[string]any{}
		if rp.Name != "" {
			p["name"] = rp.Name
		}
		if rp.Package != "" {
			p["package"] = rp.Package
		}
		if len(rp.Headers) > 0 {
			p["headers"] = rp.Headers
		}
		if len(rp.Models) > 0 {
			p["models"] = rp.Models
		}
		opts := map[string]any{}
		if rp.APIKey != "" {
			opts["apiKey"] = rp.APIKey
		}
		if rp.BaseURL != "" {
			opts["baseURL"] = rp.BaseURL
		}
		if len(opts) > 0 {
			p["settings"] = opts
		}
		if len(p) > 0 {
			providers[id] = p
		}
	}
	if len(providers) > 0 {
		cfg["providers"] = providers
	}
	return cfg
}

// Hint redacts a secret to its last four characters.
func Hint(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 4 {
		return "••••"
	}
	return "••••" + secret[len(secret)-4:]
}

func monitorEnvName(id string) string {
	return "MONITOR_LLM_" + envToken(id) + "_API_KEY"
}

func providerEnvNames(id string) []string {
	names := []string{monitorEnvName(id), envToken(id) + "_API_KEY"}
	if id == "opencode-go" {
		names = append(names, "OPENCODE_GO_API_KEY", "OPENCODE_API_KEY")
	}
	return names
}

func envToken(id string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(id))
}

func isEmptyProvider(p Provider) bool {
	return p.APIKey == "" && p.BaseURL == "" && p.Name == "" && p.Package == "" &&
		len(p.Headers) == 0 && len(p.Models) == 0
}

func cloneDocument(d document) document {
	out := document{Version: d.Version, LLM: LLM{Model: d.LLM.Model, Providers: map[string]Provider{}}}
	for id, p := range d.LLM.Providers {
		cp := p
		cp.Headers = cloneMap(p.Headers)
		if p.Models != nil {
			cp.Models = make(map[string]Model, len(p.Models))
			for k, v := range p.Models {
				cp.Models[k] = v
			}
		}
		out.LLM.Providers[id] = cp
	}
	return out
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeFile(path string, doc document) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
