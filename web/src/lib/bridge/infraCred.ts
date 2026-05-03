// bridge/infraCred.ts —— 钥匙串里的 Infra 凭证读写(配置中心 / 可观测性 / 消息平台...)。
// key 格式建议 "<type>:<env>:<field>",例 "nacos:dev:addr"。部署前端从钥匙串读,
// 映射成 install.sh 认的 env var(CC_ADDR_DEV 等),install.sh read_var 跳过交互。
import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

export interface InfraCredLoadResult {
  api_key: string
  ok: boolean
  err?: string
}

export async function saveInfraCred(key: string, value: string): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCred 只在桌面 app 可用')
  await App.SaveInfraCred(key, value)
}

export async function loadInfraCred(key: string): Promise<InfraCredLoadResult> {
  if (!isDesktop()) return { api_key: '', ok: false }
  return App.LoadInfraCred(key)
}

export async function deleteInfraCred(key: string): Promise<void> {
  if (!isDesktop()) return
  await App.DeleteInfraCred(key)
}

/** 批量保存/删(一次 RPC),value 为空串 = 删 */
export async function saveInfraCredBatch(entries: Record<string, string>): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCredBatch 只在桌面 app 可用')
  await App.SaveInfraCredBatch({ entries } as { entries: Record<string, string> })
}
