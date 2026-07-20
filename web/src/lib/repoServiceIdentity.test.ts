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

  it('migrates an existing empty frontend draft to a visible repo-name identity', () => {
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

  it('feeds explicit frontend identity to runtime mapping with a repo-name fallback', () => {
    expect(runtimeOnlyServiceNames({
      role: 'frontend', name: 'base-frontend', service_names: 'funhub-web',
    })).toEqual(['funhub-web'])
    expect(runtimeOnlyServiceNames({ role: 'frontend', name: 'base-frontend' }))
      .toEqual(['base-frontend'])
  })
})
