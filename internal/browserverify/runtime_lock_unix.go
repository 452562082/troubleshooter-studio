//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func acquireRuntimeAdvisoryLock(path string) (func() error, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fs.ErrExist
		}
		return nil, err
	}
	return func() error {
		return errors.Join(unix.Flock(int(lock.Fd()), unix.LOCK_UN), lock.Close())
	}, nil
}
