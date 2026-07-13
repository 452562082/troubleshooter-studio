package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListBotWorkspaceFiles_IDEShowsInternalAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	invalidateBotPathsCache()
	t.Cleanup(invalidateBotPathsCache)

	root := filepath.Join(home, ".claude")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{
		filepath.Join(root, "agents"),
		filepath.Join(root, "skills", "base-troubleshooter", "routing"),
		filepath.Join(root, "skills", "base-validator", "bug-verifier"),
		filepath.Join(root, "scripts", "base-troubleshooter"),
		filepath.Join(root, "scripts", "base-validator"),
	} {
		must(os.MkdirAll(dir, 0o755))
	}
	must(os.WriteFile(filepath.Join(root, "agents", "base-troubleshooter.md"), []byte("troubleshooter"), 0o644))
	must(os.WriteFile(filepath.Join(root, "agents", "base-validator.md"), []byte("validator"), 0o644))
	must(os.WriteFile(filepath.Join(root, "skills", "base-troubleshooter", "routing", "SKILL.md"), []byte("routing"), 0o644))
	must(os.WriteFile(filepath.Join(root, "skills", "base-validator", "bug-verifier", "SKILL.md"), []byte("verify"), 0o644))
	must(os.WriteFile(filepath.Join(root, "scripts", "base-validator", "verify.py"), []byte("print('ok')\n"), 0o644))
	meta := `{
  "schema_version": 1,
  "system_id": "base",
  "target": "claude-code",
  "troubleshooter_yaml": "system:\n  id: base\n"
}`
	botRoot := filepath.Join(root, "skills", "base-troubleshooter")
	must(os.WriteFile(filepath.Join(botRoot, "tshoot.json"), []byte(meta), 0o644))

	app := &App{}
	tree, err := app.ListBotWorkspaceFiles(botRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"agents/base-troubleshooter.md",
		"agents/base-validator.md",
		"skills/base-troubleshooter/routing/SKILL.md",
		"skills/base-validator/bug-verifier/SKILL.md",
		"scripts/base-validator/verify.py",
	} {
		if !treeHasPath(tree, want) {
			t.Fatalf("workspace tree missing %s: %+v", want, tree)
		}
	}
	read, err := app.ReadBotWorkspaceFile(botRoot, "agents/base-validator.md")
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "validator" {
		t.Fatalf("read validator agent = %q", read.Content)
	}
	if err := app.WriteBotWorkspaceFile(botRoot, "skills/base-validator/bug-verifier/SKILL.md", "updated"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "skills", "base-validator", "bug-verifier", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated" {
		t.Fatalf("write did not hit validator skill, got %q", data)
	}
}

func treeHasPath(node *FileNode, path string) bool {
	if node == nil {
		return false
	}
	if node.Path == path {
		return true
	}
	for i := range node.Children {
		if treeHasPath(&node.Children[i], path) {
			return true
		}
	}
	return false
}
