import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createRouter, createMemoryHistory } from 'vue-router'
import InitPage from './InitPage.vue'

vi.mock('../lib/bridge/aitools', () => ({
  detectAITools: vi.fn(async () => ({ claude_code: {installed:false}, cursor: {installed:false}, codex: {installed:false}, opencode: {installed:false} })),
  probeAgentAvailability: vi.fn(async () => ({status:'ready', message:'当前账号与默认模型调用成功'})),
}))

async function page(draft?: Record<string, unknown>) {
  if (draft) localStorage.setItem('tsf-init-wizard-v1', JSON.stringify(draft))
  const router = createRouter({history:createMemoryHistory(),routes:[{path:'/',component:InitPage}]})
  await router.push('/')
  const wrapper = mount(InitPage, {global:{plugins:[router]}, attachTo:document.body})
  await flushPromises()
  return wrapper
}
const validDraft = {
  wizardSchema:2, currentStep:5,
  system:{id:'orders',name:'订单系统',description:''},
  agent:{name:'订单排障机器人',id:'orders-troubleshooter',workspace_name:'',model:''},
  environments:[{id:'test',api_domain:'https://example.invalid',web_domain:'',is_prod:false}],
  repos:[{name:'orders',url:'https://example.invalid/orders.git',stack:'go',framework:'',role:'backend',service_names:'orders',env_branches:{test:'main'},_source:'remote'}],
  enabledTargets:{codex:true}, enabledSourceTypes:{none:true}, enabledSourceOrder:['none'],
}

