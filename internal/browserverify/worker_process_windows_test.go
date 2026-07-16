//go:build windows

package browserverify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestConfigureWorkerProcessUsesKillOnCloseJobWrapper(t *testing.T) {
	command := exec.CommandContext(context.Background(), "cmd.exe", "/c", "exit", "0")
	originalPath := command.Path
	configureWorkerProcess(command)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != executable {
		t.Fatalf("wrapped command path = %q, want current executable %q", command.Path, executable)
	}
	if len(command.Args) < 3 || command.Args[1] != windowsJobWrapperArgument || command.Args[2] != originalPath {
		t.Fatalf("wrapped command args = %q", command.Args)
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
