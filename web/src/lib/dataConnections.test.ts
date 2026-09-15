import { describe, expect, it } from 'vitest'
import { dataConnections } from './dataConnections'
describe('connection presentation', () => {
  it('groups identical connections without including credentials in DOM identity', () => {
    const groups = dataConnections({a:{redis:{url:'redis://secret'}},b:{redis:{url:'redis://secret'}}}, ['a','b'])
    expect(groups).toHaveLength(1)
    expect(groups[0].members).toHaveLength(2)
    expect(groups[0].id).toBe('redis')
  })
  it('keeps different credentials and identities separate and excludes orphaned services', () => {
    expect(dataConnections({a:{redis:{url:'one'}},b:{redis:{url:'two'},'redis-2':{url:'one'}},orphan:{redis:{url:'one'}}}, ['a','b'])).toHaveLength(3)
  })
})
