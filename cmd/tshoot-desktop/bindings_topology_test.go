package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/analyzerpipe"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
)

func TestAnalyzeServiceTopology_NoResultFromTimeoutOrCancellationReturnsError(t *testing.T) {
	yamlText, paths := desktopTopologyFixture(t)
	previousRun := runAutoAnalyzeForTopology
	previousInvalidate := invalidateAutoAnalyzeForTopology
	runAutoAnalyzeForTopology = func(agent.RunAutoAnalyzeOptions) (*analyzerpipe.Result, error) {
		return nil, nil
	}
	invalidateAutoAnalyzeForTopology = func(*config.SystemConfig, map[string]string) {}
	t.Cleanup(func() {
		runAutoAnalyzeForTopology = previousRun
		invalidateAutoAnalyzeForTopology = previousInvalidate
	})

	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "timeout without result", ctx: context.Background()},
		{name: "canceled runtime", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := &App{}
			app.setRuntimeContext(test.ctx)
			snapshot, err := app.AnalyzeServiceTopology(yamlText, paths)
			if err == nil || snapshot != nil {
				t.Fatalf("snapshot=%#v err=%v, want nil/error so the UI retains its previous snapshot", snapshot, err)
			}
		})
	}
}

func TestAnalyzeServiceTopology_NilRuntimeContextScansFixture(t *testing.T) {
	yamlText, paths := desktopTopologyFixture(t)

	snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Edges) != 2 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if snapshot.SchemaVersion == "" {
		t.Fatal("missing schema version")
	}
}

func TestAnalyzeServiceTopology_InvalidYAML(t *testing.T) {
	snapshot, err := (&App{}).AnalyzeServiceTopology("system: [", nil)
	if err == nil || !strings.Contains(err.Error(), "parse yaml") {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
}

func TestAnalyzeServiceTopology_FewerThanTwoServiceReposReturnsEmptySnapshot(t *testing.T) {
	yamlText, paths := desktopTopologyFixture(t)
	paths = map[string]string{"mall-web": paths["mall-web"]}

	snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, paths)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion == "" || len(snapshot.Edges) != 0 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestAnalyzeServiceTopology_MissingPathKeepsPartialResult(t *testing.T) {
	yamlText, paths := desktopTopologyFixture(t)
	delete(paths, "mall-web")

	snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Edges) != 1 || snapshot.Edges[0].FromService != "mall-bff" || snapshot.Edges[0].ToService != "mall-order" {
		t.Fatalf("partial snapshot=%#v", snapshot)
	}
	for _, repo := range snapshot.Repositories {
		if repo.Repo == "mall-web" && repo.State == "failed" {
			return
		}
	}
	t.Fatalf("missing repository failure in %#v", snapshot.Repositories)
}

func TestAnalyzeServiceTopology_JSONIncludesConfidenceAndReasons(t *testing.T) {
	yamlText, paths := desktopTopologyFixture(t)
	snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, paths)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Edges []struct {
			Confidence float64  `json:"confidence"`
			Reasons    []string `json:"reasons"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Edges) != 2 || payload.Edges[0].Confidence == 0 || len(payload.Edges[0].Reasons) == 0 {
		t.Fatalf("serialized snapshot=%s", data)
	}
}

func TestAnalyzeServiceTopology_ExpandsPathsAndInvalidatesExplicitRefresh(t *testing.T) {
	yamlText, _ := desktopTopologyFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	previousRun := runAutoAnalyzeForTopology
	previousInvalidate := invalidateAutoAnalyzeForTopology
	var invalidated string
	invalidateAutoAnalyzeForTopology = func(cfg *config.SystemConfig, _ map[string]string) { invalidated = cfg.System.ID }
	var gotOptions agent.RunAutoAnalyzeOptions
	runAutoAnalyzeForTopology = func(options agent.RunAutoAnalyzeOptions) (*analyzerpipe.Result, error) {
		gotOptions = options
		if invalidated != "mall" {
			t.Fatalf("RunAutoAnalyze called before cache invalidation: %q", invalidated)
		}
		return &analyzerpipe.Result{Topology: topology.Snapshot{SchemaVersion: topology.SchemaVersion}}, nil
	}
	t.Cleanup(func() {
		runAutoAnalyzeForTopology = previousRun
		invalidateAutoAnalyzeForTopology = previousInvalidate
	})

	snapshot, err := (&App{}).AnalyzeServiceTopology(yamlText, map[string]string{
		"mall-web": "~/src/mall-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != topology.SchemaVersion {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	want := filepath.Join(home, "src", "mall-web")
	if gotOptions.RepoPaths["mall-web"] != want {
		t.Fatalf("repo path=%q want=%q", gotOptions.RepoPaths["mall-web"], want)
	}
}

func desktopTopologyFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "three-tier-troubleshooter.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := map[string]string{
		"mall-web":   filepath.Join(root, "mall-web"),
		"mall-bff":   filepath.Join(root, "mall-bff"),
		"mall-order": filepath.Join(root, "mall-order"),
	}
	writeDesktopTopologyFixtureFile(t, paths["mall-web"], "src/orders.ts", `axios.get("http://mall-bff/api/orders/123")`)
	writeDesktopTopologyFixtureFile(t, paths["mall-bff"], "routes/api.php", `Route::get('/api/orders/{id}', fn () => null);`)
	writeDesktopTopologyFixtureFile(t, paths["mall-bff"], "app/Clients/Order.php", `Http::post('http://mall-order/internal/orders', []);`)
	writeDesktopTopologyFixtureFile(t, paths["mall-order"], "internal/http/order.go", `package http
func routes(r Router) { r.POST("/internal/orders", createOrder) }`)
	return string(data), paths
}

func writeDesktopTopologyFixtureFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
