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
	raw := `{"mode":"execute","plan":{"version":2,"device_profile":"mobile","start_url":"https://funhub-web-test.guadd.fun/","actions":[{"id":"wait-search-entry","action":"wait_for","locator":{"kind":"label","value":"打开搜索页","exact":true}},{"id":"open-search","action":"click","locator":{"kind":"label","value":"打开搜索页","exact":true},"screenshot_after":true},{"id":"wait-search-input","action":"wait_for","locator":{"kind":"placeholder","value":"请输入搜索关键词","exact":true}},{"id":"enter-nickname","action":"fill","locator":{"kind":"placeholder","value":"请输入搜索关键词","exact":true},"value":"汤圆"},{"id":"submit-search","action":"click","locator":{"kind":"role","value":"button","name":"搜索","exact":true},"screenshot_after":true},{"id":"wait-user-tab","action":"wait_for","locator":{"kind":"role","value":"button","name":"用户","exact":true}},{"id":"switch-user-results","action":"click","locator":{"kind":"role","value":"button","name":"用户","exact":true},"screenshot_after":true},{"id":"wait-user-results","action":"wait_for","locator":{"kind":"text","value":"汤圆","exact":false},"screenshot_after":true},{"id":"capture-user-results","action":"screenshot"}],"assertions":[{"kind":"visible_text","value":"用户"},{"kind":"visible_text","value":"汤圆"}],"request_captures":[{"id":"user-search-parameters","action_id":"switch-user-results","url_contains":"/api/content/getRecommendVideoList","method":"GET","source":"query","fields":["extra_params","page","page_size"]}],"response_assertions":[{"id":"user-name-fields-differ","action_id":"switch-user-results","url_contains":"/api/content/getRecommendVideoList","kind":"json_fields_not_equal","left_field":"nick_name","right_field":"text"}]},"policy":{"allowed_origins":["https://base-resources.chainthink.cn","https://funhub-web-test.guadd.fun","https://truss-api-test.guadd.fun"],"application_origins":["https://funhub-web-test.guadd.fun"],"start_origins":["https://funhub-web-test.guadd.fun"],"private_origins":["https://base-resources.chainthink.cn","https://funhub-web-test.guadd.fun","https://truss-api-test.guadd.fun"],"auth_origins":[],"is_prod":false},"headless":true}`
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
	var requestFactContent, responseFactContent, responseAssertionContent []byte
	for _, artifact := range result.Artifacts {
		t.Logf("artifact reference: kind=%s path=%s", artifact.Kind, artifact.Path)
		if artifact.Kind != "network" && artifact.Kind != "console" && artifact.Kind != "browser_actions" && artifact.Kind != "request_facts" && artifact.Kind != "response_facts" && artifact.Kind != "response_assertions" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(request.StagingDir, strings.TrimPrefix(artifact.Path, "browser/")))
		if readErr != nil {
			t.Logf("read %s artifact: %v", artifact.Kind, readErr)
			continue
		}
		t.Logf("%s artifact: %s", artifact.Kind, content)
		switch artifact.Kind {
		case "request_facts":
			requestFactContent = content
		case "response_facts":
			responseFactContent = content
		case "response_assertions":
			responseAssertionContent = content
		}
	}
	if result.Status != "completed" {
		t.Fatalf("live browser validation did not complete: status=%s code=%s failed_action=%s", result.Status, result.ErrorCode, result.FailedActionID)
	}
	var requestFacts []struct {
		MatchedRequests int `json:"matched_requests"`
		Fields          []struct {
			Path    string `json:"path"`
			Present bool   `json:"present"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(requestFactContent, &requestFacts); err != nil || len(requestFacts) < 1 || requestFacts[0].MatchedRequests < 1 {
		t.Fatalf("live request facts were not captured: facts=%+v err=%v", requestFacts, err)
	}
	for _, field := range requestFacts[0].Fields {
		if !field.Present {
			t.Fatalf("live request field %q was not captured", field.Path)
		}
	}
	var responseAssertions []struct {
		MatchedObjects int    `json:"matched_objects"`
		LeftField      string `json:"left_field"`
		RightField     string `json:"right_field"`
	}
	if err := json.Unmarshal(responseAssertionContent, &responseAssertions); err != nil || len(responseAssertions) != 1 || responseAssertions[0].MatchedObjects < 1 {
		t.Fatalf("live response assertion was not evaluated: assertions=%+v err=%v", responseAssertions, err)
	}
	if responseAssertions[0].LeftField != "nick_name" || responseAssertions[0].RightField != "text" {
		t.Fatalf("live response assertion fields = %+v", responseAssertions[0])
	}
	var responseFacts []struct {
		Fields []struct {
			Path string `json:"path"`
		} `json:"fields"`
		EqualFieldPairs []struct {
			LeftField      string `json:"left_field"`
			RightField     string `json:"right_field"`
			MatchedObjects int    `json:"matched_objects"`
		} `json:"equal_field_pairs"`
	}
	if err := json.Unmarshal(responseFactContent, &responseFacts); err != nil || len(responseFacts) < 1 {
		t.Fatalf("live automatic response facts were not captured: facts=%+v err=%v", responseFacts, err)
	}
	foundPair := false
	for _, fact := range responseFacts {
		for _, pair := range fact.EqualFieldPairs {
			if ((pair.LeftField == "nick_name" && pair.RightField == "text") || (pair.LeftField == "text" && pair.RightField == "nick_name")) && pair.MatchedObjects > 0 {
				foundPair = true
			}
		}
	}
	if !foundPair {
		t.Fatalf("live automatic response facts did not include nick_name/text equality: %+v", responseFacts)
	}
}
