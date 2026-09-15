import { describe, expect, it } from 'vitest'
import { useDataStoreState } from './useDataStoreState'

describe('useDataStoreState', () => {
  it('adds a manually supplied component with editable fields and enables its skill', () => {
    const enabled = { redis: false, mysql: false }
    const state = useDataStoreState({}, ['redis', 'mysql'], enabled)

    state.addManualDataStore('test', 'user-api', 'redis', ['url'])

    expect(state.scannedDS.test['user-api'].redis).toEqual({ url: '' })
    expect(state.dsScanState['test::user-api']).toMatchObject({ status: 'ok' })
    expect(enabled.redis).toBe(true)
  })

  it('does not overwrite values when the same manual component is added twice', () => {
    const enabled = { redis: false }
    const state = useDataStoreState({}, ['redis'], enabled)
    state.addManualDataStore('test', 'user-api', 'redis', ['url'])
    state.scannedDS.test['user-api'].redis.url = 'redis://existing'
    state.addManualDataStore('test', 'user-api', 'redis', ['url'])
    expect(state.scannedDS.test['user-api'].redis.url).toBe('redis://existing')
    expect(state.scannedDS.test['user-api']['redis-2']).toEqual({ url: '' })
    expect(state.dataStoreTypes['redis-2']).toBe('redis')
  })

  it('allocates datastore instance IDs globally across services and environments', () => {
    const enabled = { redis: false }
    const state = useDataStoreState({}, ['redis'], enabled)
    state.addManualDataStore('dev', 'user-api', 'redis', ['url'])
    state.addManualDataStore('prod', 'order-api', 'redis', ['url'])
    expect(state.scannedDS.dev['user-api'].redis).toBeDefined()
    expect(state.scannedDS.prod['order-api']['redis-2']).toBeDefined()
  })
})
