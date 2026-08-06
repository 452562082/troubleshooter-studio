package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

const (
	BrowserAutomationChannelExhausted   = "exhausted"
	BrowserAutomationChannelUnavailable = "unavailable"
)

const (
	BrowserManualReproductionAvailableCode = "browser_manual_reproduction_available"
	BrowserManualReproductionSelectedCode  = "browser_manual_reproduction_user_selected"
	BrowserManualReproductionDeniedCode    = "browser_manual_reproduction_denied"
)

const BrowserManualReproductionGateProofVersion = 1

// BrowserManualReproductionGateProof is a Host-issued, value-free attestation
// that the current autonomous attempt reached a genuine capability boundary.
// It is safe to persist in phase output, but it is not itself permission to
// start a visible browser: the desktop binding still requires an explicit user
// command and rechecks the current attempt and production policy.
type BrowserManualReproductionGateProof struct {
	Version                int      `json:"version"`
	Code                   string   `json:"code"`
	AttemptID              string   `json:"attempt_id"`
	SceneID                string   `json:"scene_id"`
	ScenarioContractSHA256 string   `json:"scenario_contract_sha256"`
	FrontendEntryID        string   `json:"frontend_entry_id"`
	DecisionSHA256         string   `json:"decision_sha256"`
	ExhaustedChannels      []string `json:"exhausted_channels"`
}

type BrowserManualReproductionGateInput struct {
	Decision             BrowserDecision
	IsProduction         bool
	ScenarioReady        bool
	LoginReady           bool
	TestInputsReady      bool
	AuthorizationReady   bool
	ExplorationExhausted bool
	SafeCandidateExists  bool
	SystemFailure        bool
	ChannelStates        map[string]string
	UserOptedIn          bool
}

type BrowserManualReproductionGateDecision struct {
	Available    bool
	StartAllowed bool
	Code         string
}

// EvaluateBrowserManualReproductionGate does not start a visible browser. It
// only decides whether UI may offer the last-resort option and whether a
// separate, explicit user choice authorizes starting it.
func EvaluateBrowserManualReproductionGate(input BrowserManualReproductionGateInput) (BrowserManualReproductionGateDecision, error) {
	deny := func() (BrowserManualReproductionGateDecision, error) {
		return BrowserManualReproductionGateDecision{Code: BrowserManualReproductionDeniedCode}, nil
	}
	if input.IsProduction || !input.ScenarioReady || !input.LoginReady || !input.TestInputsReady || !input.AuthorizationReady ||
		!input.ExplorationExhausted || input.SafeCandidateExists || input.SystemFailure {
		return deny()
	}
	_, canonical, err := canonicalBrowserDecision(input.Decision)
	if err != nil {
		return BrowserManualReproductionGateDecision{}, errors.New("browser manual reproduction capability decision is invalid")
	}
	if canonical.Decision != "capability_gap" || canonical.CapabilityGapCode != "browser_capability_gap" {
		return deny()
	}
	exhausted := make(map[string]struct{}, len(canonical.ExhaustedChannels))
	for _, channel := range canonical.ExhaustedChannels {
		exhausted[channel] = struct{}{}
	}
	for _, channel := range []string{"semantic_grounding", "structured_grounding", "safe_exploration"} {
		if input.ChannelStates[channel] != BrowserAutomationChannelExhausted {
			return deny()
		}
		if _, declared := exhausted[channel]; !declared {
			return deny()
		}
	}
	for _, channel := range []string{"ai_observe", "visual_grounding"} {
		switch input.ChannelStates[channel] {
		case BrowserAutomationChannelExhausted:
			if _, declared := exhausted[channel]; !declared {
				return deny()
			}
		case BrowserAutomationChannelUnavailable:
			if _, falselyDeclared := exhausted[channel]; falselyDeclared {
				return deny()
			}
		default:
			return deny()
		}
	}
	decision := BrowserManualReproductionGateDecision{Available: true, Code: BrowserManualReproductionAvailableCode}
	if input.UserOptedIn {
		decision.StartAllowed = true
		decision.Code = BrowserManualReproductionSelectedCode
	}
	return decision, nil
}

