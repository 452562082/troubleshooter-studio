package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestServiceTopologyE2E_ScansGeneratesAndQueriesThreeLayerPath(t *testing.T) {
	cfg, repoPaths := topologyE2EConfigAndRepos(t)
	result, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("RunAutoAnalyze: %v", err)
	}
	assertTopologyE2EFormalPath(t, result.Topology, "automatic")

	workspace := generateTopologyE2EWorkspace(t, cfg, repoPaths, result)
	query := runTopologyE2EQuery(t, workspace, "GET", "/api/orders/123")
	if query.Status != "ok" || len(query.Paths) == 0 {
		t.Fatalf("query result=%#v", query)
	}
	if got, want := query.Paths[0].Services, []string{"mall-web", "mall-bff", "mall-order"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query services=%v, want %v", got, want)
	}
	if got := len(query.Paths[0].Edges); got != 2 {
		t.Fatalf("query edge count=%d, want 2: %#v", got, query.Paths[0].Edges)
	}
	for index, edge := range query.Paths[0].Edges {
		if len(edge.Evidence) == 0 || len(edge.Evidence[0].Reasons) == 0 || edge.Evidence[0].FromLocation == "" || edge.Evidence[0].ToLocation == "" {
			t.Fatalf("query edge %d lacks evidence: %#v", index, edge)
		}
	}

	formal := readTopologyE2EYAML(t, filepath.Join(workspace, "skills/routing/references/service-topology.yaml"))
	dependency := readTopologyE2EYAML(t, filepath.Join(workspace, "skills/routing/references/service-dependency-map.yaml"))
	formalServices := topologyE2EServiceDownstreams(t, formal)
	dependencyServices := topologyE2EServiceDownstreams(t, dependency)
	if !reflect.DeepEqual(dependencyServices, formalServices) {
		t.Fatalf("dependency projection=%v, formal topology=%v", dependencyServices, formalServices)
	}
}

func TestServiceTopologyE2E_MissingOrderRepositoryGeneratesPartialEvidence(t *testing.T) {
	cfg, repoPaths := topologyE2EConfigAndRepos(t)
	delete(repoPaths, "mall-order")
	result, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("RunAutoAnalyze partial: %v", err)
	}
	var missing *topology.RepositoryStatus
	for index := range result.Topology.Repositories {
		if result.Topology.Repositories[index].Repo == "mall-order" {
			missing = &result.Topology.Repositories[index]
			break
		}
	}
	if missing == nil || missing.State != "failed" || missing.Error == "" {
		t.Fatalf("missing repo status=%#v, all=%#v", missing, result.Topology.Repositories)
	}

	workspace := generateTopologyE2EWorkspace(t, cfg, repoPaths, result)
	evidence := readTopologyE2EYAML(t, filepath.Join(workspace, "skills/routing/references/endpoint-evidence.yaml"))
	if state := topologyE2ERepositoryState(t, evidence, "mall-order"); state != "failed" {
		t.Fatalf("generated mall-order state=%q, want failed", state)
	}
}

func TestServiceTopologyE2E_ConfirmedOverrideCachesUntilRepositoryHeadChanges(t *testing.T) {
	cfg, repoPaths := topologyE2EConfigAndRepos(t)
	cfg.ServiceTopology.Overrides = []config.ServiceTopologyOverride{{
		Action: "confirm", FromService: "mall-bff", ToService: "mall-order",
		Protocol: "http", Method: "POST", Path: "/internal/orders",
	}}

	var firstLogs []string
	first, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths, OnLog: func(message string) {
		firstLogs = append(firstLogs, message)
	}})
	if err != nil {
		t.Fatalf("first RunAutoAnalyze: %v", err)
	}
	assertTopologyE2EFormalPath(t, first.Topology, "confirmed")

	var cachedLogs []string
	cached, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths, OnLog: func(message string) {
		cachedLogs = append(cachedLogs, message)
	}})
	if err != nil {
		t.Fatalf("cached RunAutoAnalyze: %v", err)
	}
	if cached != first || !topologyE2ELogContains(cachedLogs, "auto-analyze cache 命中") {
		t.Fatalf("unchanged HEAD did not hit cache: first=%p cached=%p logs=%v", first, cached, cachedLogs)
	}

	stableCfg := *cfg
	stableCfg.System.ID += "-stable"
	stable, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: &stableCfg, RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("prime stable analysis: %v", err)
	}
	commitTopologyE2ERepo(t, repoPaths["mall-order"], "head-change.txt", "changed\n")

	var changedLogs []string
	changed, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: cfg, RepoPaths: repoPaths, OnLog: func(message string) {
		changedLogs = append(changedLogs, message)
	}})
	if err != nil {
		t.Fatalf("changed RunAutoAnalyze: %v", err)
	}
	if changed == first || topologyE2ELogContains(changedLogs, "auto-analyze cache 命中") || !topologyE2ELogContains(changedLogs, "auto-analyze 开始扫") {
		t.Fatalf("changed HEAD reused old analysis: first=%p changed=%p logs=%v", first, changed, changedLogs)
	}

	stableCached, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: &stableCfg, RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("stable cached analysis: %v", err)
	}
	if stableCached == stable {
		t.Fatal("stable key unexpectedly ignored the shared repository HEAD change")
	}

	// The previous, unchanged analysis key remains independently cached. Restore
	// the original HEAD and prove it can still be reused without another scan.
	gitTopologyE2E(t, repoPaths["mall-order"], "reset", "--hard", "HEAD^")
	reused, err := RunAutoAnalyze(RunAutoAnalyzeOptions{Cfg: &stableCfg, RepoPaths: repoPaths})
	if err != nil {
		t.Fatalf("reused stable analysis: %v", err)
	}
	if reused != stable {
		t.Fatalf("unchanged analysis key was evicted: first=%p reused=%p", stable, reused)
	}
}

