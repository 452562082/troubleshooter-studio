// Package standalone 把"装好的 standalone target 机器人"托管到桌面 app 里跑。
//
// 产物目录里已经有:
//   - .venv/bin/python3(由 install.sh 建好的 venv)
//   - server.py(Flask, 读 os.environ["PORT"] / os.environ["LLM_API_KEY"])
//   - system-prompt.md / index.html(server.py 内部加载)
//
// Runner 负责:
//   - 找一个空闲 TCP 端口,export PORT=<free> 给 server.py
//   - 用 exec.CommandContext 起 .venv/bin/python3 server.py
//   - 读 stdout 等到 Flask 打印 " * Running on http://..." 视作就绪,回调 onReady
//   - pid + 进程组设置(跟 deploy 包同风格),ctx cancel 时 SIGKILL pgid
//
// 不做:
//   - LLM_API_KEY 的获取 —— 留给上层:os.Environ 继承 / Wails binding 带入
//   - Docker compose 路径 —— 桌面端嵌入场景只跑 venv 那条(docker 是 CLI 用户的备选)
package standalone

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Runner 是单个 standalone 机器人进程的句柄。
// 每个已装 standalone 机器人最多对应一个 Runner(跑多份浪费 venv)。
type Runner struct {
	Path string // 机器人产物目录(含 .venv / server.py)
	Port int    // 分配到的端口(Start 成功后才有值)

	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{} // Wait 返回后 close,用于 Status 判断存活

	mu      sync.Mutex
	stdout  strings.Builder // 合并 stdout+stderr,Status 可以拿
	lastErr error           // 进程意外退出时的 Wait err
}

// Status 是 UI 侧可读的状态快照。
type Status struct {
	Running bool   `json:"running"`
	Port    int    `json:"port,omitempty"`
	PID     int    `json:"pid,omitempty"`
	LastErr string `json:"last_err,omitempty"` // 非 nil 时说明上次跑挂了
}

// Start 选一个空闲端口,起 python3 server.py,阻塞等 "Running on" 信号 或超时。
// 成功返回 Runner(已在后台跑),调用方要保留它以便后续 Stop。
//
// extraEnv 里通常放 LLM_API_KEY=xxx 之类;不要放 PORT,会被本函数覆盖。
// readyTimeout 一般 15s 够(冷启动 import anthropic + flask 要几秒)。
func Start(parentCtx context.Context, path string, extraEnv []string, readyTimeout time.Duration) (*Runner, error) {
	pyBin := filepath.Join(path, ".venv", "bin", "python3")
	if _, err := os.Stat(pyBin); err != nil {
		return nil, fmt.Errorf("venv python 不在: %s —— 先跑 bash install.sh 建 venv", pyBin)
	}
	serverPy := filepath.Join(path, "server.py")
	if _, err := os.Stat(serverPy); err != nil {
		return nil, fmt.Errorf("server.py 不在 %s", path)
	}

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("找空闲端口失败: %w", err)
	}

	ctx, cancel := context.WithCancel(parentCtx)
	cmd := exec.CommandContext(ctx, pyBin, "server.py")
	cmd.Dir = path
	// 父进程环境 + PORT + 调用方带的 extraEnv(LLM_API_KEY 等)
	env := append([]string{}, os.Environ()...)
	env = append(env, fmt.Sprintf("PORT=%d", port))
	env = append(env, extraEnv...)
	cmd.Env = env
	setProcessGroup(cmd)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 python3 server.py: %w", err)
	}

	r := &Runner{
		Path:   path,
		Port:   port,
		cmd:    cmd,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// 后台 goroutine:扫 stdout,通知 ready,等进程退出
	readyCh := make(chan struct{}, 1)
	go r.scanOutput(pr, readyCh)
	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		close(r.done)
	}()

	// 阻塞等就绪,或超时 / 进程已退出
	select {
	case <-readyCh:
		return r, nil
	case <-r.done:
		// 进程没到 ready 就挂了,把 stdout 里的报错抛出去
		r.mu.Lock()
		tail := tailLines(r.stdout.String(), 10)
		r.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("server.py 启动失败 (exit: %v); 最后 10 行输出:\n%s", r.lastErr, tail)
	case <-time.After(readyTimeout):
		cancel()
		<-r.done
		r.mu.Lock()
		tail := tailLines(r.stdout.String(), 10)
		r.mu.Unlock()
		return nil, fmt.Errorf("server.py 在 %v 内没就绪; 最后 10 行输出:\n%s", readyTimeout, tail)
	}
}

// scanOutput 逐行扫 server.py 合并输出,匹配 Flask 就绪打印时 push 到 readyCh。
// Flask 默认启动日志里有 " * Running on http://0.0.0.0:3000" 这样的行。
// 同时把所有行追加到 r.stdout,用于 Status / 诊断。
func (r *Runner) scanOutput(pr io.Reader, readyCh chan<- struct{}) {
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	readySent := false
	for sc.Scan() {
		line := sc.Text()
		r.mu.Lock()
		r.stdout.WriteString(line)
		r.stdout.WriteByte('\n')
		r.mu.Unlock()
		// 匹配两种可能的就绪标记:server.py 自己 print 的"启动:http://..."
		// 和 Flask werkzeug 自动打印的" * Running on ..."
		if !readySent && (strings.Contains(line, "Running on") || strings.Contains(line, "启动：http://")) {
			readyCh <- struct{}{}
			readySent = true
		}
	}
}

// Stop 给进程组发 SIGKILL。已经停了的 Runner 调 Stop 是幂等的。
func (r *Runner) Stop() {
	r.cancel()              // ctx cancel -> CommandContext 发信号
	killProcessGroup(r.cmd) // 兜底:直接 kill pgid
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
		// Wait goroutine 没在 3s 内退出 —— 极端情况,留个日志就算了
	}
}

// Status 返回当前运行状态的快照。
func (r *Runner) Status() Status {
	s := Status{Port: r.Port}
	select {
	case <-r.done:
		// 已退出
		r.mu.Lock()
		if r.lastErr != nil {
			s.LastErr = r.lastErr.Error()
		}
		r.mu.Unlock()
		s.Running = false
	default:
		s.Running = true
		if r.cmd.Process != nil {
			s.PID = r.cmd.Process.Pid
		}
	}
	return s
}

// freePort 让 OS 分配一个临时端口(:0),然后关闭 listener 把端口"还"给 OS。
// 从 listener 关闭到 python 开始监听有竞争窗口(理论上几十 ms),但并发创建
// 桌面端不会有第二个同时抢,接受这个风险换简单。
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// tailLines 返回 s 最后 n 行(不含空行).用于错误信息里摘要 stdout。
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
