//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func trySessionFileAdvisoryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}

func unlockSessionFileAdvisoryLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
