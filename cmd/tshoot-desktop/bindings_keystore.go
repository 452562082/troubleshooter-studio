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

// ── Provider 级 API key ────────────────────────────────────────────
//
// 为什么单独加一套:以前 key 只按 botPath 存,每个 bot 独立一把。但 Init wizard
// 里用户还没部署,没 botPath —— 又需要提前填 key 否则部署完 chat 用不了。
// 加一套 provider 级 key(service 同上,user="provider:<id>"),语义是:
//   - 一份 key 给该 provider 的所有 bot 共享(同 provider 多 bot 时省得重填)
//   - wizard 里选模型后立刻存下来,跟 bot 部署解耦
// BotsChat 读 key 时 per-bot 优先,没有再回落 provider 级。多数用户只用一家
// provider → 只用 provider key 即可;想给某个 bot 单独配(比如工作账号 vs 个人)
// → 在 chat 页单独存,会覆盖 fallback。

const providerUserPrefix = "provider:"

// ChatSaveProviderKey 按 provider 存 key。providerID 是 "anthropic"/"openai"/...
func (a *App) ChatSaveProviderKey(providerID, apiKey string) error {
	if providerID == "" {
		return fmt.Errorf("providerID 必填")
	}
	if apiKey == "" {
		return fmt.Errorf("apiKey 不能空;想清除请用 ChatDeleteProviderKey")
	}
	return keyring.Set(keyringService, providerUserPrefix+providerID, apiKey)
}

// ChatLoadProviderKey 读 provider 级 key。跟 ChatLoadKey 同一形状,UI 统一处理。
func (a *App) ChatLoadProviderKey(providerID string) *ChatLoadKeyResult {
	if providerID == "" {
		return &ChatLoadKeyResult{Err: "providerID 必填"}
	}
	k, err := keyring.Get(keyringService, providerUserPrefix+providerID)
	if err != nil {
		return &ChatLoadKeyResult{OK: false, Err: err.Error()}
	}
	return &ChatLoadKeyResult{APIKey: k, OK: true}
}

// ChatDeleteProviderKey 删 provider 级 key(幂等)。
func (a *App) ChatDeleteProviderKey(providerID string) error {
	if providerID == "" {
		return fmt.Errorf("providerID 必填")
	}
	if err := keyring.Delete(keyringService, providerUserPrefix+providerID); err != nil {
		if err == keyring.ErrNotFound {
			return nil
		}
		return err
	}
	return nil
}

// ── 基础设施凭证(配置中心 / 可观测性 / 消息平台...) ──────────────────
// 存 system keychain,service="tshoot-studio-infra",user=<flat key>。
// key 命名规则建议:"<type>:<env>:<field>",例:
//
//	"nacos:dev:addr"  "nacos:dev:user"  "nacos:dev:pass"
//	"apollo:prod:meta"  "apollo:prod:token"
//	"consul:dev:host"   "consul:dev:token"
//
// 跟 install.sh read_var 变量名(CC_ADDR_DEV / APOLLO_META_DEV ...)对应,
// 部署前把 keychain 的值 export 成对应 env var,install.sh 就跳过交互直接用。
//
// 跟 ChatSave*/ProviderKey 分开 service,避免 LLM key 和 infra cred 混在一起;
// 语义上也两码事(LLM 凭 → Studio 自己直连;infra 凭 → 喂给 openclaw install.sh)。
const infraService = "tshoot-studio-infra"

// SaveInfraCred 存一条凭证。value 为空等同 Delete(方便 UI"置空保存"= 清除)。
func (a *App) SaveInfraCred(key, value string) error {
	if key == "" {
		return fmt.Errorf("key 必填")
	}
	if value == "" {
		return a.DeleteInfraCred(key)
	}
	return keyring.Set(infraService, key, value)
}

// LoadInfraCred 读一条凭证。没存过 / 读失败 ok=false(不当 error 返,UI 按"未填"处理)。
func (a *App) LoadInfraCred(key string) *ChatLoadKeyResult {
	if key == "" {
		return &ChatLoadKeyResult{Err: "key 必填"}
	}
	k, err := keyring.Get(infraService, key)
	if err != nil {
		return &ChatLoadKeyResult{OK: false, Err: err.Error()}
	}
	return &ChatLoadKeyResult{APIKey: k, OK: true}
}

// DeleteInfraCred 幂等删;没存过也返 nil。
func (a *App) DeleteInfraCred(key string) error {
	if key == "" {
		return fmt.Errorf("key 必填")
	}
	if err := keyring.Delete(infraService, key); err != nil {
		if err == keyring.ErrNotFound {
			return nil
		}
		return err
	}
	return nil
}

// InfraCredBatchInput 给前端"一次保存一组"用;避免 UI 循环 N 次 RPC。
type InfraCredBatchInput struct {
	Entries map[string]string `json:"entries"` // key → value;value 空串 = 删除
}

// SaveInfraCredBatch 批量存/删;任一失败立即返 error,前面成功的不回滚。
func (a *App) SaveInfraCredBatch(in InfraCredBatchInput) error {
	for k, v := range in.Entries {
		if err := a.SaveInfraCred(k, v); err != nil {
			return fmt.Errorf("save %s: %w", k, err)
		}
	}
	return nil
}
