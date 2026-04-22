// Package llmchat 是桌面端原生 chat 的后端 —— 直接拿 LLM provider 的 OpenAI 兼容
// /chat/completions API 流式对话,桌面端 UI 通过 Wails EventsEmit 收 token delta。
//
// 为什么用 OpenAI 兼容协议统一:
//   - Anthropic / OpenAI / DeepSeek / Qwen / MiniMax / Moonshot / 智谱 / Ollama
//     2024 年后都提供了 OpenAI 兼容的 /chat/completions 端点
//   - 一套 SDK(sashabaranov/go-openai)+ base_url 切换就能跑所有家
//   - model id 前缀解析(anthropic/xxx / openai/xxx / minimax/xxx 等)见 providers.go
//
// 路由:ResolveModel("anthropic/claude-sonnet-4-6") → Provider{...} + "claude-sonnet-4-6"
// 用 openai.NewClientWithConfig + ClientConfig.BaseURL 指向对应 provider 即可。
//
// 本包给"embedded"target 的机器人用 —— Studio 扫到 tshoot.json 打开对话页时,
// 读 system-prompt.md + agent.model,走 ResolveModel 按前缀路由到对应 provider
// 的 OpenAI 兼容 endpoint。
package llmchat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// Message 是对外(前端 -> Go)的轻量结构。不支持多模态 / 工具调用,跟 server.py 一致。
type Message struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"` // plain text
}

// Stream 代表一次正在进行的流式对话。调用方 for ev := range s.Events() 消费 token,
// 期间可随时 s.Cancel() 终止(底层 ctx cancel + http 断开)。
type Stream struct {
	events chan Event
	cancel context.CancelFunc
}

type EventKind int

const (
	EventDelta EventKind = iota
	EventDone
	EventError
)

type Event struct {
	Kind EventKind
	Text string
}

func (s *Stream) Events() <-chan Event { return s.events }
func (s *Stream) Cancel()              { s.cancel() }

type ChatOptions struct {
	APIKey       string    // 必填:当前 provider 的 API key
	Model        string    // 必填:形如 "anthropic/claude-sonnet-4-6" 或带前缀约定的 "claude-sonnet-4-6"
	MaxTokens    int       // 默认 4096
	SystemPrompt string    // 从 bot 目录的 system-prompt.md 读出来
	Messages     []Message // 完整对话历史(user/assistant 交替)
	DefaultEnv   string    // 非空则会按 server.py 的规则拼个环境前缀到 SystemPrompt
	ValidEnvs    []string  // DefaultEnv 必须在这里面才拼,防前端注入
}

// Start 起一个流式对话,立刻返回。调用方从 Stream.Events() 消费。
func Start(parentCtx context.Context, opt ChatOptions) (*Stream, error) {
	if opt.APIKey == "" {
		return nil, fmt.Errorf("APIKey 必填")
	}
	if opt.Model == "" {
		return nil, fmt.Errorf("model 必填")
	}
	if len(opt.Messages) == 0 {
		return nil, fmt.Errorf("messages 不能为空")
	}
	if opt.MaxTokens <= 0 {
		opt.MaxTokens = 4096
	}

	provider, modelID, ok := ResolveModel(opt.Model)
	if !ok {
		return nil, fmt.Errorf("未识别的 model %q:需要形如 anthropic/claude-sonnet-4-6 或已知前缀(claude-* / gpt-* / deepseek-* / qwen* / abab* / moonshot-* / glm-*)", opt.Model)
	}

	// 按 server.py L73-81 的规则拼 env 前缀到 system prompt
	effectivePrompt := opt.SystemPrompt
	if opt.DefaultEnv != "" && slices.Contains(opt.ValidEnvs, opt.DefaultEnv) {
		effectivePrompt = fmt.Sprintf(
			"# 用户当前默认环境: %s\n除非用户在具体问题里指定了别的环境，否则都按 %s 环境操作。\n\n%s",
			opt.DefaultEnv, opt.DefaultEnv, opt.SystemPrompt,
		)
	}

	// 组装 openai SDK 参数
	msgs := make([]openai.ChatCompletionMessage, 0, len(opt.Messages)+1)
	if effectivePrompt != "" {
		msgs = append(msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: effectivePrompt,
		})
	}
	for _, m := range opt.Messages {
		role := openai.ChatMessageRoleUser
		if m.Role == "assistant" {
			role = openai.ChatMessageRoleAssistant
		} else if m.Role != "user" {
			continue // 未知 role 丢弃,防 400
		}
		msgs = append(msgs, openai.ChatCompletionMessage{Role: role, Content: m.Content})
	}

	// 配置 SDK client:BaseURL 指向当前 provider 的 OpenAI 兼容端点
	cfg := openai.DefaultConfig(opt.APIKey)
	cfg.BaseURL = provider.BaseURL
	client := openai.NewClientWithConfig(cfg)

	req := openai.ChatCompletionRequest{
		Model:     modelID,
		MaxTokens: opt.MaxTokens,
		Messages:  msgs,
		Stream:    true,
	}

	ctx, cancel := context.WithCancel(parentCtx)
	events := make(chan Event, 64)

	go func() {
		defer close(events)
		stream, err := client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			select {
			case events <- Event{Kind: EventError, Text: friendlyErr(err)}:
			case <-ctx.Done():
			}
			return
		}
		defer func() { _ = stream.Close() }()

		for {
			resp, err := stream.Recv()
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			if err != nil {
				// io.EOF 表示流正常结束
				if strings.Contains(err.Error(), "EOF") {
					break
				}
				select {
				case events <- Event{Kind: EventError, Text: friendlyErr(err)}:
				case <-ctx.Done():
				}
				return
			}
			// 每个 chunk 里有 Choices[].Delta.Content,提取追加
			for _, ch := range resp.Choices {
				if ch.Delta.Content != "" {
					select {
					case events <- Event{Kind: EventDelta, Text: ch.Delta.Content}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
		select {
		case events <- Event{Kind: EventDone}:
		case <-ctx.Done():
		}
	}()

	return &Stream{events: events, cancel: cancel}, nil
}

// ReadSystemPrompt 读 bot 产物目录的 system prompt。分三档兜底,让所有 target
// 都能原生 chat,不只限 embedded target:
//  1. <botPath>/system-prompt.md(embedded target 生成的合并版)
//  2. 拼 SOUL/IDENTITY/AGENTS/CHECKLIST/TOOLS + skills/*/SKILL.md (openclaw 风格)
//  3. <botPath>/CLAUDE.md (claude-code/cursor 风格)
//  4. 默认兜底
func ReadSystemPrompt(botPath string) string {
	if data, err := os.ReadFile(filepath.Join(botPath, "system-prompt.md")); err == nil {
		return string(data)
	}
	if s := composeFromWorkspace(botPath); s != "" {
		return s
	}
	if data, err := os.ReadFile(filepath.Join(botPath, "CLAUDE.md")); err == nil {
		return string(data)
	}
	return "你是一个排障机器人。"
}

func composeFromWorkspace(botPath string) string {
	var sb strings.Builder
	written := 0
	for _, name := range []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "CHECKLIST.md", "TOOLS.md"} {
		if data, err := os.ReadFile(filepath.Join(botPath, name)); err == nil {
			sb.Write(data)
			sb.WriteString("\n\n---\n\n")
			written++
		}
	}
	skillsDir := filepath.Join(botPath, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		var skillsWritten int
		var skillsBuf strings.Builder
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			if data, err := os.ReadFile(skillMD); err == nil {
				fmt.Fprintf(&skillsBuf, "## skill: %s\n\n", e.Name())
				skillsBuf.Write(data)
				skillsBuf.WriteString("\n\n---\n\n")
				skillsWritten++
			}
		}
		if skillsWritten > 0 {
			sb.WriteString("# Skills 详细说明\n\n")
			sb.WriteString(skillsBuf.String())
			written += skillsWritten
		}
	}
	if written == 0 {
		return ""
	}
	return sb.String()
}

