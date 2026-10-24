// Package audit records every command issued through the chat dock to an
// append-only JSONL log at ~/.local/state/monitor/audit.jsonl.
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Entry is one audited action.
type Entry struct {
	Time      time.Time `json:"time"`
	SessionID string    `json:"session_id"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command,omitempty"`
	Output    string    `json:"output,omitempty"`
	Status    string    `json:"status,omitempty"`
}

// Path returns the audit log path.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "monitor", "audit.jsonl")
}

// Append writes one entry to the log (best-effort; errors are ignored).
func Append(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	p := Path()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	_, _ = f.Write(b)
}

// Read returns the last n entries (newest last).
func Read(n int) []Entry {
	data, err := os.ReadFile(Path())
	if err != nil {
		return nil
	}
	lines := splitLines(string(data))
	var entries []Entry
	for _, l := range lines {
		if l == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(l), &e) == nil {
			entries = append(entries, e)
		}
	}
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return entries
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
