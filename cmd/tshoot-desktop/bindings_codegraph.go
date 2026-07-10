package main

import (
	"context"
	"fmt"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

var ensureCodeGraphForRetry = agent.EnsureCodeGraphInstalled
var prepareCodeGraphForRetry = agent.PrepareCodeGraphIndexes
var invalidateCodeGraphForRetry = agent.InvalidateCodeGraphIndexCache
var emitCodeGraphRetryLog = func(ctx context.Context, line string) {
	wailsruntime.EventsEmit(ctx, "install:log", line)
}

// ReindexCodeGraph explicitly retries CodeGraph setup and indexing for an opted-in
// system. Unlike automatic deploy preparation, setup errors are returned to the
// caller because this action was requested directly by the user.
func (a *App) ReindexCodeGraph(yamlText string, repoPaths map[string]string) (*agent.CodeGraphIndexReport, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if !cfg.CodeIntelligence.UsesCodeGraph() {
		return nil, fmt.Errorf("CodeGraph is not enabled for system %q", cfg.System.ID)
	}

	expanded := make(map[string]string, len(repoPaths))
	for name, path := range repoPaths {
		expanded[name] = userconfig.ExpandHome(path)
	}

	ctx := a.getRuntimeContext()
	log := func(string) {}
	if ctx == nil {
		ctx = context.Background()
	} else {
		log = func(line string) {
			emitCodeGraphRetryLog(ctx, "[codegraph-retry] "+line)
		}
	}

	invalidateCodeGraphForRetry(cfg.System.ID)
	binary, err := ensureCodeGraphForRetry(log)
	if err != nil {
		return nil, fmt.Errorf("prepare CodeGraph binary: %w", err)
	}
	report := prepareCodeGraphForRetry(ctx, agent.CodeGraphIndexOptions{
		BinaryPath:     binary,
		SystemID:       cfg.System.ID,
		Repos:          agent.BuildCodeGraphRepoTargets(cfg, expanded),
		OnProgress:     log,
		InitTimeout:    120 * time.Second,
		SyncTimeout:    30 * time.Second,
		MaxConcurrency: 2,
	})
	return &report, nil
}
