// Package llmchat 是桌面端原生 chat 的后端 —— 直接拿 Anthropic Go SDK 跟模型流式
// 对话,桌面端 UI 通过 Wails EventsEmit 收 token delta,替代"起 Flask server.py +
// iframe"那套。
//
// 为什么要绕开 server.py:
//   - server.py 是 standalone target 产物的一部分,本意是给 *独立部署* 用的
//     (没 Studio 也能跑,bash install.sh + python3 server.py + 浏览器)
//   - 但桌面端用户已经装了 Studio,再额外要 Python + venv + 端口占用就很冗余
//   - 同时桌面端 iframe 嵌 localhost 的方案也能工作,但不如"Studio 原生 UI +
//     Go 直连 API"紧凑:复制/粘贴/stop 都共享 Studio 的交互
//
// 这个包对应的前端是 web/src/pages/BotsChat.vue 的原生版(非 iframe 版)。
// 逻辑保持跟 server.py 一致(复刻):读 bot 目录里的 system-prompt.md,按 env 拼前缀,
// 白名单过滤。
package llmchat

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Message 是对外(前端 -> Go)的轻量结构,保持跟 server.py /api/chat 约定一致。
// 桌面端 UI 侧维护 conversation 数组,整包传过来。
type Message struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"` // plain text;不支持附件/工具调用,跟 server.py 一样范围
}

// Stream 代表一次正在进行的流式对话。调用方 for ev := range s.Events() 消费 token,
// 期间可随时 s.Cancel() 终止(底层 ctx cancel + http 断开)。
type Stream struct {
	events chan Event
	cancel context.CancelFunc
}

// EventKind 区分事件类型,直接映射成前端监听的三种 Wails event 名。
type EventKind int

const (
	EventDelta EventKind = iota // Text 字段有值
	EventDone                   // 流正常结束
	EventError                  // Text 字段放错误文案
)

// Event 统一包装 delta/done/error 给消费端。
type Event struct {
	Kind EventKind
	Text string
}

// Events 返回只读事件通道;SDK 流结束或 cancel 后自动 close。
func (s *Stream) Events() <-chan Event { return s.events }

// Cancel 中断当前对话。调用多次幂等。
func (s *Stream) Cancel() { s.cancel() }

// ChatOptions 控制一次 Start 的行为。调用方一般对应一次 "/api/chat" 请求。
type ChatOptions struct {
	APIKey       string    // 必填:Anthropic API key
	Model        string    // 模型 ID,如 "claude-sonnet-4-5"
	MaxTokens    int64     // 上限,不传默认 4096
	SystemPrompt string    // 从 bot 目录的 system-prompt.md 读出来;本函数不做文件 IO
	Messages     []Message // 完整对话历史(user/assistant 交替)
	DefaultEnv   string    // 若非空,会按 server.py 的规则拼个环境前缀到 SystemPrompt
	ValidEnvs    []string  // 白名单:DefaultEnv 必须在这里面才会拼,防止前端注入
}

