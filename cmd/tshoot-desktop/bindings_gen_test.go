package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

func TestGen_AutoAnalyzeTopologyReachesGeneratedWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	saved := map[string]string{}
	for _, name := range []string{"mall-web", "mall-bff", "mall-order"} {
		saved[name] = filepath.Join(home, name)
		if err := os.MkdirAll(saved[name], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := userconfig.SetRepoPathsForSystem("mall", saved); err != nil {
		t.Fatal(err)
	}
	previous := runAutoAnalyzeForGen
	runAutoAnalyzeForGen = func(options agent.RunAutoAnalyzeOptions) (*analyzerpipe.Result, error) {
		if len(options.RepoPaths) != len(saved) {
			t.Fatalf("auto-analyze repo paths=%#v, want %#v", options.RepoPaths, saved)
		}
		return &analyzerpipe.Result{Topology: topology.Snapshot{
			SchemaVersion: topology.SchemaVersion,
			Services: []topology.ServiceDescriptor{
				{Repo: "mall-web", Service: "mall-web", Role: "frontend"},
				{Repo: "mall-order", Service: "mall-order", Role: "backend"},
			},
			Edges: []topology.CandidateEdge{{
				FromEndpoint: "mall-web:out", ToEndpoint: "mall-order:in",
				FromService: "mall-web", ToService: "mall-order",
				Protocol: "http", Method: "GET", Path: "/internal/orders",
				Status: "automatic", Confidence: .98,
			}},
		}}, nil
	}
	t.Cleanup(func() { runAutoAnalyzeForGen = previous })

	root := desktopProjectRoot(t)
	yamlBytes, err := os.ReadFile(filepath.Join(root, "examples", "three-tier-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	app := &App{templateRoot: filepath.Join(root, "templates")}
	if _, err := app.Gen(string(yamlBytes), out); err != nil {
		t.Fatalf("Gen: %v", err)
	}
	path := filepath.Join(out, "templates", "workspace-template", "skills", "routing", "references", "service-topology.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated topology: %v", err)
	}
	for _, want := range []string{`from: "mall-web"`, `to: "mall-order"`, `status: "automatic"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("generated topology missing %q:\n%s", want, data)
		}
	}
	queryScript := filepath.Join(out, "templates", "workspace-template", "skills", "service-topology-query", "scripts", "query.py")
	if _, err := os.Stat(queryScript); err != nil {
		t.Fatalf("Gen dropped service-topology-query despite saved repository paths: %v", err)
	}
}

func desktopProjectRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
