package bughub

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrDeploymentNotificationIntent = errors.New("message is not an explicit deployment completion notification")
	deploymentNotificationPattern   = regexp.MustCompile(`^(?:已部署|部署完成|已经部署(?:到[ ]*[A-Za-z0-9._-]+)?)$`)
)

// ParseDeploymentNotificationIntent intentionally accepts a small exact
// language. Questions, negation, future tense, quotations, and history do not
// match this anchored expression.
func ParseDeploymentNotificationIntent(text string) bool {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return deploymentNotificationPattern.MatchString(normalized)
}

// NotifyDeployedFromText is only an intent adapter. It checks current durable
// state and then delegates to NotifyDeployed, preserving the same verifier and
// idempotency gates used by the UI button.
func (o *CaseOrchestrator) NotifyDeployedFromText(ctx context.Context, text string, cmd NotifyDeployedCommand) (IncidentCase, error) {
	if !ParseDeploymentNotificationIntent(text) {
		return IncidentCase{}, ErrDeploymentNotificationIntent
	}
	incident, err := o.store.GetCase(ctx, cmd.CaseID)
	if err != nil {
		return IncidentCase{}, err
	}
	if incident.Status != CaseWaitingDeployment {
		return IncidentCase{}, ErrApprovalNotReady
	}
	return o.NotifyDeployed(ctx, cmd)
}
