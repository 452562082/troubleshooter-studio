//go:build windows

package browserverify

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func acquireRuntimeAdvisoryLock(path string) (func() error, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	overlapped := &windows.Overlapped{}
	if err := windows.LockFileEx(
		windows.Handle(lock.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		_ = lock.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fs.ErrExist
		}
		return nil, err
	}
	return func() error {
		return errors.Join(windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, overlapped), lock.Close())
	}, nil
}
