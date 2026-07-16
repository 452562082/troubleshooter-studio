//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"bytes"
	"context"
	"encoding/json"
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

	"golang.org/x/sys/unix"
)

type runtimeParentCrashProcessTree struct {
	WrapperPID    int `json:"wrapper_pid"`
	TargetPID     int `json:"target_pid"`
	GrandchildPID int `json:"grandchild_pid"`
}

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

func TestExecCommandRunnerParentCrashTerminatesOwnedProcessGroup(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "parent-crash-tree-ready")
	grandchildReadyPath := filepath.Join(temporary, "parent-crash-grandchild-ready")
	markerPath := filepath.Join(temporary, "parent-crash-grandchild-survived")
	parent := exec.Command(os.Args[0], "-test.run=^TestRuntimeCommandProcessTreeHelper$")
	parent.Env = mergeCommandEnvironment(os.Environ(), []string{
		"TSHOOT_RUNTIME_PROCESS_HELPER=crash-controller",
		"TSHOOT_RUNTIME_PROCESS_READY=" + readyPath,
		"TSHOOT_RUNTIME_PROCESS_GRANDCHILD_READY=" + grandchildReadyPath,
		"TSHOOT_RUNTIME_PROCESS_MARKER=" + markerPath,
	})
	parent.Dir = temporary
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	cleanupGroup := true
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
		if !cleanupGroup {
			return
		}
		info, err := readRuntimeParentCrashProcessTree(readyPath)
		if err != nil {
			return
		}
		if info.WrapperPID > 1 && info.WrapperPID != os.Getpid() && info.WrapperPID != parent.Process.Pid && info.WrapperPID != unix.Getpgrp() {
			_ = syscall.Kill(-info.WrapperPID, syscall.SIGKILL)
		}
	})

	waitForRuntimeTestFile(t, readyPath, 5*time.Second, func() {
		_ = parent.Process.Kill()
	})
	info, err := readRuntimeParentCrashProcessTree(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.WrapperPID <= 1 || info.TargetPID <= 1 || info.GrandchildPID <= 1 {
		t.Fatalf("invalid parent-crash process tree: %+v", info)
	}
	if info.WrapperPID == os.Getpid() || info.WrapperPID == parent.Process.Pid || info.WrapperPID == unix.Getpgrp() {
		t.Fatalf("wrapper PGID %d is not isolated from test/parent processes", info.WrapperPID)
	}
	if got, err := unix.Getpgid(info.TargetPID); err != nil || got != info.WrapperPID {
		t.Fatalf("target PGID = %d, %v; want wrapper-owned PGID %d", got, err, info.WrapperPID)
	}
	if got, err := unix.Getpgid(info.GrandchildPID); err != nil || got != info.WrapperPID {
		t.Fatalf("grandchild PGID = %d, %v; want wrapper-owned PGID %d", got, err, info.WrapperPID)
	}

	crashedAt := time.Now()
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = parent.Process.Wait()
	deadline := crashedAt.Add(processGroupTerminationGrace + 2*time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-info.WrapperPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			cleanupGroup = false
			break
		}
		if err != nil {
			t.Fatalf("probe wrapper-owned PGID %d: %v", info.WrapperPID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cleanupGroup {
		t.Fatalf("wrapper-owned PGID %d survived parent crash beyond bounded grace", info.WrapperPID)
	}
	markerDeadline := crashedAt.Add(3*time.Second + 500*time.Millisecond)
	if remaining := time.Until(markerDeadline); remaining > 0 {
		time.Sleep(remaining)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("grandchild wrote delayed marker after parent crash cleanup: %v", err)
	}
}

func TestNodeWorkerRunnerParentCrashRemovesPlaintextSession(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "parent-crash-worker.mjs")
	readyPath := filepath.Join(temporary, "parent-crash-worker-ready")
	plaintextLocatorPath := filepath.Join(temporary, "parent-crash-plaintext-path")
	workerSource := `
import { writeFileSync } from 'node:fs';
process.on('SIGTERM', () => {});
writeFileSync(process.env.TSHOOT_RUNTIME_PROCESS_READY, 'ready');
setInterval(() => {}, 1000);
`
	if err := os.WriteFile(workerPath, []byte(workerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(os.Args[0], "-test.run=^TestRuntimeCommandProcessTreeHelper$")
	parent.Env = mergeCommandEnvironment(os.Environ(), []string{
		"TSHOOT_RUNTIME_PROCESS_HELPER=crash-worker-controller",
		"TSHOOT_RUNTIME_PROCESS_READY=" + readyPath,
		"TSHOOT_RUNTIME_PROCESS_WORKER=" + workerPath,
		"TSHOOT_RUNTIME_PROCESS_PLAINTEXT_PATH=" + plaintextLocatorPath,
	})
	parent.Dir = temporary
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	var plaintextPath string
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_, _ = parent.Process.Wait()
		if plaintextPath != "" {
			_ = os.Remove(plaintextPath)
		}
	})
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, func() {
		_ = parent.Process.Kill()
	})
	waitForRuntimeTestFile(t, plaintextLocatorPath, 5*time.Second, func() {
		_ = parent.Process.Kill()
	})
	encodedPath, err := os.ReadFile(plaintextLocatorPath)
	if err != nil {
		t.Fatal(err)
	}
	plaintextPath = strings.TrimSpace(string(encodedPath))
	if !filepath.IsAbs(plaintextPath) || filepath.Clean(filepath.Dir(plaintextPath)) != filepath.Clean(os.TempDir()) {
		t.Fatalf("plaintext session path %q is not an absolute OS temp file", plaintextPath)
	}
	state, err := os.ReadFile(plaintextPath)
	if err != nil || string(state) != `{"cookies":[{"value":"parent-crash-secret"}]}` {
		t.Fatalf("plaintext session before crash=%q err=%v", state, err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = parent.Process.Wait()
	deadline := time.Now().Add(processGroupTerminationGrace + 2*time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Lstat(plaintextPath)
		if errors.Is(err, os.ErrNotExist) {
			plaintextPath = ""
			return
		}
		if err != nil {
			t.Fatalf("inspect plaintext session after parent crash: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("plaintext browser session remained after parent crash: %s", plaintextPath)
}

func TestNodeWorkerRunnerReturnsLoginStateAfterWrapperRemovesPlaintext(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "login-state-worker.mjs")
	workerSource := `
import { readFileSync, writeFileSync } from 'node:fs';
const request = JSON.parse(readFileSync(0, 'utf8'));
writeFileSync(request.storage_state_path, JSON.stringify({ cookies: [{ value: 'new-session-secret' }], origins: [] }));
process.stdout.write(JSON.stringify({ status: 'completed', artifacts: [] }));
`
	if err := os.WriteFile(workerPath, []byte(workerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		nil,
		false,
		os.Remove,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	result, err := (nodeWorkerRunner{}).Run(context.Background(), RuntimePaths{
		Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
	}, workerRequest{Mode: "login", StorageStatePath: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.sessionState) != `{"cookies":[{"value":"new-session-secret"}],"origins":[]}` {
		t.Fatalf("in-memory login state = %q", result.sessionState)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrapper left plaintext login state after normal completion: %v", err)
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
	case "crash-controller":
		err := (execCommandRunner{}).Run(context.Background(), os.Args[0], []string{
			"-test.run=^TestRuntimeCommandProcessTreeHelper$",
		}, []string{"TSHOOT_RUNTIME_PROCESS_HELPER=crash-target"}, "", nil, io.Discard, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
	case "crash-worker-controller":
		key := SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"}
		path, err := createPlaintextSessionTemp(key, []byte(`{"cookies":[{"value":"parent-crash-secret"}]}`), true, os.Remove)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_PLAINTEXT_PATH"), []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = (nodeWorkerRunner{}).Run(context.Background(), RuntimePaths{
			Root:         filepath.Dir(os.Getenv("TSHOOT_RUNTIME_PROCESS_WORKER")),
			BrowsersPath: filepath.Join(filepath.Dir(os.Getenv("TSHOOT_RUNTIME_PROCESS_WORKER")), "browsers"),
			WorkerPath:   os.Getenv("TSHOOT_RUNTIME_PROCESS_WORKER"),
		}, workerRequest{Mode: "execute", StorageStatePath: path}, nil)
		if err != nil {
			t.Fatal(err)
		}
	case "crash-target":
		signal.Ignore(syscall.SIGTERM)
		command := exec.Command(os.Args[0], "-test.run=^TestRuntimeCommandProcessTreeHelper$")
		command.Env = mergeCommandEnvironment(os.Environ(), []string{"TSHOOT_RUNTIME_PROCESS_HELPER=crash-grandchild"})
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		waitForRuntimeTestFile(t, os.Getenv("TSHOOT_RUNTIME_PROCESS_GRANDCHILD_READY"), 5*time.Second, func() {
			_ = command.Process.Kill()
		})
		info := runtimeParentCrashProcessTree{WrapperPID: os.Getppid(), TargetPID: os.Getpid(), GrandchildPID: command.Process.Pid}
		encoded, err := json.Marshal(info)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_READY"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "crash-grandchild":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_GRANDCHILD_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(os.Getenv("TSHOOT_RUNTIME_PROCESS_MARKER"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
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

func readRuntimeParentCrashProcessTree(path string) (runtimeParentCrashProcessTree, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return runtimeParentCrashProcessTree{}, err
	}
	var info runtimeParentCrashProcessTree
	if err := json.Unmarshal(encoded, &info); err != nil {
		return runtimeParentCrashProcessTree{}, err
	}
	return info, nil
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
