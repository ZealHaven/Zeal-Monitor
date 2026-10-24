// Package collect discovers listening ports, their owning processes, working
// directories and the hostnames mapped to them.
//
// Privileged reads (ss -p, /proc/<pid> of other users' processes) go through
// `sudo -n` — the account has NOPASSWD: ALL, so this is non-interactive.
package collect

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Listener is one socket a process is listening on.
type Listener struct {
	Proto string `json:"proto"` // tcp | udp
	Addr  string `json:"addr"`  // bind address, interface suffix stripped
	Port  int    `json:"port"`
	PID   int    `json:"pid"` // 0 when unknown
	Proc  string `json:"proc"`
}

// ProcessInfo is everything we know about a process.
type ProcessInfo struct {
	PID  int    `json:"pid"`
	User string `json:"user"`
	Comm string `json:"comm"`
	Cmd  string `json:"cmd"`
	Cwd  string `json:"cwd"`
	Unit string `json:"unit"` // owning systemd unit, if any
}

// NginxVhost maps server_name(s) to the upstream port via proxy_pass.
type NginxVhost struct {
	ServerNames []string `json:"server_names"`
	Port        int      `json:"port"`
	File        string   `json:"file"`
}

// CloudflareRoute maps a public hostname to its origin port.
type CloudflareRoute struct {
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
}

// HostEntry is a name from /etc/hosts.
type HostEntry struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
}

// DockerContainer is a running container with its published host ports.
type DockerContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Ports []int  `json:"ports"`
}

// Snapshot is one full discovery pass.
type Snapshot struct {
	Listeners  []Listener          `json:"listeners"`
	Processes  map[int]ProcessInfo `json:"processes"`
	Nginx      []NginxVhost        `json:"nginx"`
	Cloudflare []CloudflareRoute   `json:"cloudflare"`
	Hosts      []HostEntry         `json:"hosts"`
	Docker     []DockerContainer   `json:"docker"`
	SelfName   string              `json:"self_name"`  // MagicDNS short name (tailnet domain)
	TailnetIP  string              `json:"tailnet_ip"` // tailscale IP of this box
	ScannedAt  time.Time           `json:"scanned_at"`
}

// Collect runs one discovery pass. Individual source failures are tolerated
// (a missing tool or unreadable file yields empty data, not an error).
func Collect(ctx context.Context) *Snapshot {
	snap := &Snapshot{
		Processes: map[int]ProcessInfo{},
		ScannedAt: time.Now(),
	}

	if out := runSudo(ctx, "ss", "-H", "-tlnpH"); out != "" {
		snap.Listeners = append(snap.Listeners, parseSS(out, "tcp")...)
	}
	if out := runSudo(ctx, "ss", "-H", "-ulnpH"); out != "" {
		snap.Listeners = append(snap.Listeners, parseSS(out, "udp")...)
	}

	snap.Processes = parsePS(runSudo(ctx, "ps", "-eo", "pid=,user=,comm=,args="))
	if pidList := pids(snap); len(pidList) > 0 {
		script := "for p in " + strings.Join(pidList, " ") + `; do printf "%s\t%s\n" "$p" "$(readlink /proc/$p/cwd 2>/dev/null)"; done`
		applyCwd(snap, runSudo(ctx, "sh", "-c", script))
	}
	applyUnits(snap)

	if out, err := exec.CommandContext(ctx, "docker", "ps", "--format", "{{.Names}}\t{{.Image}}\t{{.Ports}}").Output(); err == nil {
		snap.Docker = parseDocker(out)
	}

	snap.Nginx = append(parseNginxDir("/etc/nginx/sites-enabled"), parseNginxDir("/etc/nginx/conf.d")...)
	snap.Cloudflare = parseCloudflared("/etc/cloudflared/config.yml")
	snap.Hosts = parseHosts("/etc/hosts")

	snap.SelfName = strings.TrimSpace(mustRun(ctx, "hostname"))
	snap.TailnetIP = strings.TrimSpace(run(ctx, "tailscale", "ip", "-4"))

	return snap
}

func pids(snap *Snapshot) []string {
	seen := map[int]bool{}
	var out []string
	for _, l := range snap.Listeners {
		if l.PID > 0 && !seen[l.PID] {
			seen[l.PID] = true
			out = append(out, strconv.Itoa(l.PID))
		}
	}
	return out
}

// --- sudo / exec helpers ----------------------------------------------------

func runSudo(ctx context.Context, args ...string) string {
	return run(ctx, append([]string{"sudo", "-n"}, args...)...)
}

