<script setup lang="ts">
// ConfigSourceStep —— Step 6 配置源整段(顶部多选 + 主源 per-env 表单 + 副源面板 + help text)。
// 从 InitPage 抽出来,InitPage 那边只需 <ConfigSourceStep ... /> 一行。
//
// 因为这步的状态/helper 散在 InitPage 各处,prop 数量较多;props 形态对齐 InitPage 现有
// reactive object / closure helper,不重新设计签名,以最小迁移成本完成抽离。
//
// 内部继续复用更细的子组件:CredentialField / PreloadStatusRow / ServiceChecklist /
// NamespaceServiceMap / KuboardServiceMap / SecondarySourcePanel / CredsShareWarning。

import { inject } from 'vue'
import type { CredField } from '../lib/credFields'
import type { KuboardSvcLocator } from '../lib/yamlGenerator'
import type { CCHubEntry, CCHubNamespace } from '../lib/bridge'
import { WizardStoreKey } from '../lib/wizardStore'
import CredentialField from './CredentialField.vue'
import PreloadStatusRow from './PreloadStatusRow.vue'
import ServiceChecklist from './ServiceChecklist.vue'
import NamespaceServiceMap from './NamespaceServiceMap.vue'
import KuboardServiceMap from './KuboardServiceMap.vue'
import SecondarySourcePanel from './SecondarySourcePanel.vue'
import CredsShareWarning from './CredsShareWarning.vue'

interface SourceCredsEntry { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }
interface CCHubEnvState {
  status: 'idle' | 'loading' | 'ok' | 'error'
  entries?: CCHubEntry[]
  namespaces?: CCHubNamespace[]
}

// 通用 reactive + helper 走 inject(避免每个 prop 单独透传)
const wizard = inject(WizardStoreKey)!

defineProps<{
  // Step 5 专属
  configTypeOptions: string[]
  configTypeDescriptions: Record<string, string>
  enabledSourceTypes: Record<string, boolean>
  activeSourceTypes: string[]
  isMultiSource: boolean
  configCenterType: string
  ccFieldsByType: Record<string, CredField[]>

  // Step 5 专属:凭证 / 状态 reactive map
  ccCredInputs: Record<string, string>
  sourceCreds: Record<string, SourceCredsEntry>
  ccHubStateByEnv: Record<string, CCHubEnvState | undefined>
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  kuboardSvcMap: Record<string, KuboardSvcLocator>

  // Step 5 专属 helper
  ccKeyFor: (type: string, envID: string, field: string) => string
  isFieldHidden: (t: string, envID: string, f: CredField, getSibling: (k: string) => string) => boolean
  envScanned: (envID: string) => boolean
  namespacesFor: (envID: string) => CCHubNamespace[]
  entriesForNamespace: (envID: string, ns: string) => CCHubEntry[]
  getServiceSource: (svc: string) => string
}>()

const emit = defineEmits<{
  toggleSourceType: [type: string, checked: boolean]
  updateCred: [key: string, value: string]
  clearCred: [key: string]
  runKuboardPreload: [envID: string]
  runCCHubPreload: [envID: string]
  setServiceSource: [svc: string, source: string]
  namespaceChanged: [envID: string, namespace: string]
  dataIdChanged: [envID: string, svc: string]
  setKuboardLoc: [envID: string, svc: string, field: 'cluster' | 'namespace' | 'configmap', value: string]
  preloadKuboardFromSource: [sourceType: string, envID: string]
}>()
</script>

