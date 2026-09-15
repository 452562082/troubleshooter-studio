package bughub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AgentAvailability reports a real, bounded call using the platform's current
// account/model configuration. It does not infer authentication from config files.
type AgentAvailability struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func ProbeAgentAvailability(ctx context.Context, target string) AgentAvailability {
	return probeAgentAvailability(ctx, target, NewCodexInvestigator(nil, ""))
}

func probeAgentAvailability(ctx context.Context, target string, inv *CodexInvestigator) AgentAvailability {
	if ctx.Err() != nil {
		return AgentAvailability{"error", "连接检查已取消"}
	}
	switch target {
	case "claude-code", "cursor", "codex", "opencode":
	default:
		return AgentAvailability{"error", "不支持的 AI 平台"}
	}
	parent, err := os.MkdirTemp("", "studio-platform-check-")
	if err != nil {
		return AgentAvailability{"error", "无法创建临时检查目录"}
	}
	defer os.RemoveAll(parent)
	const name = "studio-availability"
	root := filepath.Join(parent, name)
	agentDir := filepath.Join(root, ".claude", "agents")
	if err = os.MkdirAll(agentDir, 0700); err != nil {
		return AgentAvailability{"error", "无法准备连接检查"}
	}
	if err = os.WriteFile(filepath.Join(agentDir, name+".md"), []byte("---\nname: "+name+"\ndescription: Connection check\n---\nReturn only the requested text. Do not use tools, access files, networks, or other agents.\n"), 0600); err != nil {
		return AgentAvailability{"error", "无法准备连接检查"}
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return AgentAvailability{"error", "无法准备连接检查"}
	}
	token := "STUDIO_READY_" + hex.EncodeToString(nonce)
	prompt := "This is a model connection check. Do not call tools, inspect files, invoke other agents, or perform any actions. Reply with only the following exact plain text: " + token
	cmd, parser, err := inv.buildCommand(target, BotRef{Target: target, Path: root, AgentID: name}, prompt)
	if err != nil {
		return AgentAvailability{"error", "未找到可运行的命令行工具，请安装后重新检测"}
	}
	bounded, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	result, err := inv.executePreparedPhase(bounded, "availability-"+hex.EncodeToString(nonce), cmd, parser, func(InvestigationEvent) {})
	// Never return raw provider/CLI output: it may include credentials or private config.
	if err != nil {
		if bounded.Err() != nil {
			return AgentAvailability{"error", "连接检查超时或已取消，请检查网络及默认模型后重试"}
		}
		return AgentAvailability{"error", "模型调用失败，请确认平台已登录、默认模型可用及网络正常后重试"}
	}
	if strings.TrimSpace(result.FinalYAML) != token {
		return AgentAvailability{"error", "未收到预期的模型回复，请检查默认模型后重试"}
	}
	return AgentAvailability{"ready", "当前账号与默认模型调用成功"}
}
