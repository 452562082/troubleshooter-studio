// useDeployPath:把"选 target + destPath"的交互差异抽成一个 composable,
// 三处部署 UI(InitPage Step 7 / EditorPage 一键部署 / BotsPage 导入部署)共用。
//
// 核心逻辑:
//   - openclaw:Studio 替用户管路径,默认塞 ~/.tshoot/openclaw/<id>/
//     UI 显示"将部署到 <path>"+ 折叠"自定义"入口;用户点自定义才露 input
//   - claude-code / cursor:用户必填(装到某个项目根),UI 保持 input 可见
//
// composable 接收 3 个 ref:
//   target      — 当前选的 target(v-model 绑的)
//   systemId    — yaml 里 system.id(不同页数据源不同,传进来就好)
//   destPath    — 外部维护的 path 值(v-model 绑的),composable 会按 target 自动填/清
//
// 返回一组 reactive:
//   isManagedTarget      — true = openclaw(Studio 管路径)
//   customPathExpanded   — 用户是否展开了"自定义"; openclaw 下有意义
//   autoDefaultPath      — 当前 target+id 算出的默认路径(纯展示)
//   resetCustomPath      — 折回默认值(展开时的"用默认"按钮调)

import { computed, ref, watch, type MaybeRefOrGetter, type Ref } from 'vue'
import { defaultDestPath } from './bridge'

export function useDeployPath(
  target: Ref<string>,
  systemIdSource: MaybeRefOrGetter<string>,
  destPath: Ref<string>,
) {
  // 全部 3 个 target 都改成 Studio 托管:
  //   openclaw   → ~/.tshoot/openclaw/<id>/(中间包,后续 install 装到 ~/.openclaw/workspace)
  //   claude-code → ~/.tshoot/claude-code/<id>/(中间包,install.sh 装到 ~/.claude/agents/<name>)
  //   cursor     → ~/.tshoot/cursor/<id>/(中间包,install.sh 装到 ~/.cursor/agents/<name>)
  const isManagedTarget = computed(
    () => target.value === 'openclaw' || target.value === 'claude-code' || target.value === 'cursor',
  )
  const customPathExpanded = ref(false)
  const autoDefaultPath = ref('')

  async function recompute() {
    const id = typeof systemIdSource === 'function'
      ? systemIdSource()
      : (systemIdSource as Ref<string>).value
    try {
      autoDefaultPath.value = await defaultDestPath(target.value, id || '')
    } catch {
      // home dir 读不到或其它,退回空,UI 会 fallback 到 input 路径
      autoDefaultPath.value = ''
    }
    if (isManagedTarget.value && !customPathExpanded.value) {
      destPath.value = autoDefaultPath.value
    } else if (!isManagedTarget.value && destPath.value === autoDefaultPath.value) {
      // 切到 claude-code/cursor 且 destPath 还是上一次的默认值(没被用户改过),
      // 清掉让 input placeholder 提示用户选项目根
      destPath.value = ''
    }
  }

  watch(
    [target, () => (typeof systemIdSource === 'function' ? systemIdSource() : (systemIdSource as Ref<string>).value)],
    recompute,
    { immediate: true },
  )

  watch(customPathExpanded, (expanded) => {
    if (!expanded && isManagedTarget.value) {
      // 折回默认:把 destPath 恢复
      destPath.value = autoDefaultPath.value
    }
  })

  function resetCustomPath() {
    customPathExpanded.value = false
    destPath.value = autoDefaultPath.value
  }

  return {
    isManagedTarget,
    customPathExpanded,
    autoDefaultPath,
    resetCustomPath,
  }
}
