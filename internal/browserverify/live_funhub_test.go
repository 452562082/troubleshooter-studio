package browserverify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestLiveFunhubRepairWorker(t *testing.T) {
	if os.Getenv("TSHOOT_LIVE_FUNHUB_WORKER") != "1" {
		t.Skip("set TSHOOT_LIVE_FUNHUB_WORKER=1 to run")
	}
	var request workerRequest
	raw := `{"mode":"execute","plan":{"version":1,"start_url":"https://funhub-web-test.guadd.fun/","actions":[{"id":"wait-search-entry","action":"wait_for","locator":{"kind":"text","value":"搜索内容"}},{"id":"open-search","action":"click","locator":{"kind":"text","value":"搜索内容"},"screenshot_after":true},{"id":"wait-search-input","action":"wait_for","locator":{"kind":"placeholder","value":"搜索"}},{"id":"enter-nickname","action":"fill","locator":{"kind":"placeholder","value":"搜索"},"value":"汤圆"},{"id":"submit-search","action":"press","locator":{"kind":"placeholder","value":"搜索"},"key":"Enter","screenshot_after":true},{"id":"wait-user-tab","action":"wait_for","locator":{"kind":"text","value":"用户"}},{"id":"switch-user-results","action":"click","locator":{"kind":"text","value":"用户"},"screenshot_after":true},{"id":"wait-user-results","action":"wait_for","locator":{"kind":"text","value":"汤圆"},"screenshot_after":true},{"id":"capture-user-results","action":"screenshot"}],"assertions":[{"kind":"visible_text","value":"用户"},{"kind":"visible_text","value":"汤圆"}]},"policy":{"allowed_origins":["https://base-resources.chainthink.cn","https://funhub-web-test.guadd.fun","https://truss-api-test.guadd.fun"],"application_origins":["https://funhub-web-test.guadd.fun"],"start_origins":["https://funhub-web-test.guadd.fun"],"private_origins":["https://base-resources.chainthink.cn","https://funhub-web-test.guadd.fun","https://truss-api-test.guadd.fun"],"auth_origins":[],"is_prod":false},"headless":true}`
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	request.StagingDir = t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, ".tshoot", "bugs", "browser-runtime", BrowserRuntimeVersion)
	if configured := os.Getenv("TSHOOT_LIVE_BROWSER_RUNTIME_ROOT"); configured != "" {
		root = configured
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := (nodeWorkerRunner{}).Run(ctx, RuntimePaths{
		Root: root, WorkerPath: filepath.Join(root, "browser_worker.mjs"),
		BrowsersPath: filepath.Join(root, "browsers"), NodeModules: filepath.Join(root, "node_modules"),
	}, request, func(progress bughub.BrowserProgress) { t.Logf("progress: %+v", progress) })
	if err != nil {
		t.Fatalf("worker runner: %v", err)
	}
	t.Logf("worker result: status=%s code=%s failed=%s url=%s title=%s accessibility=%+v", result.Status, result.ErrorCode, result.FailedActionID, result.FinalURL, result.Title, result.AccessibilitySummary)
	for _, artifact := range result.Artifacts {
		t.Logf("artifact reference: kind=%s path=%s", artifact.Kind, artifact.Path)
		if artifact.Kind != "network" && artifact.Kind != "console" && artifact.Kind != "browser_actions" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(request.StagingDir, strings.TrimPrefix(artifact.Path, "browser/")))
		if readErr != nil {
			t.Logf("read %s artifact: %v", artifact.Kind, readErr)
			continue
		}
		t.Logf("%s artifact: %s", artifact.Kind, content)
	}
}
