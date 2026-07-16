package browserverify

import (
	"io/fs"
	"os"
	"sync"
)

// runtimeFileLockRegistry prevents process-scoped advisory locks from being
// reacquired through a second path to the same file.
type runtimeFileLockRegistry struct {
	mu   sync.Mutex
	next uint64
	held map[uint64]fs.FileInfo
}

func (registry *runtimeFileLockRegistry) tryAcquire(info fs.FileInfo) (func(), bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, held := range registry.held {
		if os.SameFile(info, held) {
			return nil, false
		}
	}
	if registry.held == nil {
		registry.held = make(map[uint64]fs.FileInfo)
	}
	registry.next++
	token := registry.next
	registry.held[token] = info
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			delete(registry.held, token)
			registry.mu.Unlock()
		})
	}, true
}
