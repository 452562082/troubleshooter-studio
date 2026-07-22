import { describe, expect, it } from 'vitest'
import {
  isConfigServiceRole,
  runtimeOnlyServiceNames,
  serviceNamesAfterScan,
  serviceNamesForRole,
  supportsRuntimeServiceNames,
} from './repoServiceIdentity'

describe('repo service identity roles', () => {
  it('keeps frontend runtime identity separate from backend config services', () => {
    expect(isConfigServiceRole('frontend')).toBe(false)
    expect(supportsRuntimeServiceNames('frontend')).toBe(true)
    expect(isConfigServiceRole('backend')).toBe(true)
    expect(supportsRuntimeServiceNames('common-lib')).toBe(false)
  })

  it('uses repo name instead of scoped package name for a newly scanned frontend', () => {
    expect(serviceNamesAfterScan({
      role: 'frontend',
      repoName: 'base-frontend',
      detectedServiceNames: ['@funhub/app'],
      previousRole: 'backend',
    })).toBe('base-frontend')
  })

  it('defaults a newly selected frontend role to the repo-name identity', () => {
    expect(serviceNamesForRole('frontend', 'base-frontend', '')).toBe('base-frontend')
    expect(serviceNamesForRole('frontend', 'base-frontend', 'funhub-web')).toBe('funhub-web')
    expect(serviceNamesForRole('docs', 'docs', 'stale-name')).toBe('')
  })

  it('preserves a user-confirmed frontend runtime name on rescan', () => {
    expect(serviceNamesAfterScan({
      role: 'frontend',
      repoName: 'base-frontend',
      detectedServiceNames: ['@funhub/app'],
      previousRole: 'frontend',
      previousServiceNames: 'funhub-web',
    })).toBe('funhub-web')
  })

  it('preserves an explicitly emptied frontend runtime identity on rescan', () => {
    expect(serviceNamesAfterScan({
      role: 'frontend',
      repoName: 'base-frontend',
      detectedServiceNames: ['base-frontend', 'base-frontend-document'],
      previousRole: 'frontend',
      previousServiceNames: '',
    })).toBe('')
  })

  it('uses all deployable frontend entries and replaces the legacy package identity', () => {
    expect(serviceNamesAfterScan({
      role: 'frontend',
      repoName: 'base-frontend',
      detectedServiceNames: ['base-frontend', 'base-frontend-document'],
      previousRole: 'frontend',
      previousServiceNames: '@funhub/app',
    })).toBe('base-frontend, base-frontend-document')

    expect(serviceNamesAfterScan({
      role: 'frontend',
      repoName: 'base-frontend',
      detectedServiceNames: ['base-frontend', 'base-frontend-document'],
      previousRole: 'frontend',
      previousServiceNames: 'base-frontend',
    })).toBe('base-frontend, base-frontend-document')
  })

  it('retains backend analyzer behavior and clears non-runtime repositories', () => {
    expect(serviceNamesAfterScan({
      role: 'backend', repoName: 'base-backend', detectedServiceNames: ['base-api'],
    })).toBe('base-api')
    expect(serviceNamesAfterScan({
      role: 'backend', repoName: 'mono', detectedServiceNames: ['a', 'b'],
    })).toBe('mono')
    expect(serviceNamesAfterScan({
      role: 'docs', repoName: 'docs', detectedServiceNames: ['docs'],
    })).toBe('')
  })

  it('feeds only explicit frontend identities to runtime mapping', () => {
    expect(runtimeOnlyServiceNames({
      role: 'frontend', name: 'base-frontend', service_names: 'funhub-web',
    })).toEqual(['funhub-web'])
    expect(runtimeOnlyServiceNames({ role: 'frontend', name: 'base-frontend' }))
      .toEqual([])
  })
})
