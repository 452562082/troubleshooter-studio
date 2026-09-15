package analyzer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectSubmodulesPrefersDeployableNodeAppsOverUnrelatedGitSubmodule(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "base-frontend")
	writeAnalyzerFixture(t, filepath.Join(repo, "package.json"), `{
  "name": "@funhub/app",
  "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "16.1.6"}
}`)
	writeAnalyzerFixture(t, filepath.Join(repo, "Dockerfile"), "FROM node:22\n")
	writeAnalyzerFixture(t, filepath.Join(repo, "pnpm-workspace.yaml"), "packages:\n  - packages/*\n")
	writeAnalyzerFixture(t, filepath.Join(repo, ".gitmodules"), `[submodule "external/sdk"]
	path = external/sdk
	url = https://example.com/sdk.git
`)
	writeAnalyzerFixture(t, filepath.Join(repo, "external/sdk/package.json"), `{"name":"sdk"}`)

	writeAnalyzerFixture(t, filepath.Join(repo, "packages/document/package.json"), `{
  "name": "@funhub/document",
  "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "16.1.6"}
}`)
	writeAnalyzerFixture(t, filepath.Join(repo, "packages/document/Dockerfile"), "FROM node:22\n")
	writeAnalyzerFixture(t, filepath.Join(repo, "packages/platform/package.json"), `{
  "name": "@funhub/platform",
  "scripts": {"build": "tsdown"},
  "dependencies": {"react": "19.0.0"}
}`)
	writeAnalyzerFixture(t, filepath.Join(repo, "packages/comment-sdk/package.json"), `{
  "name": "@funhub-ugc/comment-sdk",
  "scripts": {"build": "tsdown", "playground:dev": "next dev ./playground"},
  "devDependencies": {"next": "16.1.6"}
}`)

	hints := DetectSubmodules(repo)
	if len(hints) != 2 {
		t.Fatalf("DetectSubmodules() returned %d hints, want 2: %#v", len(hints), hints)
	}
	if hints[0].Name != "base-frontend" || hints[0].SubPath != "." || hints[0].URL != "" {
		t.Fatalf("root hint = %#v, want stable repo root service", hints[0])
	}
	if hints[1].Name != "document" || hints[1].SubPath != "packages/document" || hints[1].URL != "" {
		t.Fatalf("document hint = %#v, want deployable document app", hints[1])
	}
}

func TestExpandNodeDeployableAppsKeepsRootAndAddsChild(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "base-frontend")
	writeAnalyzerFixture(t, filepath.Join(repo, "package.json"), `{
  "name": "@funhub/app",
  "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "16.1.6"}
}`)
	writeAnalyzerFixture(t, filepath.Join(repo, "Dockerfile"), "FROM node:22\n")
	writeAnalyzerFixture(t, filepath.Join(repo, "pnpm-workspace.yaml"), "packages:\n  - packages/*\n")
	writeAnalyzerFixture(t, filepath.Join(repo, "packages/document/package.json"), `{
  "name": "@funhub/document",
  "scripts": {"build": "next build", "start": "next start"},
  "dependencies": {"next": "16.1.6"}
}`)
	writeAnalyzerFixture(t, filepath.Join(repo, "packages/document/Dockerfile"), "FROM node:22\n")

	ra := &RepoAnalysis{ServiceNames: []string{"@funhub/app"}}
	ExpandCmdEntriesAsServiceNames(ra, "base-frontend", repo)
	want := []string{"base-frontend", "base-frontend-document"}
	if len(ra.ServiceNames) != len(want) {
		t.Fatalf("ServiceNames = %#v, want %#v", ra.ServiceNames, want)
	}
	for i := range want {
		if ra.ServiceNames[i] != want[i] {
			t.Fatalf("ServiceNames = %#v, want %#v", ra.ServiceNames, want)
		}
	}
}

func writeAnalyzerFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
