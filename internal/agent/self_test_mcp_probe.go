// self_test_mcp_probe.go —— 跑 stdio MCP 协议握手,验证已注册的 mcp 进程能起 + 暴露工具。
//
// 解决的真问题(2026-05-15 真实事故):
//   - rabbitmq mcp(amazon-mq/amq-mcp-server-rabbitmq)上游包源码引用 fastmcp 已删的
//     BearerAuthProvider — 装上后进程秒挂,但 install 显示 "success"(只看 IDE config 里注册了
//     mcp.servers,不看进程能不能起)。truss 现场曾因 nacos mcp-router 同款 silent-fallback
//     撞坑;rabbitmq 这次靠人工 probe 才发现。
//   - 本文件把那条人工 probe 工程化:每次 self-test 对每个 servers[<name>] 起一次子进程,
//     发 initialize + tools/list,验证 (a) 进程能起 (b) 工具列表非空。两条护栏覆盖大部分
//     "包能起 / 凭据被接受 / 协议没崩"的真错配。
//
// **不做**的事(避免过度规约):
//   - 不为通用 MCP 强制工具名清单跟 SKILL.md 文档对照 —— CodeGraph 是例外:它只有
//     codegraph_explore 这一个关键工具,tools/list 缺它等价于能力不可用
//   - 不真调任何工具(无副作用),只 tools/list
//   - 不验证写工具是否被拦截(那是 LLM SKILL 软约束的事,跟 mcp probe 无关)
//
// 顺带覆盖 P2.6 "凭据热验证":如果凭据错,大部分 mcp 进程起不来或 tools/list 拒绝 → probe FAIL
// 自动暴露(nacos mcp-router 那种 silent-fallback 设计是反模式,正经 mcp 会失败叫出来)。
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MCPProbeResult 单次 probe 结果。
type MCPProbeResult struct {
	Tools      []string // 实际暴露的工具名(initialize + tools/list 后拿到)
	Err        error    // 进程起不来 / 协议错误 / timeout / tools/list 拒绝等
	StderrTail string   // 失败时截 ≤300 字节 stderr,给故障定位
}

// probeMCPFunc 是 probe 入口的 package var,测试时可 monkey patch 避免真起子进程
// (CI 上没 npx/uvx/docker,直接调真 probe 会全 FAIL)。生产用默认 doProbeMCPServer。
var probeMCPFunc = doProbeMCPServer

// doProbeMCPServer 起一个 mcp 子进程,跑完整的 stdio JSON-RPC 握手:
//
//  1. initialize 请求 → 等响应
//  2. notifications/initialized 通知
//  3. tools/list 请求 → 等响应,提取工具名
//
// timeout 覆盖整个流程。npx/uvx 冷启动可能 30-60s(首次下包),后续 cache 命中后秒级 —
// 调用方建议给 60-90s headroom。
func doProbeMCPServer(ctx context.Context, command string, args, env []string, timeout time.Duration) MCPProbeResult {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(pctx, command, args...)
	cmd.Env = env
	// 把 npx 放到独立进程组,defer 时 killProcessGroup 可一次性杀整组(unix only;
	// windows noop)。详见 self_test_mcp_probe_unix.go / _windows.go 的 platform helper。
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return MCPProbeResult{Err: fmt.Errorf("stdin pipe: %w", err)}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return MCPProbeResult{Err: fmt.Errorf("stdout pipe: %w", err)}
	}
	var stderrBuf strings.Builder
	// 限 stderr 上限 16KB 防 mcp 进程刷量日志撑爆内存(实际只取末 300 字节给用户)。
	cmd.Stderr = &boundedWriter{w: &stderrBuf, max: 16 * 1024}

	if err := cmd.Start(); err != nil {
		return MCPProbeResult{Err: fmt.Errorf("start %s: %w", command, err)}
	}
	defer func() {
		// 杀整组而不只杀顶层:npx fork 出来的 node/npm 孙子进程一并 SIGKILL,
		// 避免孙子进程持有 stdin/stdout pipe 让 cmd.Wait() 永远等不到。
		// unix 走 syscall.Kill(-pgid, SIGKILL);windows 上 killProcessGroup 是 noop,
		// 后面的 cmd.Process.Kill() 兜底杀顶层。
		if cmd.Process != nil {
			killProcessGroup(cmd.Process.Pid)
		}
		_ = cmd.Process.Kill()
		// cmd.Wait() 仍保留 1s 上限作为最后防线:即使 setpgid + killpg 也理论上不能 100%
		// 收尾(extreme case:某进程 trap SIGKILL 或 fd 被传给系统服务)。1s 内没退出
		// 就放弃,goroutine 后台等 OS reaper(通常秒级),不阻塞 wg.Wait() 返回。
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
		}
	}()

	reader := bufio.NewReader(stdout)
	tailStderr := func() string {
		s := stderrBuf.String()
		if len(s) > 300 {
			s = s[len(s)-300:]
		}
		return s
	}

	// 1. initialize
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "tshoot-self-test", "version": "0"},
		},
	}
	if err := writeJSONLine(stdin, initReq); err != nil {
		return MCPProbeResult{Err: fmt.Errorf("send initialize: %w", err), StderrTail: tailStderr()}
	}
	initResp, err := readJSONLine(pctx, reader)
	if err != nil {
		return MCPProbeResult{Err: fmt.Errorf("read initialize resp: %w", err), StderrTail: tailStderr()}
	}
	if e, ok := initResp["error"].(map[string]any); ok {
		return MCPProbeResult{Err: fmt.Errorf("initialize error: %v", e), StderrTail: tailStderr()}
	}

	// 2. notifications/initialized
	_ = writeJSONLine(stdin, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})

	// 3. tools/list
	if err := writeJSONLine(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}); err != nil {
		return MCPProbeResult{Err: fmt.Errorf("send tools/list: %w", err), StderrTail: tailStderr()}
	}
	toolsResp, err := readJSONLine(pctx, reader)
	if err != nil {
		return MCPProbeResult{Err: fmt.Errorf("read tools/list resp: %w", err), StderrTail: tailStderr()}
	}
	if e, ok := toolsResp["error"].(map[string]any); ok {
		return MCPProbeResult{Err: fmt.Errorf("tools/list error: %v", e), StderrTail: tailStderr()}
	}

	// 提取工具名
	var tools []string
	if result, ok := toolsResp["result"].(map[string]any); ok {
		if arr, ok := result["tools"].([]any); ok {
			for _, t := range arr {
				if m, ok := t.(map[string]any); ok {
					if name, ok := m["name"].(string); ok {
						tools = append(tools, name)
					}
				}
			}
		}
	}
	return MCPProbeResult{Tools: tools}
}

