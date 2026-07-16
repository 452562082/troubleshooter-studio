//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWorkerProcessWaitRequestsWrapperCleanupBeforeWrapperReapAndPGIDReuse(t *testing.T) {
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(statusWrite).Encode(unixTargetStatus{}); err != nil {
		t.Fatal(err)
	}
	if err := statusWrite.Close(); err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlRead.Close()
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	reaped := false
	reused := false
	controller := &workerProcessController{
		command:      command,
		statusRead:   statusRead,
		controlWrite: controlWrite,
		signalProcessGroup: func(processGroup int, signal syscall.Signal) error {
			if reaped || reused {
				t.Fatalf("reused process group received signal %v after wrapper reap", signal)
			}
			return nil
		},
		beforeGroupCleanup: func() {
			time.Sleep(25 * time.Millisecond)
			if reaped {
				t.Fatal("wrapper was reaped during artificial cleanup delay")
			}
		},
		waitWrapper: func(*exec.Cmd) error {
			command := make([]byte, 1)
			if _, err := io.ReadFull(controlRead, command); err != nil {
				t.Fatalf("read wrapper cleanup command: %v", err)
			}
			if command[0] != unixWrapperCleanupCommand {
				t.Fatalf("wrapper cleanup command = %q, want %q", command[0], unixWrapperCleanupCommand)
			}
			reaped = true
			reused = true
			return nil
		},
	}
	if err := controller.wait(command); err != nil {
		t.Fatal(err)
	}
	if err := controller.finish(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
}

func TestWorkerProcessCancelCannotSignalAfterOverlappingWaitReapsWrapper(t *testing.T) {
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(statusWrite).Encode(unixTargetStatus{}); err != nil {
		t.Fatal(err)
	}
	if err := statusWrite.Close(); err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlRead.Close()
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	firstProbeEntered := make(chan struct{})
	releaseFirstProbe := make(chan struct{})
	var stateMu sync.Mutex
	probeCount := 0
	reaped := false
	signaledAfterReap := false
	controller := &workerProcessController{
		command:      command,
		statusRead:   statusRead,
		controlWrite: controlWrite,
		signalProcessGroup: func(_ int, signal syscall.Signal) error {
			stateMu.Lock()
			if reaped {
				signaledAfterReap = true
			}
			if signal == 0 {
				probeCount++
				if probeCount == 1 {
					close(firstProbeEntered)
					stateMu.Unlock()
					<-releaseFirstProbe
					return nil
				}
				stateMu.Unlock()
				return syscall.ESRCH
			}
			stateMu.Unlock()
			return nil
		},
		sleep: func(time.Duration) {},
		grace: time.Second,
		waitWrapper: func(*exec.Cmd) error {
			stateMu.Lock()
			reaped = true
			stateMu.Unlock()
			return nil
		},
	}
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- controller.cancel(command) }()
	select {
	case <-firstProbeEntered:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not enter process-group probe")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- controller.wait(command) }()
	waitReturnedDuringCancel := false
	select {
	case <-waitDone:
		waitReturnedDuringCancel = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirstProbe)
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not finish after probe release")
	}
	if !waitReturnedDuringCancel {
		select {
		case err := <-waitDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("wait did not finish after Cancel")
		}
	}
	lateCancelErr := controller.cancel(command)
	stateMu.Lock()
	gotSignaledAfterReap := signaledAfterReap
	stateMu.Unlock()
	if waitReturnedDuringCancel {
		t.Fatal("wait reaped wrapper while Cancel still owned group termination")
	}
	if gotSignaledAfterReap {
		t.Fatal("Cancel signaled numeric PGID after wrapper reap/reuse")
	}
	if !errors.Is(lateCancelErr, os.ErrProcessDone) {
		t.Fatalf("late Cancel error = %v, want os.ErrProcessDone", lateCancelErr)
	}
}

func TestWorkerProcessExpectedKillDoesNotRecordCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(statusWrite).Encode(unixTargetStatus{}); err != nil {
		t.Fatal(err)
	}
	if err := statusWrite.Close(); err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlRead.Close()
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	controller := &workerProcessController{
		ctx:                ctx,
		statusRead:         statusRead,
		controlWrite:       controlWrite,
		signalProcessGroup: func(int, syscall.Signal) error { return nil },
		waitWrapper:        func(*exec.Cmd) error { return nil },
	}
	if err := controller.kill(command); err != nil {
		t.Fatal(err)
	}
	if err := controller.wait(command); err != nil {
		t.Fatalf("expected cleanup became context cancellation: %v", err)
	}
}

func TestUnixPlaintextCleanupRejectsUnmanagedAndReboundPaths(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink, err := os.CreateTemp(os.TempDir(), ".tshoot-browser-session-symlink-")
	if err != nil {
		t.Fatal(err)
	}
	symlinkPath := symlink.Name()
	if err := symlink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(symlinkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, symlinkPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(symlinkPath) })
	if identity, err := openUnixPlaintextCleanupIdentity(symlinkPath); err == nil {
		_ = identity.Close()
		t.Fatal("symlink plaintext cleanup path was accepted")
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "keep" {
		t.Fatalf("symlink victim changed: %q err=%v", content, err)
	}

	original, err := os.CreateTemp(os.TempDir(), ".tshoot-browser-session-rebound-")
	if err != nil {
		t.Fatal(err)
	}
	originalPath := original.Name()
	if err := original.Chmod(0o600); err != nil {
		t.Fatal(err)
	}
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(originalPath) })
	identity, err := openUnixPlaintextCleanupIdentity(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.CreateTemp(os.TempDir(), ".tshoot-browser-session-replacement-")
	if err != nil {
		t.Fatal(err)
	}
	replacementPath := replacement.Name()
	if _, err := replacement.WriteString("replacement-must-remain"); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Chmod(0o600); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, originalPath); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUnixPlaintextSession(identity, originalPath); err == nil {
		t.Fatal("rebound plaintext cleanup path was removed")
	}
	if content, err := os.ReadFile(originalPath); err != nil || string(content) != "replacement-must-remain" {
		t.Fatalf("rebound file changed: %q err=%v", content, err)
	}
}
