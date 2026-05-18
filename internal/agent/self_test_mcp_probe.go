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
//   - 不强制工具名清单跟 SKILL.md 文档对照 —— 那是 doc 漂移检测,做 driftcheck 单独工具更合适
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
		_ = cmd.Process.Kill()
		// cmd.Wait() 加 1s 上限:npx/uvx 进程在 cold install 网络下载阶段被 SIGKILL,
		// 自身退出,但内部 fork 的 node/npm 孙子进程不会一起死,孙子进程持有的 stdin/stdout
		// pipe 让父 cmd.Wait() 永远等不到 — 这是父函数 return 后 wg.Done 触发不了的根因,
		// 进而让 wg.Wait() 卡满 SelfTestAgent 的 120s total timeout。
		//
		// 1s 不收尾就放弃 wait,接受 zombie 孙子进程(OS reaper 后续会清理)。代价是短期
		// fd 泄漏,但 self-test 单次跑 17 个 mcp probe 后 .app 继续跑,GC + OS 兜底足够。
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
// 并发 + 短 timeout:之前串行 11 个 npx mcp × 60s/个 = 最坏 660s,被 SelfTestAgent 120s
// total timeout 砍掉一半;现在并发跑,总耗时 ≈ max(单个) 而非 sum。timeout 15s 接受
// "首次冷启动起不来就算 FAIL",cache 命中本来秒级就过 — 比"卡 660s 才知道结果"好得多。
// add() 在多 goroutine 调要加 mutex 保护 res.Checks 切片 append。
func probeMCPServersFromConfig(ctx context.Context, servers map[string]any, add func(name, status, detail string)) {
	if len(servers) == 0 {
		return // 没注册任何 mcp(配置太精简或 install 没跑过),跳过
	}
	const probeTimeout = 15 * time.Second
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
		// 不继承 os.Environ() — mcp 进程应该跑在 install 配的隔离环境里(凭据走 spec env 段),
		// 但 PATH / HOME 必须保留:PATH 让 npx/uvx 二进制找得到,HOME 让 npm/uv cache 命中
		// (~/.npm/_npx / ~/.cache/uv),否则冷启动每次都重下包。
		var env []string
		for _, k := range []string{"PATH", "HOME"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
		if envMap, ok := specMap["env"].(map[string]any); ok {
			for k, v := range envMap {
				if s, ok := v.(string); ok {
					env = append(env, k+"="+s)
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
				safeAdd("mcp probe "+probeName, "FAIL", detail)
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
