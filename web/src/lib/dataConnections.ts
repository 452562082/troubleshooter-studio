import type { DSByService, DSFieldMap } from './useDataStoreState'

export interface DataConnection {
  id: string
  members: Array<{ service: string; key: string }>
  fields: DSFieldMap
}

/** Group only identical identities and fields within one environment. Never expose field values in DOM keys. */
export function dataConnections(stores: DSByService, services: readonly string[]): DataConnection[] {
  const groups: DataConnection[] = []
  for (const service of services) {
    for (const [key, fields] of Object.entries(stores[service] || {})) {
      const keys = Object.keys(fields).sort()
      const group = groups.find(g => g.id === key && Object.keys(g.fields).length === keys.length && keys.every(k => g.fields[k] === fields[k]))
      if (group) group.members.push({ service, key })
      else groups.push({ id: key, fields, members: [{ service, key }] })
    }
  }
  return groups
}
