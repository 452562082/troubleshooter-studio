package bughub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const maxStagedEvidenceBytes int64 = 16 << 20

var ErrStagedEvidenceTooLarge = errors.New("staged evidence exceeds the fixed size limit")

type attemptEvidenceStaging interface {
	Path() string
	Capture(string) (capturedArtifactSource, error)
	Cleanup() error
	Close() error
}

func readStagedEvidence(reader io.Reader) ([]byte, string, error) {
	hash := sha256.New()
	var content bytes.Buffer
	limited := io.LimitReader(reader, maxStagedEvidenceBytes+1)
	if _, err := io.Copy(io.MultiWriter(&content, hash), limited); err != nil {
		return nil, "", err
	}
	if int64(content.Len()) > maxStagedEvidenceBytes {
		return nil, "", fmt.Errorf("%w: maximum is %d bytes", ErrStagedEvidenceTooLarge, maxStagedEvidenceBytes)
	}
	return content.Bytes(), hex.EncodeToString(hash.Sum(nil)), nil
}
