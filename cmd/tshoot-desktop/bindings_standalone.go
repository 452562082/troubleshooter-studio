// bindings_standalone.go —— 把 standalone target 机器人嵌进桌面 app 跑的 binding:
// StartStandalone / StopStandalone / StandaloneStatus / stopAllStandalones。
//
// 产品需求:现在 standalone 产物部署完是独立的(flask + 浏览器打开 localhost),
// 用户要切出去用很割裂。这里让桌面端托管 server.py 进程,前端用 iframe
// 指向 localhost:<port> 把对话嵌进工作台 —— 用户不离开 Studio 就能跟机器人聊。
//
// LLM_API_KEY 的获取优先级:
//  1. 前端 apiKey 参数(UI 输入,优先级最高)
//  2. Studio 启动时进程自己的 LLM_API_KEY 环境变量
//  3. 都没有 → 返回错误,UI 弹输入框
//
// 跟 RunInstall 的取消机制共用同款 process-group kill 风格(见
// internal/standalone/process_unix.go),兜死 brew/npm/python 子孙进程。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/standalone"
)

// StartStandaloneResult 返回给前端的启动结果,成功后 UI 用 Port 构造 iframe URL。
type StartStandaloneResult struct {
	Port int `json:"port"`
	PID  int `json:"pid"`
}

// StartStandalone 起一个 standalone 机器人的 server.py,返回端口。
//
// path 是机器人产物目录(含 .venv / server.py,由 discover.Scan 提供)。
// apiKey 是 LLM_API_KEY;空串时 fallback 到 Studio 进程自己的环境变量;
// 仍没有则返回清晰错误,UI 要引导用户填。
//
// 同一个 path 已经有 runner 在跑时幂等 —— 直接返回现有 port,不起第二份。
func (a *App) StartStandalone(path string, apiKey string) (*StartStandaloneResult, error) {
	if path == "" {
		return nil, fmt.Errorf("path 必填")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	a.standaloneMu.Lock()
	if r, ok := a.standaloneRunners[absPath]; ok {
		// 已经在跑 —— 检查进程还活着再返回
		st := r.Status()
		if st.Running {
			a.standaloneMu.Unlock()
			return &StartStandaloneResult{Port: r.Port, PID: st.PID}, nil
		}
		// 挂了(用户 Stop 过或进程自己退了),清掉再重启
		delete(a.standaloneRunners, absPath)
	}
	a.standaloneMu.Unlock()

	// 定 LLM_API_KEY —— 前端传的优先,没传就看 Studio 自己的 env
	if apiKey == "" {
		apiKey = os.Getenv("LLM_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY 未设置:请在启动对话时填入,或在启动 Studio 前 `export LLM_API_KEY=xxx`")
	}

	r, err := standalone.Start(a.ctx, absPath,
		[]string{"LLM_API_KEY=" + apiKey},
		15*time.Second, // 冷启动 import anthropic+flask 要几秒,15s 留余
	)
	if err != nil {
		return nil, err
	}

	a.standaloneMu.Lock()
	a.standaloneRunners[absPath] = r
	a.standaloneMu.Unlock()

	return &StartStandaloneResult{Port: r.Port, PID: r.Status().PID}, nil
}

// StopStandalone 停掉 path 对应的 runner。没在跑时静默返回 false(前端忽略)。
func (a *App) StopStandalone(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	a.standaloneMu.Lock()
	r, ok := a.standaloneRunners[absPath]
	if ok {
		delete(a.standaloneRunners, absPath)
	}
	a.standaloneMu.Unlock()
	if !ok {
		return false
	}
	r.Stop()
	return true
}

// StandaloneStatus 返回某个 path 的状态。没 runner 的返回 Running=false。
// UI 用这个画"运行中/已停止"徽章,并在进入 chat 页时探活。
func (a *App) StandaloneStatus(path string) standalone.Status {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return standalone.Status{}
	}
	a.standaloneMu.Lock()
	r, ok := a.standaloneRunners[absPath]
	a.standaloneMu.Unlock()
	if !ok {
		return standalone.Status{}
	}
	return r.Status()
}

// stopAllStandalones app 退出时由 main defer 调,把所有托管进程清掉。
// 并发扫 map 就 OK:Stop 内部已经 serialize。
func (a *App) stopAllStandalones() {
	a.standaloneMu.Lock()
	runners := make([]*standalone.Runner, 0, len(a.standaloneRunners))
	for _, r := range a.standaloneRunners {
		runners = append(runners, r)
	}
	a.standaloneRunners = map[string]*standalone.Runner{}
	a.standaloneMu.Unlock()
	for _, r := range runners {
		r.Stop()
	}
}
