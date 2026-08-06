package browserverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

var frozenBrowserSceneReferencePattern = regexp.MustCompile(`^browser-scenes/scene-[0-9]{3}-[a-f0-9]{16}\.json$`)

type frozenBrowserSceneFileStore struct {
	root      string
	directory browserDirectoryIdentity
}

func newFrozenBrowserSceneFileStore(root string) (*frozenBrowserSceneFileStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("frozen browser Scene root must be absolute")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("frozen browser Scene root is unsafe")
	}
	directory := filepath.Join(root, "browser-scenes")
	if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("frozen browser Scene directory is unsafe")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	identity, err := pinBrowserDirectory(directory)
	if err != nil {
		return nil, err
	}
	return &frozenBrowserSceneFileStore{root: root, directory: identity}, nil
}

func (store *frozenBrowserSceneFileStore) FreezeBrowserScene(_ context.Context, stepNo int, scene bughub.BrowserScene, attemptID string) (string, error) {
	if store == nil || stepNo < 0 || stepNo > 40 {
		return "", errors.New("frozen browser Scene store is unavailable")
	}
	content, err := bughub.EncodeFrozenBrowserScene(scene, attemptID)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	name := fmt.Sprintf("scene-%03d-%s.json", stepNo, hex.EncodeToString(digest[:8]))
	reference := filepath.ToSlash(filepath.Join("browser-scenes", name))
	path := filepath.Join(store.directory.path, name)
	if err := store.directory.Verify(); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := store.read(reference)
		if readErr != nil || !bytes.Equal(existing, content) {
			return "", errors.Join(errors.New("frozen browser Scene reference collision"), readErr)
		}
		return reference, nil
	}
	if err != nil {
		return "", err
	}
	writeErr := writeAndSyncBrowserScene(file, content)
	if writeErr != nil {
		_ = os.Remove(path)
		return "", writeErr
	}
	if err := store.directory.Verify(); err != nil {
		return "", err
	}
	if err := syncRuntimeDirectory(store.directory.path); err != nil {
		return "", err
	}
	return reference, nil
}

func (store *frozenBrowserSceneFileStore) LoadFrozenBrowserScene(_ context.Context, attemptID, reference string) ([]byte, error) {
	content, err := store.read(reference)
	if err != nil {
		return nil, err
	}
	if _, err := bughub.DecodeFrozenBrowserScene(content, attemptID); err != nil {
		return nil, err
	}
	return content, nil
}

func (store *frozenBrowserSceneFileStore) read(reference string) ([]byte, error) {
	if store == nil || !frozenBrowserSceneReferencePattern.MatchString(reference) || filepath.IsAbs(reference) {
		return nil, errors.New("frozen browser Scene reference is invalid")
	}
	if err := store.directory.Verify(); err != nil {
		return nil, err
	}
	path := filepath.Join(store.root, filepath.FromSlash(reference))
	if filepath.Dir(path) != store.directory.path {
		return nil, errors.New("frozen browser Scene reference escapes its store")
	}
	lstat, err := os.Lstat(path)
	if err != nil || !lstat.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 || lstat.Size() < 1 || lstat.Size() > 1<<20 {
		return nil, errors.New("frozen browser Scene file is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fstat, err := file.Stat()
	if err != nil || !fstat.Mode().IsRegular() || !os.SameFile(lstat, fstat) {
		return nil, errors.New("frozen browser Scene file changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(content) < 1 || len(content) > 1<<20 {
		return nil, errors.Join(errors.New("frozen browser Scene file size is invalid"), err)
	}
	if err := store.directory.Verify(); err != nil {
		return nil, err
	}
	return content, nil
}

func writeAndSyncBrowserScene(file *os.File, content []byte) error {
	if file == nil {
		return errors.New("frozen browser Scene file is unavailable")
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
