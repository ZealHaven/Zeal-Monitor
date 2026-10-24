package collect

import (
	"os"
	"testing"
)

func TestParseSS(t *testing.T) {
	out := `LISTEN 0      511                        0.0.0.0:5678       0.0.0.0:*    users:(("node",pid=1663,fd=19))
LISTEN 0      4096                 127.0.0.53%lo:53         0.0.0.0:*    users:(("systemd-resolve",pid=2677071,fd=17))
UNCONN 0      0                        0.0.0.0:53490        0.0.0.0:*    users:(("tailscaled",pid=2679872,fd=24))
LISTEN 0      128                      127.0.0.1:43491      0.0.0.0:*
`
	got := parseSS(out, "tcp")
	if len(got) != 4 { // last line has no process -> ssNoProc matches
		t.Fatalf("want 4, got %d: %+v", len(got), got)
	}
	if got[0].Port != 5678 || got[0].PID != 1663 || got[0].Proc != "node" || got[0].Addr != "0.0.0.0" {
		t.Fatalf("first listener wrong: %+v", got[0])
	}
	if got[1].Addr != "127.0.0.53" {
		t.Fatalf("addr suffix not stripped: %q", got[1].Addr)
	}
	// udp line parsed as udp proto
	udp := parseSS("UNCONN 0 0 0.0.0.0:123 0.0.0.0:* users:((\"ntpd\",pid=9,fd=3))\n", "udp")
	if len(udp) != 1 || udp[0].Proto != "udp" || udp[0].Port != 123 {
		t.Fatalf("udp parse: %+v", udp)
	}
}

func TestCleanAddr(t *testing.T) {
	if got := cleanAddr("127.0.0.53%lo"); got != "127.0.0.53" {
		t.Fatalf("got %q", got)
	}
	if got := cleanAddr("[::]"); got != "[::]" {
		t.Fatalf("got %q", got)
	}
}

func TestParsePS(t *testing.T) {
	out := ` 1663 alice node node /home/alice/.nvm/versions/node/v22.9.0/bin/n8n start --tunnel
 2677071 systemd-resolve systemd-resolved /usr/bin/systemd-resolved
`
	procs := parsePS(out)
	if len(procs) != 2 {
		t.Fatalf("want 2, got %d", len(procs))
	}
	if procs[1663].User != "alice" || procs[1663].Comm != "node" || procs[1663].Cmd != "node /home/alice/.nvm/versions/node/v22.9.0/bin/n8n start --tunnel" {
		t.Fatalf("proc 1663 wrong: %+v", procs[1663])
	}
}

func TestParseNginx(t *testing.T) {
	content := `server {
    listen 80;
    server_name 9router.local;

    location / {
        proxy_pass http://127.0.0.1:20128;
    }
}
server {
    listen 80;
    server_name jobhunt.local tailnet.example.net;
    location / {
        proxy_pass http://tailnet.example.net:5101;
    }
}
`
	vhosts := parseNginx(content, "test.conf")
	if len(vhosts) != 2 {
		t.Fatalf("want 2 vhosts, got %d: %+v", len(vhosts), vhosts)
	}
	if vhosts[0].Port != 20128 || len(vhosts[0].ServerNames) != 1 || vhosts[0].ServerNames[0] != "9router.local" {
		t.Fatalf("vhost 0 wrong: %+v", vhosts[0])
	}
	if vhosts[1].Port != 5101 || len(vhosts[1].ServerNames) != 2 {
		t.Fatalf("vhost 1 wrong: %+v", vhosts[1])
	}
}

func TestPortFromURL(t *testing.T) {
	cases := map[string]int{
		"http://127.0.0.1:20128":                     20128,
		"http://localhost:8088":                      8088,
		"http://tailnet.example.net:5101": 5101,
		"http://example.com":                         80,
	}
	for in, want := range cases {
		if got := portFromURL(in); got != want {
			t.Errorf("portFromURL(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseCloudflared(t *testing.T) {
	content := `tunnel: abc
ingress:
  - hostname: mermaid.example.com
    service: http://localhost:8088
  - hostname: monitor-dev.example.com
    service: http://127.0.0.1:9050
  - service: http_status:404
`
	tmp := t.TempDir() + "/cf.yml"
	_ = writeFile(tmp, content)
	routes := parseCloudflared(tmp)
	if len(routes) != 2 {
		t.Fatalf("want 2 routes, got %d: %+v", len(routes), routes)
	}
	if routes[0].Hostname != "mermaid.example.com" || routes[0].Port != 8088 {
		t.Fatalf("route 0: %+v", routes[0])
	}
	if routes[1].Hostname != "monitor-dev.example.com" || routes[1].Port != 9050 {
		t.Fatalf("route 1: %+v", routes[1])
	}
}

func TestParseHosts(t *testing.T) {
	content := "127.0.0.1  localhost test1.local 9router.local\n" +
		"# comment\n" +
		"127.0.1.1  zeal-001\n" +
		"192.168.1.5  other.local\n"
	tmp := t.TempDir() + "/hosts"
	_ = writeFile(tmp, content)
	entries := parseHosts(tmp)
	if len(entries) != 3 {
		t.Fatalf("want 3 .local entries, got %d: %+v", len(entries), entries)
	}
}

func TestParseDocker(t *testing.T) {
	out := []byte("uptime-kuma\tlouislam/uptime-kuma:1\t0.0.0.0:9009->3001/tcp, [::]:9009->3001/tcp\n" +
		"local-db-1\tmariadb:11.8\t3306/tcp\n")
	containers := parseDocker(out)
	if len(containers) != 2 {
		t.Fatalf("want 2, got %d", len(containers))
	}
	if containers[0].Name != "uptime-kuma" || len(containers[0].Ports) != 1 || containers[0].Ports[0] != 9009 {
		t.Fatalf("container 0: %+v", containers[0])
	}
	if len(containers[1].Ports) != 0 {
		t.Fatalf("container 1 should have no published ports: %+v", containers[1])
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
