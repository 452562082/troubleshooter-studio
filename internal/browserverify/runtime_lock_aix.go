//go:build aix

package browserverify

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

var aixRuntimeLockRegistry runtimeFileLockRegistry

func acquireRuntimeAdvisoryLock(path string) (func() error, error) {
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := lock.Stat()
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	releaseIdentity, acquired := aixRuntimeLockRegistry.tryAcquire(info)
	if !acquired {
		_ = lock.Close()
		return nil, fs.ErrExist
	}
	flock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if err := unix.FcntlFlock(lock.Fd(), unix.F_SETLK, &flock); err != nil {
		_ = lock.Close()
		releaseIdentity()
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
			return nil, fs.ErrExist
		}
		return nil, err
	}
	return func() error {
		unlock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 1}
		err := errors.Join(unix.FcntlFlock(lock.Fd(), unix.F_SETLK, &unlock), lock.Close())
		releaseIdentity()
		return err
	}, nil
}
