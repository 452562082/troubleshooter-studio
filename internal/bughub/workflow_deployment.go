package bughub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrDeploymentVerifierUnavailable        = errors.New("deployment verifier is unavailable")
	ErrDeploymentReservationIdentityInvalid = errors.New("deployment reservation caller identity is invalid")
)

func validateDeploymentReservationIdentity(reservation DeploymentReservation, expectedReservationKey, callerKey, actorID string) error {
	if strings.TrimSpace(reservation.ReservationKey) == "" || reservation.ReservationKey != expectedReservationKey {
		return fmt.Errorf("%w: reservation key is missing or inconsistent", ErrDeploymentReservationIdentityInvalid)
	}
	if reservation.ReservationID != stableID("deployment-reservation", expectedReservationKey) {
		return fmt.Errorf("%w: reservation ID is not canonical", ErrDeploymentReservationIdentityInvalid)
	}
	if strings.TrimSpace(reservation.CallerIdempotencyKey) == "" || strings.TrimSpace(callerKey) == "" || reservation.CallerIdempotencyKey != callerKey {
		return fmt.Errorf("%w: caller idempotency key is missing or inconsistent", ErrDeploymentReservationIdentityInvalid)
	}
	if strings.TrimSpace(reservation.ActorID) == "" || strings.TrimSpace(actorID) == "" || reservation.ActorID != actorID {
		return fmt.Errorf("%w: actor is missing or inconsistent", ErrDeploymentReservationIdentityInvalid)
	}
	return nil
}

// CommitDescendantVerifier must return true only after independently proving
// expected is an ancestor of observed in repo. User input alone is never proof.
type CommitDescendantVerifier func(context.Context, string, string, string) (bool, error)

// ManualVersionVerifier validates proof entered after an external, human-run
// deployment. It never performs a deployment itself.
type ManualVersionVerifier struct {
	Environment  string
	IsDescendant CommitDescendantVerifier
}

func (v ManualVersionVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	observation := DeploymentObservation{
		Environment:        strings.TrimSpace(request.Environment),
		ExpectedCommits:    CloneStringMap(request.ExpectedCommits),
		VerificationSource: strings.TrimSpace(request.Source),
		ObservedVersion:    strings.TrimSpace(request.ObservedVersion),
		ObservedCommits:    CloneStringMap(request.ObservedCommits),
		Result:             DeploymentResultUnavailable,
		ObservedAt:         time.Now().UTC(),
	}
	if observation.VerificationSource == "" || observation.ObservedVersion == "" || observation.Environment == "" || len(observation.ExpectedCommits) == 0 {
		switch {
		case observation.Environment == "":
			setDeploymentDiagnostic(&observation, "manual_environment_missing", "人工部署证明缺少环境")
		case observation.ObservedVersion == "":
			setDeploymentDiagnostic(&observation, "manual_version_missing", "人工部署证明缺少版本")
		default:
			setDeploymentDiagnostic(&observation, "manual_scope_missing", "人工部署证明缺少期望仓库")
		}
		return observation, nil
	}
	if observation.VerificationSource != "manual" {
		setDeploymentDiagnostic(&observation, "provider_mismatch", "部署版本验证方式不匹配")
		return observation, fmt.Errorf("%w: manual verifier cannot handle source %q", ErrDeploymentVerifierUnavailable, observation.VerificationSource)
	}
	if expectedEnvironment := strings.TrimSpace(v.Environment); expectedEnvironment != "" && observation.Environment != expectedEnvironment {
		observation.Result = DeploymentResultMismatched
		setDeploymentDiagnostic(&observation, "environment_mismatch", "部署证明环境与 Case 不一致")
		return observation, nil
	}
	verifiedAncestors := map[string]string{}
	for repo, expected := range observation.ExpectedCommits {
		observed, ok := observation.ObservedCommits[repo]
		if !ok || strings.TrimSpace(observed) == "" {
			observation.Result = DeploymentResultMismatched
			setDeploymentDiagnostic(&observation, "manual_repo_missing", "人工部署证明缺少仓库提交")
			return observation, nil
		}
		if observed == expected {
			continue
		}
		if v.IsDescendant == nil {
			observation.Result = DeploymentResultMismatched
			setDeploymentDiagnostic(&observation, "manual_commit_mismatch", "人工部署提交与期望不一致")
			return observation, nil
		}
		matched, err := v.IsDescendant(ctx, repo, expected, observed)
		if err != nil {
			setDeploymentDiagnostic(&observation, "descendant_check_failed", "提交祖先关系暂不可验证")
			return observation, err
		}
		if !matched {
			observation.Result = DeploymentResultMismatched
			setDeploymentDiagnostic(&observation, "manual_commit_mismatch", "人工部署提交与期望不一致")
			return observation, nil
		}
		verifiedAncestors[repo] = expected
	}
	if len(verifiedAncestors) > 0 {
		observation.VerifiedCommitAncestors = verifiedAncestors
	}
	now := time.Now().UTC()
	observation.VerifiedAt = &now
	observation.Result = DeploymentResultMatched
	observation.DiagnosticCode = ""
	observation.DiagnosticMessage = ""
	return observation, nil
}

type CompositeDeploymentVerifier struct {
	providers map[string]DeploymentVerifier
}

func NewCompositeDeploymentVerifier(providers map[string]DeploymentVerifier) *CompositeDeploymentVerifier {
	cloned := make(map[string]DeploymentVerifier, len(providers))
	for source, verifier := range providers {
		if normalized := strings.ToLower(strings.TrimSpace(source)); normalized != "" && verifier != nil {
			cloned[normalized] = verifier
		}
	}
	return &CompositeDeploymentVerifier{providers: cloned}
}

func (v *CompositeDeploymentVerifier) Verify(ctx context.Context, request DeploymentVerificationRequest) (DeploymentObservation, error) {
	if v == nil {
		return DeploymentObservation{Result: DeploymentResultUnavailable, ObservedAt: time.Now().UTC(), DiagnosticCode: "provider_unavailable", DiagnosticMessage: "部署版本验证方式不可用"}, ErrDeploymentVerifierUnavailable
	}
	source := normalizedDeploymentSource(request.Source)
	provider := v.providers[source]
	if provider == nil {
		return DeploymentObservation{Environment: request.Environment, ExpectedCommits: CloneStringMap(request.ExpectedCommits), VerificationSource: source, ObservedVersion: request.ObservedVersion, ObservedCommits: CloneStringMap(request.ObservedCommits), ObservedAt: time.Now().UTC(), DiagnosticCode: "provider_unavailable", DiagnosticMessage: "部署版本验证方式不可用", Result: DeploymentResultUnavailable}, fmt.Errorf("%w: source %q", ErrDeploymentVerifierUnavailable, source)
	}
	request.Source = source
	observation, err := provider.Verify(ctx, request.Clone())
	if observation.Result != DeploymentResultMatched && observation.DiagnosticCode == "" {
		if err != nil {
			setDeploymentDiagnostic(&observation, "verifier_unavailable", "部署版本验证暂不可用")
		} else {
			setDeploymentDiagnostic(&observation, "version_mismatch", "运行版本与期望提交不一致")
		}
	}
	return observation, err
}
