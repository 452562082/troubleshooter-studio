//go:build windows

package bughub

import "fmt"

// Secure artifact registration is intentionally unavailable on Windows.
// Pathname checks, chmod, and ordinary CreateFile calls do not provide the
// handle-relative NT traversal plus restricted DACL guarantees required by the
// evidence store. Keep this fail-closed until those primitives are implemented.
func captureArtifactSource(string) (capturedArtifactSource, error) {
	return capturedArtifactSource{}, fmt.Errorf("%w: Windows requires handle-relative NT traversal and restricted DACL support", ErrSecureArtifactStoreUnsupported)
}

func captureRegisteredArtifact(string, string, string, string) (capturedArtifactSource, error) {
	return capturedArtifactSource{}, fmt.Errorf("%w: Windows requires handle-relative NT traversal and restricted DACL support", ErrSecureArtifactStoreUnsupported)
}

func publishArtifact(string, string, string, []byte) (artifactPublication, error) {
	return nil, ErrSecureArtifactStoreUnsupported
}

func verifyRegisteredArtifact(string, string) error {
	return ErrSecureArtifactStoreUnsupported
}
