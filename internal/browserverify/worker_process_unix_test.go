//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWorkerProcessWaitCleansGroupBeforeWrapperReapAndPGIDReuse(t *testing.T) {
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
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	var signals []syscall.Signal
	reaped := false
	reused := false
	controller := &workerProcessController{
		command:    command,
		statusRead: statusRead,
		signalProcessGroup: func(processGroup int, signal syscall.Signal) error {
			if processGroup != 4242 {
				t.Fatalf("process group = %d, want 4242", processGroup)
			}
			if reaped || reused {
				t.Fatalf("reused process group received signal %v after wrapper reap", signal)
			}
			signals = append(signals, signal)
			return nil
		},
		beforeGroupCleanup: func() {
			time.Sleep(25 * time.Millisecond)
			if reaped {
				t.Fatal("wrapper was reaped during artificial cleanup delay")
			}
		},
		waitWrapper: func(*exec.Cmd) error {
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
	countAtReturn := len(signals)
	time.Sleep(50 * time.Millisecond)
	if len(signals) != countAtReturn {
		t.Fatalf("signals continued after wrapper reap: before=%d after=%d", countAtReturn, len(signals))
	}
	if len(signals) != 1 || signals[0] != syscall.SIGKILL {
		t.Fatalf("owned-group cleanup signals = %v, want synchronous SIGKILL", signals)
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
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	firstProbeEntered := make(chan struct{})
	releaseFirstProbe := make(chan struct{})
	var stateMu sync.Mutex
	probeCount := 0
	reaped := false
	signaledAfterReap := false
	controller := &workerProcessController{
		command:    command,
		statusRead: statusRead,
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
	command := &exec.Cmd{Process: &os.Process{Pid: 4242}}
	controller := &workerProcessController{
		ctx:                ctx,
		statusRead:         statusRead,
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
