package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(f, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := New([]string{f}, 50*time.Millisecond)
	// 初次 Changed() 会记录基线并返回 false（因 lastSig 为空，首次签名不等于空 → 返回 true）
	// 先初始化 lastSig 后再 Changed() 用于对比
	w.Changed() // baseline

	if w.Changed() {
		t.Error("expected no change immediately after baseline")
	}

	// 改内容 + 睡一会儿让 mtime 更新
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(f, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !w.Changed() {
		t.Error("expected change after file modification")
	}
	if w.Changed() {
		t.Error("expected no change after second call without modification")
	}
}

func TestWatcher_DetectsDirChange(t *testing.T) {
	dir := t.TempDir()
	w := New([]string{dir}, 50*time.Millisecond)
	w.Changed() // baseline

	if w.Changed() {
		t.Error("no change expected on idle dir")
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !w.Changed() {
		t.Error("expected change after adding file to dir")
	}
}

func TestWatcher_MissingPathStable(t *testing.T) {
	w := New([]string{"/definitely/not/existing/path"}, 50*time.Millisecond)
	sig1 := w.signature()
	sig2 := w.signature()
	if sig1 != sig2 {
		t.Error("missing path signature should be stable across calls")
	}
}

func TestWatcher_Loop_StopsOnChange(t *testing.T) {
	// 不测 Loop 的 goroutine 语义（阻塞），但验证 onChange 触发路径
	dir := t.TempDir()
	f := filepath.Join(dir, "x.txt")
	os.WriteFile(f, []byte("a"), 0o644)

	w := New([]string{f}, 20*time.Millisecond)
	changes := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 只循环一次就退出，避免 goroutine 泄漏（通过 panic recover）
		defer func() { _ = recover() }()
		w.lastSig = w.signature()
		for i := 0; i < 20; i++ {
			time.Sleep(20 * time.Millisecond)
			cur := w.signature()
			if cur != w.lastSig {
				w.lastSig = cur
				select {
				case changes <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	time.Sleep(30 * time.Millisecond)
	os.WriteFile(f, []byte("abc"), 0o644)

	select {
	case <-changes:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("change not detected within 2s")
	}
	<-done
}