func writeJSONLine(w io.Writer, msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// readJSONLine 读一行 stdout 解 JSON,期间允许 ctx 取消(timeout / 调用方主动取消)。
// MCP 子进程经常先吐协议无关的 stderr 行(npm warn / OTel banner 等),那些会被 cmd.Stderr 截走,
// 这里只读 stdout — 但 stdout 也可能混入非协议输出(部分包行为不规范),解 JSON 失败的行直接跳过。
func readJSONLine(ctx context.Context, r *bufio.Reader) (map[string]any, error) {
	type lineResult struct {
		line string
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() {
		// 单 goroutine 持续读 — 父函数返回后 stdout 被 cmd.Wait 关闭,这里 r.ReadString 会出错退出。
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- lineResult{err: err}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// 非 JSON 行(协议外噪音)跳过,只把首个 parse 成功的 JSON 推回。
			var probe map[string]any
			if json.Unmarshal([]byte(line), &probe) == nil {
				ch <- lineResult{line: line}
				return
			}
		}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(res.line), &msg); err != nil {
			return nil, fmt.Errorf("unmarshal %q: %w", res.line, err)
		}
		return msg, nil
	case <-ctx.Done():
		return nil, errors.New("timeout / canceled before mcp response")
	}
}

// boundedWriter 是 io.Writer 实现,达到 max 字节后丢弃新数据(防 mcp 子进程刷量日志撑爆内存)。
// 不报错,只静默 truncate — stderr 取末段是排障用,不是关键路径。
type boundedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.n >= b.max {
		return len(p), nil // 假装写完
	}
	remaining := b.max - b.n
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := b.w.Write(p)
	b.n += n
	return n, err
}

