//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestNodeWorkerRunnerDirectExitCleansDescendantHoldingOutputPipes(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "worker-with-output-holding-grandchild.mjs")
	readyPath := filepath.Join(temporary, "worker-grandchild-ready")
	markerPath := filepath.Join(temporary, "worker-grandchild-survived")
	source := `
import { existsSync } from 'node:fs';
import { spawn } from 'node:child_process';
const childSource = ` + "`" + `
  const { writeFileSync } = require('node:fs');
  process.on('SIGTERM', () => {});
  writeFileSync(process.env.TSHOOT_TEST_GRANDCHILD_READY, String(process.pid));
  setTimeout(() => writeFileSync(process.env.TSHOOT_TEST_GRANDCHILD_MARKER, 'alive'), 3000);
  setInterval(() => {}, 1000);
` + "`" + `;
const child = spawn(process.execPath, ['-e', childSource], { env: process.env, stdio: ['ignore', 'inherit', 'inherit'] });
child.unref();
const ready = setInterval(() => {
  if (!existsSync(process.env.TSHOOT_TEST_GRANDCHILD_READY)) return;
  clearInterval(ready);
  process.stdout.write(JSON.stringify({ status: 'completed' }));
}, 10);
`
	if err := os.WriteFile(workerPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TSHOOT_TEST_GRANDCHILD_READY", readyPath)
	t.Setenv("TSHOOT_TEST_GRANDCHILD_MARKER", markerPath)
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
		_, err := (nodeWorkerRunner{}).Run(ctx, RuntimePaths{
			Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
		}, workerRequest{Mode: "execute"}, nil)
		result <- err
	}()
	waitForRuntimeTestFile(t, readyPath, 5*time.Second, cancel)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("worker error: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("worker returned after %s, want bounded cleanup", elapsed)
		}
	case <-time.After(4 * time.Second):
		killRuntimeTestProcess(t, readyPath)
		select {
		case <-result:
		case <-time.After(time.Second):
		}
		t.Fatal("worker hung on output pipes inherited by a grandchild")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker output-pipe grandchild survived cleanup: %v", err)
	}
	cleanupNeeded = false
}

func TestNodeWorkerRunnerSlowProgressObserverCannotBlockStderrDrain(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "worker-with-progress.mjs")
	source := `
process.stderr.write('TSHOOT_BROWSER_PROGRESS {"code":"browser_action_started","message":"running","action_id":"step","current":1,"total":1}\n');
process.stdout.write(JSON.stringify({ status: 'completed' }));
`
	if err := os.WriteFile(workerPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	observerStarted := make(chan struct{})
	releaseObserver := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := (nodeWorkerRunner{}).Run(context.Background(), RuntimePaths{
			Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
		}, workerRequest{Mode: "execute"}, func(bughub.BrowserProgress) {
			close(observerStarted)
			<-releaseObserver
		})
		result <- err
	}()
	select {
	case <-observerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("progress observer was not called")
	}
	// Keep the observer blocked beyond the child-output drain deadline. The
	// worker pipe itself must still be consumed and remain valid.
	time.Sleep(commandOutputDrainTimeout + 100*time.Millisecond)
	close(releaseObserver)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("slow progress observer caused worker failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not return after releasing progress observer")
	}
}

func TestNodeWorkerRunnerPostStartOutputCloseErrorIsBoundedAndClosesParentPipes(t *testing.T) {
	temporary := t.TempDir()
	workerPath := filepath.Join(temporary, "worker-output-close-error.mjs")
	if err := os.WriteFile(workerPath, []byte(`process.stdout.write(JSON.stringify({ status: 'completed' }));`), 0o600); err != nil {
		t.Fatal(err)
	}
	var parentPipes []*os.File
	runner := nodeWorkerRunner{attachOutputs: attachOutputsWithInjectedCloseError(&parentPipes)}
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(context.Background(), RuntimePaths{
			Root: temporary, BrowsersPath: filepath.Join(temporary, "browsers"), WorkerPath: workerPath,
		}, workerRequest{Mode: "execute"}, nil)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, errInjectedOutputClose) {
			t.Fatalf("worker post-Start error = %v, want injected output close error", err)
		}
		assertFilesClosed(t, parentPipes)
	case <-time.After(3 * time.Second):
		t.Fatal("worker post-Start output close error hung before wrapper wait")
	}
}
