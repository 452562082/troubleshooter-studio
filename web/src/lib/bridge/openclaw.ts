// bridge/openclaw.ts —— OpenClaw 模型探测。
// 读本机 ~/.openclaw/openclaw.json(真实 schema,从真实 install 反推),
// 把 agents.defaults.model.primary / agents.defaults.models / agents.list[].model
// 聚合成"可用模型"清单给向导 OpenClaw 卡用。
//
// 三种状态:
//   installed=false      → 没装(openclaw.json 缺失),让用户装完再来 / 手选目录
//   installed_but_empty  → openclaw.json 存在但无任何 model 字段,让用户先装个 agent
//   ok + models          → 正常展示下拉

import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from '../bridge'

export interface OpenClawModelEntry {
  id: string               // "openai-codex/gpt-5.4"
  provider?: string        // "openai-codex"
  label?: string           // id 或 "id (默认)"
  source?: string          // 来自 openclaw.json 哪个字段
  primary?: boolean        // 是否 defaults.model.primary
}
export interface OpenClawDetectResult {
  ok: boolean
  installed: boolean
  installed_but_empty?: boolean
  install_dir?: string
  config_path?: string
  version?: string
  models?: OpenClawModelEntry[]
  auth_providers?: string[]
  err?: string
}
/** 探测 installDir 下的 OpenClaw 配置;installDir 为空 = 用 ~/.openclaw 默认路径 */
export async function detectOpenClawModels(installDir: string): Promise<OpenClawDetectResult> {
  if (!isDesktop()) return { ok: false, installed: false, err: '浏览器模式不支持' }
  return App.DetectOpenClawModels(installDir)
}
