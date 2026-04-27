// Package deploy 管理"产物目录的凭证持久化":WriteEnvFile / ReadEnvFile
// 把用户填的凭证写到 <staging>/scripts/.env(mode 0600),下次 import 用 Studio
// 时 UI 表单可以预填,不用反复输入。
//
// 历史:本包过去还负责 shell-out 跑 `bash scripts/install.sh`,所以叫 deploy。
// install.sh 已被原生 Go(internal/agent.InstallNativeOpenclaw)替换,本包瘦身
// 到只剩 .env 读写;后续如还有跨 target 通用部署辅助再往里加。
package deploy

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prompt 描述一次凭证收集所需的 UI 表单字段。
// agent.DerivePrompts 按 system.yaml 派生,UI 据此渲染输入框。
type Prompt struct {
	Name   string `json:"name"`   // env 变量名（写 .env 时用），如 CC_ADDR_DEV
	Prompt string `json:"prompt"` // 给用户看的提示文案
	Secret bool   `json:"secret"` // true = password 遮罩
}

// WriteEnvFile 把 kv 写成 KEY='value' 格式到 <dir>/scripts/.env，mode 0600。
// value 里的单引号会被 bash 兼容地转义('\'')。空 value 的键依然写出来,UI
// 看到完整列表后能决定哪些需要补。kv 为空时直接 no-op(避免建空 scripts/)。
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
	buf.WriteString("# 删除此文件 = 下次 import 不再预填,需重新输入凭证。\n\n")
	for k, v := range kv {
		if k == "" {
			continue
		}
		escaped := strings.ReplaceAll(v, "'", `'\''`)
		fmt.Fprintf(&buf, "%s='%s'\n", k, escaped)
	}
	if err := os.WriteFile(envPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	return nil
}

// ReadEnvFile 如果 <dir>/scripts/.env 存在，解析成 map 返回；不存在返回 (nil, nil)。
// 解析很松:KEY='value' / KEY="value" / KEY=value 都能识别;# 开头和空行忽略。
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
				val = strings.ReplaceAll(val, `'\''`, `'`)
			}
		}
		out[key] = val
	}
	return out, sc.Err()
}
