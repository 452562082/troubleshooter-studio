package bughub

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func MatchBots(b Bug, bots []BotRef) []BotMatch {
	out := make([]BotMatch, 0, len(bots))
	for _, bot := range bots {
		score := 0
		var reasons []string
		if sameText(bot.SystemID, b.SystemID) && b.SystemID != "" {
			score += 50
			reasons = append(reasons, "system_id matched")
		}
		if botSupportsEnv(bot, matchEnv(b)) {
			score += 20
			reasons = append(reasons, "env matched")
		}
		haystack := strings.ToLower(strings.Join([]string{bot.Name, bot.Path, bot.SystemID, bot.Target}, " "))
		for _, term := range append([]string{b.FrontendRepo}, b.ServiceHints...) {
			term = strings.ToLower(strings.TrimSpace(term))
			if term != "" && strings.Contains(haystack, term) {
				score += 10
				reasons = append(reasons, "hint matched: "+term)
			}
		}
		out = append(out, BotMatch{Bot: bot, Score: score, Reasons: reasons})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Bot.Key < out[j].Bot.Key
	})
	return out
}

func ValidatorBotFor(selected BotRef) BotRef {
	return roleBotFor(selected, "validator", "validator")
}

func FixerBotFor(selected BotRef) BotRef {
	return roleBotFor(selected, "fixer", "fixer")
}

func roleBotFor(selected BotRef, role string, suffix string) BotRef {
	agentID := internalAgentIDForRole(selected, role)
	if strings.TrimSpace(agentID) == "" {
		agentID = strings.TrimSpace(selected.SystemID)
		if agentID != "" {
			agentID += "-" + suffix
		}
	}
	out := selected
	out.Role = role
	out.AgentID = agentID
	if strings.TrimSpace(out.Target) == "openclaw" {
		out.AgentID = firstNonEmpty(strings.TrimSpace(agentID), internalAgentIDForRole(selected, role), strings.TrimSpace(selected.AgentID), internalAgentIDForRole(selected, "troubleshooter"), strings.TrimSpace(selected.SystemID))
	}
	out.Key = strings.TrimSpace(selected.Key)
	if out.Key != "" {
		out.Key += "#" + role
	}
	if strings.TrimSpace(out.Target) != "openclaw" && strings.TrimSpace(agentID) != "" {
		candidate := filepath.Join(filepath.Dir(strings.TrimSpace(selected.Path)), agentID)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			out.Path = candidate
		}
	}
	return out
}

func internalAgentIDForRole(bot BotRef, role string) string {
	for _, ag := range bot.InternalAgents {
		if sameText(ag.Role, role) {
			return strings.TrimSpace(ag.ID)
		}
	}
	return ""
}

func sameText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func matchEnv(b Bug) string {
	if strings.TrimSpace(b.BotEnv) != "" {
		return b.BotEnv
	}
	return b.Env
}

func botSupportsEnv(bot BotRef, env string) bool {
	env = strings.TrimSpace(env)
	if env == "" {
		return false
	}
	if sameText(bot.Env, env) {
		return true
	}
	for _, candidate := range bot.Envs {
		if sameText(candidate, env) {
			return true
		}
	}
	return false
}