type topologyE2EQueryResult struct {
	Status string `json:"status"`
	Paths  []struct {
		Services []string `json:"services"`
		Edges    []struct {
			Evidence []struct {
				Reasons      []string `json:"reasons"`
				FromLocation string   `json:"from_location"`
				ToLocation   string   `json:"to_location"`
			} `json:"evidence"`
		} `json:"edges"`
	} `json:"paths"`
}

func topologyE2EConfigAndRepos(t *testing.T) (*config.SystemConfig, map[string]string) {
	t.Helper()
	root := topologyE2EProjectRoot(t)
	cfg, err := config.Load(filepath.Join(root, "examples/three-tier-troubleshooter.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !containsTopologyE2EString(cfg.Generation.SkillsWhitelist, "service-topology-query") {
		cfg.Generation.SkillsWhitelist = append(cfg.Generation.SkillsWhitelist, "service-topology-query")
	}

	repoPaths := make(map[string]string, 3)
	for _, name := range []string{"mall-web", "mall-bff", "mall-order"} {
		source := filepath.Join(root, "examples/fake-repos", "topology-"+strings.TrimPrefix(name, "mall-"))
		destination := filepath.Join(t.TempDir(), name)
		copyTopologyE2EDirectory(t, source, destination)
		initTopologyE2ERepo(t, destination)
		repoPaths[name] = destination
	}
	return cfg, repoPaths
}

func assertTopologyE2EFormalPath(t *testing.T, snapshot topology.Snapshot, bffOrderStatus string) {
	t.Helper()
	wantPairs := map[string]string{"mall-web>mall-bff": "automatic", "mall-bff>mall-order": bffOrderStatus}
	for _, edge := range snapshot.Edges {
		key := edge.FromService + ">" + edge.ToService
		want, ok := wantPairs[key]
		if !ok || edge.Status != want {
			continue
		}
		if edge.FromEndpoint == "" || edge.ToEndpoint == "" || len(edge.Reasons) == 0 {
			t.Fatalf("formal edge lacks endpoint evidence: %#v", edge)
		}
		delete(wantPairs, key)
	}
	if len(wantPairs) != 0 {
		t.Fatalf("missing formal edges %v in %#v", wantPairs, snapshot.Edges)
	}
}

func generateTopologyE2EWorkspace(t *testing.T, cfg *config.SystemConfig, repoPaths map[string]string, result *analyzerpipe.Result) string {
	t.Helper()
	root := topologyE2EProjectRoot(t)
	output := t.TempDir()
	g := generator.New(cfg, filepath.Join(root, "templates"), output)
	g.RepoLocalPaths = repoPaths
	g.LoadAnalysisResult(result)
	if err := g.Generate(); err != nil {
		t.Fatalf("generate workspace: %v", err)
	}
	return filepath.Join(output, "templates/workspace-template")
}

func runTopologyE2EQuery(t *testing.T, workspace, method, path string) topologyE2EQueryResult {
	t.Helper()
	script := filepath.Join(workspace, "skills/service-topology-query/scripts/query.py")
	output, err := exec.Command("python3", script, "--workspace", workspace, "--method", method, "--path", path, "--max-depth", "3", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("query service topology: %v\n%s", err, output)
	}
	var result topologyE2EQueryResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode query result: %v\n%s", err, output)
	}
	return result
}

func readTopologyE2EYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return document
}

func topologyE2EServiceDownstreams(t *testing.T, document map[string]any) map[string][]string {
	t.Helper()
	services, ok := document["services"].(map[string]any)
	if !ok {
		t.Fatalf("services shape=%T %#v", document["services"], document["services"])
	}
	result := make(map[string][]string, len(services))
	for name, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("service %s shape=%T", name, raw)
		}
		items, _ := service["downstream"].([]any)
		for _, item := range items {
			result[name] = append(result[name], item.(string))
		}
		if result[name] == nil {
			result[name] = []string{}
		}
	}
	return result
}

func topologyE2ERepositoryState(t *testing.T, document map[string]any, repo string) string {
	t.Helper()
	items, ok := document["repositories"].([]any)
	if !ok {
		t.Fatalf("repositories shape=%T", document["repositories"])
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["repo"] == repo {
			return item["state"].(string)
		}
	}
	return ""
}

func copyTopologyE2EDirectory(t *testing.T, source, destination string) {
	t.Helper()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read fixture %s: %v", source, err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(destination, entry.Name())
		if entry.IsDir() {
			copyTopologyE2EDirectory(t, src, dst)
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func initTopologyE2ERepo(t *testing.T, path string) {
	t.Helper()
	gitTopologyE2E(t, path, "init", "-q")
	gitTopologyE2E(t, path, "config", "user.email", "topology@example.test")
	gitTopologyE2E(t, path, "config", "user.name", "Topology Fixture")
	gitTopologyE2E(t, path, "add", ".")
	gitTopologyE2E(t, path, "commit", "-qm", "fixture")
}

func commitTopologyE2ERepo(t *testing.T, repo, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTopologyE2E(t, repo, "add", name)
	gitTopologyE2E(t, repo, "commit", "-qm", "change head")
}

func gitTopologyE2E(t *testing.T, repo string, args ...string) {
	t.Helper()
	command := append([]string{"-C", repo}, args...)
	if output, err := exec.Command("git", command...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func topologyE2EProjectRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}

func topologyE2ELogContains(logs []string, fragment string) bool {
	for _, message := range logs {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func containsTopologyE2EString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