// CheckKey 跟 provider 做一次最小交互,验证 apiKey 是否有效 + endpoint 可达。
// 不消耗/消耗极少 token:请求 max_tokens=1 + 一句 "hi"。成功返回 nil;
// 失败返回用户能看懂的原因(401/429/等),前端 key 表单拿这个信息决定要不要换 key。
//
// 为什么不用 Models.List:有的 provider (比如 ollama) 没暴露这个 endpoint,
// 但所有 provider 都有 /chat/completions,一试 1 token 统一。
func CheckKey(ctx context.Context, apiKey, model string) error {
	if apiKey == "" {
		return fmt.Errorf("apiKey 为空")
	}
	provider, modelID, ok := ResolveModel(model)
	if !ok {
		return fmt.Errorf("未识别的 model %q:需要形如 anthropic/claude-sonnet-4-6 或已知前缀", model)
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = provider.BaseURL
	client := openai.NewClientWithConfig(cfg)
	_, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     modelID,
		MaxTokens: 1,
		Messages:  []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		return errors.New(friendlyErr(err))
	}
	return nil
}

// EstimateTokens 粗略估算 text 的 token 数。不用 tiktoken 避免引入依赖,
// 用混合启发式:ASCII 字符按 4:1 比例(英文 token 平均长度),CJK 等多字节
// 字符按 1:1(中文基本一字一 token 或更糟)。结果误差 ±25%,够 UI 给用户
// 一个"是不是快爆 context"的大致指引。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var ascii, other int
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	// 估算 = ascii/4(英文 ~4 字符 1 token) + other*1(中文 ~1:1,稍保守)
	return ascii/4 + other
}

// ModelDisplay 给 UI 渲染用:从 rawModel 抽出对用户友好的显示形式。
// 如果解析成功,返回 "claude-sonnet-4-6 · Anthropic";否则原样返回。
func ModelDisplay(rawModel string) string {
	if provider, id, ok := ResolveModel(rawModel); ok {
		return id + " · " + provider.DisplayName
	}
	return rawModel
}

// friendlyErr 把 SDK / HTTP 错误翻译成用户能看懂的一句话。
// openai SDK 的 APIError 带 HTTPStatusCode + Message,比裸 HTTP 错有信息。
func friendlyErr(err error) string {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.HTTPStatusCode {
		case http.StatusUnauthorized:
			return "API key 无效或已失效,请换一个 key 重试 (401)"
		case http.StatusForbidden:
			return "API key 没有对应模型的访问权限 (403);检查该 provider 下模型是否已开通"
		case http.StatusNotFound:
			return fmt.Sprintf("model 不存在或 provider 端点不对 (404):%s", apiErr.Message)
		case http.StatusTooManyRequests:
			return "限流或余额不足,请稍后重试或充值 (429)"
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			return fmt.Sprintf("LLM 服务暂时不可用 (%d),请稍后重试", apiErr.HTTPStatusCode)
		}
		return fmt.Sprintf("LLM API 错误 (%d): %s", apiErr.HTTPStatusCode, apiErr.Message)
	}
	// 非 APIError:网络问题 / ctx cancel / 其它
	msg := err.Error()
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "connection refused") {
		return fmt.Sprintf("连不到 LLM provider 端点:%s(检查网络 / provider base_url 配置)", msg)
	}
	return msg
}
