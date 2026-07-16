//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWorkerProcessFinishCleansRemainingGroupSynchronously(t *testing.T) {
	var mu sync.Mutex
	var signals []syscall.Signal
	controller := &workerProcessController{
		command: &exec.Cmd{Process: &os.Process{Pid: 4242}},
		signalProcessGroup: func(_ int, signal syscall.Signal) error {
			mu.Lock()
			defer mu.Unlock()
			signals = append(signals, signal)
			return nil
		},
		sleep: func(time.Duration) {},
		grace: time.Second,
	}
	if err := controller.finish(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	countAtReturn := len(signals)
	got := append([]syscall.Signal(nil), signals...)
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countAfterReturn := len(signals)
	mu.Unlock()
	if countAfterReturn != countAtReturn {
		t.Fatalf("signals continued after finish returned: before=%v after=%v", countAtReturn, countAfterReturn)
	}
	want := []syscall.Signal{0, syscall.SIGKILL}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("finish signals = %v, want synchronous %v", got, want)
	}
}

func TestWorkerProcessFinishDoesNotSignalReusedProcessGroupAfterIdentityDisappears(t *testing.T) {
	var mu sync.Mutex
	var signals []syscall.Signal
	controller := &workerProcessController{
		command: &exec.Cmd{Process: &os.Process{Pid: 4242}},
		signalProcessGroup: func(_ int, signal syscall.Signal) error {
			mu.Lock()
			defer mu.Unlock()
			signals = append(signals, signal)
			return syscall.ESRCH
		},
	}
	if err := controller.finish(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	countAtReturn := len(signals)
	got := append([]syscall.Signal(nil), signals...)
	mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	countAfterReturn := len(signals)
	mu.Unlock()
	if countAfterReturn != countAtReturn {
		t.Fatalf("reused process group was signaled after finish returned: before=%v after=%v", countAtReturn, countAfterReturn)
	}
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("signals for disappeared process group = %v, want identity probe only", got)
	}
}
