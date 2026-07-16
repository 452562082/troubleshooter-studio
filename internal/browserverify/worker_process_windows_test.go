//go:build windows

package browserverify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