describe('creation journey', () => {
  beforeEach(() => {
    const storage = new Map<string,string>()
    vi.stubGlobal('localStorage', {
      getItem: (key:string) => storage.get(key) ?? null,
      setItem: (key:string,value:string) => storage.set(key,value),
      removeItem: (key:string) => storage.delete(key),
      clear: () => storage.clear(),
    })
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok:false, status:503, json:async()=>({}),text:async()=>'' })))
    Element.prototype.scrollIntoView = vi.fn()
  })
  afterEach(()=>{document.body.innerHTML='';vi.unstubAllGlobals()})
  it('restores an old draft and walks through all four phases without configuring optional connections', async () => {
    const wrapper = await page(validDraft)
    expect(wrapper.get('.journey-progress [aria-current=step]').text()).toContain('选择项目')
    await wrapper.get('.next-wrap button').trigger('click')
    expect(wrapper.text()).toContain('在哪个环境排障')
    expect(wrapper.findAll('.target-card input:checked')).toHaveLength(1)
    await wrapper.get('.next-wrap button').trigger('click')
    expect(wrapper.text()).toContain('连接排障所需的数据')
    await wrapper.get('.next-wrap button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('确认机器人配置')
    expect(wrapper.get('.wizard-summary').text()).toContain('订单系统')
    expect(wrapper.find('.yaml-preview').exists()).toBe(false)
    wrapper.unmount()
  })
  it('adds a database without a configuration source and blocks progression until checked or removed', async () => {
    const wrapper = await page({...validDraft,currentStep:6})
    expect(wrapper.get('.next-wrap button').text()).toBe('暂不连接，继续创建')
    await wrapper.findAll('.capability-grid button')[1].trigger('click')
    await wrapper.get('.connection-heading button').trigger('click')
    await wrapper.get('.connection-add').trigger('submit')
    expect(wrapper.get('.connection-item').text()).toContain('Redis')
    expect(wrapper.get('.connection-item').text()).toContain('待补充')
    expect(wrapper.get('.next-wrap button').attributes('disabled')).toBeDefined()
    await wrapper.get('.connection-item input').setValue('redis://cache:6379')
    expect(wrapper.get('.connection-item').text()).toContain('待检查')
    expect(wrapper.get('.connection-item').attributes('open')).toBeDefined()
    await wrapper.findAll('.connection-item button').find(b => b.text() === '移除此连接')!.trigger('click')
    expect(wrapper.get('.next-wrap button').attributes('disabled')).toBeUndefined()
    await wrapper.get('.next-wrap button').trigger('click')
    expect(wrapper.text()).toContain('确认机器人配置')
    wrapper.unmount()
  })
  it('adds one shared connection for multiple services and edits them together', async () => {
    const wrapper = await page({...validDraft,currentStep:6,repos:[{...validDraft.repos[0],service_names:'orders,payments'}]})
    await wrapper.findAll('.capability-grid button')[1].trigger('click')
    await wrapper.get('.connection-heading button').trigger('click')
    for (const checkbox of wrapper.findAll('.service-option input')) await checkbox.setValue(true)
    await wrapper.get('.connection-add').trigger('submit')
    expect(wrapper.findAll('.connection-item')).toHaveLength(1)
    expect(wrapper.get('.connection-item summary').text()).toContain('payments')
    await wrapper.get('.connection-item input').setValue('redis://example.invalid')
    expect(wrapper.findAll('.connection-item')).toHaveLength(1)
    await wrapper.findAll('.connection-item button').find(b=>b.text()==='移除此连接')!.trigger('click')
    expect(wrapper.get('.next-wrap button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
  it('reuses one2all credentials and environment location for service status', async () => {
    const wrapper = await page({...validDraft,currentStep:6,enabledSourceTypes:{one2all:true},enabledSourceOrder:['one2all'],
      sourceCreds:{one2all:{creds:{_shared_:{mcp_url:'https://one2all.invalid/mcp',token:'test-token'}}}},
      one2allSvcMap:{'test::orders':{cluster_id:'1',namespace:'orders-test',configmap:'orders'}}})
    await wrapper.findAll('.capability-grid button')[0].trigger('click')
    await wrapper.findAll('.runtime-reuse-offer button').find(b=>b.text().includes('one2all'))!.trigger('click')
    expect(wrapper.get('.connection-credentials summary').text()).toContain('复用配置源 one2all 连接')
    expect(wrapper.get('.connection-credentials').attributes('open')).toBeUndefined()
    wrapper.unmount()
  })
  it('keeps a shared manual connection when a configuration source is changed', async () => {
    const wrapper = await page({...validDraft,currentStep:6,scannedDS:{test:{orders:{redis:{url:'redis://example.invalid'}}}},manualDataStoreEntries:{'test::orders::redis':true}})
    await wrapper.findAll('.capability-grid button')[0].trigger('click')
    await wrapper.findAll('.source-type-pill').find(p=>p.text().includes('env-vars'))!.get('input').setValue(true)
    await wrapper.findAll('.capability-grid button')[1].trigger('click')
    expect(wrapper.get('.connection-item').text()).toContain('Redis')
    wrapper.unmount()
  })
  it('offers Grafana discovery rather than exposing dependent tools as independent platforms', async () => {
    const wrapper = await page({...validDraft,currentStep:6})
    await wrapper.findAll('.capability-grid button')[2].trigger('click')
    const choices = wrapper.findAll('.obs-tool-chips')[0]
    expect(choices.text()).toContain('Grafana')
    expect(choices.text()).not.toContain('Loki')
    await choices.findAll('input')[0].setValue(true)
    expect(wrapper.find('.grafana-discovery').exists()).toBe(true)
    expect(wrapper.text()).toContain('填写连接信息后自动发现')
    wrapper.unmount()
  })
  it('repairs an uppercase legacy ID even when manually locked, without asking for input', async () => {
    const wrapper = await page({...validDraft, system:{...validDraft.system,id:'Base',name:'Base'}, idManualOverride:true})
    expect(wrapper.get('[data-test=system-id]').text()).toBe('base')
    expect(wrapper.find('#wizard-system-id').exists()).toBe(false)
    expect(wrapper.get('.next-wrap button').attributes('disabled')).toBeUndefined()
    await wrapper.get('#wizard-system-name').setValue('新的中文名称')
    expect(wrapper.get('[data-test=system-id]').text()).toBe('base')
    await wrapper.get('.next-wrap button').trigger('click')
    expect(wrapper.get('.journey-progress [aria-current=step]').text()).toContain('运行方式')
    wrapper.unmount()
  })
  it('does not let the progress bar skip invalid earlier sections', async () => {
    const wrapper = await page({...validDraft, system:{...validDraft.system,name:''}})
    await wrapper.findAll('.journey-progress button')[3].trigger('click')
    expect(wrapper.get('.journey-progress [aria-current=step]').text()).toContain('选择项目')
    expect(wrapper.find('.deploy-final-btn').exists()).toBe(false)
    wrapper.unmount()
  })
  it('restores old deployment step into review while retaining configuration', async () => {
    const wrapper = await page({...validDraft,currentStep:10})
    expect(wrapper.get('.journey-progress [aria-current=step]').text()).toContain('确认并创建')
    expect(wrapper.get('.wizard-summary').text()).toContain('orders')
    wrapper.unmount()
  })
  it('prevents deployment from an old draft that has invalid configuration', async () => {
    const wrapper = await page({...validDraft,currentStep:10,environments:[{id:'test',api_domain:''}]})
    await wrapper.get('.deploy-final-btn').trigger('click')
    expect(wrapper.get('.journey-progress [aria-current=step]').text()).toContain('运行方式')
    wrapper.unmount()
  })
})

describe('new project defaults', () => {
  it('creates a usable identity from the first repository without enabling infrastructure', async () => {
    const storage = new Map<string,string>()
    vi.stubGlobal('localStorage', { getItem:(key:string)=>storage.get(key)??null, setItem:(key:string,value:string)=>storage.set(key,value) })
    vi.stubGlobal('fetch', vi.fn(async()=>({ok:false,json:async()=>({}),text:async()=>''})))
    const wrapper = await page()
    await wrapper.get('input[placeholder="git@github.com:org/order-service.git"]').setValue('https://example.invalid/orders.git')
    await wrapper.get('select[aria-label="技术栈"]').setValue('go')
    expect((wrapper.get('#wizard-system-name').element as HTMLInputElement).value).toBe('orders')
    expect(wrapper.findAll('.journey-progress button')).toHaveLength(4)
    expect(wrapper.get('.next-wrap button').attributes('disabled')).toBeUndefined()
    await wrapper.get('.next-wrap button').trigger('click')
    expect(wrapper.findAll('[data-test=environment-card]')).toHaveLength(1)
    wrapper.unmount()
    vi.unstubAllGlobals()
  })
})
