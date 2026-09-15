package bughub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FindCursorCLI also checks the official install directory because desktop
// applications do not necessarily inherit the user's interactive shell PATH.
func FindCursorCLI() (string, error) {
	home, _ := os.UserHomeDir()
	for _, name := range []string{"cursor-agent", filepath.Join(home, ".local", "bin", "cursor-agent")} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin, nil
		}
	}
	// "agent" is generic: only accept it when it identifies itself as Cursor.
	for _, name := range []string{"agent", filepath.Join(home, ".local", "bin", "agent")} {
		bin, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		output, err := exec.CommandContext(ctx, bin, "--help").CombinedOutput()
		cancel()
		if err == nil && strings.Contains(strings.ToLower(string(output)), "cursor") {
			return bin, nil
		}
	}
	return "", errors.New("未检测到 Cursor Agent CLI；请从 https://cursor.com/install 安装，并运行 cursor-agent login 登录")
}

// BuildCursorInvestigationCommand uses the installed robot workspace for skills
// and Cursor's normal global/project MCP configuration. The host's phase prompt
// supplies the authorized scope for both investigation and repair.
func BuildCursorInvestigationCommand(binary, workspace, prompt string) (*exec.Cmd, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, errors.New("cursor 机器人工作目录不能为空")
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return nil, fmt.Errorf("cursor 机器人工作目录不可用: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("cursor 机器人工作目录必须是目录")
	}
	if strings.TrimSpace(binary) == "" {
		binary, err = FindCursorCLI()
		if err != nil {
			return nil, err
		}
	}
	// --force permits tools in print mode; explicit Cursor deny rules still apply.
	// Do not request partial output: assistant events must contain whole messages.
	cmd := exec.Command(binary, "--print", "--output-format", "stream-json", "--force", "--trust", "--approve-mcps", "--", prompt)
	cmd.Dir = workspace
	return cmd, nil
}
