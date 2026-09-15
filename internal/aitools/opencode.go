package aitools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/platform"
)

func FindOpenCodeCLI() (string, error) {
	home, _ := os.UserHomeDir()
	for _, name := range []string{"opencode", filepath.Join(home, ".opencode/bin/opencode"), filepath.Join(home, ".local/bin/opencode"), "/opt/homebrew/bin/opencode", "/usr/local/bin/opencode"} {
		if binary, err := exec.LookPath(name); err == nil {
			return binary, nil
		}
	}
	return "", fmt.Errorf("未检测到 OpenCode CLI，请安装 OpenCode 并运行 opencode auth login 配置模型账号")
}

func DetectOpenCode() *Result {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return detectOpenCode(ctx)
}

func detectOpenCode(ctx context.Context) *Result {
	home, _ := os.UserHomeDir()
	result := &Result{ConfigRoot: platform.OpenCodeRoot(home)}
	binary, err := FindOpenCodeCLI()
	if err != nil {
		result.Note = err.Error()
		return result
	}
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = append(os.Environ(), "OPENCODE_DISABLE_MODELS_FETCH=true")
	data, err := cmd.Output()
	if err != nil {
		result.Note = "OpenCode CLI 版本检测失败"
		return result
	}
	result.Installed, result.Path, result.Version = true, binary, strings.TrimSpace(string(data))
	return result
}
