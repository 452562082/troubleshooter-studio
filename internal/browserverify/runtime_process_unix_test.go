//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecCommandRunnerWrapperPreservesArgvEnvironmentStdinAndExit(t *testing.T) {
	temporary := t.TempDir()
	sideEffectPath := filepath.Join(temporary, "shell-interpolation-must-not-run")
	literalArgument := "literal;touch " + sideEffectPath
	var stdout bytes.Buffer
	if err := (execCommandRunner{}).Run(context.Background(), os.Args[0], []string{
		"-test.run=^TestRuntimeCommandProcessTreeHelper$", literalArgument,
	}, []string{
		"TSHOOT_RUNTIME_PROCESS_HELPER=protocol",
		"TSHOOT_RUNTIME_PROTOCOL_ENV=environment-value",
	}, temporary, strings.NewReader("stdin-value"), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := literalArgument + "\nenvironment-value\nstdin-value"
	if stdout.String() != want {
		t.Fatalf("wrapped command protocol output = %q, want %q", stdout.String(), want)
	}
	if _, err := os.Stat(sideEffectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("literal argv was interpreted by a shell: %v", err)
	}
	err := (execCommandRunner{}).Run(context.Background(), os.Args[0], []string{
		"-test.run=^TestRuntimeCommandProcessTreeHelper$",
	}, []string{"TSHOOT_RUNTIME_PROCESS_HELPER=exit"}, temporary, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exit status 23") {
		t.Fatalf("wrapped target exit error = %v, want exit status 23", err)
	}
}

func TestExecCommandRunnerDirectExitCleansDescendantHoldingOutputPipes(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "pipe-grandchild-ready")
	markerPath := filepath.Join(temporary, "pipe-grandchild-survived")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanupNeeded := true
	t.Cleanup(func() {
		if cleanupNeeded {
			killRuntimeTestProcess(t, readyPath)
		}
	})
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		result <- (execCommandRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestRuntimeCommandProcessTreeHelper$"}, []string{
			"TSHOOT_RUNTIME_PROCESS_HELPER=pipe-parent",
			"TSHOOT_RUNTIME_PROCESS_READY=" + readyPath,
			"TSHOOT_RUNTIME_PROCESS_MARKER=" + markerPath,
		}, temporary, nil, io.Discard, io.Discard)
	}()
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, cancel)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runtime command error: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("runtime command returned after %s, want bounded cleanup", elapsed)
		}
	case <-time.After(4 * time.Second):
		killRuntimeTestProcess(t, readyPath)
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatal("runtime command hung on output pipes inherited by a grandchild")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime output-pipe grandchild survived cleanup: %v", err)
	}
	cleanupNeeded = false
}

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

func TestExecCommandRunnerContextCancellationSurvivesTargetExitZero(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "term-zero-ready")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (execCommandRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestRuntimeCommandProcessTreeHelper$"}, []string{
			"TSHOOT_RUNTIME_PROCESS_HELPER=term-zero",
			"TSHOOT_RUNTIME_PROCESS_READY=" + readyPath,
		}, temporary, nil, io.Discard, io.Discard)
	}()
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, cancel)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runtime cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runtime command did not return after target handled SIGTERM with exit 0")
	}
}

func TestExecCommandRunnerPostStartOutputCloseErrorIsBoundedAndClosesParentPipes(t *testing.T) {
	var parentPipes []*os.File
	runner := execCommandRunner{attachOutputs: attachOutputsWithInjectedCloseError(&parentPipes)}
	temporary := t.TempDir()
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(context.Background(), os.Args[0], []string{
			"-test.run=^TestRuntimeCommandProcessTreeHelper$",
		}, []string{"TSHOOT_RUNTIME_PROCESS_HELPER=protocol"}, temporary, nil, io.Discard, io.Discard)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, errInjectedOutputClose) {
			t.Fatalf("runtime post-Start error = %v, want injected output close error", err)
		}
		assertFilesClosed(t, parentPipes)
	case <-time.After(3 * time.Second):
		t.Fatal("runtime post-Start output close error hung before wrapper wait")
	}
}

var errInjectedOutputClose = errors.New("injected parent output close error")

func attachOutputsWithInjectedCloseError(parentPipes *[]*os.File) commandOutputAttacher {
	return func(command *exec.Cmd) (*ownedCommandOutputs, error) {
		outputs, err := attachOwnedCommandOutputs(command)
		if err != nil {
			return nil, err
		}
		*parentPipes = []*os.File{outputs.stdoutRead, outputs.stdoutWrite, outputs.stderrRead, outputs.stderrWrite}
		outputs.closeWrite = func(file *os.File) error {
			return errors.Join(file.Close(), errInjectedOutputClose)
		}
		return outputs, nil
	}
}

func assertFilesClosed(t *testing.T, files []*os.File) {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("no parent pipe files were captured")
	}
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("parent pipe %q remains open: %v", file.Name(), err)
		}
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
	case "pipe-parent":
		command := exec.Command(os.Args[0], "-test.run=^TestRuntimeCommandProcessTreeHelper$")
		command.Env = mergeCommandEnvironment(os.Environ(), []string{"TSHOOT_RUNTIME_PROCESS_HELPER=pipe-grandchild"})
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		waitForRuntimeTestFile(t, os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), 5*time.Second, func() {
			_ = command.Process.Kill()
		})
	case "grandchild":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_MARKER"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "pipe-grandchild":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_MARKER"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "protocol":
		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(os.Stdout, os.Args[len(os.Args)-1]+"\n"+os.Getenv("TSHOOT_RUNTIME_PROTOCOL_ENV")+"\n"+string(content))
		os.Exit(0)
	case "exit":
		os.Exit(23)
	case "term-zero":
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		defer signal.Stop(terminated)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		<-terminated
		os.Exit(0)
	default:
		t.Fatalf("unknown process-tree helper mode %q", os.Getenv("TSHOOT_RUNTIME_PROCESS_HELPER"))
	}
}

func killRuntimeTestProcess(t *testing.T, path string) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Logf("read process id for cleanup: %v", err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Logf("parse process id for cleanup: %v", err)
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}
