package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallGitLabReleaseQueryReportsInvalidToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test requires bash")
	}

	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "uname"), `#!/usr/bin/env bash
echo Darwin
`)
	writeExecutable(t, filepath.Join(tmp, "curl"), `#!/usr/bin/env bash
echo "curl: (22) The requested URL returned error: 401" >&2
exit 22
`)

	cmd := exec.Command("bash", "install.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+tmp+":"+os.Getenv("PATH"),
		"SOURCE=gitlab",
		"GITLAB_TOKEN=definitely-invalid",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install.sh succeeded unexpectedly; output:\n%s", out)
	}
	got := string(out)
	if !strings.Contains(got, "GITLAB_TOKEN 无效或已过期") {
		t.Fatalf("expected invalid token hint, got:\n%s", got)
	}
	if strings.Contains(got, "项目可能私有") {
		t.Fatalf("invalid token should not be reported as a possible private project; got:\n%s", got)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
