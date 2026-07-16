//go:build windows

package browserverify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestConfigureWorkerProcessUsesKillOnCloseJobWrapper(t *testing.T) {
	command := exec.CommandContext(context.Background(), "cmd.exe", "/c", "exit", "0")
	originalPath := command.Path
	controller, err := configureWorkerProcess(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.finish()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != executable {
		t.Fatalf("wrapped command path = %q, want current executable %q", command.Path, executable)
	}
	if len(command.Args) < 4 || command.Args[1] != windowsJobWrapperArgument || command.Args[3] != originalPath {
		t.Fatalf("wrapped command args = %q", command.Args)
	}
}

func TestConfigureWorkerProcessPassesManagedPlaintextToWrapper(t *testing.T) {
	path, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		nil,
		false,
		os.Remove,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	command := exec.CommandContext(context.Background(), "cmd.exe", "/c", "exit", "0")
	controller, err := configureWorkerProcess(context.Background(), command, path)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.finish()
	if !slices.Contains(command.Args, path) {
		t.Fatalf("wrapped command does not carry managed plaintext path: %q", command.Args)
	}
}

func TestWindowsPlaintextCleanupFailsClosedAfterDirectoryRebound(t *testing.T) {
	originalState := []byte(`{"cookies":[{"value":"original-secret"}]}`)
	path, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		originalState,
		true,
		os.Remove,
	)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(path)
	detachedDirectory := directory + "-detached"
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
		_ = os.RemoveAll(detachedDirectory)
	})
	identity, err := openWindowsPlaintextCleanupIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsPlaintextCleanupIdentity(identity, path); err != nil {
		_ = identity.Close()
		t.Fatal(err)
	}
	if err := os.Rename(directory, detachedDirectory); err != nil {
		_ = identity.Close()
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		_ = identity.Close()
		t.Fatal(err)
	}
	replacementPath := filepath.Join(directory, plaintextSessionFileName)
	replacementState := []byte(`{"cookies":[{"value":"replacement-secret"}]}`)
	if err := os.WriteFile(replacementPath, replacementState, 0o600); err != nil {
		_ = identity.Close()
		t.Fatal(err)
	}

	err = cleanupWindowsPlaintextSessionAfterValidation(identity, path)
	if err == nil {
		t.Fatal("cleanup after directory rebound unexpectedly succeeded")
	}
	got, readErr := os.ReadFile(replacementPath)
	if readErr != nil {
		t.Fatalf("replacement state was removed after directory rebound: %v", readErr)
	}
	if string(got) != string(replacementState) {
		t.Fatalf("replacement state = %q, want %q", got, replacementState)
	}
	detachedStatePath := filepath.Join(detachedDirectory, plaintextSessionFileName)
	if _, statErr := os.Stat(detachedStatePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("original state bound to cleanup identity survived: %v", statErr)
	}
}

func TestWindowsJobGateCancellationNeverLeavesTarget(t *testing.T) {
	stages := []windowsProcessStage{
		windowsProcessStageWrapperStarted,
		windowsProcessStageWrapperAssigned,
		windowsProcessStageTargetReleased,
	}
	for _, stage := range stages {
		t.Run(string(stage), func(t *testing.T) {
			markerPath := filepath.Join(t.TempDir(), "target-survived")
			ctx, cancel := context.WithCancel(context.Background())
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsGatedTargetHelper$")
			command.Env = mergeCommandEnvironment(os.Environ(), []string{"TSHOOT_WINDOWS_GATED_TARGET_MARKER=" + markerPath})
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			controller, err := configureWorkerProcess(ctx, command)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.finish()
			stageEntered := make(chan struct{})
			releaseStage := make(chan struct{})
			var stageOnce sync.Once
			controller.stageHook = func(got windowsProcessStage) {
				if got == stage {
					stageOnce.Do(func() { close(stageEntered) })
					<-releaseStage
				}
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			afterStartDone := make(chan error, 1)
			go func() { afterStartDone <- controller.afterStart(command) }()
			select {
			case <-stageEntered:
			case <-time.After(3 * time.Second):
				_ = controller.kill(command)
				t.Fatalf("afterStart did not reach %s", stage)
			}
			cancel()
			deadline := time.Now().Add(3 * time.Second)
			for !controller.cancelRequested.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if !controller.cancelRequested.Load() {
				close(releaseStage)
				_ = controller.kill(command)
				t.Fatal("cancellation did not overlap afterStart")
			}
			close(releaseStage)
			select {
			case <-afterStartDone:
			case <-time.After(3 * time.Second):
				_ = controller.kill(command)
				t.Fatal("afterStart did not finish after overlapping cancellation")
			}
			waitDone := make(chan error, 1)
			go func() { waitDone <- command.Wait() }()
			select {
			case <-waitDone:
			case <-time.After(3 * time.Second):
				_ = controller.kill(command)
				t.Fatal("gated Windows wrapper did not exit after cancellation")
			}
			time.Sleep(1200 * time.Millisecond)
			if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target survived cancellation at %s: %v", stage, err)
			}
		})
	}
}

