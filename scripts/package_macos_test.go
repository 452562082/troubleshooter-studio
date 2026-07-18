package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPackageMacOSBundlesPreparedBrowserRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test requires bash")
	}
	root := repositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "studio")
	if err := os.WriteFile(binary, []byte("desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := filepath.Join(temporary, "runtime", "1.61.1")
	for _, directory := range []string{
		filepath.Join(runtimeRoot, "node_modules", "playwright"),
		filepath.Join(runtimeRoot, "browsers"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(runtimeRoot, ".runtime-ready.json"), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(temporary, "TroubleshooterStudio.app")
	command := exec.Command("bash", filepath.Join(root, "scripts", "package-macos.sh"))
	command.Env = append(os.Environ(),
		"BIN="+binary,
		"BUNDLE_DIR="+bundle,
		"BUNDLE_NAME=TroubleshooterStudio",
		"BUNDLE_ID=studio.troubleshooter.desktop.test",
		"VERSION=test",
		"BROWSER_RUNTIME_SRC="+runtimeRoot,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package-macos.sh: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(bundle, "Contents", "MacOS", "TroubleshooterStudio"),
		filepath.Join(bundle, "Contents", "Resources", "browser-runtime", "1.61.1", ".runtime-ready.json"),
		filepath.Join(bundle, "Contents", "Resources", "browser-runtime", "1.61.1", "node_modules", "playwright"),
		filepath.Join(bundle, "Contents", "Resources", "browser-runtime", "1.61.1", "browsers"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("bundled path %q: %v", path, err)
		}
	}
}

func TestPackageMacOSRejectsMissingBrowserRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test requires bash")
	}
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "studio")
	if err := os.WriteFile(binary, []byte("desktop"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("bash", filepath.Join(repositoryRoot(t), "scripts", "package-macos.sh"))
	command.Env = append(os.Environ(),
		"BIN="+binary,
		"BUNDLE_DIR="+filepath.Join(temporary, "TroubleshooterStudio.app"),
		"BUNDLE_NAME=TroubleshooterStudio",
		"BUNDLE_ID=studio.troubleshooter.desktop.test",
		"VERSION=test",
		"BROWSER_RUNTIME_SRC="+filepath.Join(temporary, "missing"),
	)
	if err := command.Run(); err == nil {
		t.Fatal("package-macos.sh accepted a missing browser runtime")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
