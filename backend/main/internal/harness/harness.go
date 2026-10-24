// Package harness manages a dedicated OpenCode serve instance and drives it
// via `opencode run --format json`, streaming normalized events to callers.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor/backend/main/internal/audit"
	"monitor/backend/main/internal/settings"
)

// ErrChatInFlight is returned when a configuration change needs a restart but
// a chat is currently running against the agent.
var ErrChatInFlight = errors.New("chat in progress")

// ConfigSource supplies the OpenCode config. *settings.Store implements it.
type ConfigSource interface {
	OpenCodeConfig(prompt string) map[string]any
	Model() string
}

// Event is a normalized OpenCode streaming event.
type Event struct {
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id,omitempty"`
	Text       string         `json:"text,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	ToolInput  map[string]any `json:"tool_input,omitempty"`
	ToolOutput string         `json:"tool_output,omitempty"`
	ToolStatus string         `json:"tool_status,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// readyState is one serve process's readiness signal. A new state is created
// for every process so restarts do not reuse a closed channel.
type readyState struct {
	ch   chan struct{}
	once sync.Once
	ok   bool
}

func newReadyState() *readyState { return &readyState{ch: make(chan struct{})} }

// signal closes the channel exactly once. ok records whether the process
// actually became ready; readers observe it after the channel closes.
func (r *readyState) signal(ok bool) {
	r.once.Do(func() {
		r.ok = ok
		close(r.ch)
	})
}

func (r *readyState) isReady() bool {
	select {
	case <-r.ch:
		return r.ok
	default:
		return false
	}
}

// Manager owns the dedicated opencode serve process.
type Manager struct {
	binPath    string
	host       string
	port       int
	promptPath string
	cfg        ConfigSource
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	cmd        *exec.Cmd
	password   string
	ready      *readyState
	restarting bool

	restartCh chan struct{}
	runMu     sync.RWMutex
}

// New creates a manager. cfg may be nil, in which case a minimal default
// configuration is used.
func New(binPath, host string, port int, promptPath string, cfg ConfigSource, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		binPath:    binPath,
		host:       host,
		port:       port,
		promptPath: promptPath,
		cfg:        cfg,
		logger:     logger,
		restartCh:  make(chan struct{}, 1),
	}
}

// Start launches the serve process in a supervised goroutine. It returns
// immediately; call WaitReady to block until the server accepts requests.
func (m *Manager) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)
	go m.loop()
}

func (m *Manager) loop() {
	for {
		if m.ctx.Err() != nil {
			return
		}
		st := newReadyState()
		m.mu.Lock()
		m.ready = st
		m.cmd = nil
		m.password = ""
		m.mu.Unlock()

		configJSON := m.buildConfig()
		cmd := exec.CommandContext(m.ctx, m.binPath, "serve",
			"--hostname", m.host, "--port", strconv.Itoa(m.port))
		cmd.Env = append(os.Environ(), "OPENCODE_CONFIG_CONTENT="+configJSON)
		// serve prints "server password XXX" to stdout
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			m.logger.Error("opencode stdout pipe", "err", err)
			m.sleepOrStop(2 * time.Second)
			continue
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			m.logger.Error("failed to start opencode serve", "err", err)
			m.sleepOrStop(2 * time.Second)
			continue
		}
		m.mu.Lock()
		m.cmd = cmd
		m.mu.Unlock()

		go m.scanStdout(stdout, st)

		waitErr := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			err := cmd.Wait()
			waitErr <- err
			close(done)
		}()

		m.waitReady(st, done)

		exitErr := <-waitErr
		st.signal(false) // unblock any waiter if the process never became ready
		m.mu.Lock()
		intentional := m.restarting
		m.restarting = false
		if m.cmd == cmd {
			m.cmd = nil
			m.password = ""
		}
		m.mu.Unlock()
		if exitErr != nil && m.ctx.Err() == nil && !intentional {
			m.logger.Warn("opencode serve exited", "err", exitErr)
		}
		if m.ctx.Err() != nil {
			return
		}
		m.sleepOrStop(2 * time.Second)
	}
}

func (m *Manager) scanStdout(r io.Reader, st *readyState) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		m.logger.Debug("opencode serve", "out", line)
		if strings.HasPrefix(line, "server password ") {
			pw := strings.TrimSpace(strings.TrimPrefix(line, "server password "))
			m.mu.Lock()
			// Ignore output from a process that has already been replaced.
			if m.ready == st {
				m.password = pw
			}
			m.mu.Unlock()
		}
	}
}

