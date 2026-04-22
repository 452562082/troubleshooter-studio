// bindings_chat.go —— 桌面端原生 LLM 对话 binding。取代 standalone target
// 的 Flask server.py 方案,Studio 进程直接拿 Anthropic Go SDK 跟模型流式对话,
// token delta 通过 Wails EventsEmit 推给前端 BotsChat.vue。
//
// 流程:
//
//	前端 ChatSend(botPath, messages, apiKey, env) → Go 起一次 llmchat.Start,
//	启动 goroutine 消费 stream.Events,每个 delta emit "chat:delta:<reqId>";
//	结束 emit "chat:done:<reqId>";错误 emit "chat:error:<reqId>"。reqId 让前端
//	能区分并发请求(虽然 UI 一般一次只发一条,留接口兼容后面 tab/多机器人场景)。
//	用户点"停止"调 ChatStop(reqId) → 触发 stream.Cancel() → SDK 端断 http。
package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/llmchat"
)

// ChatContext 对应 bot 目录里能读出来的聊天相关元信息:system prompt + model + env 列表。
// 让前端"打开对话"页初始化时一次拿到,不用自己解 tshoot.json。
type ChatContext struct {
	SystemID   string   `json:"system_id"`
	SystemName string   `json:"system_name"`
	Model      string   `json:"model"` // 归一后可直接用的 Anthropic model id
	Envs       []string `json:"envs"`  // env id 列表,UI 下拉选用
}

// 进行中的 chat streams。key = reqId(字符串,前端传进来当"本次会话的句柄")。
// chatStreams 的锁跟 install / standalone 的 mu 分开,Chat 相对独立不争资源。
type chatStreamRegistry struct {
	mu      sync.Mutex
	streams map[string]*llmchat.Stream
	nextID  atomic.Int64
}

func (r *chatStreamRegistry) put(s *llmchat.Stream) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streams == nil {
		r.streams = map[string]*llmchat.Stream{}
	}
	id := strconv.FormatInt(r.nextID.Add(1), 10)
	r.streams[id] = s
	return id
}

func (r *chatStreamRegistry) take(id string) *llmchat.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.streams[id]
	if ok {
		delete(r.streams, id)
	}
	return s
}

// ChatContextFor 前端点"打开对话"后第一件事:把 bot 目录里的元信息捞出来。
// tshoot.json 里已经内嵌了 system_yaml,直接拿来解析,不用再回 discover.Scan。
func (a *App) ChatContextFor(botPath string) (*ChatContext, error) {
	if botPath == "" {
		return nil, fmt.Errorf("botPath 必填")
	}
	// 复用 discover.Scan 读元信息(会走 tshoot.json 解析);走单根 scan 最多几 ms
	found, err := discover.Scan([]string{botPath})
	if err != nil {
		return nil, fmt.Errorf("读 %s 的 tshoot.json: %w", botPath, err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s 下没有 tshoot.json,可能不是 tshoot 生成的机器人", botPath)
	}
	// 同目录可能命中多个 target(在 ImportAndDeploy 的 claude-code + cursor 合装时),
	// 选路径最短那个(最贴近 botPath 根),保持跟 ApplyBot 一致。
	ag := found[0]
	for _, cand := range found[1:] {
		if len(cand.Path) < len(ag.Path) {
			ag = cand
		}
	}
	if ag.Meta.SystemYAML == "" {
		return nil, fmt.Errorf("tshoot.json 没嵌 system_yaml,无法解析 model/env 列表")
	}
	cfg, err := config.LoadFromBytes([]byte(ag.Meta.SystemYAML))
	if err != nil {
		return nil, fmt.Errorf("yaml 解析: %w", err)
	}
	envs := make([]string, 0, len(cfg.Environments))
	for _, e := range cfg.Environments {
		envs = append(envs, e.ID)
	}
	return &ChatContext{
		SystemID:   cfg.System.ID,
		SystemName: cfg.System.Name,
		Model:      llmchat.AnthropicDefaultModel(cfg.Agent.Model),
		Envs:       envs,
	}, nil
}

// ChatSendInput 前端传进来的一次请求,跟 server.py /api/chat 的语义一一对应。
type ChatSendInput struct {
	BotPath    string            `json:"bot_path"`
	APIKey     string            `json:"api_key"`
	Messages   []llmchat.Message `json:"messages"`
	DefaultEnv string            `json:"default_env"`
}

// ChatSend 发起一次流式对话,返回 reqId;立刻返回不等流结束。
// 前端 EventsOn("chat:delta:"+reqId, cb) 收 token,"chat:done:"+reqId / "chat:error:"+reqId 表示终态。
func (a *App) ChatSend(in ChatSendInput) (string, error) {
	if in.BotPath == "" {
		return "", fmt.Errorf("bot_path 必填")
	}
	if in.APIKey == "" {
		return "", fmt.Errorf("api_key 必填(UI 应引导用户填入 LLM_API_KEY)")
	}
	if len(in.Messages) == 0 {
		return "", fmt.Errorf("messages 不能为空")
	}

	ctx, err := a.ChatContextFor(in.BotPath)
	if err != nil {
		return "", err
	}
	sysPrompt := llmchat.ReadSystemPrompt(in.BotPath)

	stream, err := llmchat.Start(a.ctx, llmchat.ChatOptions{
		APIKey:       in.APIKey,
		Model:        ctx.Model,
		MaxTokens:    4096,
		SystemPrompt: sysPrompt,
		Messages:     in.Messages,
		DefaultEnv:   in.DefaultEnv,
		ValidEnvs:    ctx.Envs,
	})
	if err != nil {
		return "", err
	}

	reqID := a.chatStreams.put(stream)

	// 背景 goroutine:把 stream 事件转成 Wails event。
	// reqID 挂在 event 名后缀,前端只订阅自己的那个 id 对应的事件,不会串。
	go func() {
		for ev := range stream.Events() {
			switch ev.Kind {
			case llmchat.EventDelta:
				wailsruntime.EventsEmit(a.ctx, "chat:delta:"+reqID, ev.Text)
			case llmchat.EventError:
				wailsruntime.EventsEmit(a.ctx, "chat:error:"+reqID, ev.Text)
			case llmchat.EventDone:
				wailsruntime.EventsEmit(a.ctx, "chat:done:"+reqID, "")
			}
		}
		// goroutine 退出时从注册表里摘掉,避免 stop 时拿到僵尸句柄
		a.chatStreams.take(reqID)
	}()

	return reqID, nil
}

// ChatStop 前端点"停止"触发;cancel 对应的 stream。未知 reqId 静默 false,UI 可忽略。
func (a *App) ChatStop(reqID string) bool {
	s := a.chatStreams.take(reqID)
	if s == nil {
		return false
	}
	s.Cancel()
	return true
}

// stopAllChats app 退出时由 main defer 调,cancel 所有未结束的 stream。
// 跟 stopAllStandalones 类似:虽然 ctx cancel 会级联,但显式 Stop 让前端也能收到
// 最后的 error 事件(前端订阅要卸载了倒是没所谓,但保险)。
func (a *App) stopAllChats() {
	a.chatStreams.mu.Lock()
	streams := make([]*llmchat.Stream, 0, len(a.chatStreams.streams))
	for _, s := range a.chatStreams.streams {
		streams = append(streams, s)
	}
	a.chatStreams.streams = map[string]*llmchat.Stream{}
	a.chatStreams.mu.Unlock()
	for _, s := range streams {
		s.Cancel()
	}
}

// 编译器防误删:确保 context 仍被引用(未来如果想把 request-level ctx 从前端传过来就用)
var _ = context.Background
