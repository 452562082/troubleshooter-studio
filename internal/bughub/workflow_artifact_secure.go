package bughub

import (
	"errors"
	"hash/fnv"
	"path/filepath"
	"sync"
	"time"
)

var ErrSecureArtifactStoreUnsupported = errors.New("secure artifact store is unsupported on this platform")

var ErrEvidenceArtifactReused = errors.New("evidence artifact was already registered by an earlier attempt")

type capturedArtifactSource struct {
	Content    []byte
	SHA256     string
	CapturedAt time.Time
}

type artifactPublication interface {
	Path() string
	Created() bool
	Verify() error
	Cleanup() error
	Close() error
}

type artifactHooks struct {
	BeforeCommit func()
}

var artifactPublicationLocks [64]sync.Mutex

func lockArtifactPublication(root, caseID, digest string) func() {
	absRoot, _ := filepath.Abs(root)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(absRoot + "\x00" + caseID + "\x00" + digest))
	lock := &artifactPublicationLocks[hash.Sum32()%uint32(len(artifactPublicationLocks))]
	lock.Lock()
	return lock.Unlock
}
