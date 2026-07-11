package bughub

type attemptEvidenceStaging interface {
	Path() string
	Capture(string) (capturedArtifactSource, error)
	Cleanup() error
	Close() error
}
