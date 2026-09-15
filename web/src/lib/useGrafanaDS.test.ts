import { afterEach, describe, expect, it, vi } from 'vitest'
import { reactive, effectScope } from 'vue'
const mocks = vi.hoisted(() => ({list:vi.fn(), desktop:vi.fn(() => false)}))
vi.mock('./bridge', () => ({listGrafanaDatasources:mocks.list}))
vi.mock('./bridge/shared', () => ({isDesktop:mocks.desktop}))
vi.mock('./logStore', () => ({pushLog:vi.fn()}))
import { useGrafanaDS } from './useGrafanaDS'
import { useLokiMappingState } from './useLokiMappingState'
function setup() {
  const mapping = useLokiMappingState()
  const toolInputs = reactive<Record<string,string>>({'obs:grafana:test:url':'https://grafana.invalid'})
  const scope = effectScope()
  const api = scope.run(()=>useGrafanaDS({...mapping, toolInputs, enabledObservability:{grafana:true},toolKeyFor:(c,t,e,f)=>`${c}:${t}:${e}:${f}`}))!
  return {...mapping,...api,toolInputs,scope}
}
const ds = (uid:string,type:string) => ({uid,type,name:uid,is_loki:type==='loki'})
describe('Grafana discovery', () => {
  afterEach(() => {
    mocks.desktop.mockReturnValue(false)
    vi.useRealTimers()
    vi.clearAllMocks()
  })
  it('invalidates success immediately and cancels discovery when credentials are cleared', async () => {
    vi.useFakeTimers()
    mocks.desktop.mockReturnValue(true)
    const s = setup()
    s.toolInputs['obs:grafana:test:api_key'] = 'test-key'
    s.getLokiMapping('test').dsListStatus = 'ok'
    s.scheduleGrafanaDsAutoload('test')
    expect(s.getLokiMapping('test').dsListStatus).toBe('loading')
    s.toolInputs['obs:grafana:test:api_key'] = ''
    s.scheduleGrafanaDsAutoload('test')
    await vi.advanceTimersByTimeAsync(1000)
    expect(s.getLokiMapping('test').dsListStatus).toBe('idle')
    expect(mocks.list).not.toHaveBeenCalled()
    s.scope.stop()
  })
  it('restores selected sources without treating cached discovery as a fresh authentication check', () => {
    const old = useLokiMappingState().getLokiMapping('test')
    old.dsListStatus = 'ok'
    old.dsList = [ds('saved-loki', 'loki')]
    old.dsUID = 'saved-loki'
    const restored = useLokiMappingState({test:old}).getLokiMapping('test')
    expect(restored.dsListStatus).toBe('idle')
    expect(restored.dsUID).toBe('saved-loki')
    expect(restored.dsList).toEqual([ds('saved-loki', 'loki')])
  })
  it('selects unique candidates but leaves ambiguous sources for the user', async () => {
    const s = setup()
    mocks.list.mockResolvedValue([ds('l1','loki'),ds('l2','loki'),ds('p1','prometheus'),ds('t1','tempo'),ds('t2','tempo')])
    await s.loadLokiDatasources('test')
    expect(s.getLokiMapping('test').dsUID).toBe('')
    expect(s.grafanaDsUidByObsEnv['prometheus:test']).toBe('p1')
    expect(s.grafanaDsUidByObsEnv['tempo:test']).toBe('')
    s.scope.stop()
  })
  it('preserves valid selections and removes missing selections after refresh', async () => {
    const s = setup()
    s.getLokiMapping('test').dsUID='l2'
    mocks.list.mockResolvedValue([ds('l1','loki'),ds('l2','loki')])
    await s.loadLokiDatasources('test')
    expect(s.getLokiMapping('test').dsUID).toBe('l2')
    s.getLokiMapping('test').serviceValues = {orders:'old-service'}
    mocks.list.mockResolvedValue([])
    await s.loadLokiDatasources('test')
    expect(s.getLokiMapping('test').dsUID).toBe('')
    expect(s.getLokiMapping('test').serviceValues).toEqual({})
    s.scope.stop()
  })
  it('ignores late replies for a previous connection and after disposal', async () => {
    const s = setup()
    let resolve!: (list:unknown[])=>void
    mocks.list.mockImplementation(()=>new Promise(r=>{resolve=r}))
    const pending = s.loadLokiDatasources('test')
    s.toolInputs['obs:grafana:test:url']='https://other.invalid'
    resolve([ds('old','loki')]); await pending
    expect(s.getLokiMapping('test').dsList).toEqual([])
    expect(s.getLokiMapping('test').dsListStatus).toBe('idle')
    const disposed = s.loadLokiDatasources('test')
    s.scope.stop(); resolve([ds('late','loki')]); await disposed
    expect(s.getLokiMapping('test').dsList).toEqual([])
  })
})
