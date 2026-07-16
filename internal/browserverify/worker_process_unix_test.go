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

func TestWorkerProcessWaitSurfacesWrapperCleanupFailure(t *testing.T) {
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
	wrapperCleanupErr := errors.New("wrapper cleanup failed")
	controller := &workerProcessController{
		statusRead:       statusRead,
		controlWrite:     controlWrite,
		managedPlaintext: true,
		waitWrapper:      func(*exec.Cmd) error { return wrapperCleanupErr },
	}
	if err := controller.wait(&exec.Cmd{}); !errors.Is(err, wrapperCleanupErr) {
		t.Fatalf("wait error=%v, want wrapper cleanup failure", err)
	}
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

func TestManagedPlaintextKillRequestsWrapperOwnedTermination(t *testing.T) {
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer controlRead.Close()
	signaled := false
	controller := &workerProcessController{
		managedPlaintext: true,
		controlWrite:     controlWrite,
		signalProcessGroup: func(int, syscall.Signal) error {
			signaled = true
			return nil
		},
	}
	if err := controller.kill(&exec.Cmd{Process: &os.Process{Pid: 4242}}); err != nil {
		t.Fatal(err)
	}
	var command [1]byte
	if _, err := io.ReadFull(controlRead, command[:]); err != nil {
		t.Fatal(err)
	}
	if command[0] != unixWrapperCleanupCommand || signaled {
		t.Fatalf("cleanup command=%q signaled=%v", command[0], signaled)
	}
}

func TestUnixPlaintextCleanupRejectsUnmanagedAndReboundPaths(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if identity, err := openUnixPlaintextCleanupIdentity(victim); err == nil {
		_ = identity.Close()
		t.Fatal("unmanaged plaintext cleanup path was accepted")
	}

	path, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		[]byte(`{"cookies":[]}`), true, os.Remove,
	)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(path)
	displaced := directory + ".displaced"
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
		_ = os.RemoveAll(displaced)
	})
	identity, err := openUnixPlaintextCleanupIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(directory, plaintextSessionFileName)
	if err := os.WriteFile(replacementPath, []byte("replacement-must-remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUnixPlaintextSession(identity, path); err == nil {
		t.Fatal("rebound plaintext cleanup directory was removed")
	}
	if content, err := os.ReadFile(replacementPath); err != nil || string(content) != "replacement-must-remain" {
		t.Fatalf("rebound file changed: %q err=%v", content, err)
	}
	if content, err := os.ReadFile(victim); err != nil || string(content) != "keep" {
		t.Fatalf("unmanaged victim changed: %q err=%v", content, err)
	}
}

func TestUnixPlaintextCleanupAcceptsAtomicReplacementAndRemovesWorkerSiblings(t *testing.T) {
	path, err := createPlaintextSessionTemp(
		SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
		[]byte(`{"cookies":[{"value":"old"}]}`), true, os.Remove,
	)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(path)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	identity, err := openUnixPlaintextCleanupIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(directory, "."+plaintextSessionFileName+"-replacement")
	if err := os.WriteFile(replacementPath, []byte(`{"cookies":[{"value":"new"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "."+plaintextSessionFileName+"-incomplete"), []byte(`{"cookies":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUnixPlaintextSession(identity, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext workspace remains after cleanup: %v", err)
	}
}

func TestUnixPlaintextCleanupRejectsUnsafeManagedEntries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "symlink", mutate: func(t *testing.T, path string) {
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte("must-remain"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "over-permissive mode", mutate: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "multiple links", mutate: func(t *testing.T, path string) {
			link := filepath.Join(t.TempDir(), "second-link")
			if err := os.Link(path, link); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", mutate: func(t *testing.T, path string) {
			if err := os.Truncate(path, maxBrowserSessionBytes+1); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, err := createPlaintextSessionTemp(
				SessionKey{SystemID: "shop", Environment: "test", Origin: "https://app.test"},
				[]byte(`{"cookies":[]}`), true, os.Remove,
			)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Dir(path)
			t.Cleanup(func() { _ = os.RemoveAll(directory) })
			identity, err := openUnixPlaintextCleanupIdentity(path)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path)
			if err := cleanupUnixPlaintextSession(identity, path); err == nil {
				t.Fatal("unsafe managed plaintext entry was removed")
			}
		})
	}
}
