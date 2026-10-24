// Package harness manages a dedicated OpenCode serve instance and drives it
// via `opencode run --format json`, streaming normalized events to callers.
package harness

import (
	"bufio"
	"context"
	"encoding/json"
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
)

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

// Manager owns the dedicated opencode serve process.
type Manager struct {
	binPath    string
	host       string
	port       int
	promptPath string
	logger     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cmd    *exec.Cmd
	ready  chan struct{}
	once   sync.Once

	password string
}

func New(binPath, host string, port int, promptPath string, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{binPath: binPath, host: host, port: port, promptPath: promptPath, logger: logger}
}

// Start launches the serve process in a supervised goroutine. It returns
// immediately; call WaitReady to block until the server accepts requests.
func (m *Manager) Start(ctx context.Context) {
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.ready = make(chan struct{})
	go m.loop()
}

func (m *Manager) loop() {
	for {
		if m.ctx.Err() != nil {
			return
		}
		configJSON := m.buildConfig()
		cmd := exec.CommandContext(m.ctx, m.binPath, "serve",
			"--hostname", m.host, "--port", strconv.Itoa(m.port))
		cmd.Env = append(os.Environ(), "OPENCODE_CONFIG_CONTENT="+configJSON)
		// serve prints "server password XXX" to stdout
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			m.logger.Error("failed to start opencode serve", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}
		m.mu.Lock()
		m.cmd = cmd
		m.password = ""
		m.mu.Unlock()

		// capture password from stdout
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				line := sc.Text()
				m.logger.Debug("opencode serve", "out", line)
				if strings.HasPrefix(line, "server password ") {
					pw := strings.TrimSpace(strings.TrimPrefix(line, "server password "))
					m.mu.Lock()
					m.password = pw
					m.mu.Unlock()
				}
			}
		}()

		m.waitReady()

		if err := cmd.Wait(); err != nil && m.ctx.Err() == nil {
			m.logger.Warn("opencode serve exited", "err", err)
		}
		if m.ctx.Err() != nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func (m *Manager) waitReady() {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m.ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		pw := m.password
		m.mu.Unlock()
		if pw == "" {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		req, _ := http.NewRequest("GET", m.ServerURL()+"/api/info", nil)
		req.SetBasicAuth("opencode", pw)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				m.once.Do(func() { close(m.ready) })
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	m.logger.Warn("opencode serve did not become ready in time")
	m.once.Do(func() { close(m.ready) })
}

// WaitReady blocks until the serve is ready (or 30s elapses).
func (m *Manager) WaitReady() {
	if m.ready == nil {
		return
	}
	<-m.ready
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

func (m *Manager) buildConfig() string {
	prompt := m.readPrompt()
	cfg := map[string]any{
		"model": "opencode-go/beam-free",
		"agent": map[string]any{
			"monitor": map[string]any{
				"description": "Machine ops agent for the monitor dashboard",
				"mode":        "subagent",
				"system":      prompt,
				"permission":  map[string]any{"bash": "allow"},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func (m *Manager) readPrompt() string {
	data, err := os.ReadFile(m.promptPath)
	if err != nil {
		return "You are the monitor ops agent. Be concise and careful."
	}
	return string(data)
}

// Run executes one user message in the given session and streams normalized
// events via onEvent. It blocks until the opencode process exits.
func (m *Manager) Run(ctx context.Context, sessionID, message string, onEvent func(Event)) error {
	m.mu.Lock()
	pw := m.password
	m.mu.Unlock()

	args := []string{
		"run", "--format", "json",
		"--server", m.ServerURL(),
		"--session", sessionID,
		"--auto",
		"--agent", "monitor",
		message,
	}
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
