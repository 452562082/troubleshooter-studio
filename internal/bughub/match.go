package bughub

import (
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
	validatorID := internalAgentIDForRole(selected, "validator")
	if strings.TrimSpace(validatorID) == "" {
		validatorID = strings.TrimSpace(selected.SystemID)
		if validatorID != "" {
			validatorID += "-validator"
		}
	}
	out := selected
	out.Role = "validator"
	out.AgentID = validatorID
	if strings.TrimSpace(out.Target) == "openclaw" {
		out.AgentID = firstNonEmpty(strings.TrimSpace(selected.AgentID), internalAgentIDForRole(selected, "troubleshooter"), strings.TrimSpace(selected.SystemID))
	}
	out.Key = strings.TrimSpace(selected.Key)
	if out.Key != "" {
		out.Key += "#validator"
	}
	if strings.TrimSpace(out.Target) != "openclaw" && strings.TrimSpace(validatorID) != "" {
		out.Path = filepath.Join(filepath.Dir(strings.TrimSpace(selected.Path)), validatorID)
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
