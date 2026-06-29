export type One2AllPreloadPurpose = 'config_source' | 'k8s_runtime'

export interface One2AllPreloadOptions {
  includeConfigMaps: boolean
  loadDeployments: boolean
}

export function one2allPreloadOptionsForPurpose(purpose: One2AllPreloadPurpose): One2AllPreloadOptions {
  if (purpose === 'k8s_runtime') {
    return { includeConfigMaps: false, loadDeployments: true }
  }
  return { includeConfigMaps: true, loadDeployments: false }
}
