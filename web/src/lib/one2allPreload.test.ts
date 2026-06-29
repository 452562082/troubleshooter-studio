import { describe, expect, it } from 'vitest'
import { one2allPreloadOptionsForPurpose } from './one2allPreload'

describe('one2allPreloadOptionsForPurpose', () => {
  it('config_source only loads config source resources, not k8s deployments', () => {
    expect(one2allPreloadOptionsForPurpose('config_source')).toEqual({
      includeConfigMaps: true,
      loadDeployments: false,
    })
  })

  it('k8s_runtime skips configmaps and loads deployments', () => {
    expect(one2allPreloadOptionsForPurpose('k8s_runtime')).toEqual({
      includeConfigMaps: false,
      loadDeployments: true,
    })
  })
})
