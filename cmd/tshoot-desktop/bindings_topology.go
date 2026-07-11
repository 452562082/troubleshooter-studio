package main

import (
	"context"
	"fmt"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/topology"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

var runAutoAnalyzeForTopology = agent.RunAutoAnalyze
var invalidateAutoAnalyzeForTopology = agent.InvalidateAutoAnalyzeCache

// AnalyzeServiceTopology explicitly refreshes the rebuildable topology snapshot
// for the repository paths selected in the wizard. It reads source repositories
// through the analyzer pipeline but never writes YAML or repository contents.
func (a *App) AnalyzeServiceTopology(yamlText string, repoPaths map[string]string) (*topology.Snapshot, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	expanded := make(map[string]string, len(repoPaths))
	for name, path := range repoPaths {
		expanded[name] = userconfig.ExpandHome(path)
	}

	ctx := a.getRuntimeContext()
	onLog := func(string) {}
	if ctx == nil {
		ctx = context.Background()
	} else {
		onLog = func(line string) {
			wailsruntime.EventsEmit(ctx, "analyze:log", line)
		}
	}

	// This binding represents an explicit user refresh, so bypass only this
	// configuration/path cache entry before invoking the shared pipeline.
	invalidateAutoAnalyzeForTopology(cfg, expanded)
	result, err := runAutoAnalyzeForTopology(agent.RunAutoAnalyzeOptions{
		Cfg:       cfg,
		RepoPaths: expanded,
		OnLog:     onLog,
		Ctx:       ctx,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		if len(expanded) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("service topology analysis canceled: %w", err)
			}
			return nil, fmt.Errorf("service topology analysis timed out or returned no result")
		}
		return &topology.Snapshot{SchemaVersion: topology.SchemaVersion}, nil
	}
	return &result.Topology, nil
}
