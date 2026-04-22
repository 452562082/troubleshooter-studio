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
	"context"
	"fmt"
	"io"
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

// FindInstallSh 找产物目录里的 install.sh。openclaw target 在 <dir>/scripts/install.sh,
// claude-code / cursor / standalone 在 <dir>/install.sh。返回绝对路径,找不到返回
// ("", os.ErrNotExist)。
func FindInstallSh(dir string) (string, error) {
	for _, rel := range []string{"scripts/install.sh", "install.sh"} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

// ParseInstallPrompts 扫 install.sh(scripts/install.sh 或 root install.sh)里所有
// read_var 调用,按文件内出现顺序返回,**去重**:同一变量只保留第一次出现。
// install.sh 不存在返回 (nil, nil) 表示"无 install 步骤";解析失败返回 error。
func ParseInstallPrompts(dir string) ([]Prompt, error) {
	path, err := FindInstallSh(dir)
	if err != nil {
		if err == os.ErrNotExist {
			return nil, nil
		}
		return nil, err
	}
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
// value 里的单引号会被转义（双 ” 写法，bash 兼容）。空 value 的键依然写出来
// （install.sh 的 read_var 看到空就会 prompt；这里写出来是为了让用户在 UI 上
// 看到完整列表，决定哪些需要 prompt）。
// kv 为空时直接 no-op（non-openclaw target 没有凭证,不需要 .env,也避免建空 scripts/ 子目录）。
func WriteEnvFile(dir string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
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
		// 去掉外层引号,并反向解码 WriteEnvFile 里的单引号转义 '\'' → '
		if len(val) >= 2 && (val[0] == '\'' || val[0] == '"') && val[len(val)-1] == val[0] {
			quote := val[0]
			val = val[1 : len(val)-1]
			if quote == '\'' {
				// bash 单引号字符串里 '\'' 是唯一一种转义,表示字面量单引号
				val = strings.ReplaceAll(val, `'\''`, `'`)
			}
		}
		out[key] = val
	}
	return out, sc.Err()
}

// RunInstall shell-out 跑 install.sh(auto-locate:scripts/install.sh 或 root),
// 同步捕获 stdout+stderr 合并回来。cmd.Dir 设为 install.sh 所在目录(让脚本里
// 的相对路径 / $(dirname "$0") 能正常 work)。
// 前提:.env 已被 WriteEnvFile 填好(否则 install.sh 会卡在 stdin 上 prompt,
// 没人输就永久 hang)。返回合并的日志和退出错误。
//
// 注意:install.sh 里某些依赖检查(brew install node 之类)如果需要 sudo,也会 hang。
// UI 侧应该提前引导用户装好 node / python3 / uvx。
func RunInstall(dir string) (string, error) {
	return RunInstallStreaming(context.Background(), dir, nil)
}

// RunInstallStreaming 同 RunInstall,但额外把 stdout+stderr 逐行 callback 出去,
// 便于 UI 实时展示而不是等脚本跑完一次性吐。onLine 可以是 nil(退化成 RunInstall)。
// 返回的完整 log 仍然保留(前端刷新 / 持久化用),跟回调并行写入。
//
// ctx 用于取消:桌面端用户点"取消安装"时,传入的 ctx 被 cancel,exec.CommandContext
// 会发 SIGKILL 给 install.sh 进程(及其子进程组,见下面 Setpgid)。cancel 后 cmd.Run
// 返回 context-cancelled 风格的 error,调用方据此知道是用户主动 abort 而非 install.sh
// 自身 exit 非零。ctx=nil 时用 context.Background()(相当于不可取消)。
func RunInstallStreaming(ctx context.Context, dir string, onLine func(line string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	installSh, err := FindInstallSh(dir)
	if err != nil {
		return "", fmt.Errorf("install.sh not found under %s: %w", dir, err)
	}
	cmd := exec.CommandContext(ctx, "bash", installSh)
	cmd.Dir = filepath.Dir(installSh)
	// 让 install.sh 自己 fork 出来的 brew / npm / pip 子进程跟脚本同属一个进程组,
	// 我们 cancel 时 exec.CommandContext 只会 kill pid 本身,子进程会变孤儿继续跑。
	// 用 Setpgid + 后面手动 kill -pgid 保证干净收尾,见 killProcessGroup。
	setProcessGroup(cmd)

	// 把 stdin 接到 /dev/null —— 如果 install.sh 真遇到了未喂的 read_var,立即 EOF 而不是挂死
	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devnull
		defer devnull.Close()
	}

	// stdout 和 stderr 合并到同一个 pipe,保证输出顺序跟用户实际看到的 shell 一致。
	// io.Pipe 让 Scanner 阻塞等待下一行,进程退出时由 goroutine 关 writer,Scanner 自然结束。
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start install.sh: %w", err)
	}

	// ctx cancel 时杀整个进程组,否则 bash 退了子孙进程还在跑。
	// Wait 返回(无论是正常 exit 还是被 kill)后让这个 goroutine 自然退出。
	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-waitDone:
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		close(waitDone)
		_ = pw.Close()
		// 被 ctx 杀掉时 Wait 会返回 "signal: killed" 之类,把 ctx.Err 覆盖回来更清晰
		if ctx.Err() != nil {
			errCh <- ctx.Err()
		} else {
			errCh <- err
		}
	}()

	var buf bytes.Buffer
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 留 1MB 给长行(一般 install.sh 不至于)
	for sc.Scan() {
		line := sc.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}
	return buf.String(), <-errCh
}
