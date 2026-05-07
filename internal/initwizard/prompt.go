package initwizard

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Wizard 封装问答原语：所有交互都走 in/out，便于测试注入脚本
type Wizard struct {
	in  *bufio.Reader
	out io.Writer

	// Defaults 可选：启动时从已有 troubleshooter.yaml 预填；每个 ask 优先用其中对应字段作为默认值
	Defaults *Answers

	// current 实时追踪已回答到哪一步的 Answers 快照，供外部(signal handler)读取保存草稿
	mu      sync.Mutex
	current *Answers
}

func New(in io.Reader, out io.Writer) *Wizard {
	return &Wizard{in: bufio.NewReader(in), out: out}
}

// Snapshot 返回当前进行中的 Answers 深拷贝（用于 Ctrl+C 时把草稿落盘）。
// 没开始前返回 nil。
func (w *Wizard) Snapshot() *Answers {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current == nil {
		return nil
	}
	// 浅拷贝对主用途(导出 yaml)已够；列表字段在 Run 中每步重建，不会被后续改写
	snap := *w.current
	return &snap
}

// setCurrent 由 wizard.Run 在每步结束时调用。
func (w *Wizard) setCurrent(a *Answers) {
	w.mu.Lock()
	w.current = a
	w.mu.Unlock()
}

// defaultOr 返回 preferred（如非空）或 fallback；用来让 Run 里的 ask 带预填
func defaultOr(preferred, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return preferred
	}
	return fallback
}

func (w *Wizard) printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

// ask 读一行；用户回车空行 → 返回 defaultVal
func (w *Wizard) ask(label, defaultVal string) (string, error) {
	if defaultVal != "" {
		w.printf("  %s [%s]: ", label, defaultVal)
	} else {
		w.printf("  %s: ", label)
	}
	line, err := w.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return defaultVal, nil
	}
	return s, nil
}

// askBool: [Y/n] 默认 true，[y/N] 默认 false
func (w *Wizard) askBool(label string, defaultYes bool) (bool, error) {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	w.printf("  %s %s: ", label, hint)
	line, err := w.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return defaultYes, err
	}
	s := strings.ToLower(strings.TrimSpace(line))
	if s == "" {
		return defaultYes, nil
	}
	return s == "y" || s == "yes", nil
}

// askChoice: 选项中选一个，不匹配则重问一次；第二次仍不匹配则用 defaultVal
func (w *Wizard) askChoice(label string, choices []string, defaultVal string) (string, error) {
	hint := strings.Join(choices, "/")
	for attempt := 0; attempt < 2; attempt++ {
		s, err := w.ask(fmt.Sprintf("%s (%s)", label, hint), defaultVal)
		if err != nil {
			return "", err
		}
		for _, c := range choices {
			if s == c {
				return s, nil
			}
		}
		w.printf("    ! 无效选项 %q，请在 %s 中选\n", s, hint)
	}
	return defaultVal, nil
}

// section 打印分段标题
func (w *Wizard) section(title string) {
	w.printf("\n== %s ==\n", title)
}

// modelPresets 是推荐的 model id 列表，按提供商分组展示。
// 选择编号 = 用对应 value；回车 = 默认；输入任意字符串 = 自定义（老用户 / 企业网关）。
// 预设跟 web/src/pages/InitPage.vue 的 modelGroups 对齐(要改同步改),provider 列表
// 跟 internal/llmchat/providers.go 一致。4 种 target 都走 OpenAI 兼容协议 + base_url
// 切换,所有 provider 都能直连,没有"embedded 回落到 Claude"这种局限。
var modelPresets = []struct {
	group string
	items []struct{ value, desc string }
}{
	{
		group: "Anthropic (Claude 系列)",
		items: []struct{ value, desc string }{
			{"anthropic/claude-opus-4-7", "Claude Opus 4.7 — 最强、偏贵"},
			{"anthropic/claude-sonnet-4-6", "Claude Sonnet 4.6 — 默认推荐"},
			{"anthropic/claude-haiku-4-5", "Claude Haiku 4.5 — 便宜、快"},
		},
	},
	{
		group: "OpenAI",
		items: []struct{ value, desc string }{
			{"openai/gpt-5-codex", "GPT-5 Codex"},
			{"openai/gpt-4o", "GPT-4o"},
			{"openai/o3", "o3"},
		},
	},
	{
		group: "国产大模型",
		items: []struct{ value, desc string }{
			{"deepseek/deepseek-chat", "DeepSeek Chat"},
			{"deepseek/deepseek-reasoner", "DeepSeek Reasoner (推理)"},
			{"qwen/qwen-max", "通义千问 Qwen Max"},
			{"qwen/qwen-plus", "通义千问 Qwen Plus"},
			{"minimax/abab6.5s-chat", "MiniMax abab6.5s"},
			{"minimax/abab6.5-chat", "MiniMax abab6.5 (长上下文)"},
			{"moonshot/moonshot-v1-8k", "Moonshot Kimi v1-8k"},
			{"moonshot/moonshot-v1-128k", "Moonshot Kimi v1-128k"},
			{"zhipu/glm-4", "智谱 GLM-4"},
			{"zhipu/glm-4-plus", "智谱 GLM-4 Plus"},
		},
	},
	{
		group: "本地 / 自部署",
		items: []struct{ value, desc string }{
			{"ollama/llama3.1", "Ollama llama3.1 (本地)"},
			{"ollama/qwen2.5", "Ollama qwen2.5 (本地)"},
		},
	},
}

// askModel 让用户按编号选预设 model，或直接输入自定义字符串。
// 回车 = defaultVal；纯数字 = 对应预设；其他字符串 = 自定义 model id。
func (w *Wizard) askModel(defaultVal string) (string, error) {
	w.printf("  Agent 模型（回车=默认；数字=选预设；或直接填 model id）\n")
	// 按顺序编号打印
	idx := 0
	flat := []string{}
	for _, grp := range modelPresets {
		w.printf("    [%s]\n", grp.group)
		for _, it := range grp.items {
			idx++
			marker := " "
			if it.value == defaultVal {
				marker = "*"
			}
			w.printf("      %s %d) %-35s  %s\n", marker, idx, it.value, it.desc)
			flat = append(flat, it.value)
		}
	}
	w.printf("    [自定义]\n      %d) 手填任意 model id（企业内部网关 / 新模型）\n", idx+1)
	customIdx := idx + 1

	// 前缀多少空格跟 ask() 的 "  " 缩进保持一致
	w.printf("  选择或输入 [%s]: ", defaultVal)
	line, err := w.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return defaultVal, err
	}
	s := strings.TrimSpace(line)
	if s == "" {
		return defaultVal, nil
	}
	// 纯数字：按预设编号
	var n int
	if _, errNum := fmt.Sscanf(s, "%d", &n); errNum == nil && fmt.Sprintf("%d", n) == s {
		if n >= 1 && n <= len(flat) {
			return flat[n-1], nil
		}
		if n == customIdx {
			return w.ask("  自定义 model id", defaultVal)
		}
		w.printf("    ! 编号 %d 越界（1-%d），用输入的原文作 model id\n", n, customIdx)
	}
	// 非数字：当作自定义 model id 直接用
	return s, nil
}