<template>
  <div class="card lg">
    <h2>配置源</h2>

    <!-- 多源:顶部多选,勾哪些 type 就声明哪些源 -->
    <div class="form-group">
      <label>
        系统用到的配置源(可多选)
        <span class="field-hint">
          — 一种源勾一次(nacos / apollo / kuboard 等);多选会让你为每个服务挑走哪个源,单选则全员默认走它
        </span>
      </label>
      <div class="source-types-checkboxes">
        <label
          v-for="t in configTypeOptions"
          :key="t"
          class="source-type-pill"
          :class="{ active: enabledSourceTypes[t] }"
        >
          <input
            type="checkbox"
            :checked="!!enabledSourceTypes[t]"
            @change="(e) => emit('toggleSourceType', t, (e.target as HTMLInputElement).checked)"
          />
          <span class="source-type-pill-name">{{ t }}</span>
          <span class="source-type-pill-desc">{{ configTypeDescriptions[t] }}</span>
        </label>
      </div>
      <div v-if="activeSourceTypes.length === 0" class="alert warn" style="margin-top:8px;">
        至少勾选一个配置源(若系统真不用配置中心,后面 Step 6/7 也基本啥都填不了)
      </div>
      <div v-else-if="isMultiSource" class="multi-source-mgr-hint">
        🔀 多源模式:每个源独立填写下面的连接信息;Step 6/7 数据层和可观测会按服务的源路由
      </div>
    </div>

    <!-- 凭证表单:主源(activeSourceTypes[0])完整功能(连接 + 预读 + namespace + 服务 dataId 选择)。
         nacos/apollo/consul 才展;env-vars/kubernetes/none 不需要。
         副源在主源块下方独立渲染只填连接信息(预读 + namespace 下拉留给主源,副源走 yaml 手填或 CLI)。 -->
    <div v-if="ccFieldsByType[configCenterType]" class="form-group">
      <label>
        <code>{{ configCenterType }}</code> 连接配置
        <span v-if="isMultiSource" class="auto-tag" style="background:#dbeafe;color:#1e40af;">主源 · 完整 preload</span>
        <span class="field-hint">— 按环境维度填写,保存后写入 system.yaml(标 <code># ⚠ secret</code> 注释),部署时注入到目标平台的 MCP Server env</span>
      </label>
      <CredsShareWarning title="⚠ 凭证与共享提醒">
        <li>这里填的账号密码会以明文写入 <code>system.yaml</code>(每条带 <code># ⚠ secret</code> 注释),并部署时注入到机器人 MCP Server 的 env 块 + <code>~/.tshoot/&lt;agent-id&gt;-creds.json</code>。</li>
        <li>分享 yaml 请限**团队内部 / 私有仓库**,<strong>不要提交到公开代码仓库</strong>。</li>
      </CredsShareWarning>
      <div v-for="env in wizard.environments" :key="env.id" class="cc-env-block">
        <div class="cc-env-head">
          <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
          <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
        </div>
        <div class="cc-env-fields">
          <CredentialField
            v-for="f in (configCenterType === 'kuboard'
              ? ccFieldsByType[configCenterType].filter(f2 => f2.key !== 'cluster' && f2.key !== 'namespace' && f2.key !== 'configmap')
              : ccFieldsByType[configCenterType])"
            :key="f.key"
            v-show="!isFieldHidden(configCenterType, env.id, f, (k) => ccCredInputs[ccKeyFor(configCenterType, env.id, k)] || '')"
            :field="f"
            :env-i-d="env.id"
            :model-value="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)] || ''"
            :is-revealed="wizard.isRevealed(ccKeyFor(configCenterType, env.id, f.key))"
            :is-kuboard="configCenterType === 'kuboard'"
            :kuboard-state="wizard.kuboardStateByEnv[env.id]"
            :sibling-cluster-value="ccCredInputs[ccKeyFor(configCenterType, env.id, 'cluster')] || ''"
            :sibling-namespace-value="ccCredInputs[ccKeyFor(configCenterType, env.id, 'namespace')] || ''"
            @update:model-value="(v: string) => emit('updateCred', ccKeyFor(configCenterType, env.id, f.key), v)"
            @toggle-reveal="wizard.toggleReveal(ccKeyFor(configCenterType, env.id, f.key))"
            @clear="emit('clearCred', ccKeyFor(configCenterType, env.id, f.key))"
          />
        </div>

        <!-- kuboard 专属:点这个按钮拉资源,把后面 cluster/namespace/cm 三个字段从手填变下拉 -->
        <PreloadStatusRow
          v-if="configCenterType === 'kuboard'"
          :status="wizard.kuboardStateByEnv[env.id]?.status"
          idle-text="📥 从 Kuboard 读取可选项"
          ok-text="🔄 重新读取"
          :error-message="wizard.kuboardErrorOf(env.id)"
          @click="emit('runKuboardPreload', env.id)"
        >
          <template #ok>✓ {{ wizard.kuboardClusterCountOf(env.id) }} 个集群</template>
        </PreloadStatusRow>

        <!-- 服务勾选清单:勾哪些服务走当前源(主源)。多源场景下,某服务在主源勾选 = 它的
             config_source 设为主源 type;副源场景下用户去对应副源面板勾选。
             单源场景默认所有服务都走唯一源,checkbox 全勾。 -->
        <ServiceChecklist
          v-if="wizard.allServiceNames.length > 0"
          :services="wizard.allServiceNames"
          :source-i-d="configCenterType"
          :hint-html="`勾选要走 <code>${configCenterType}</code> 源的服务;点下面&quot;拉取配置&quot;会列出这些服务对应的配置项`"
          :get-service-source="getServiceSource"
          @toggle="(svc, checked) => emit('setServiceSource', svc, checked ? configCenterType : '')"
        />

        <!-- 真实预加载:用户填完凭证 + 勾选服务后,点一下连目标配置中心拉可用条目清单。
             按钮挨着每个 env 块,各 env 独立 loading / 错误态。 -->
        <PreloadStatusRow
          :status="ccHubStateByEnv[env.id]?.status"
          idle-text="📥 拉取勾选服务的配置"
          ok-text="🔄 重新拉取勾选服务的配置"
          @click="emit('runCCHubPreload', env.id)"
        >
          <template #ok>✓ {{ ccHubStateByEnv[env.id]!.entries?.length || 0 }} 条</template>
        </PreloadStatusRow>

        <!-- 映射块:只有**本 env** 自己预加载成功时才显示。不借其他 env 的扫描结果 ——
             每个 env 必须用自己的凭证各扫一次,才能呈现自己的 namespace / dataId 选项。 -->
        <NamespaceServiceMap
          v-if="envScanned(env.id) && wizard.allServiceNames.filter(s => getServiceSource(s) === configCenterType).length > 0"
          :env-i-d="env.id"
          :config-center-type="configCenterType"
          :services="wizard.allServiceNames.filter(s => getServiceSource(s) === configCenterType)"
          :env-namespaces="envNamespaces"
          :service-config-sel="serviceConfigSel"
          :service-config-group="serviceConfigGroup"
          :namespaces="namespacesFor(env.id)"
          :entries="entriesForNamespace(env.id, envNamespaces[env.id] || '')"
          :svc-key="wizard.svcKey"
          :has-error="wizard.hasError"
          @namespace-changed="(_e, v) => emit('namespaceChanged', env.id, v)"
          @data-id-changed="(_e, svc) => emit('dataIdChanged', env.id, svc)"
        />
        <div
          v-else-if="envScanned(env.id) && wizard.allServiceNames.length === 0"
          class="cc-map-block cc-map-hint"
        >
          先在 Step 4 填好 repos 的 <code>service_names</code>,这里才有服务列表可映射。
        </div>
        <div
          v-else-if="envScanned(env.id)"
          class="cc-map-block cc-map-hint"
        >
          没有服务被勾选走 <code>{{ configCenterType }}</code> 源 —— 在上面的"本环境包含的服务"清单里勾要走当前源的服务。
        </div>

        <!-- kuboard 主源:per-service cluster/namespace/configmap 三联映射。
             nacos 走上面的 cc-map-block(envNamespaces + serviceConfigSel),kuboard 走这里。 -->
        <KuboardServiceMap
          v-if="configCenterType === 'kuboard'
                && wizard.kuboardStateByEnv[env.id]?.status === 'ok'
                && wizard.allServiceNames.filter(s => getServiceSource(s) === configCenterType).length > 0"
          :env-i-d="env.id"
          :services="wizard.allServiceNames.filter(s => getServiceSource(s) === configCenterType)"
          :kuboard-svc-map="kuboardSvcMap"
          :clusters="wizard.kuboardClustersOf(env.id)"
          :svc-key="wizard.svcKey"
          :namespaces-for="wizard.kuboardNamespacesFor"
          :configmaps-for="wizard.kuboardConfigMapsFor"
          @set-loc="(envID, svc, field, value) => emit('setKuboardLoc', envID, svc, field, value)"
        />

      </div>
    </div>

    <!-- 副源连接表单:每个非主源 type 一份;主源在上面已渲染。 -->
    <SecondarySourcePanel
      v-for="t in activeSourceTypes.slice(1).filter(t2 => ccFieldsByType[t2])"
      :key="`secsrc-${t}`"
      :source-type="t"
      :fields="ccFieldsByType[t]"
      :environments="wizard.environments"
      :all-service-names="wizard.allServiceNames"
      :source-creds="sourceCreds"
      :kuboard-state-by-env="wizard.kuboardStateByEnv"
      :kuboard-svc-map="kuboardSvcMap"
      :is-field-hidden="isFieldHidden"
      :get-service-source="getServiceSource"
      :svc-key="wizard.svcKey"
      :kuboard-namespaces-for="wizard.kuboardNamespacesFor"
      :kuboard-config-maps-for="wizard.kuboardConfigMapsFor"
      @preload-kuboard="(srcType, envID) => emit('preloadKuboardFromSource', srcType, envID)"
      @toggle-service-source="(svc, checked, srcType) => emit('setServiceSource', svc, checked ? srcType : '')"
      @set-kuboard-loc="(envID, svc, field, value) => emit('setKuboardLoc', envID, svc, field, value)"
    />

    <!-- env-vars 源(无远程连接,但每个 env 各数据层的静态连接串在 Step 6 数据层里按 data_store 维度填) -->
    <div v-if="enabledSourceTypes['env-vars']" class="form-group">
      <p class="help-text">
        <strong>env-vars</strong> 源:机器人直接读取仓库内 <code>.env</code> 文件 + Step 6 数据层里填的静态连接串。
        这里没有连接信息要填,具体数据层(redis / mysql / ...)的 endpoint 走 Step 6 的"数据层"页。
      </p>
    </div>

    <!-- none 源:整个系统不接配置中心,本步无需任何输入,继续往下走即可 -->
    <div v-if="enabledSourceTypes['none']" class="form-group">
      <p class="help-text" style="background:#fffbeb;border-left-color:#f59e0b;color:#92400e;line-height:1.7;">
        <strong>不使用任何配置源</strong><br/>
        系统的连接串 / 业务配置不来自 nacos/apollo/consul/kuboard,也不走 <code>.env</code>。本步骤无需填写,直接"下一步"即可。
        <br/>
        下游影响:
        <br/>
        ① <code>config-executor</code> skill 不会装到工作区(机器人不会主动去读配置中心);
        <br/>
        ② Step 6 数据层连接串需要在仓库代码里硬编码 / 部署时手动注入,机器人不再帮忙读;
        <br/>
        ③ 生成的 <code>system.yaml</code> 仅占位 <code>config_center.type: none</code>。
      </p>
    </div>

    <!-- kuboard 源说明:简短引导用户填 URL + 鉴权,点拉取按钮自动加载 K8s 资源 -->
    <div v-if="enabledSourceTypes['kuboard']" class="form-group">
      <p class="help-text" style="background:#eff6ff;border-left-color:#3b82f6;color:#1e3a8a;line-height:1.7;">
        <strong>Kuboard 源使用说明</strong><br/>
        通过 Kuboard v4 API 读 K8s ConfigMap,本机无需 <code>~/.kube/config</code>,适合<strong>能登 Kuboard、拿不到 kubeconfig</strong> 的场景。
        <br/>
        <strong>鉴权方式(二选一)</strong>:
        <br/>
        ① <strong>API 访问凭证</strong>(推荐):Kuboard <strong>个人中心 → API 访问凭证 → 创建</strong>,粘到下方"API 访问凭证"字段。不暴露密码、可独立吊销、长期有效。
        <br/>
        ② <strong>用户名 + 密码</strong>:走 Kuboard <code>/login</code> 换临时 token,适合临时验证。
        <br/>
        填好 URL + 任一鉴权 → 点 <strong>📥 从 Kuboard 读取可选项</strong>,集群 / namespace / ConfigMap 自动下拉,再为每个服务挑对应位置即可。
      </p>
    </div>
  </div>
</template>
