package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeYAML 写一个带注释、空行、多仓库的 troubleshooter.yaml 片段，用来验证 fixer
// 做行级替换时不破坏原格式。
func writeYAML(t *testing.T) string {
	t.Helper()
	src := `# 由 tshoot init 生成，可手工调整
system:
  id: shop                    # 机器可读标识
  name: "Shop"

# repos: 所有纳入排障范围的代码仓库
repos:
  - name: order-service
    url: git@x:y.git
    role: backend
    stack: go                 # go/java/node/php/python
    service_names:
      - order-service

  - name: payment-service
    url: git@x:z.git
    role: backend
    stack: go

infrastructure:
  config_center:
    type: nacos               # nacos / apollo / consul / kubernetes / env-vars / none
    endpoints:
      - env: dev
        addr: "x"
`
	p := filepath.Join(t.TempDir(), "sys.yaml")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanFixes_StackMismatch(t *testing.T) {
	p := writeYAML(t)
	issues := []Issue{
		{
			Category: CatStackMismatch,
			FixKey:   "repo.order-service.stack",
			FixValue: "java",
		},
	}
	patches, err := PlanFixes(p, issues)
	if err != nil {
		t.Fatalf("PlanFixes: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	got := patches[0]
	if got.From != "go" || got.To != "java" {
		t.Errorf("bad patch values: %+v", got)
	}
	if got.Key != "stack" {
		t.Errorf("Key should be stack, got %q", got.Key)
	}
	if got.Line == 0 {
		t.Errorf("Line should be non-zero")
	}
	if !strings.Contains(got.Path, "order-service") {
		t.Errorf("Path should reference target repo, got %q", got.Path)
	}
}

func TestPlanFixes_StackMismatch_TargetsCorrectRepo(t *testing.T) {
	p := writeYAML(t)
	// yaml 里有 order-service + payment-service 两个 repo 都是 stack: go
	// fix 只针对 order-service，确认 patch 指向的行号是正确的那一行
	issues := []Issue{{
		Category: CatStackMismatch,
		FixKey:   "repo.order-service.stack",
		FixValue: "java",
	}}
	patches, _ := PlanFixes(p, issues)
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}

	// 应用后只有 order-service 的 stack 变，payment-service 保持 go
	if _, err := ApplyAndWrite(p, patches); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	content := string(data)
	// order-service 块里 stack: java
	orderIdx := strings.Index(content, "name: order-service")
	paymentIdx := strings.Index(content, "name: payment-service")
	if orderIdx < 0 || paymentIdx < 0 {
		t.Fatalf("blocks not found, content:\n%s", content)
	}
	orderBlock := content[orderIdx:paymentIdx]
	paymentBlock := content[paymentIdx:]
	if !strings.Contains(orderBlock, "stack: java") {
		t.Errorf("order-service.stack should be java, block:\n%s", orderBlock)
	}
	if !strings.Contains(paymentBlock, "stack: go") {
		t.Errorf("payment-service.stack should stay go, block:\n%s", paymentBlock)
	}
}

func TestPlanFixes_ConfigCenterType(t *testing.T) {
	p := writeYAML(t)
	issues := []Issue{{
		Category: CatConfigCenterUnused,
		FixKey:   "config-center.type",
		FixValue: "none",
	}}
	patches, err := PlanFixes(p, issues)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 {
		t.Fatalf("want 1 patch, got %d", len(patches))
	}
	if patches[0].From != "nacos" || patches[0].To != "none" {
		t.Errorf("bad patch: %+v", patches[0])
	}
	if patches[0].Key != "type" {
		t.Errorf("Key should be type, got %q", patches[0].Key)
	}
}

func TestPlanFixes_SkipNoFixKey(t *testing.T) {
	p := writeYAML(t)
	issues := []Issue{
		{Category: CatMissingRepo, FixKey: ""}, // 无 FixKey，忽略
		{Category: CatOriginMismatch},          // 同上
	}
	patches, err := PlanFixes(p, issues)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 0 {
		t.Errorf("expected 0 patches, got %d", len(patches))
	}
}

func TestPlanFixes_UnknownFixKey(t *testing.T) {
	p := writeYAML(t)
	issues := []Issue{{FixKey: "unknown.category.here", FixValue: "x"}}
	patches, _ := PlanFixes(p, issues)
	if len(patches) != 0 {
		t.Errorf("unknown FixKey should produce 0 patches, got %d", len(patches))
	}
}

func TestApplyAndWrite_BitPerfect(t *testing.T) {
	p := writeYAML(t)
	orig, _ := os.ReadFile(p)
	origLines := strings.Split(string(orig), "\n")

	issues := []Issue{{FixKey: "repo.order-service.stack", FixValue: "java"}}
	patches, _ := PlanFixes(p, issues)
	backup, err := ApplyAndWrite(p, patches)
	if err != nil {
		t.Fatal(err)
	}
	// backup 应该存在且内容等于原文件
	bk, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(bk) != string(orig) {
		t.Errorf("backup content diverges from original")
	}

	// 修改后文件：对比每一行，允许变化的行只有目标行
	newContent, _ := os.ReadFile(p)
	newLines := strings.Split(string(newContent), "\n")
	if len(newLines) != len(origLines) {
		t.Fatalf("line count changed: %d → %d", len(origLines), len(newLines))
	}
	changedLines := 0
	for i := range origLines {
		if origLines[i] != newLines[i] {
			changedLines++
			if !strings.Contains(newLines[i], "stack: java") {
				t.Errorf("line %d changed unexpectedly:\nbefore: %q\nafter:  %q", i+1, origLines[i], newLines[i])
			}
		}
	}
	if changedLines != 1 {
		t.Errorf("expected exactly 1 changed line, got %d", changedLines)
	}

	// 注释 / 空行 保留
	if !strings.Contains(string(newContent), "# 由 tshoot init 生成") {
		t.Errorf("top-of-file comment lost")
	}
	if !strings.Contains(string(newContent), "# repos: 所有纳入排障范围") {
		t.Errorf("section comment lost")
	}
}

func TestApplyAndWrite_NoPatches(t *testing.T) {
	p := writeYAML(t)
	orig, _ := os.ReadFile(p)
	if _, err := ApplyAndWrite(p, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(orig) {
		t.Errorf("ApplyAndWrite with no patches should not change file")
	}
}
