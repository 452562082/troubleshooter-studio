// bindings_keystore.go —— 把 LLM API key 存到系统 Keychain,持久化跨 app 重启。
//
// 之前 BotsChat 的 key 只活在 window 全局变量里,app 关就没,用户每次打开对话
// 都得重填。现在用 go-keyring 封装的系统钥匙串:
//   - macOS: Security.framework / Keychain(第一次访问时弹"允许访问"对话框)
//   - Linux: libsecret(需要 gnome-keyring 或 KeePassXC secretservice 跑着)
//   - Windows: Credential Manager
//
// 标识策略:service = "tshoot-studio", user = botPath(每台机器人一把)。
// 不用 provider id 当 user,是因为:不同 bot 可能都用 anthropic 但 key 不同
// (比如公司 key + 个人 key 并存),按 bot 区分最省事。
//
// Linux 上 libsecret 没装会直接报错;UI 收到就会 fallback 到"会话级",
// 用户不用感知这个兼容层。
package main

import (
	"fmt"

	"github.com/zalando/go-keyring"
)

const keyringService = "tshoot-studio-chat"

// ChatSaveKey 把 apiKey 存到系统钥匙串,service=tshoot-studio-chat, user=<botPath>。
// 同一 botPath 重复 set 直接覆盖,不用先 delete。
func (a *App) ChatSaveKey(botPath, apiKey string) error {
	if botPath == "" {
		return fmt.Errorf("botPath 必填")
	}
	if apiKey == "" {
		return fmt.Errorf("apiKey 不能空;想清除请用 ChatDeleteKey")
	}
	return keyring.Set(keyringService, botPath, apiKey)
}

// ChatLoadKeyResult UI 用的返回:ok=false 表示 keychain 里没存过或读取失败,
// UI 据此决定要不要弹 key 表单。不把 bool 塞进 error 是因为"没存过"不是错误,
// 是正常流程的一部分。
type ChatLoadKeyResult struct {
	APIKey string `json:"api_key"`
	OK     bool   `json:"ok"`
	// 读取失败时的原因(比如 Linux 没装 libsecret),UI 可以提示
	// "无法访问系统钥匙串,会话级降级 —— 每次打开都要重填"
	Err string `json:"err,omitempty"`
}

// ChatLoadKey 从钥匙串读。用户从没 set 过 / keychain 被拒绝访问 /
// Linux 缺 libsecret 等,都会返回 ok=false,前端据此弹 key 表单。
func (a *App) ChatLoadKey(botPath string) *ChatLoadKeyResult {
	if botPath == "" {
		return &ChatLoadKeyResult{Err: "botPath 必填"}
	}
	k, err := keyring.Get(keyringService, botPath)
	if err != nil {
		// keyring.ErrNotFound 是正常"没存过";其它是真出错
		// 两种都走 ok=false,前端统一按"没保存"处理即可,err 字段留给"想看细节"的场景
		return &ChatLoadKeyResult{OK: false, Err: err.Error()}
	}
	return &ChatLoadKeyResult{APIKey: k, OK: true}
}

// ChatDeleteKey 用户点"重置 API key"时调;没保存过也幂等返回 nil。
func (a *App) ChatDeleteKey(botPath string) error {
	if botPath == "" {
		return fmt.Errorf("botPath 必填")
	}
	if err := keyring.Delete(keyringService, botPath); err != nil {
		// keyring.ErrNotFound = 本来就没存过,当成功
		if err == keyring.ErrNotFound {
			return nil
		}
		return err
	}
	return nil
}
