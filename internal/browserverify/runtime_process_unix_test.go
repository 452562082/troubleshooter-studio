//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestExecCommandRunnerCancellationTerminatesDescendantProcessGroup(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "grandchild-ready")
	markerPath := filepath.Join(temporary, "grandchild-survived")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (execCommandRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestRuntimeCommandProcessTreeHelper$"}, []string{
			"TSHOOT_RUNTIME_PROCESS_HELPER=parent",
			"TSHOOT_RUNTIME_PROCESS_READY=" + readyPath,
			"TSHOOT_RUNTIME_PROCESS_MARKER=" + markerPath,
		}, temporary, nil, io.Discard, io.Discard)
	}()
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, cancel)
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled runtime command")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runtime command process group was not terminated within the grace period")
	}
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime command grandchild survived cancellation: %v", err)
	}
}

func TestRuntimeCommandProcessTreeHelper(t *testing.T) {
	switch os.Getenv("TSHOOT_RUNTIME_PROCESS_HELPER") {
	case "":
		return
	case "parent":
		command := exec.Command(os.Args[0], "-test.run=^TestRuntimeCommandProcessTreeHelper$")
		command.Env = mergeCommandEnvironment(os.Environ(), []string{"TSHOOT_RUNTIME_PROCESS_HELPER=grandchild"})
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
	case "grandchild":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_MARKER"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown process-tree helper mode %q", os.Getenv("TSHOOT_RUNTIME_PROCESS_HELPER"))
	}
}
