import { describe, expect, it } from 'vitest'
import type { CredField } from './credFields'
import { isCredFieldHidden, resolveCredFieldDisplay } from './credFields'

describe('credential field rules', () => {
  const getOne2All = (key: string) => ({ provider: 'one2all', auth_mode: 'access_key' })[key] || ''
  const getKuboardAccessKey = (key: string) => ({ provider: 'kuboard', auth_mode: 'access_key' })[key] || ''
  const getKuboardPassword = (key: string) => ({ provider: 'kuboard', auth_mode: 'username_password' })[key] || ''

  it('hides kuboard-only credential fields when k8s runtime provider is one2all', () => {
    const accessKey: CredField = {
      key: 'access_key',
      label: 'API 访问凭证',
      secret: true,
      envVar: () => 'KUBOARD_ACCESS_KEY_DEV',
      showWhenAll: [
        { field: 'provider', equals: 'kuboard' },
        { field: 'auth_mode', equals: 'access_key' },
      ],
    }
    const username: CredField = {
      key: 'username',
      label: '用户名(v3 必填 / Cookie KuboardUsername)',
      secret: false,
      envVar: () => 'KUBOARD_USER_DEV',
      showWhenAll: [{ field: 'provider', equals: 'kuboard' }],
    }
    const password: CredField = {
      key: 'password',
      label: '密码',
      secret: true,
      envVar: () => 'KUBOARD_PASS_DEV',
      showWhenAll: [
        { field: 'provider', equals: 'kuboard' },
        { field: 'auth_mode', equals: 'username_password' },
      ],
    }

    expect(isCredFieldHidden(accessKey, getOne2All)).toBe(true)
    expect(isCredFieldHidden(username, getOne2All)).toBe(true)
    expect(isCredFieldHidden(password, getOne2All)).toBe(true)

    expect(isCredFieldHidden(accessKey, getKuboardAccessKey)).toBe(false)
    expect(isCredFieldHidden(username, getKuboardAccessKey)).toBe(false)
    expect(isCredFieldHidden(password, getKuboardPassword)).toBe(false)
  })

  it('switches URL copy by k8s runtime provider', () => {
    const field: CredField = {
      key: 'url',
      label: 'URL',
      secret: false,
      envVar: () => 'KUBOARD_URL_DEV',
      envVarBy: {
        field: 'provider',
        values: {
          one2all: () => 'ONE2ALL_MCP_URL',
        },
      },
      labelBy: {
        field: 'provider',
        values: {
          kuboard: 'Kuboard URL',
          one2all: 'MCP Server URL',
        },
      },
      placeholderBy: {
        field: 'provider',
        values: {
          kuboard: 'http://kuboard.example.com',
          one2all: 'http://one2all.example.com/one2all/api/v1/platform/public/mcp/xxx',
        },
      },
    }

    expect(resolveCredFieldDisplay(field, getKuboardAccessKey).label).toBe('Kuboard URL')
    expect(resolveCredFieldDisplay(field, getOne2All).label).toBe('MCP Server URL')
    expect(resolveCredFieldDisplay(field, getOne2All).placeholder).toContain('/mcp/')
    expect(resolveCredFieldDisplay(field, getKuboardAccessKey).envVar('dev')).toBe('KUBOARD_URL_DEV')
    expect(resolveCredFieldDisplay(field, getOne2All).envVar('dev')).toBe('ONE2ALL_MCP_URL')
  })
})