// Start 起一个流式对话,立刻返回。调用方从 Stream.Events() 消费。
// 参数错误(比如空 APIKey)直接返回 error,这种情况前端应该 toast 报给用户。
// 流启动后才发现的错(API 返回 4xx / 5xx / 网络断),通过 EventError 传出来。
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

	// env 白名单校验 + 拼 prompt 前缀(复刻 server.py L73-81 的逻辑)
	effectivePrompt := opt.SystemPrompt
	if opt.DefaultEnv != "" && slices.Contains(opt.ValidEnvs, opt.DefaultEnv) {
		effectivePrompt = fmt.Sprintf(
			"# 用户当前默认环境: %s\n除非用户在具体问题里指定了别的环境，否则都按 %s 环境操作。\n\n%s",
			opt.DefaultEnv, opt.DefaultEnv, opt.SystemPrompt,
		)
	}

	// 组装 SDK 参数
	msgs := make([]anthropic.MessageParam, 0, len(opt.Messages))
	for _, m := range opt.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			// unknown role:丢弃(保守),避免 400
		}
	}

	params := anthropic.MessageNewParams{
		Model:     opt.Model,
		MaxTokens: opt.MaxTokens,
		Messages:  msgs,
	}
	if effectivePrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: effectivePrompt}}
	}

	ctx, cancel := context.WithCancel(parentCtx)
	events := make(chan Event, 64)

	go func() {
		defer close(events)
		client := anthropic.NewClient(option.WithAPIKey(opt.APIKey))
		stream := client.Messages.NewStreaming(ctx, params)
		defer func() { _ = stream.Close() }()
		for stream.Next() {
			ev := stream.Current()
			// 只关心 content_block_delta 里的 Text 字段(文本流);其它事件
			// (message_start / content_block_start / message_delta 带 usage 等)
			// 桌面端 UI 不 care,忽略。
			if ev.Type == "content_block_delta" && ev.Delta.Text != "" {
				select {
				case events <- Event{Kind: EventDelta, Text: ev.Delta.Text}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := stream.Err(); err != nil {
			select {
			case events <- Event{Kind: EventError, Text: friendlyErr(err)}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case events <- Event{Kind: EventDone}:
		case <-ctx.Done():
		}
	}()

	return &Stream{events: events, cancel: cancel}, nil
}

// ReadSystemPrompt 读 bot 产物目录的 system prompt。分三档兜底,让所有 target
// 都能原生 chat,不只限 standalone:
//
//  1. 直接读 <botPath>/system-prompt.md(standalone target 生成的完整合并版)
//  2. 读不到 → 在运行时拼一个,跟 generator.buildSystemPrompt 逻辑一致:
//     SOUL.md + IDENTITY.md + AGENTS.md + CHECKLIST.md + TOOLS.md + skills/*/SKILL.md
//     (openclaw target 的 ~/.openclaw/workspace/<bot>/ 布局)
//  3. 还没凑出文字 → claude-code 的 CLAUDE.md 或默认兜底
//     (claude-code / cursor target 把知识写到这一个文件里)
func ReadSystemPrompt(botPath string) string {
	// Case 1: standalone 的合并文件
	if data, err := os.ReadFile(filepath.Join(botPath, "system-prompt.md")); err == nil {
		return string(data)
	}
	// Case 2: openclaw workspace 风格(多 .md 散落)
	if s := composeFromWorkspace(botPath); s != "" {
		return s
	}
	// Case 3: claude-code 的 CLAUDE.md
	if data, err := os.ReadFile(filepath.Join(botPath, "CLAUDE.md")); err == nil {
		return string(data)
	}
	return "你是一个排障机器人。"
}

// composeFromWorkspace 把 openclaw/claude-code/cursor 风格机器人目录里的
// SOUL/IDENTITY/AGENTS/CHECKLIST/TOOLS + skills/*/SKILL.md 拼成一份 prompt。
// 跟 generator.buildSystemPrompt 的顺序保持一致,保证 chat 行为跟 standalone target 等价。
// 任何一份都没命中返回空串,让调用方走下一档兜底。
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
	// skills/*/SKILL.md 叠加上去,跟 standalone 的拼法一致
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

// AnthropicDefaultModel 跟 generator.anthropicDefaultModel 保持一致逻辑:把
// system.yaml 里 agent.model 的值(可能带 "anthropic/" / "openai-codex/" 前缀)
// 归一到 Anthropic SDK 能直接用的 model id。非 Anthropic 风格(openai/codex 等)
// 回落默认模型。复制而非 import 是为了保持本包跟 generator 解耦。
func AnthropicDefaultModel(raw string) string {
	const fallback = "claude-sonnet-4-6"
	s := strings.TrimSpace(raw)
	if s == "" {
		return fallback
	}
	if rest, ok := strings.CutPrefix(s, "anthropic/"); ok {
		s = rest
	}
	if strings.HasPrefix(s, "claude-") {
		return s
	}
	return fallback
}

// friendlyErr 把 SDK 原始 error 转成用户能看懂的一句话。
// anthropic.Error 有 StatusCode / Message / Type 字段,401/429 等错误要特殊处理,
// 让前端能对症下药(填新 key / 等额度 / 换模型)。
func friendlyErr(err error) string {
	var apiErr *anthropic.Error
	// SDK 的 error 类型断言:不用 errors.As 因为 SDK 返回的是指针原生类型
	if e, ok := err.(*anthropic.Error); ok {
		apiErr = e
	}
	if apiErr != nil {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return "API key 无效或已失效,请换一个 key 重试 (401)"
		case http.StatusForbidden:
			return "API key 没有对应模型的访问权限 (403)"
		case http.StatusTooManyRequests:
			return "Anthropic 限流或余额不足,请稍后重试或充值 (429)"
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			return fmt.Sprintf("Anthropic 服务暂时不可用 (%d),请稍后重试", apiErr.StatusCode)
		}
		return fmt.Sprintf("Anthropic API 错误 (%d): %s", apiErr.StatusCode, apiErr.Error())
	}
	return err.Error()
}
