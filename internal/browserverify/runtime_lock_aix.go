//go:build aix

package browserverify

import (
	"errors"
	"io/fs"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var aixRuntimeLockState = struct {
	sync.Mutex
	held map[string]struct{}
}{held: make(map[string]struct{})}

func acquireRuntimeAdvisoryLock(path string) (func() error, error) {
	aixRuntimeLockState.Lock()
	if _, exists := aixRuntimeLockState.held[path]; exists {
		aixRuntimeLockState.Unlock()
		return nil, fs.ErrExist
	}
	aixRuntimeLockState.held[path] = struct{}{}
	aixRuntimeLockState.Unlock()

	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		releaseAIXRuntimeProcessLock(path)
		return nil, err
	}
	flock := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 1}
	if err := unix.FcntlFlock(lock.Fd(), unix.F_SETLK, &flock); err != nil {
		_ = lock.Close()
		releaseAIXRuntimeProcessLock(path)
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
			return nil, fs.ErrExist
		}
		return nil, err
	}
	return func() error {
		unlock := unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 1}
		err := errors.Join(unix.FcntlFlock(lock.Fd(), unix.F_SETLK, &unlock), lock.Close())
		releaseAIXRuntimeProcessLock(path)
		return err
	}, nil
}

func releaseAIXRuntimeProcessLock(path string) {
	aixRuntimeLockState.Lock()
	delete(aixRuntimeLockState.held, path)
	aixRuntimeLockState.Unlock()
}
