//go:build windows

package bughub

func createAttemptEvidenceStaging(string, string) (string, error) {
	return "", ErrSecureArtifactStoreUnsupported
}

func openAttemptEvidenceStaging(string, string) (attemptEvidenceStaging, error) {
	return nil, ErrSecureArtifactStoreUnsupported
}

func captureAttemptStagedArtifact(string, string) (capturedArtifactSource, error) {
	return capturedArtifactSource{}, ErrSecureArtifactStoreUnsupported
}
