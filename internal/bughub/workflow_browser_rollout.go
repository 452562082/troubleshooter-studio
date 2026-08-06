package bughub

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
)

const BrowserDecisionRolloutVersion = 1

const browserDecisionRolloutBuckets = 10_000

const (
	BrowserDecisionRolloutDisabled          = "disabled"
	BrowserDecisionRolloutProduction        = "production_forbidden"
	BrowserDecisionRolloutCapabilityMissing = "capability_missing"
	BrowserDecisionRolloutOutsidePercentage = "outside_percentage"
	BrowserDecisionRolloutEnabled           = "enabled"
	BrowserDecisionRolloutConfigInvalid     = "config_invalid"
)

// BrowserDecisionRolloutPolicy is deliberately default-off. Percentage is a
// whole number in [0, 100]; callers cannot use the policy to enable production.
type BrowserDecisionRolloutPolicy struct {
	Version    int
	Enabled    bool
	Percentage int
}

func DefaultBrowserDecisionRolloutPolicy() BrowserDecisionRolloutPolicy {
	return BrowserDecisionRolloutPolicy{Version: BrowserDecisionRolloutVersion}
}

// BrowserDecisionRolloutCapabilities names the Host-owned dependencies needed
// before the autonomous loop may execute. A partial stack always fails closed.
type BrowserDecisionRolloutCapabilities struct {
	PersistentSession bool
	FrozenSceneStore  bool
	DecisionProvider  bool
	RecoveryEvidence  bool
}

func (capabilities BrowserDecisionRolloutCapabilities) complete() bool {
	return capabilities.PersistentSession && capabilities.FrozenSceneStore &&
		capabilities.DecisionProvider && capabilities.RecoveryEvidence
}

type BrowserDecisionRolloutRequest struct {
	CaseID       string
	IsProduction bool
	Capabilities BrowserDecisionRolloutCapabilities
}

type BrowserDecisionRolloutDecision struct {
	Enabled bool
	Bucket  int
	Reason  string
}

type BrowserDecisionCoordinatorRunRequest struct {
	Verification     BrowserVerificationRequest
	Plan             BrowserPlan
	Readiness        BrowserDecisionLoopReadinessProvider
	Emit             func(InvestigationEvent)
	AutonomousRecipe *AutonomousValidationRecipe
}

// BrowserDecisionCoordinatorRunner is the optional composition boundary for
// the persistent Host session, Decision provider, journal and evidence store.
// Once called it owns the attempt; Coordinator must not replay the same plan
// through the legacy verifier after an error.
type BrowserDecisionCoordinatorRunner interface {
	BrowserDecisionRolloutCapabilities() BrowserDecisionRolloutCapabilities
	ExecuteBrowserDecision(context.Context, BrowserDecisionCoordinatorRunRequest) (BrowserVerificationResult, error)
}

func BrowserDecisionRolloutEvent(decision BrowserDecisionRolloutDecision, percentage int) InvestigationEvent {
	return InvestigationEvent{
		Type:    "browser_decision_rollout",
		Message: "浏览器自主验证灰度资格已评估",
		Meta: map[string]any{
			"enabled": decision.Enabled, "reason": decision.Reason,
			"bucket": decision.Bucket, "percentage": percentage,
		},
	}
}

// DecideBrowserDecisionRollout assigns the same Case to the same cohort across
// retries. The internal versioned salt makes bucket changes explicit instead
// of allowing an accidental refactor to reshuffle a live cohort.
func DecideBrowserDecisionRollout(policy BrowserDecisionRolloutPolicy, request BrowserDecisionRolloutRequest) (BrowserDecisionRolloutDecision, error) {
	if err := ValidateBrowserDecisionRolloutPolicy(policy); err != nil {
		return BrowserDecisionRolloutDecision{}, err
	}
	caseID := strings.TrimSpace(request.CaseID)
	if caseID == "" {
		return BrowserDecisionRolloutDecision{}, errors.New("browser decision rollout Case ID is required")
	}
	bucket := browserDecisionRolloutBucket(caseID)
	decision := BrowserDecisionRolloutDecision{Bucket: bucket}
	if !policy.Enabled || policy.Percentage == 0 {
		decision.Reason = BrowserDecisionRolloutDisabled
		return decision, nil
	}
	if request.IsProduction {
		decision.Reason = BrowserDecisionRolloutProduction
		return decision, nil
	}
	if !request.Capabilities.complete() {
		decision.Reason = BrowserDecisionRolloutCapabilityMissing
		return decision, nil
	}
	if bucket >= policy.Percentage*100 {
		decision.Reason = BrowserDecisionRolloutOutsidePercentage
		return decision, nil
	}
	decision.Enabled = true
	decision.Reason = BrowserDecisionRolloutEnabled
	return decision, nil
}

// ValidateBrowserDecisionRolloutPolicy validates configuration without
// requiring a Case ID. AgentPhaseRunner uses it when configuration is applied
// so an invalid percentage can never become a latent runtime fallback.
func ValidateBrowserDecisionRolloutPolicy(policy BrowserDecisionRolloutPolicy) error {
	if policy.Version != BrowserDecisionRolloutVersion {
		return errors.New("browser decision rollout policy version is invalid")
	}
	if policy.Percentage < 0 || policy.Percentage > 100 {
		return errors.New("browser decision rollout percentage is invalid")
	}
	return nil
}

func browserDecisionRolloutBucket(caseID string) int {
	digest := sha256.Sum256([]byte("browser-decision-rollout-v1\x00" + caseID))
	value := uint32(digest[0])<<24 | uint32(digest[1])<<16 | uint32(digest[2])<<8 | uint32(digest[3])
	return int(value % browserDecisionRolloutBuckets)
}
