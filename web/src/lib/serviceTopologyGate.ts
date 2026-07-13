export interface ServiceTopologyRepoGateInput {
  name: string
  role?: string
}

const excludedServiceNodeRoles = new Set(['common-lib', 'infra', 'docs'])

export function hasServiceTopologyWorkbenchRepos(
  repos: readonly ServiceTopologyRepoGateInput[],
  topologyRepoPaths: Readonly<Record<string, string>>,
): boolean {
  const repoNames = new Set<string>()
  const resolvedPaths = new Set<string>()

  for (const repo of repos) {
    const name = repo.name.trim()
    const role = repo.role?.trim() || 'backend'
    if (!name || excludedServiceNodeRoles.has(role) || repoNames.has(name)) continue
    if (!Object.prototype.hasOwnProperty.call(topologyRepoPaths, name)) continue
    const path = topologyRepoPaths[name]?.trim()
    if (!path) continue
    repoNames.add(name)
    resolvedPaths.add(path.replace(/\/$/, ''))
  }

  return repoNames.size >= 2 && resolvedPaths.size >= 2
}
