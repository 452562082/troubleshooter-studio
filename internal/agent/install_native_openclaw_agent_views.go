package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func installOpenclawAgentViews(wsDir string, cfg *config.SystemConfig) error {
	agents := openclawInternalAgents(cfg)
	if len(agents) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(wsDir, "agents"), 0o755); err != nil {
		return err
	}
	for _, ag := range agents {
		if err := writeOpenclawAgentDefinition(wsDir, ag); err != nil {
			return err
		}
		if err := installOpenclawAgentSkills(wsDir, ag); err != nil {
			return err
		}
		if err := installOpenclawAgentScripts(wsDir, ag); err != nil {
			return err
		}
	}
	return nil
}

type openclawInternalAgent struct {
	ID      string
	Role    generator.AgentRole
	Summary string
	Entry   string
}

func openclawInternalAgents(cfg *config.SystemConfig) []openclawInternalAgent {
	troubleshooterID := strings.TrimSpace(cfg.ResolveID())
	base := strings.TrimSpace(cfg.System.ID)
	if base == "" {
		base = strings.TrimSuffix(troubleshooterID, "-troubleshooter")
	}
	validatorID := strings.TrimSpace(base + "-validator")
	return []openclawInternalAgent{
		{
			ID:      troubleshooterID,
			Role:    generator.AgentRoleTroubleshooter,
			Summary: "定位根因、给出修复建议",
			Entry:   "skills/" + troubleshooterID + "/incident-investigator/SKILL.md",
		},
		{
			ID:      validatorID,
			Role:    generator.AgentRoleValidator,
			Summary: "复现、回归、采集证据",
			Entry:   "skills/" + validatorID + "/bug-verifier/SKILL.md",
		},
	}
}

func writeOpenclawAgentDefinition(wsDir string, ag openclawInternalAgent) error {
	if ag.ID == "" {
		return nil
	}
	body := fmt.Sprintf("# %s\n\nRole: %s\n\n%s。\n\nEntry:\n\n- Read `%s`\n", ag.ID, ag.Role, ag.Summary, ag.Entry)
	return os.WriteFile(filepath.Join(wsDir, "agents", ag.ID+".md"), []byte(body), 0o644)
}

func installOpenclawAgentSkills(wsDir string, ag openclawInternalAgent) error {
	if ag.ID == "" {
		return nil
	}
	srcRoot := filepath.Join(wsDir, "skills")
	if info, err := os.Stat(srcRoot); err != nil || !info.IsDir() {
		return nil
	}
	dstRoot := filepath.Join(srcRoot, ag.ID)
	if err := os.RemoveAll(dstRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ag.ID || strings.HasSuffix(name, "-troubleshooter") || strings.HasSuffix(name, "-validator") {
			continue
		}
		if !generator.SkillAllowedForAgentRole(name, ag.Role) {
			continue
		}
		if err := copyDirAll(filepath.Join(srcRoot, name), filepath.Join(dstRoot, name)); err != nil {
			return fmt.Errorf("copy %s skills for %s: %w", name, ag.ID, err)
		}
	}
	return nil
}

func installOpenclawAgentScripts(wsDir string, ag openclawInternalAgent) error {
	if ag.ID == "" {
		return nil
	}
	srcRoot := filepath.Join(wsDir, "scripts")
	if info, err := os.Stat(srcRoot); err != nil || !info.IsDir() {
		return nil
	}
	dstRoot := filepath.Join(srcRoot, ag.ID)
	if err := os.RemoveAll(dstRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ag.ID || strings.HasSuffix(name, "-troubleshooter") || strings.HasSuffix(name, "-validator") {
			continue
		}
		src := filepath.Join(srcRoot, name)
		dst := filepath.Join(dstRoot, name)
		if entry.IsDir() {
			if err := copyDirAll(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := copyFileSimple(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func syncOpenclawInternalAgentsToMeta(wsDir string, cfg *config.SystemConfig) error {
	metaPath := filepath.Join(wsDir, discover.MetaFilename)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}
	meta.InternalAgents = nil
	for _, ag := range openclawInternalAgents(cfg) {
		if ag.ID == "" {
			continue
		}
		meta.InternalAgents = append(meta.InternalAgents, discover.InternalAgent{
			ID:   ag.ID,
			Role: string(ag.Role),
		})
	}
	meta.AgentID = strings.TrimSpace(cfg.ResolveID())
	meta.Role = discover.RoleTroubleshooter
	updated, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, append(updated, '\n'), 0o644)
}
