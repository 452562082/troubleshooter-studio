package bughub

import "context"

// BrowserDecisionHostSession is one Host-owned browser process and context.
// Scene refs are immutable attempt-scoped evidence, while Finish produces the
// normal verifier result consumed by Coordinator's existing artifact freezer.
type BrowserDecisionHostSession interface {
	BoundBrowserStepExecutor
	BrowserSceneEvidenceLoader
	InitialBrowserDecisionScene() (BrowserScene, string)
	FinishBrowserDecision(context.Context) (BrowserVerificationResult, error)
	Close() error
}

type BrowserDecisionSessionOpener interface {
	OpenBrowserDecisionSession(context.Context, BrowserVerificationRequest) (BrowserDecisionHostSession, error)
}