func (m *Manager) waitReady(st *readyState, done <-chan struct{}) {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-done:
			return
		case <-deadline.C:
			m.logger.Warn("opencode serve did not become ready in time")
			st.signal(false)
			return
		default:
		}
		m.mu.Lock()
		pw := m.password
		ready := m.ready == st
		m.mu.Unlock()
		if ready && pw != "" {
			req, _ := http.NewRequest("GET", m.ServerURL()+"/api/info", nil)
			req.SetBasicAuth("opencode", pw)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					st.signal(true)
					return
				}
			}
		}
		select {
		case <-m.ctx.Done():
			return
		case <-done:
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// WaitReady blocks until the serve is ready (or 30s elapses).
func (m *Manager) WaitReady() {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		st := m.ready
		m.mu.Unlock()
		if st != nil {
			<-st.ch
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Ready reports whether the current serve process accepted requests.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	st := m.ready
	m.mu.Unlock()
	return st != nil && st.isReady()
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
}

func (m *Manager) ServerURL() string {
	return fmt.Sprintf("http://%s:%d", m.host, m.port)
}

// password returns the current serve password.
func (m *Manager) passwordValue() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.password
}

func (m *Manager) buildConfig() string {
	prompt := m.readPrompt()
	var cfg map[string]any
	if m.cfg != nil {
		cfg = m.cfg.OpenCodeConfig(prompt)
	} else {
		cfg = defaultConfig(prompt)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func defaultConfig(prompt string) map[string]any {
	return map[string]any{
		"model": settings.DefaultModel,
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
}

func (m *Manager) readPrompt() string {
	data, err := os.ReadFile(m.promptPath)
	if err != nil {
		return "You are the monitor ops agent. Be concise and careful."
	}
	return string(data)
}

// Run executes one user message in the given session and streams normalized
// events via onEvent. It blocks until the opencode process exits. An empty
// model uses the configured default.
func (m *Manager) Run(ctx context.Context, sessionID, message, model string, onEvent func(Event)) error {
	// A read lock keeps configuration restarts from tearing down a live chat.
	m.runMu.RLock()
	defer m.runMu.RUnlock()

	if model == "" && m.cfg != nil {
		model = m.cfg.Model()
	}
	pw := m.passwordValue()

	args := []string{
		"run", "--format", "json",
		"--server", m.ServerURL(),
		"--session", sessionID,
		"--auto",
		"--agent", "monitor",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, message)

	cmd := exec.CommandContext(ctx, m.binPath, args...)
	if pw != "" {
		cmd.Env = append(os.Environ(), "OPENCODE_SERVER_PASSWORD="+pw)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	go io.Copy(io.Discard, stderr)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ev := parseEvent(line)
		if ev == nil {
			continue
		}
		ev.SessionID = sessionID
		if ev.Type == "tool" && (ev.Tool == "shell" || ev.Tool == "bash") {
			cmdStr := ""
			if v, ok := ev.ToolInput["command"].(string); ok {
				cmdStr = v
			}
			audit.Append(audit.Entry{
				SessionID: sessionID,
				Tool:      ev.Tool,
				Command:   cmdStr,
				Output:    ev.ToolOutput,
				Status:    ev.ToolStatus,
			})
		}
		if onEvent != nil {
			onEvent(*ev)
		}
	}
	if err := scanner.Err(); err != nil {
		m.logger.Warn("scanner error", "err", err)
	}
	return cmd.Wait()
}

// WithExclusive runs prepare (if any) and restarts the serve while holding the
// chat-exclusion lock. It returns ErrChatInFlight if a chat is running.
func (m *Manager) WithExclusive(ctx context.Context, prepare func() error) error {
	if !m.runMu.TryLock() {
		return ErrChatInFlight
	}
	defer m.runMu.Unlock()
	if prepare != nil {
		if err := prepare(); err != nil {
			return err
		}
	}
	return m.restartLocked(ctx)
}

// Restart restarts the serve unless a chat is in flight.
func (m *Manager) Restart(ctx context.Context) error {
	return m.WithExclusive(ctx, nil)
}

// restartLocked restarts the serve. The caller must hold runMu.
func (m *Manager) restartLocked(ctx context.Context) error {
	m.mu.Lock()
	cmd := m.cmd
	old := m.ready
	if cmd != nil && cmd.Process != nil {
		m.restarting = true
	}
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		m.logger.Info("restarting opencode serve")
		_ = cmd.Process.Kill()
	}
	select {
	case m.restartCh <- struct{}{}:
	default:
	}

	waitCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	for {
		m.mu.Lock()
		st := m.ready
		m.mu.Unlock()
		if st != nil && st != old {
			select {
			case <-st.ch:
				if !st.ok {
					return errors.New("agent failed to start")
				}
				return nil
			case <-waitCtx.Done():
				return fmt.Errorf("agent restart: %w", waitCtx.Err())
			}
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("agent restart: %w", waitCtx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Manager) sleepOrStop(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-m.ctx.Done():
	case <-m.restartCh:
	case <-t.C:
	}
}

// ProviderInfo is a provider as reported by the OpenCode server.
type ProviderInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Activation string `json:"activation"`
}

// ModelVariant is a selectable model variant.
type ModelVariant struct {
	ID string `json:"id"`
}

// ModelLimit reports model token limits.
type ModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// ModelCost reports pricing per million tokens.
type ModelCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// ModelInfo is a model as reported by the OpenCode server.
type ModelInfo struct {
	ID         string         `json:"id"`
	ModelID    string         `json:"modelID"`
	ProviderID string         `json:"providerID"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Enabled    bool           `json:"enabled"`
	Variants   []ModelVariant `json:"variants"`
	Limit      ModelLimit     `json:"limit"`
	Cost       []ModelCost    `json:"cost"`
}

// IntegrationMethod is one way an integration can be authenticated.
type IntegrationMethod struct {
	Type  string   `json:"type"`
	Names []string `json:"names,omitempty"`
}

// IntegrationConnection is an active credential or environment connection.
type IntegrationConnection struct {
	Type   string `json:"type"` // credential, env, ...
	Name   string `json:"name,omitempty"`
	ID     string `json:"id,omitempty"`
	Label  string `json:"label,omitempty"`
	Method string `json:"method,omitempty"`
}

// IntegrationInfo is a provider integration as reported by the OpenCode server.
// The integration list includes providers that are not authenticated yet.
type IntegrationInfo struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Methods     []IntegrationMethod     `json:"methods"`
	Connections []IntegrationConnection `json:"connections"`
}

// Integrations lists every known provider integration and its active
// connections. This is the only reliable source for unconnected providers.
func (m *Manager) Integrations(ctx context.Context) ([]IntegrationInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var resp struct {
		Data []IntegrationInfo `json:"data"`
	}
	if err := m.APIRequest(ctx, http.MethodGet, "/api/integration", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// Providers lists providers known to the running serve.
func (m *Manager) Providers(ctx context.Context) ([]ProviderInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var resp struct {
		Data []ProviderInfo `json:"data"`
	}
	if err := m.APIRequest(ctx, http.MethodGet, "/api/provider", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// Models lists models known to the running serve.
func (m *Manager) Models(ctx context.Context) ([]ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var resp struct {
		Data []ModelInfo `json:"data"`
	}
	if err := m.APIRequest(ctx, http.MethodGet, "/api/model", &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// APIRequest calls the authenticated OpenCode server API.
func (m *Manager) APIRequest(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, m.ServerURL()+path, nil)
	if err != nil {
		return err
	}
	if pw := m.passwordValue(); pw != "" {
		req.SetBasicAuth("opencode", pw)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func parseEvent(line string) *Event {
	var raw struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionID"`
		Part      struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string         `json:"status"`
				Input  map[string]any `json:"input"`
				Output string         `json:"output"`
			} `json:"state"`
		} `json:"part"`
		Error struct {
			Message string `json:"message"`
			Name    string `json:"name"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil
	}
	ev := &Event{Type: raw.Type, SessionID: raw.SessionID}
	switch raw.Type {
	case "text":
		ev.Text = raw.Part.Text
	case "tool_use":
		ev.Type = "tool"
		ev.Tool = raw.Part.Tool
		ev.ToolInput = raw.Part.State.Input
		ev.ToolOutput = raw.Part.State.Output
		ev.ToolStatus = raw.Part.State.Status
	case "error":
		if raw.Error.Message != "" {
			ev.Error = raw.Error.Message
		} else {
			ev.Error = raw.Error.Name
		}
	}
	return ev
}
