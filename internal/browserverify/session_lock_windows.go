//go:build windows

package browserverify

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func trySessionFileAdvisoryLock(file *os.File) (bool, error) {
	overlapped := &windows.Overlapped{}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return true, nil
	}
	return false, err
}

func unlockSessionFileAdvisoryLock(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}

// Windows FileMode permission bits do not represent the file's ACL. Path,
// reparse-point and File ID checks provide the identity boundary on Windows.
func sessionModeIsPrivate(os.FileMode) bool {
	return true
}

func sessionDirectoryModeHasOwnerAccess(os.FileMode) bool {
	return true
}
