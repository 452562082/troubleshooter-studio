//go:build aix

package browserverify

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func trySessionFileAdvisoryLock(file *os.File) (bool, error) {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart, Start: 0, Len: 1}
	err := unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}

func unlockSessionFileAdvisoryLock(file *os.File) error {
	lock := unix.Flock_t{Type: unix.F_UNLCK, Whence: io.SeekStart, Start: 0, Len: 1}
	return unix.FcntlFlock(file.Fd(), unix.F_SETLK, &lock)
}
