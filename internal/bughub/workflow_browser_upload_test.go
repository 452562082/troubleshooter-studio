package bughub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareBrowserUploadFilesCapturesOnlyControlledBusinessInputs(t *testing.T) {
	root := t.TempDir()
	sheetPath := filepath.Join(root, "fixture.xlsx")
	screenshotPath := filepath.Join(root, "screenshot.png")
	if err := os.WriteFile(sheetPath, []byte("xlsx-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(screenshotPath, []byte("\x89PNG\r\n\x1a\nfixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, manifest, err := prepareBrowserUploadFiles(Bug{Attachments: []Attachment{
		{ID: "sheet-1", Name: "用户补充测试文件-1.xlsx", Type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", LocalPath: sheetPath},
		{ID: "screenshot-1", Name: "工单截图.png", Type: "image/png", LocalPath: screenshotPath},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != "sheet-1" || string(files[0].Content) != "xlsx-fixture" || files[0].SHA256 == "" {
		t.Fatalf("files = %+v", files)
	}
	if len(manifest) != 1 || manifest[0]["id"] != "sheet-1" || manifest[0]["source"] != "user_supplemental" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestBrowserUploadPlanRequiresAvailableControlledReference(t *testing.T) {
	plan := BrowserPlan{Actions: []BrowserAction{{
		ID: "upload-sheet", Action: "upload_file",
		Locator: &BrowserLocator{Kind: "css", Value: "input[type=file]"},
		FileRef: "sheet-1",
	}}}
	if err := validateBrowserPlanUploadBindings(plan, []BrowserUploadFile{{ID: "sheet-1"}}); err != nil {
		t.Fatalf("controlled reference rejected: %v", err)
	}
	if err := validateBrowserPlanUploadBindings(plan, nil); err == nil || !strings.Contains(err.Error(), "unavailable controlled file") {
		t.Fatalf("missing controlled reference error = %v", err)
	}
}

func TestBrowserScenarioRequiresUploadOnlyFromReproductionContext(t *testing.T) {
	request := BrowserCoordinatorRequest{Bug: Bug{
		Title: "上传媒资失败",
		Steps: "1. 点击创建媒资\n2. 选择文件并上传",
	}}
	if !browserScenarioRequiresFileUpload(request, nil) {
		t.Fatal("file-selection reproduction was not detected")
	}
	request.Bug.Steps = "1. 打开媒资列表\n2. 查看上传历史"
	if browserScenarioRequiresFileUpload(request, nil) {
		t.Fatal("an upload-history title must not require a local file")
	}
}

func TestBrowserScenarioRequiresUploadDoesNotCombineSeparateContexts(t *testing.T) {
	request := BrowserCoordinatorRequest{
		Bug:                Bug{Steps: "1. 进入关注 Tab\n2. 依次选择用户头像查看作者内容"},
		UserClarifications: []string{`{"previous_gap":"没有测试文件，需要什么测试文件"}`},
	}
	if browserScenarioRequiresFileUpload(request, nil) {
		t.Fatal("avatar selection and an unrelated historical file mention must not require a local file")
	}
}

func TestBrowserScenarioLatestClarificationCanRejectInferredUpload(t *testing.T) {
	request := BrowserCoordinatorRequest{
		Bug:                Bug{Steps: "1. 选择文件\n2. 点击用户头像"},
		UserClarifications: []string{"本工单只需点击头像用户并读取接口返回的最新发布时间，不涉及上传或导入操作"},
	}
	if browserScenarioRequiresFileUpload(request, nil) {
		t.Fatal("an explicit latest clarification that the scenario has no upload must override stale reproduction text")
	}
}

func TestNormalizeBrowserUploadFileNameRejectsExecutableAndPaths(t *testing.T) {
	for _, test := range []struct {
		name, mime string
	}{
		{"../fixture.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"fixture.sh", "text/plain"},
		{"fixture.xlsx", "text/plain"},
	} {
		if _, err := NormalizeBrowserUploadFileName(test.name, test.mime); err == nil {
			t.Fatalf("expected %q (%q) to be rejected", test.name, test.mime)
		}
	}
}
