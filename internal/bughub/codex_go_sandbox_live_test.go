package bughub

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexGoSandboxLive(t *testing.T) {
	if os.Getenv("TSHOOT_LIVE_CODEX_GO_SANDBOX") != "1" {
		t.Skip("set TSHOOT_LIVE_CODEX_GO_SANDBOX=1 to run a real Codex Go sandbox probe")
	}
	repository := filepath.Join(t.TempDir(), "probe-repository")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module example.com/tshoot-go-sandbox-probe\n\ngo 1.22\n\nrequire rsc.io/quote v1.5.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "probe_test.go"), []byte("package probe\n\nimport (\n\t\"testing\"\n\t\"rsc.io/quote\"\n)\n\nfunc TestDependencyAndStandardLibrary(t *testing.T) {\n\tif quote.Go() == \"\" { t.Fatal(\"empty quote\") }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	manifest := repositoryAccessManifest{
		Version: 1,
		Phase:   PhaseFix,
		Roots:   []repositoryAccessRoot{{Repo: "sandbox-probe", Path: repository, Access: "write"}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, repositoryAccessManifestName), data, 0o400); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(staging, "go-sandbox-probe-passed")
	prompt := "Run exactly one shell command and do nothing else. The command is:\n" +
		"cd " + shellQuote(repository) + " && go env GOROOT GOCACHE GOMODCACHE GOTELEMETRY GOTELEMETRYDIR GOFLAGS && go mod tidy && go test -count=1 ./... && printf passed > " + shellQuote(resultPath) + "\n" +
		"Do not claim success unless the command exits zero.\n" +
		"STUDIO_EVIDENCE_STAGING_DIR=" + staging + "\n"
	cmd, err := BuildCodexExecCommand("codex", repository, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("real Codex Go sandbox probe failed: %v\n%s", err, output.String())
	}
	result, err := os.ReadFile(resultPath)
	if err != nil || strings.TrimSpace(string(result)) != "passed" {
		t.Fatalf("Codex did not complete the sandboxed Go command: result=%q err=%v\n%s", result, err, output.String())
	}
	for _, directory := range []string{
		filepath.Join(staging, codexGoSandboxDirectory, "build-cache"),
		filepath.Join(staging, codexGoSandboxDirectory, "path", "pkg", "mod", "rsc.io"),
	} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) == 0 {
			t.Fatalf("Codex Go sandbox did not populate %s: entries=%d err=%v", directory, len(entries), err)
		}
	}
}
