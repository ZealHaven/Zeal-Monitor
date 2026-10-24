package inventory

import (
	"testing"

	"monitor/backend/main/internal/collect"
)

func TestBuildAliases(t *testing.T) {
	snap := &collect.Snapshot{
		SelfName:  "tailnet",
		TailnetIP: "10.0.0.1",
		Processes: map[int]collect.ProcessInfo{},
		Nginx: []collect.NginxVhost{
			{ServerNames: []string{"9router.local"}, Port: 5678},
			{ServerNames: []string{"monitor.tailnet"}, Port: 5678},
		},
		Cloudflare: []collect.CloudflareRoute{
			{Hostname: "mermaid.example.com", Port: 5678},
		},
	}
	snap.Processes[1663] = collect.ProcessInfo{PID: 1663, User: "alice", Cmd: "node app.js", Cwd: "/home/alice/app"}

	snap.Listeners = []collect.Listener{
		{Proto: "tcp", Addr: "0.0.0.0", Port: 5678, PID: 1663, Proc: "node"},
	}

	services := Build(snap)
	if len(services) != 1 {
		t.Fatalf("want 1 service, got %d", len(services))
	}
	svc := services[0]
	if svc.PID != 1663 || svc.Cwd != "/home/alice/app" {
		t.Fatalf("service wrong: %+v", svc)
	}

	// expect: local 9router.local, tailnet monitor.tailnet, public mermaid.example.com, auto tailnet link
	kinds := map[string]int{}
	for _, a := range svc.Aliases {
		kinds[a.Kind]++
	}
	if kinds["local"] != 1 || kinds["tailnet"] < 2 || kinds["public"] != 1 {
		t.Fatalf("alias kinds wrong: %+v  aliases=%+v", kinds, svc.Aliases)
	}
	// auto tailnet raw link should be present
	found := false
	for _, a := range svc.Aliases {
		if a.URL == "http://tailnet:5678" {
			found = true
		}
	}
	if !found {
		t.Fatalf("auto tailnet link missing: %+v", svc.Aliases)
	}
}

func TestBuildContainerAttribution(t *testing.T) {
	snap := &collect.Snapshot{
		SelfName:  "tailnet",
		Processes: map[int]collect.ProcessInfo{},
		Docker: []collect.DockerContainer{
			{Name: "uptime-kuma", Image: "louislam/uptime-kuma:1", Ports: []int{9009}},
		},
		Listeners: []collect.Listener{
			{Proto: "tcp", Addr: "0.0.0.0", Port: 9009, PID: 7949, Proc: "docker-proxy"},
		},
	}
	services := Build(snap)
	if len(services) != 1 {
		t.Fatalf("want 1, got %d", len(services))
	}
	if services[0].Container != "uptime-kuma" {
		t.Fatalf("container not attributed: %+v", services[0])
	}
}
