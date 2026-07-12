//go:build windows

package bughub

func createAttemptEvidenceStaging(string, string) (string, error) {
	return "", ErrSecureArtifactStoreUnsupported
}

func openAttemptEvidenceStaging(string, string) (attemptEvidenceStaging, error) {
	return nil, ErrSecureArtifactStoreUnsupported
}

func openExistingAttemptEvidenceStaging(string, string, string) (attemptEvidenceStaging, error) {
	return nil, ErrSecureArtifactStoreUnsupported
}

func sweepTerminalFixStaging(string, []string) error { return nil }

func captureAttemptStagedArtifact(string, string) (capturedArtifactSource, error) {
	return capturedArtifactSource{}, ErrSecureArtifactStoreUnsupported
}
