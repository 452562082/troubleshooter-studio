// useOpenClawDetect —— OpenClaw 模型探测的 InitPage 子状态打包成 composable。
//
// 用户在 Step 2 勾上 openclaw target → 探测当前机器装的 OpenClaw,成功后填模型下拉
// (覆盖 hardcoded modelGroups)+ auth providers + version。失败给"选目录"按钮兜底。
//
// 持久化:openclawInstallDir 走 InitPage 的 draft(传 initialDir 进来由 saved 反填),
// 其它字段都是 detect 期内存,会话结束就丢。
import { ref } from 'vue'
import {
  detectOpenClawModels,
  isDesktop,
  openDir,
  type OpenClawModelEntry,
} from './bridge'

export function useOpenClawDetect(initialDir: string) {
  const openclawInstallDir = ref<string>(initialDir)
  const openclawDetectStatus = ref<'idle' | 'loading' | 'ok' | 'not-installed' | 'error'>('idle')
  const openclawDetectedModels = ref<OpenClawModelEntry[]>([])
  const openclawDetectError = ref<string>('')
  const openclawResolvedDir = ref<string>('') // backend 返回的实际路径(展开 ~ 后)
  const openclawVersion = ref<string>('')     // openclaw.json meta.lastTouchedVersion
  const openclawAuthProviders = ref<string[]>([]) // auth.profiles 里出现的 provider 名字

  async function runOpenClawDetect(dir: string = openclawInstallDir.value) {
    if (!isDesktop()) {
      openclawDetectStatus.value = 'error'
      openclawDetectError.value = '浏览器模式不支持探测 OpenClaw,请用桌面 app'
      return
    }
    openclawDetectStatus.value = 'loading'
    openclawDetectError.value = ''
    try {
      const r = await detectOpenClawModels(dir)
      if (r.ok) {
        openclawDetectStatus.value = 'ok'
        openclawDetectedModels.value = r.models || []
        openclawResolvedDir.value = r.install_dir || ''
        openclawVersion.value = r.version || ''
        openclawAuthProviders.value = r.auth_providers || []
      } else {
        openclawDetectStatus.value = r.installed ? 'error' : 'not-installed'
        openclawDetectError.value = r.err || '未知错误'
      }
    } catch (e: any) {
      openclawDetectStatus.value = 'error'
      openclawDetectError.value = String(e?.message || e)
    }
  }

  async function pickOpenClawInstallDir() {
    if (!isDesktop()) return
    try {
      const p = await openDir('选择 OpenClaw 安装目录(含 config.yaml / gateway/ 等)')
      if (!p) return
      openclawInstallDir.value = p
      await runOpenClawDetect(p)
    } catch (e: any) {
      openclawDetectError.value = String(e?.message || e)
      openclawDetectStatus.value = 'error'
    }
  }

  return {
    openclawInstallDir,
    openclawDetectStatus,
    openclawDetectedModels,
    openclawDetectError,
    openclawResolvedDir,
    openclawVersion,
    openclawAuthProviders,
    runOpenClawDetect,
    pickOpenClawInstallDir,
  }
}
