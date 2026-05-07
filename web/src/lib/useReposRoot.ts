// useReposRoot —— 仓库 clone 父目录管理(全局默认 + 当前会话覆写 + 主目录折叠)。
//
// 三个 ref:
//   - reposRootInput        本会话用户在输入框看到的值;不写 yaml 也不进 draft,
//                           "研制环境偏好"性质 —— 写进 troubleshooter.yaml 会污染跨机器分享
//   - globalDefaultReposRoot ~/.tshoot/config.json 里持久的默认值,空表示用户没设过
//   - resolvedReposRoot     永远非空(回落 ~/.tshoot/repos),给 placeholder + 实际 clone 目标用
//   - homeDir               $HOME,用于 displayPath 折叠成 ~/...;拿不到原样展示
//
// 持久化唯一路径:saveAsGlobalDefault → setDefaultReposRoot → Go binding → userconfig.Save。
// 导入 yaml / 清空草稿都不动它(刻意不进 draft)。

import { onMounted, ref } from 'vue'
import { getUserConfig, isDesktop, openDir, setDefaultReposRoot } from './bridge'
import { toast } from './toast'

export function useReposRoot() {
  const reposRootInput = ref('')
  const globalDefaultReposRoot = ref('') // 用户设过的,可能空
  const resolvedReposRoot = ref('~/.tshoot/repos') // 永远非空;load 后会覆盖
  const homeDir = ref('')

  onMounted(async () => {
    try {
      const r = await getUserConfig()
      globalDefaultReposRoot.value = r.default_repos_root
      homeDir.value = r.home_dir || ''
      if (r.resolved_repos_root) resolvedReposRoot.value = r.resolved_repos_root
      // 本会话没人改过 reposRootInput(还是空)的话,拿它填一下方便扫描
      if (!reposRootInput.value && r.resolved_repos_root) {
        reposRootInput.value = r.resolved_repos_root
      }
    } catch { /* 读不到 config.json 不打扰用户 */ }
  })

  // displayPath: 把绝对路径前缀 $HOME 折成 ~,仅用于 UI 展示 placeholder / hint。
  // 实际存盘 / 传给后端的路径保持绝对路径不变(git clone / Go os.Stat 不识别 ~)。
  // homeDir 拿不到时直接原样返回,不影响可用性。
  function displayPath(abs: string): string {
    if (!abs) return ''
    const h = homeDir.value
    if (h && abs === h) return '~'
    if (h && abs.startsWith(h + '/')) return '~' + abs.slice(h.length)
    return abs
  }

  async function saveAsGlobalDefault() {
    if (!reposRootInput.value.trim()) {
      toast.error('先填路径再设默认')
      return
    }
    try {
      await setDefaultReposRoot(reposRootInput.value.trim())
      globalDefaultReposRoot.value = reposRootInput.value.trim()
      resolvedReposRoot.value = reposRootInput.value.trim()
      toast.success(`已设为全局默认 clone 目录,下次打开 Studio 自动用这里`)
    } catch (e: any) {
      toast.error(`保存失败: ${String(e?.message || e)}`)
    }
  }

  async function pickReposRoot() {
    if (!isDesktop()) {
      toast.error('选目录需要桌面 app 环境;浏览器模式请手动输入路径')
      return
    }
    try {
      const p = await openDir('选择仓库根目录(含各个 repo.name 子目录)')
      if (p) reposRootInput.value = p
    } catch (e: any) {
      toast.error(String(e?.message || e))
    }
  }

  return {
    reposRootInput,
    globalDefaultReposRoot,
    resolvedReposRoot,
    homeDir,
    displayPath,
    saveAsGlobalDefault,
    pickReposRoot,
  }
}
