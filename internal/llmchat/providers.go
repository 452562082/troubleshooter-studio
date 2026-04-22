package llmchat

import "strings"

// Provider 描述一家 LLM 厂商的接入信息。所有 provider 都要提供 OpenAI 兼容的
// /v1/chat/completions(大部分厂商 2024 年后都提供了)。这样 llmchat 和 server.py
// 都只用一套 openai SDK + base_url 切换即可。
//
// 如何添加新 provider:
//  1. 在下面 providersRegistry 里加一行,填 ID / BaseURL / FallbackModel
//  2. ID 要跟 system.yaml 里 agent.model 的前缀一致(如 "anthropic/claude-sonnet" 的前缀是 "anthropic")
//  3. 用户得自己拿这个 provider 的 API key,填到 Studio UI / 环境变量里
type Provider struct {
	// ID 也是 agent.model 的前缀标识,比如 "anthropic" 对应 "anthropic/claude-*"
	ID string
	// DisplayName UI 展示给用户看,比如"Anthropic (Claude 系列)"
	DisplayName string
	// BaseURL 是厂商的 OpenAI 兼容 endpoint(末尾不带 /chat/completions,SDK 自己补)
	BaseURL string
	// FallbackModel 当 model id 解析不到有效值时的兜底。必须是该 provider 真实存在的 id。
	FallbackModel string
	// EnvKeyName 默认从哪个环境变量读 API key。UI 里也可以直接让用户填。
	EnvKeyName string
}

// providersRegistry 是所有支持的 provider 注册表。
// 新家加在这里一行即可,前后端都自动支持(Python server.py 通过 Go 端 ChatContext
// 或直接读 yaml 来识别)。
var providersRegistry = map[string]Provider{
	"anthropic": {
		ID:            "anthropic",
		DisplayName:   "Anthropic (Claude 系列)",
		BaseURL:       "https://api.anthropic.com/v1",
		FallbackModel: "claude-sonnet-4-6",
		EnvKeyName:    "ANTHROPIC_API_KEY",
	},
	"openai": {
		ID:            "openai",
		DisplayName:   "OpenAI (GPT 系列)",
		BaseURL:       "https://api.openai.com/v1",
		FallbackModel: "gpt-4o",
		EnvKeyName:    "OPENAI_API_KEY",
	},
	"deepseek": {
		ID:            "deepseek",
		DisplayName:   "DeepSeek",
		BaseURL:       "https://api.deepseek.com/v1",
		FallbackModel: "deepseek-chat",
		EnvKeyName:    "DEEPSEEK_API_KEY",
	},
	"qwen": {
		ID:            "qwen",
		DisplayName:   "通义千问 (Qwen)",
		BaseURL:       "https://dashscope.aliyuncs.com/compatible-mode/v1",
		FallbackModel: "qwen-max",
		EnvKeyName:    "DASHSCOPE_API_KEY",
	},
	"minimax": {
		ID:            "minimax",
		DisplayName:   "MiniMax (海螺)",
		BaseURL:       "https://api.minimaxi.chat/v1",
		FallbackModel: "abab6.5s-chat",
		EnvKeyName:    "MINIMAX_API_KEY",
	},
	"moonshot": {
		ID:            "moonshot",
		DisplayName:   "Moonshot (月之暗面 Kimi)",
		BaseURL:       "https://api.moonshot.cn/v1",
		FallbackModel: "moonshot-v1-8k",
		EnvKeyName:    "MOONSHOT_API_KEY",
	},
	"zhipu": {
		ID:            "zhipu",
		DisplayName:   "智谱 (GLM)",
		BaseURL:       "https://open.bigmodel.cn/api/paas/v4",
		FallbackModel: "glm-4",
		EnvKeyName:    "ZHIPU_API_KEY",
	},
	"ollama": {
		ID:            "ollama",
		DisplayName:   "Ollama (本地)",
		BaseURL:       "http://localhost:11434/v1",
		FallbackModel: "llama3.1",
		EnvKeyName:    "", // 本地通常不要 key
	},
}

// ResolveModel 把 agent.model 解析成 (provider, modelID, ok)。
//
//	"anthropic/claude-opus-4-7" → (providers["anthropic"], "claude-opus-4-7", true)
//	"claude-sonnet-4-6"         → (providers["anthropic"], "claude-sonnet-4-6", true)  // 前缀缺省看 prefix
//	"gpt-4o"                     → (Provider{}, "", false)  // 无前缀也不匹配已知模型前缀
//
// 未知 provider 或空 model 返回 ok=false,调用方应给出清晰错误指引用户改 yaml。
func ResolveModel(rawModel string) (Provider, string, bool) {
	s := strings.TrimSpace(rawModel)
	if s == "" {
		return Provider{}, "", false
	}
	// 带 provider 前缀:按前缀查
	if idx := strings.Index(s, "/"); idx > 0 {
		prefix := strings.ToLower(s[:idx])
		rest := s[idx+1:]
		if p, ok := providersRegistry[prefix]; ok {
			if rest == "" {
				return p, p.FallbackModel, true
			}
			return p, rest, true
		}
		return Provider{}, "", false
	}
	// 没前缀:尝试按模型 ID 常见约定识别
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "claude-"):
		return providersRegistry["anthropic"], s, true
	case strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o3") || strings.HasPrefix(lower, "o1"):
		return providersRegistry["openai"], s, true
	case strings.HasPrefix(lower, "deepseek-"):
		return providersRegistry["deepseek"], s, true
	case strings.HasPrefix(lower, "qwen"):
		return providersRegistry["qwen"], s, true
	case strings.HasPrefix(lower, "abab") || strings.Contains(lower, "minimax"):
		return providersRegistry["minimax"], s, true
	case strings.HasPrefix(lower, "moonshot") || strings.HasPrefix(lower, "kimi"):
		return providersRegistry["moonshot"], s, true
	case strings.HasPrefix(lower, "glm-"):
		return providersRegistry["zhipu"], s, true
	}
	return Provider{}, "", false
}

// ProviderFor 是 ResolveModel 的薄 wrapper,只返回 provider;失败返回零值 +
// ok=false。UI 初始化 chat 页用这个算"该问哪家的 api key"。
func ProviderFor(rawModel string) (Provider, bool) {
	p, _, ok := ResolveModel(rawModel)
	return p, ok
}

// AllProviders 给 UI / 工具列所有支持的 provider。按字母序稳定输出。
func AllProviders() []Provider {
	keys := make([]string, 0, len(providersRegistry))
	for k := range providersRegistry {
		keys = append(keys, k)
	}
	// sort.Strings 也行,这里量小直接稳定序
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := make([]Provider, 0, len(keys))
	for _, k := range keys {
		out = append(out, providersRegistry[k])
	}
	return out
}
