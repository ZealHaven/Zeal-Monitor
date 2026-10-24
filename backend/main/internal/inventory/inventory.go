// Package inventory merges raw discovery results into the Service model,
// caches the snapshot and runs health probes.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"monitor/backend/main/internal/collect"
)

// Alias is a clickable name for a service.
type Alias struct {
	Kind string `json:"kind"` // local | tailnet | public
	Host string `json:"host"`
	URL  string `json:"url"`
}

// Listen is one socket of a service.
type Listen struct {
	Proto string `json:"proto"`
	Addr  string `json:"addr"`
	Port  int    `json:"port"`
}

// Health is the result of an HTTP probe.
type Health struct {
	State string `json:"state"` // up | down | unknown
	HTTP  int    `json:"http,omitempty"`
	MS    int    `json:"ms"`
}

// Service is one process with everything the dashboard shows.
type Service struct {
	PID       int      `json:"pid"`
	Proc      string   `json:"proc"`
	User      string   `json:"user"`
	Cmd       string   `json:"cmd"`
	Cwd       string   `json:"cwd"`
	Unit      string   `json:"unit,omitempty"`
	Container string   `json:"container,omitempty"`
	Image     string   `json:"image,omitempty"`
	Listen    []Listen `json:"listen"`
	Aliases   []Alias  `json:"aliases"`
	Health    *Health  `json:"health,omitempty"`
}

// Build merges a collect snapshot into services, attaching aliases and
// probing health concurrently.
func Build(snap *collect.Snapshot) []Service {
	byPID := map[int][]collect.Listener{}
	for _, l := range snap.Listeners {
		if l.PID <= 0 {
			continue
		}
		byPID[l.PID] = append(byPID[l.PID], l)
	}

	// port -> docker container, for container attribution
	portToContainer := map[int]collect.DockerContainer{}
	for _, c := range snap.Docker {
		for _, p := range c.Ports {
			portToContainer[p] = c
		}
	}

	services := make([]Service, 0, len(byPID))
	for pid, listeners := range byPID {
		svc := Service{
			PID:     pid,
			Proc:    listeners[0].Proc,
			Cmd:     snap.Processes[pid].Cmd,
			User:    snap.Processes[pid].User,
			Cwd:     snap.Processes[pid].Cwd,
			Unit:    snap.Processes[pid].Unit,
			Listen:  make([]Listen, 0, len(listeners)),
			Aliases: []Alias{},
		}
		for _, l := range listeners {
			svc.Listen = append(svc.Listen, Listen{Proto: l.Proto, Addr: l.Addr, Port: l.Port})
		}

		ports := map[int]bool{}
		for _, l := range listeners {
			ports[l.Port] = true
		}

		// container attribution by published port
		for port, c := range portToContainer {
			if ports[port] {
				svc.Container = c.Name
				svc.Image = c.Image
				break
			}
		}

		svc.Aliases = aliasesFor(snap, listeners, ports)
		services = append(services, svc)
	}

	probeAll(services)
	return services
}

func aliasesFor(snap *collect.Snapshot, listeners []collect.Listener, ports map[int]bool) []Alias {
	var aliases []Alias = []Alias{}
	add := func(a Alias) {
		for _, existing := range aliases {
			if existing.URL == a.URL {
				return
			}
		}
		aliases = append(aliases, a)
	}

	// nginx vhosts (server_name -> proxy_pass port)
	for _, v := range snap.Nginx {
		if !ports[v.Port] {
			continue
		}
		for _, name := range v.ServerNames {
			switch {
			case strings.HasSuffix(name, ".local"):
				add(Alias{Kind: "local", Host: name, URL: "http://" + name})
			case name == snap.SelfName || strings.HasSuffix(name, "."+snap.SelfName):
				add(Alias{Kind: "tailnet", Host: name, URL: "http://" + name})
			default:
				// FQDN form (tailnet MagicDNS name) — still tailnet
				add(Alias{Kind: "tailnet", Host: name, URL: "http://" + name})
			}
		}
	}

	// cloudflared public routes
	for _, r := range snap.Cloudflare {
		if ports[r.Port] {
			add(Alias{Kind: "public", Host: r.Hostname, URL: "https://" + r.Hostname})
		}
	}

	// auto tailnet link for every non-loopback listener: <host>:<port>
	for _, l := range listeners {
		if isLoopback(l.Addr) {
			continue
		}
		add(Alias{
			Kind: "tailnet",
			Host: snap.SelfName,
			URL:  fmt.Sprintf("http://%s:%d", snap.SelfName, l.Port),
		})
	}

	return aliases
}

func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}

// probeAll probes the first TCP listener of each service concurrently.
func probeAll(services []Service) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := range services {
		svc := &services[i]
		var target *Listen
		for j := range svc.Listen {
			if svc.Listen[j].Proto == "tcp" {
				target = &svc.Listen[j]
				break
			}
		}
		if target == nil {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s *Service, port int) {
			defer wg.Done()
			defer func() { <-sem }()
			s.Health = probe(port)
		}(svc, target.Port)
	}
	wg.Wait()
}

func probe(port int) *Health {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	ms := int(time.Since(start).Milliseconds())
	if err != nil {
		state := "unknown"
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			state = "down" // connection refused
		}
		return &Health{State: state, MS: ms}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return &Health{State: "up", HTTP: resp.StatusCode, MS: ms}
}

// --- store -------------------------------------------------------------------

// Store caches the current services snapshot and broadcasts changes to
// SSE subscribers.
type Store struct {
	mu        sync.RWMutex
	services  []Service
	scannedAt time.Time
	subs      map[chan []Service]struct{}
	logger    *slog.Logger
}

func NewStore(ctx context.Context, interval time.Duration, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Store{subs: map[chan []Service]struct{}{}, logger: logger}
	s.refresh(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refresh(ctx)
			}
		}
	}()
	return s
}

func (s *Store) refresh(ctx context.Context) {
	snap := collect.Collect(ctx)
	services := Build(snap)
	s.mu.Lock()
	s.services = services
	s.scannedAt = time.Now()
	s.mu.Unlock()
	s.broadcast(services)
}

// Scan forces an immediate rescan (POST /api/scan) and returns the result.
func (s *Store) Scan(ctx context.Context) []Service {
	s.refresh(ctx)
	return s.Snapshot()
}

func (s *Store) Snapshot() []Service {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Service, len(s.services))
	copy(out, s.services)
	return out
}

func (s *Store) ScannedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scannedAt
}

func (s *Store) Subscribe() chan []Service {
	ch := make(chan []Service, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	ch <- s.Snapshot() // send current state immediately
	return ch
}

func (s *Store) Unsubscribe(ch chan []Service) {
	s.mu.Lock()
	delete(s.subs, ch)
	s.mu.Unlock()
	close(ch)
}

func (s *Store) broadcast(services []Service) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.subs {
		select {
		case ch <- services:
		default:
		}
	}
}

// Find returns one service by pid.
func (s *Store) Find(pid int) (Service, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, svc := range s.services {
		if svc.PID == pid {
			return svc, true
		}
	}
	return Service{}, false
}