// probeMCPServersFromConfig 对 servers map(self_test 从 openclaw.json 反读出来的)
// **并发** 跑 probe,返每个 mcp 的 PASS/FAIL/WARN 项。
//
//   - PASS:进程起 + tools/list 返非空 → mcp 真能用
//   - WARN:进程起 + tools/list 返空 → 协议起来了但工具集为 0(罕见,可能凭据问题让 mcp 不暴露任何工具)
//   - FAIL:进程没起 / 协议崩 / timeout
//
// 跳过场景:cfg 期望 mcp 但 servers 没注册(已被 "mcp.servers 齐全" 那条检查 FAIL 覆盖)。
//
// 并发 + 有界 timeout:之前串行 11 个 npx mcp × 60s/个 = 最坏 660s,被 SelfTestAgent 120s
// total timeout 砍掉一半;现在并发跑,总耗时 ≈ max(单个) 而非 sum。timeout 给 30s:
// 缓存命中通常秒级,冷启动/网络慢时给一点余量;仍超时则降级为 WARN,避免把本机瞬时资源
// 或 npx/uvx 首次拉包误判成机器人不可用。
// add() 在多 goroutine 调要加 mutex 保护 res.Checks 切片 append。
func probeMCPServersFromConfig(ctx context.Context, servers map[string]any, add func(name, status, detail string)) {
	if len(servers) == 0 {
		return // 没注册任何 mcp(配置太精简或 install 没跑过),跳过
	}
	const probeTimeout = 30 * time.Second
	var mu sync.Mutex
	safeAdd := func(name, status, detail string) {
		mu.Lock()
		defer mu.Unlock()
		add(name, status, detail)
	}
	var wg sync.WaitGroup
	for name, spec := range servers {
		specMap, ok := spec.(map[string]any)
		if !ok {
			continue
		}
		command, _ := specMap["command"].(string)
		if command == "" {
			safeAdd("mcp probe "+name, "WARN", "spec 缺 command,跳过 probe")
			continue
		}
		var args []string
		if arr, ok := specMap["args"].([]any); ok {
			for _, a := range arr {
				if s, ok := a.(string); ok {
					args = append(args, s)
				}
			}
		}
		// env 段是 map[string]any,转 []string{"K=V"} 给 exec.Cmd.Env。
		// 继承完整 os.Environ(),再用 mcp spec env 覆盖。之前只保留 PATH/HOME,
		// 在桌面 GUI 场景会丢掉 Go/Node/Python runtime 依赖的一些环境变量,导致二进制
		// MCP 在 probe 时因环境不完整误报 EOF。凭据仍以 spec env 为准,同名覆盖父进程。
		env := os.Environ()
		if envMap, ok := specMap["env"].(map[string]any); ok {
			for k, v := range envMap {
				if s, ok := v.(string); ok {
					env = upsertEnv(env, k, s)
				}
			}
		}
		wg.Add(1)
		go func(probeName string, cmd string, ar []string, ev []string) {
			defer wg.Done()
			r := probeMCPFunc(ctx, cmd, ar, ev, probeTimeout)
			switch {
			case r.Err != nil:
				detail := fmt.Sprintf("起不来: %v", r.Err)
				if r.StderrTail != "" {
					detail += "\nstderr tail: " + r.StderrTail
				}
				if shouldWarnMCPProbeFailure(probeName, cmd, r) {
					safeAdd("mcp probe "+probeName, "WARN", detail+"\nKafka MCP 已注册,但本机当前连不上 broker 或 broker 初始化失败;不阻断机器人安装。排障时该 Kafka 工具可能不可用。")
				} else if isTransientMCPProbeStartupTimeout(r.Err) {
					safeAdd("mcp probe "+probeName, "WARN", detail+"\nMCP 已注册,但本次自检在启动/初始化阶段超时。常见原因是 npx/uvx 冷启动、依赖首次拉取或本机资源占用;不阻断机器人安装。首次使用该工具时可能仍会较慢。")
				} else {
					safeAdd("mcp probe "+probeName, "FAIL", detail)
				}
			case isCodeGraphMCP(probeName, cmd) && !containsMCPTool(r.Tools, "codegraph_explore"):
				safeAdd("mcp probe "+probeName, "FAIL", fmt.Sprintf(
					"MCP %s tool surface: expected codegraph_explore, got %s",
					probeName, strings.Join(r.Tools, ", ")))
			case len(r.Tools) == 0:
				safeAdd("mcp probe "+probeName, "WARN", "进程起了但 tools/list 返空(可能凭据被拒或上游协议变化)")
			default:
				safeAdd("mcp probe "+probeName, "PASS", fmt.Sprintf("%d 工具: %s",
					len(r.Tools), strings.Join(r.Tools, ", ")))
			}
		}(name, command, args, env)
	}
	wg.Wait()
}

func isCodeGraphMCP(name, command string) bool {
	if strings.HasSuffix(strings.ToLower(name), "-codegraph") {
		return true
	}
	// filepath.Base follows the host OS. Normalize separators first so a Windows
	// codegraph.cmd spec can also be inspected by tests/tools running on Unix.
	base := filepath.Base(strings.ReplaceAll(command, "\\", "/"))
	base = strings.ToLower(base)
	return base == "codegraph" || base == "codegraph.cmd"
}

func containsMCPTool(tools []string, expected string) bool {
	for _, tool := range tools {
		if tool == expected {
			return true
		}
	}
	return false
}

func shouldWarnMCPProbeFailure(name, command string, r MCPProbeResult) bool {
	if !isKafkaMCP(name, command) || r.Err == nil {
		return false
	}
	errText := r.Err.Error()
	if strings.Contains(errText, "start ") {
		return false
	}
	return strings.Contains(errText, "read initialize resp") ||
		strings.Contains(errText, "read tools/list resp") ||
		strings.Contains(errText, "timeout / canceled before mcp response")
}

func isTransientMCPProbeStartupTimeout(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "timeout / canceled before mcp response") ||
		strings.Contains(errText, "context deadline exceeded") ||
		strings.Contains(errText, "context canceled")
}

func isKafkaMCP(name, command string) bool {
	return strings.Contains(strings.ToLower(name), "kafka") ||
		strings.Contains(strings.ToLower(filepath.Base(command)), "kafka")
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
