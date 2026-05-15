// install_native_mcp_messaging.go —— messaging(lark)+ project tracking(feishu_project)
// 两家 MCP 的 builder。2026-05-15 从 install_native_mcp_common.go 拆出。
package agent

import (
	"fmt"
	"os"
)

// buildLark messaging:lark — 上游正式包名是 @larksuiteoapi/lark-mcp(注意 oapi 不是 suite),
// 且 binary 是 commander 多子命令,启动 mcp server 必须显式 `mcp` 子命令(没有就只是
// CLI 工具 exit)。env: process.env.APP_ID / APP_SECRET(在 dist/utils/constants.js
// 里写死,不接 LARK_APP_* 前缀)。
//
// `-t preset.im.default` 把工具集从默认 19 个(preset.default = IM + Bitable + Doc +
// Contact 全套)缩到 5 个(IM 群消息相关)。排障机器人对飞书的真实需求是"发故障快报
// 到群" + 偶尔查群信息,IM 子集足够。19 → 5 工具:启动快、Claude /mcp 面板列表清爽、
// LLM tools[] context 也轻不少(每个工具一份 zod-to-json-schema 描述都不便宜)。
//
// LARK_DOMAIN env(可选):海外用户填 https://open.larksuite.com,留空 → lark-mcp
// 走默认 https://open.feishu.cn(国内飞书 endpoint)。lark-mcp 源码
// `package/dist/utils/constants.js:29` 读 process.env.LARK_DOMAIN,我们透传即可。
func (b *mcpBuilder) buildLark(servers map[string]any) {
	for _, m := range b.cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			servers[b.keyFixed("lark-openapi")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuiteoapi/lark-mcp", "mcp", "-t", "preset.im.default"},
				"env": b.envBlock(map[string]any{
					"APP_ID":      b.get("LARK_APP_ID"),
					"APP_SECRET":  b.get("LARK_APP_SECRET"),
					"LARK_DOMAIN": b.get("LARK_DOMAIN"),
				}),
			}
			return
		}
	}
}

// buildFeishuProject project tracking:feishu_project
//
// 2026-05-15 审计后**暂时禁用 mcp 注册**(yaml schema / wizard / prompts 凭据收集都不动,
// 留作字节发正式版时重启用一行翻开)。理由:
//
// 上游包 @lark-project/mcp v0.0.1 验真后判定为字节内部 prototype-grade:
//   - npm 元数据 repository / homepage / readme **全空**(`@bytedance.com` 个人邮箱 maintainer)
//   - 主版本号 0.0.x → 稳定性 / 工具集 / 协议都可能未来 break
//   - 架构上是 stdio→HTTPS 透明代理(转发到 https://project.feishu.cn/mcp_server/v1),
//     工具集**完全由飞书服务端控制**,我们这边无法保证;字节运维改了我们这边自动跟着变
//   - MCP_USER_TOKEN 经 `X-Mcp-Token` header 传 — 从命名看是 user-scoped token,
//     **飞书规范 user_access_token 默认 2h 过期**,bake 进 mcp env 就是 nacos 同款失效坑
//
// 等条件:字节发 v1.x 正式版(有 README + 公开 repo + 长期 token 续期机制)后,把下面 if 分支
// 的 `_ = ...` 替换回真注册代码即可。当前 install 时打 warn 让用户知道为啥没用 mcp。
func (b *mcpBuilder) buildFeishuProject(servers map[string]any) {
	for _, p := range b.cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			fmt.Fprintf(os.Stderr, "[warn] feishu_project mcp 暂未启用注册\n")
			fmt.Fprintf(os.Stderr, "        理由:@lark-project/mcp v0.0.1 是字节内部 prototype(repo/readme 空),\n")
			fmt.Fprintf(os.Stderr, "             MCP_USER_TOKEN 疑似 2h 过期 user token,bake 后会失效\n")
			fmt.Fprintf(os.Stderr, "        现状(2026-05-15 B 方案):yaml 仍合法,但 install_prompts 暂停收 MCP_USER_TOKEN,\n")
			fmt.Fprintf(os.Stderr, "             SKILL / Python OpenAPI 脚本都未补 — 项目跟踪能力当前不可用\n")
			fmt.Fprintf(os.Stderr, "        等条件:字节发 v1.x 正式版 + 我们补完 SKILL 后重启用 — 详见 install_native_mcp_messaging.go::buildFeishuProject 注释\n")
			_ = servers // 占位:重启用时改回 servers[b.keyFixed("FeishuProjectMcp")] = ...
			return
		}
	}
}
