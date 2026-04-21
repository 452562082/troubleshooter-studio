// Package deploy 管理"产物目录 → 已装机器人"的最后一公里：
// 解析 install.sh 需要哪些凭证、把凭证写进 scripts/.env、shell-out 跑 install.sh。
//
// 核心设计：**不替代 install.sh**，而是为它铺路。install.sh 启动时会 source .env
// 并用 read_var 跳过已提供的变量（见 templates/scripts/install.sh.tmpl 的 preflight
// 段）。所以我们只要把值塞进 .env，install.sh 就不会交互 prompt。
package deploy

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Prompt 描述 install.sh 里一个 read_var 调用的元信息，
// 用于 UI 渲染表单（前端需要知道：变量名、给用户看的提示文案、要不要密码遮罩）。
type Prompt struct {
	Name   string `json:"name"`   // env 变量名（写 .env 时用），如 CC_ADDR_DEV
	Prompt string `json:"prompt"` // 给用户看的提示文案，来自 read_var 第二个参数
	Secret bool   `json:"secret"` // true = password 遮罩
}

// read_var 的正则：read_var VAR "prompt text" [secret]
// install.sh.tmpl 里生成的格式固定，允许少量空白差异。
var reReadVar = regexp.MustCompile(`read_var\s+([A-Za-z_][A-Za-z0-9_]*)\s+"([^"]*)"(\s+secret)?`)

// ParseInstallPrompts 扫 <dir>/scripts/install.sh 里所有 read_var 调用，
// 按文件内出现顺序返回，**去重**：同一变量只保留第一次出现（install.sh 里不会真正重复，
// 但保险起见）。install.sh 不存在或解析失败都返回 error。
func ParseInstallPrompts(dir string) ([]Prompt, error) {
	path := filepath.Join(dir, "scripts", "install.sh")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	var out []Prompt
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// 注释行跳过（bash # 开头），避免把示例里的 read_var 当真
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := reReadVar.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Prompt{
			Name:   name,
			Prompt: strings.TrimSpace(m[2]),
			Secret: m[3] != "",
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// WriteEnvFile 把 kv 写成 KEY='value' 格式到 <dir>/scripts/.env，mode 0600。
// value 里的单引号会被转义（双 '' 写法，bash 兼容）。空 value 的键依然写出来
// （install.sh 的 read_var 看到空就会 prompt；这里写出来是为了让用户在 UI 上
// 看到完整列表，决定哪些需要 prompt）。
func WriteEnvFile(dir string, kv map[string]string) error {
	envDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(envDir, ".env")

	var buf bytes.Buffer
	buf.WriteString("# 由 tshoot 桌面端写入。编辑前先备份。\n")
	buf.WriteString("# 删除此文件或某行 = 下次 install.sh 会重新交互 prompt 对应变量。\n\n")
	for k, v := range kv {
		// 跳过明显非法的 key
		if k == "" {
			continue
		}
		// 单引号转义：bash 里 'a''b' 代表 a'b
		escaped := strings.ReplaceAll(v, "'", `'\''`)
		fmt.Fprintf(&buf, "%s='%s'\n", k, escaped)
	}
	if err := os.WriteFile(envPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	return nil
}

// ReadEnvFile 如果 <dir>/scripts/.env 存在，解析成 map 返回；
// 不存在返回 (nil, nil)。解析很松：识别 `KEY='value'` / `KEY="value"` / `KEY=value`，
// 忽略 # 开头的行和空行。
func ReadEnvFile(dir string) (map[string]string, error) {
	envPath := filepath.Join(dir, "scripts", ".env")
	f, err := os.Open(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// 去掉外层引号
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out, sc.Err()
}

// RunInstall shell-out 跑 <dir>/scripts/install.sh，同步捕获 stdout+stderr 合并回来。
// 前提：.env 已被 WriteEnvFile 填好（否则 install.sh 会卡在 stdin 上 prompt，
// 没人输就永久 hang）。返回合并的日志和退出错误。
//
// 注意：install.sh 里某些依赖检查（brew install node 之类）如果需要 sudo，也会 hang。
// UI 侧应该提前引导用户装好 node / python3 / uvx。
func RunInstall(dir string) (string, error) {
	installSh := filepath.Join(dir, "scripts", "install.sh")
	if _, err := os.Stat(installSh); err != nil {
		return "", fmt.Errorf("install.sh not found at %s: %w", installSh, err)
	}
	cmd := exec.Command("bash", installSh)
	cmd.Dir = dir
	// 把 stdin 接到 /dev/null —— 如果 install.sh 真遇到了未喂的 read_var，立即 EOF 而不是挂死
	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devnull
		defer devnull.Close()
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