func run(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func mustRun(ctx context.Context, args ...string) string {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// --- ss ---------------------------------------------------------------------

var (
	ssWithProc = regexp.MustCompile(`^(LISTEN|UNCONN)\s+\d+\s+\d+\s+(\S+):(\d+)\s+\S+\s+users:\(\("([^"]*)",pid=(\d+),fd=\d+\)\)`)
	ssNoProc   = regexp.MustCompile(`^(LISTEN|UNCONN)\s+\d+\s+\d+\s+(\S+):(\d+)\s+\S+\s*$`)
)

func parseSS(out, proto string) []Listener {
	var listeners []Listener
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := ssWithProc.FindStringSubmatch(line); m != nil {
			port, _ := strconv.Atoi(m[3])
			pid, _ := strconv.Atoi(m[5])
			listeners = append(listeners, Listener{
				Proto: proto,
				Addr:  cleanAddr(m[2]),
				Port:  port,
				PID:   pid,
				Proc:  m[4],
			})
			continue
		}
		if m := ssNoProc.FindStringSubmatch(line); m != nil {
			port, _ := strconv.Atoi(m[3])
			listeners = append(listeners, Listener{Proto: proto, Addr: cleanAddr(m[2]), Port: port})
		}
	}
	return listeners
}

// cleanAddr strips the interface suffix systemd-resolved/ss add (127.0.0.53%lo).
func cleanAddr(addr string) string {
	if i := strings.Index(addr, "%"); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// --- ps ---------------------------------------------------------------------

func parsePS(out string) map[int]ProcessInfo {
	procs := map[int]ProcessInfo{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		procs[pid] = ProcessInfo{
			PID:  pid,
			User: fields[1],
			Comm: fields[2],
			Cmd:  strings.Join(fields[3:], " "),
		}
	}
	return procs
}

func applyCwd(snap *Snapshot, out string) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		p := snap.Processes[pid]
		p.Cwd = parts[1]
		snap.Processes[pid] = p
	}
}

// applyUnits reads /proc/<pid>/cgroup (world-readable) to find the owning
// systemd unit, e.g. "nginx.service".
func applyUnits(snap *Snapshot) {
	for pid := range snap.Processes {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			paths := strings.Split(line, ":")
			if len(paths) != 3 {
				continue
			}
			unit := paths[2]
			if i := strings.LastIndex(unit, "/"); i >= 0 {
				unit = unit[i+1:]
			}
			if strings.HasSuffix(unit, ".service") {
				p := snap.Processes[pid]
				p.Unit = unit
				snap.Processes[pid] = p
				break
			}
		}
	}
}

// --- docker -----------------------------------------------------------------

var dockerPortRe = regexp.MustCompile(`(\d+)->`)

func parseDocker(out []byte) []DockerContainer {
	var containers []DockerContainer
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		c := DockerContainer{Name: fields[0], Image: fields[1]}
		seen := map[int]bool{}
		for _, m := range dockerPortRe.FindAllStringSubmatch(fields[2], -1) {
			port, _ := strconv.Atoi(m[1])
			if !seen[port] {
				seen[port] = true
				c.Ports = append(c.Ports, port)
			}
		}
		containers = append(containers, c)
	}
	return containers
}

// --- nginx ------------------------------------------------------------------

func parseNginxDir(dir string) []NginxVhost {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var vhosts []NginxVhost
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		vhosts = append(vhosts, parseNginx(string(data), path)...)
	}
	return vhosts
}

// parseNginx walks a config line by line, tracking server-block depth so
// server_name/proxy_pass pairs stay associated across location blocks.
func parseNginx(content, file string) []NginxVhost {
	var vhosts []NginxVhost
	var names []string
	depth := 0
	inServer := false

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.SplitN(trimmed, "#", 2)[0] // strip comments
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "server") && strings.Contains(trimmed, "{") {
			names = nil
			inServer = true
		}
		if inServer {
			if m := serverNameRe.FindStringSubmatch(trimmed); m != nil {
				names = append(names, strings.Fields(m[1])...)
			}
			if m := proxyPassRe.FindStringSubmatch(trimmed); m != nil {
				if port := portFromURL(m[1]); port > 0 && len(names) > 0 {
					vhosts = append(vhosts, NginxVhost{ServerNames: names, Port: port, File: file})
				}
			}
		}

		depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		if inServer && depth <= 0 {
			inServer = false
			names = nil
			depth = 0
		}
	}
	return vhosts
}

var (
	serverNameRe = regexp.MustCompile(`server_name\s+([^;]+);`)
	proxyPassRe  = regexp.MustCompile(`proxy_pass\s+(\S+?);`)
)

// portFromURL extracts the port from proxy_pass targets like
// http://127.0.0.1:20128, http://localhost:8088 or
// http://<host>:<port>. Defaults to 80.
func portFromURL(raw string) int {
	s := strings.TrimSuffix(raw, ";")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if port, err := strconv.Atoi(s[i+1:]); err == nil {
			return port
		}
	}
	return 80
}

// --- cloudflared ------------------------------------------------------------

func parseCloudflared(path string) []CloudflareRoute {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var routes []CloudflareRoute
	var host string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if m := hostnameRe.FindStringSubmatch(trimmed); m != nil {
			host = m[1]
			continue
		}
		if m := serviceRe.FindStringSubmatch(trimmed); m != nil && host != "" {
			if port := portFromURL(m[1]); port > 0 {
				routes = append(routes, CloudflareRoute{Hostname: host, Port: port})
			}
			host = ""
		}
	}
	return routes
}

var (
	hostnameRe = regexp.MustCompile(`^-\s+hostname:\s+(\S+)`)
	serviceRe  = regexp.MustCompile(`^service:\s+(\S+)`)
)

// --- /etc/hosts --------------------------------------------------------------

func parseHosts(path string) []HostEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []HostEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, name := range fields[1:] {
			if strings.HasSuffix(name, ".local") {
				entries = append(entries, HostEntry{IP: fields[0], Name: name})
			}
		}
	}
	return entries
}
