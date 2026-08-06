package browserverify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func TestFrozenBrowserSceneFileStoreRoundTripsAttemptBoundEvidence(t *testing.T) {
	root := t.TempDir()
	store, err := newFrozenBrowserSceneFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	scene := bindBrowserScene("attempt-scene-store", workerSceneFixture())
	if scene == nil {
		t.Fatal("expected bound Scene")
	}
	reference, err := store.FreezeBrowserScene(context.Background(), 3, *scene, "attempt-scene-store")
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.LoadFrozenBrowserScene(context.Background(), "attempt-scene-store", reference)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := bughub.DecodeFrozenBrowserScene(content, "attempt-scene-store")
	if err != nil || decoded.SceneSHA256 != scene.SceneSHA256 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if second, err := store.FreezeBrowserScene(context.Background(), 3, *scene, "attempt-scene-store"); err != nil || second != reference {
		t.Fatalf("idempotent freeze reference=%q err=%v", second, err)
	}
	if _, err := store.LoadFrozenBrowserScene(context.Background(), "another-attempt", reference); err == nil {
		t.Fatal("expected cross-attempt load to fail")
	}
	if _, err := store.LoadFrozenBrowserScene(context.Background(), "attempt-scene-store", "../scene.json"); err == nil {
		t.Fatal("expected traversal reference to fail")
	}
}

func TestFrozenBrowserSceneFileStoreRejectsReplacedFileAndSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := newFrozenBrowserSceneFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	scene := bindBrowserScene("attempt-scene-store", workerSceneFixture())
	reference, err := store.FreezeBrowserScene(context.Background(), 1, *scene, "attempt-scene-store")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(reference))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadFrozenBrowserScene(context.Background(), "attempt-scene-store", reference); err == nil {
		t.Fatal("expected replaced Scene symlink to fail")
	}

	otherRoot := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(otherRoot, "browser-scenes")); err != nil {
		t.Fatal(err)
	}
	if _, err := newFrozenBrowserSceneFileStore(otherRoot); err == nil {
		t.Fatal("expected Scene directory symlink to fail")
	}
}
