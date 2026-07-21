package bughub

import (
	"crypto/sha256"
	"encoding/hex"
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

// Most supported Unix filesystems cap one pathname component at 255 bytes.
// Case IDs are logical database identifiers and legacy reset flows could grow
// them beyond that limit, so the evidence store must not use an unbounded ID as
// a directory name. Keep existing short paths stable and map only oversized
// IDs to a deterministic, collision-resistant component.
func artifactStorageCaseComponent(caseID string) string {
	if len([]byte(caseID)) <= 255 {
		return caseID
	}
	digest := sha256.Sum256([]byte(caseID))
	return "case-sha256-" + hex.EncodeToString(digest[:])
}

func lockArtifactPublication(root, caseID, digest string) func() {
	absRoot, _ := filepath.Abs(root)
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(absRoot + "\x00" + caseID + "\x00" + digest))
	lock := &artifactPublicationLocks[hash.Sum32()%uint32(len(artifactPublicationLocks))]
	lock.Lock()
	return lock.Unlock
}