func TestWindowsGatedTargetHelper(t *testing.T) {
	markerPath := os.Getenv("TSHOOT_WINDOWS_GATED_TARGET_MARKER")
	if markerPath == "" {
		return
	}
	time.Sleep(time.Second)
	if err := os.WriteFile(markerPath, []byte("alive"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestCreateKillOnCloseJobSetsRequiredLimit(t *testing.T) {
	job, err := createKillOnCloseJob()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if information.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatalf("job limit flags = %#x, want JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE", information.BasicLimitInformation.LimitFlags)
	}
}

func TestWindowsControllerDistinguishesContextCancelFromExpectedKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	controller := &workerProcessController{ctx: ctx, closed: true}
	command := &exec.Cmd{}
	if err := controller.kill(command); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	expectedCleanupErr := controller.contextErr
	controller.mu.Unlock()
	if expectedCleanupErr != nil {
		t.Fatalf("expected cleanup recorded context error %v", expectedCleanupErr)
	}
	cancel()
	if err := controller.cancel(command); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	contextErr := controller.contextErr
	controller.mu.Unlock()
	if !errors.Is(contextErr, context.Canceled) {
		t.Fatalf("Cancel context error = %v, want context.Canceled", contextErr)
	}
}

func TestExecCommandRunnerCancellationClosesJobAndTerminatesDescendants(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "grandchild-ready")
	markerPath := filepath.Join(temporary, "grandchild-survived")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- (execCommandRunner{}).Run(ctx, os.Args[0], []string{"-test.run=^TestWindowsRuntimeProcessTreeHelper$"}, []string{
			"TSHOOT_WINDOWS_PROCESS_HELPER=parent",
			"TSHOOT_WINDOWS_PROCESS_READY=" + readyPath,
			"TSHOOT_WINDOWS_PROCESS_MARKER=" + markerPath,
		}, temporary, nil, os.Stdout, os.Stderr)
	}()
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, cancel)
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected canceled runtime command")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Windows job wrapper did not terminate")
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Windows job descendant survived cancellation: %v", err)
	}
}

func TestWindowsRuntimeProcessTreeHelper(t *testing.T) {
	switch os.Getenv("TSHOOT_WINDOWS_PROCESS_HELPER") {
	case "":
		return
	case "parent":
		command := exec.Command(os.Args[0], "-test.run=^TestWindowsRuntimeProcessTreeHelper$")
		command.Env = mergeCommandEnvironment(os.Environ(), []string{"TSHOOT_WINDOWS_PROCESS_HELPER=grandchild"})
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
	case "grandchild":
		if err := os.WriteFile(os.Getenv("TSHOOT_WINDOWS_PROCESS_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if err := os.WriteFile(os.Getenv("TSHOOT_WINDOWS_PROCESS_MARKER"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown Windows process-tree helper mode %q", os.Getenv("TSHOOT_WINDOWS_PROCESS_HELPER"))
	}
}

func TestWindowsWorkerParentCrashRemovesPlaintextSession(t *testing.T) {
	temporary := t.TempDir()
	readyPath := filepath.Join(temporary, "target-ready")
	locatorPath := filepath.Join(temporary, "plaintext-path")
	survivalPath := filepath.Join(temporary, "target-survived")
	parent := exec.Command(os.Args[0], "-test.run=^TestWindowsPlaintextParentCrashHelper$")
	parent.Env = mergeCommandEnvironment(os.Environ(), []string{
		"TSHOOT_WINDOWS_PLAINTEXT_HELPER=parent",
		"TSHOOT_WINDOWS_PLAINTEXT_READY=" + readyPath,
		"TSHOOT_WINDOWS_PLAINTEXT_LOCATOR=" + locatorPath,
		"TSHOOT_WINDOWS_PLAINTEXT_SURVIVAL=" + survivalPath,
	})
	parent.Stdout = os.Stdout
	parent.Stderr = os.Stderr
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentDone := false
	var plaintextDirectory string
	t.Cleanup(func() {
		if !parentDone {
			_ = parent.Process.Kill()
			_, _ = parent.Process.Wait()
		}
		if plaintextDirectory != "" {
			_ = os.RemoveAll(plaintextDirectory)
		}
	})
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, func() { _ = parent.Process.Kill() })
	encodedPath, err := os.ReadFile(locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	plaintextPath := strings.TrimSpace(string(encodedPath))
	plaintextDirectory, ok := plaintextSessionWorkspace(plaintextPath)
	if !ok {
		t.Fatalf("unmanaged plaintext path %q", plaintextPath)
	}
	state, err := os.ReadFile(plaintextPath)
	if err != nil || !strings.Contains(string(state), "parent-crash-secret") {
		t.Fatalf("plaintext before parent crash=%q err=%v", state, err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	parentDone = true
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(plaintextDirectory); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Lstat(plaintextDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext workspace survived parent crash: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(survivalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target survived parent crash cleanup: %v", err)
	}
}

func TestWindowsPlaintextParentCrashHelper(t *testing.T) {
	switch os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_HELPER") {
	case "":
		return
	case "parent":
		path, err := createPlaintextSessionTemp(
			SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
			nil,
			false,
			os.Remove,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_LOCATOR"), []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(os.Args[0], "-test.run=^TestWindowsPlaintextParentCrashHelper$")
		command.Env = mergeCommandEnvironment(os.Environ(), []string{
			"TSHOOT_WINDOWS_PLAINTEXT_HELPER=target",
			"TSHOOT_WINDOWS_PLAINTEXT_PATH=" + path,
		})
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		controller, err := configureWorkerProcess(context.Background(), command, path)
		if err != nil {
			t.Fatal(err)
		}
		defer controller.finish()
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		if err := controller.afterStart(command); err != nil {
			t.Fatal(err)
		}
		if err := controller.wait(command); err != nil {
			t.Fatal(err)
		}
	case "target":
		path := os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_PATH")
		if err := os.WriteFile(path, []byte(`{"cookies":[{"value":"parent-crash-secret"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if err := os.WriteFile(os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_SURVIVAL"), []byte("alive"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown plaintext helper mode %q", os.Getenv("TSHOOT_WINDOWS_PLAINTEXT_HELPER"))
	}
}
