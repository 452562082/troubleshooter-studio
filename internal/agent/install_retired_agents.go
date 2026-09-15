package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

func retiredValidator(ag discover.InternalAgent) bool {
	role := strings.ToLower(strings.TrimSpace(ag.Role))
	return role == discover.RoleValidator || (role == "" && strings.HasSuffix(strings.ToLower(ag.ID), "-validator"))
}

// Preserve the legacy anchor format, but never advertise a retired execution role.
func copyActiveAgentMeta(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var meta discover.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}
	active := make([]discover.InternalAgent, 0, len(meta.InternalAgents))
	for _, ag := range meta.InternalAgents {
		if !retiredValidator(ag) {
			active = append(active, ag)
		}
	}
	if len(active) == len(meta.InternalAgents) {
		return os.WriteFile(dst, data, 0o644)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	fields["internal_agents"], err = json.Marshal(active)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, append(data, '\n'), 0o644)
}

// Redeploy retires only validator entries belonging to this bot. Keep a
// recoverable backup outside the platform's agents/skills discovery directories.
func retireNativeValidators(root, staging, primary string, agentExt string) error {
	ids := map[string]bool{}
	for _, path := range []string{filepath.Join(staging, discover.MetaFilename), filepath.Join(root, "skills", primary, discover.MetaFilename)} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var meta discover.Meta
		if err := json.Unmarshal(data, &meta); err != nil {
			return fmt.Errorf("read agent ownership: %w", err)
		}
		if meta.SystemID != "" {
			ids[meta.SystemID+"-validator"] = true
		}
		for _, ag := range meta.InternalAgents {
			if retiredValidator(ag) {
				ids[ag.ID] = true
			}
		}
	}
	backup := filepath.Join(root, ".retired-agents", nanoTimestamp())
	for id := range ids {
		if id == primary || id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
			continue
		}
		for _, rel := range []string{filepath.Join("agents", id+agentExt), filepath.Join("skills", id), filepath.Join("scripts", id)} {
			src := filepath.Join(root, rel)
			if _, err := os.Lstat(src); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			dst := filepath.Join(backup, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			if err := os.Rename(src, dst); err != nil {
				return fmt.Errorf("back up retired agent %s: %w", id, err)
			}
		}
	}
	return nil
}
