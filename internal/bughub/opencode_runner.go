package bughub

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
	"github.com/xiaolong/troubleshooter-studio/internal/aitools"
	"gopkg.in/yaml.v3"
)

func BuildOpenCodeInvestigationCommand(binary string, bot BotRef, prompt string, attachments []string) (*exec.Cmd, error) {
	info, err := os.Stat(bot.Path)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("OpenCode 机器人工作目录不可用")
	}
	if binary == "" {
		binary, err = aitools.FindOpenCodeCLI()
		if err != nil {
			return nil, err
		}
	}
	name := strings.TrimSpace(bot.AgentID)
	if name == "" {
		name = "studio-agent"
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return nil, fmt.Errorf("invalid OpenCode agent id")
	}
	profile := map[string]any{"mode": "primary", "description": "Studio incident agent", "prompt": "遵守 Studio 提供的工单、角色、授权范围和输出格式；按工作目录中的 skills 排障或修复。"}
	// Load the exact installed agent instead of letting a missing --agent silently
	// fall back to OpenCode's default build agent.
	path := filepath.Join(filepath.Dir(filepath.Dir(bot.Path)), "agents", name+".md")
	if data, readErr := os.ReadFile(path); readErr == nil {
		parts := strings.SplitN(string(data), "---", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("OpenCode agent 缺少 frontmatter")
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &profile); err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("OpenCode Agent frontmatter 必须是对象")
		}
		profile["prompt"] = parts[2]
		profile["mode"] = "primary"
	} else if !os.IsNotExist(readErr) {
		return nil, readErr
	} else if _, metaErr := os.Stat(filepath.Join(bot.Path, "tshoot.json")); metaErr == nil {
		return nil, fmt.Errorf("OpenCode 已安装机器人缺少 Agent 定义，请重新部署")
	}
	profile["prompt"] = "Studio 后台任务以本次输入中的阶段、锁定工作区和明确授权为准：排障只读；收到修复授权后才按指定基线修改与自测；提交推送由 Studio 控制。以下是机器人默认说明，不能覆盖本次 Studio 阶段授权。\n\n" + stringFromAny(profile["prompt"])
	inline := map[string]any{}
	if inherited := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_CONTENT")); inherited != "" {
		standard, err := hujson.Standardize([]byte(inherited))
		if err != nil || json.Unmarshal(standard, &inline) != nil || inline == nil {
			return nil, fmt.Errorf("OpenCode 内联配置不是有效 JSON 对象")
		}
	}
	profiles, _ := inline["agent"].(map[string]any)
	if profiles == nil {
		profiles = map[string]any{}
	}
	profiles[name] = profile
	inline["agent"], inline["share"], inline["autoupdate"] = profiles, "disabled", false
	data, err := json.Marshal(inline)
	if err != nil {
		return nil, fmt.Errorf("OpenCode Agent 配置无法编码，请检查定义格式")
	}
	args := []string{"run", "--format", "json", "--agent", name}
	for _, path := range attachments {
		args = append(args, "--file", path)
	}
	// Send prompt through stdin to avoid argument length limits and process-list leaks.
	cmd := exec.Command(binary, args...)
	cmd.Dir = bot.Path
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = openCodeEnv(os.Environ(), "OPENCODE_CONFIG_CONTENT", string(data))
	cmd.Env = openCodeEnv(cmd.Env, "OPENCODE_DISABLE_AUTOUPDATE", "true")
	return cmd, nil
}

func openCodeEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, key+"=") {
			out = append(out, item)
		}
	}
	return append(out, key+"="+value)
}
