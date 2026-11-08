import { describe, expect, test } from 'bun:test'
import {
  decodeAuth,
  encodeAuth,
  normalizeRegistry,
  parseDockerConfig,
} from '../app/utils/registries'

describe('Registry import', () => {
  test('invalid JSON', () => {
    expect(() => parseDockerConfig('{')).toThrow()
    for (const value of [null, [], { auths: [] }, { credHelpers: 1 }, { credsStore: {} }]) {
      expect(() => parseDockerConfig(JSON.stringify(value))).toThrow()
    }
  })

  test('UTF-8 and colons', () => {
    const auth = encodeAuth('用户', 'päss:word:')
    const result = parseDockerConfig(
      JSON.stringify({ auths: { 'registry.example.com:5000': { auth } } }),
    )
    expect(result.entries).toEqual([
      {
        registry: 'registry.example.com:5000',
        label: '',
        auth: { auth },
        background_checks: false,
        selected: true,
        message: '',
      },
    ])
    expect(decodeAuth(auth)).toEqual({ username: '用户', password: 'päss:word:' })
  })

  test('native fields', () => {
    const auth = {
      auth: encodeAuth('native', 'password'),
      username: 'native',
      password: 'password',
      email: 'native@example.com',
      serveraddress: 'https://GHCR.IO/',
      identitytoken: 'identity',
      registrytoken: 'bearer',
    }
    const { entries } = parseDockerConfig(JSON.stringify({ auths: { 'https://GHCR.IO/': auth } }))
    expect(entries[0]?.auth).toEqual(auth)
    expect(entries[0]?.registry).toBe('https://GHCR.IO/')
    expect(entries[0]?.selected).toBe(true)
  })

  test('unreadable auth is retained', () => {
    for (const auth of ['!!!', 'a', btoa('user'), btoa('\xff:pass')]) {
      const { entries } = parseDockerConfig(JSON.stringify({ auths: { 'ghcr.io': { auth } } }))
      expect(entries[0]?.selected).toBe(true)
      expect(entries[0]?.auth).toEqual({ auth })
      expect(() => decodeAuth(auth)).toThrow()
    }
  })

  test('malformed optional field', () => {
    const auth = encodeAuth('native', 'password')
    const { entries } = parseDockerConfig(
      JSON.stringify({
        auths: {
          'ghcr.io': {
            auth,
            email: 42,
            username: 'native',
            password: 'password',
            identitytoken: 'identity',
            registrytoken: 'bearer',
            serveraddress: 'ghcr.io',
          },
        },
      }),
    )
    expect(entries[0]?.selected).toBe(true)
    expect(entries[0]?.message).toBeTruthy()
    expect(entries[0]?.auth).toEqual({
      auth,
      username: 'native',
      password: 'password',
      identitytoken: 'identity',
      registrytoken: 'bearer',
      serveraddress: 'ghcr.io',
    })
  })

  test('helpers', () => {
    const auth = { auth: encodeAuth('inline', 'password') }
    const { entries, credentialStore } = parseDockerConfig(
      JSON.stringify({
        auths: { 'docker.io': {}, 'inline.example.com': auth },
        credHelpers: { 'ghcr.io': 'pass' },
        credsStore: 'desktop',
      }),
    )
    expect(credentialStore).toBe('desktop')
    expect(entries).toHaveLength(3)
    expect(entries[0]).toMatchObject({ registry: 'docker.io', auth: {}, selected: false })
    expect(entries[2]).toMatchObject({ registry: 'ghcr.io', auth: {}, selected: false })
    expect(entries[0]?.message).toBeTruthy()
    expect(entries[2]?.message).toBeTruthy()
    expect(entries[1]).toMatchObject({ auth, selected: true })
    expect(parseDockerConfig('{"auths":{},"credsStore":"desktop"}')).toMatchObject({
      entries: [],
      credentialStore: 'desktop',
    })
  })

  test('independent keys', () => {
    const keys = [
      'docker.io',
      'index.docker.io',
      'registry-1.docker.io',
      'https://index.docker.io/v1/',
      'https://index.docker.io/v1/access-token',
      'https://index.docker.io/v1/refresh-token',
      'git.m7eesn.net',
      'ghcr.io/team/image',
    ]
    const auths = Object.fromEntries(
      keys.map((key, index) => [key, { auth: encodeAuth(`user${index}`, `secret${index}`) }]),
    )
    const { entries } = parseDockerConfig(JSON.stringify({ auths }))
    expect(entries.map((entry) => entry.registry)).toEqual(keys)
    expect(entries.map((entry) => entry.auth)).toEqual(Object.values(auths))
    expect(entries.every((entry) => entry.selected && !entry.background_checks)).toBe(true)
    expect(entries.map((entry) => normalizeRegistry(entry.registry))).toEqual(keys)
  })

  test('auth keys are data', () => {
    for (const key of ['HTTPS://GHCR.IO/namespace', 'registry.example:5000', 'original/key']) {
      expect(normalizeRegistry(key)).toBe(key)
    }
    for (const key of ['', ' ', 'user:pass@ghcr.io', 'ghcr.io\n', 'ghcr.io\x00']) {
      expect(() => normalizeRegistry(key)).toThrow()
    }
  })
})
