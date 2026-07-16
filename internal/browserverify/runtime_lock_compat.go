package browserverify

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const runtimeInstallLockV2Marker = "tshoot-browser-runtime-install-lock:v2\n"

var errLegacyRuntimeInstallLock = errors.New("legacy browser runtime install lock requires manual recovery")

func acquireRuntimeInstallCompatibilityLock(path string) (func() error, error) {
	if err := ensureRuntimeInstallCompatibilityMarker(path); err != nil {
		return nil, err
	}
	return acquireRuntimeAdvisoryLock(path)
}

func ensureRuntimeInstallCompatibilityMarker(path string) error {
	for {
		marker, err := os.ReadFile(path)
		switch {
		case err == nil:
			if bytes.Equal(marker, []byte(runtimeInstallLockV2Marker)) {
				return nil
			}
			return fmt.Errorf("%w: %s", errLegacyRuntimeInstallLock, filepath.Base(path))
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
		if err := publishRuntimeInstallCompatibilityMarker(path); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return err
		}
		return nil
	}
}

func publishRuntimeInstallCompatibilityMarker(path string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".install-compat-v2-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	writeErr := error(nil)
	if _, err := temporary.WriteString(runtimeInstallLockV2Marker); err != nil {
		writeErr = err
	}
	writeErr = errors.Join(writeErr, temporary.Sync(), temporary.Close())
	if writeErr != nil {
		return writeErr
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return syncRuntimeDirectory(filepath.Dir(path))
}
