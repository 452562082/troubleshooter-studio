package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// writeTshootMeta 写产物根下的 tshoot.json 锚点，discover 靠这个文件识别机器人。
// dir 是"产物根"——对 openclaw 是 <OutputDir>/templates/workspace-template/，
// 对 claude-code/cursor/embedded 是各自的输出目录。
//
// 字段取自 g.Ctx（system.id / name）+ g.TshootVersion + g.TroubleshooterYAMLSource。
// TroubleshooterYAMLSource 为空时字段留空字符串，不影响 JSON 结构，只影响后续 apply 能不能找到"真源"。
func (g *Generator) writeTshootMeta(dir, target string) error {
	return g.writeTshootMetaForRole(dir, target, AgentRoleTroubleshooter)
}

func (g *Generator) writeTshootMetaForRole(dir, target string, role AgentRole) error {
	agentID := agentIDForRole(g.Ctx, role)
	meta := discover.Meta{
		SchemaVersion:       2,
		TshootVersion:       g.TshootVersion,
		SystemID:            g.Ctx.System.ID,
		SystemName:          g.Ctx.System.Name,
		AgentID:             agentID,
		Role:                string(role),
		InternalAgents:      internalAgentsForMeta(g.Ctx),
		ProjectRepositories: projectRepositoriesForMeta(g.Ctx.SystemConfig, g.Ctx.RepoLocalPaths),
		Target:              target,
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		TroubleshooterYAML:  string(g.TroubleshooterYAMLSource),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tshoot.json: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, discover.MetaFilename)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func projectRepositoriesForMeta(cfg *config.SystemConfig, repoLocalPaths map[string]string) []discover.ProjectRepository {
	if cfg == nil {
		return nil
	}
	repos := make([]discover.ProjectRepository, 0, len(cfg.Repos))
	for _, repo := range cfg.Repos {
		repos = append(repos, discover.ProjectRepository{
			Name:      strings.TrimSpace(repo.Name),
			URL:       strings.TrimSpace(repo.URL),
			LocalPath: strings.TrimSpace(repoLocalPaths[repo.Name]),
			SubPath:   strings.Trim(strings.TrimSpace(repo.SubPath), "/\\"),
		})
	}
	return repos
}

func (g *Generator) writeIDEAgentMetas(dir, target string) error {
	for _, role := range internalAgentRoles() {
		agentID := agentIDForRole(g.Ctx, role)
		metaDir := filepath.Join(dir, "agents-meta", agentID)
		if err := g.writeTshootMetaForRole(metaDir, target, role); err != nil {
			return err
		}
	}
	return g.writeTshootMeta(dir, target)
}

func internalAgentsForMeta(ctx *Context) []discover.InternalAgent {
	return []discover.InternalAgent{
		{ID: agentIDForRole(ctx, AgentRoleTroubleshooter), Role: string(AgentRoleTroubleshooter)},
		{ID: agentIDForRole(ctx, AgentRoleValidator), Role: string(AgentRoleValidator)},
		{ID: agentIDForRole(ctx, AgentRoleFixer), Role: string(AgentRoleFixer)},
	}
}
