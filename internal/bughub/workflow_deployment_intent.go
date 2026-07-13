package bughub

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrDeploymentNotificationIntent  = errors.New("message is not an explicit deployment completion notification")
	ErrDeploymentEnvironmentMismatch = errors.New("deployment notification environment does not match Case environment")
	deploymentNotificationPattern    = regexp.MustCompile(`^(?:已部署|部署完成|已经部署(?:到[ ]*([A-Za-z0-9._-]+))?)$`)
)

// ParseDeploymentNotificationIntent intentionally accepts a small exact
// language. Questions, negation, future tense, quotations, and history do not
// match this anchored expression.
func ParseDeploymentNotificationIntent(text string) bool {
	_, ok := parseDeploymentNotificationIntent(text)
	return ok
}

func parseDeploymentNotificationIntent(text string) (string, bool) {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	matches := deploymentNotificationPattern.FindStringSubmatch(normalized)
	if len(matches) == 0 {
		return "", false
	}
	return normalizedDeploymentEnvironment(matches[1]), true
}

func normalizedDeploymentEnvironment(environment string) string {
	return strings.ToLower(strings.TrimSpace(environment))
}

// NotifyDeployedFromText is only an intent adapter. It checks current durable
// state and then delegates to NotifyDeployed, preserving the same verifier and
// idempotency gates used by the UI button.
func (o *CaseOrchestrator) NotifyDeployedFromText(ctx context.Context, text string, cmd NotifyDeployedCommand) (IncidentCase, error) {
	environment, ok := parseDeploymentNotificationIntent(text)
	if !ok {
		return IncidentCase{}, ErrDeploymentNotificationIntent
	}
	incident, err := o.store.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingDeployment {
		return IncidentCase{}, ErrApprovalNotReady
	}
	if environment != "" && environment != normalizedDeploymentEnvironment(incident.Environment) {
		return IncidentCase{}, ErrDeploymentEnvironmentMismatch
	}
	return o.NotifyDeployed(ctx, cmd)
}
