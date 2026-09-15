package main

import (
	"context"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

// ProbeAgentAvailability is user-triggered and does not change provider settings.
func (a *App) ProbeAgentAvailability(target string) bughub.AgentAvailability {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return bughub.ProbeAgentAvailability(ctx, target)
}