func browserManualReproductionChannelStates(exhaustedChannels []string) map[string]string {
	states := map[string]string{
		"semantic_grounding":   BrowserAutomationChannelUnavailable,
		"structured_grounding": BrowserAutomationChannelUnavailable,
		"safe_exploration":     BrowserAutomationChannelUnavailable,
		"ai_observe":           BrowserAutomationChannelUnavailable,
		"visual_grounding":     BrowserAutomationChannelUnavailable,
	}
	for _, channel := range exhaustedChannels {
		if _, known := states[channel]; known {
			states[channel] = BrowserAutomationChannelExhausted
		}
	}
	return states
}

func issueBrowserManualReproductionGateProof(
	input BrowserManualReproductionGateInput,
	attemptID string,
	scene BrowserScene,
	readiness BrowserDecisionLoopReadiness,
) (*BrowserManualReproductionGateProof, error) {
	gate, err := EvaluateBrowserManualReproductionGate(input)
	if err != nil {
		return nil, err
	}
	if !gate.Available || gate.StartAllowed || gate.Code != BrowserManualReproductionAvailableCode {
		return nil, nil
	}
	canonical, decision, err := canonicalBrowserDecision(input.Decision)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	channels := append([]string(nil), decision.ExhaustedChannels...)
	sort.Strings(channels)
	proof := &BrowserManualReproductionGateProof{
		Version: BrowserManualReproductionGateProofVersion, Code: gate.Code,
		AttemptID: strings.TrimSpace(attemptID), SceneID: strings.TrimSpace(scene.SceneID),
		ScenarioContractSHA256: strings.TrimSpace(readiness.ScenarioContractSHA256),
		FrontendEntryID:        strings.TrimSpace(readiness.FrontendEntryID),
		DecisionSHA256:         hex.EncodeToString(digest[:]), ExhaustedChannels: channels,
	}
	if err := ValidateBrowserManualReproductionGateProof(*proof, attemptID); err != nil {
		return nil, err
	}
	return proof, nil
}

// ValidateBrowserManualReproductionGateProof validates only the persisted Host
// attestation and its attempt binding. The caller must separately verify the
// current Case/attempt state, explicit user command and current environment.
func ValidateBrowserManualReproductionGateProof(proof BrowserManualReproductionGateProof, expectedAttemptID string) error {
	if proof.Version != BrowserManualReproductionGateProofVersion || proof.Code != BrowserManualReproductionAvailableCode ||
		!validBrowserDecisionIdentifier(strings.TrimSpace(proof.AttemptID), 128) || strings.TrimSpace(proof.AttemptID) != strings.TrimSpace(expectedAttemptID) ||
		!validBrowserDecisionIdentifier(strings.TrimSpace(proof.SceneID), 128) || !validLowerSHA256(strings.TrimSpace(proof.ScenarioContractSHA256)) ||
		!validBrowserDecisionIdentifier(strings.TrimSpace(proof.FrontendEntryID), 128) || !validLowerSHA256(strings.TrimSpace(proof.DecisionSHA256)) ||
		!validBrowserDecisionExhaustedChannels(proof.ExhaustedChannels) {
		return errors.New("browser manual reproduction gate proof is invalid")
	}
	channels := append([]string(nil), proof.ExhaustedChannels...)
	sort.Strings(channels)
	if !equalBrowserStringSlices(channels, proof.ExhaustedChannels) {
		return errors.New("browser manual reproduction gate proof is not canonical")
	}
	required := map[string]bool{"semantic_grounding": false, "structured_grounding": false, "safe_exploration": false}
	for _, channel := range proof.ExhaustedChannels {
		if _, ok := required[channel]; ok {
			required[channel] = true
		}
	}
	for _, present := range required {
		if !present {
			return errors.New("browser manual reproduction gate proof is incomplete")
		}
	}
	return nil
}

func equalBrowserStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
