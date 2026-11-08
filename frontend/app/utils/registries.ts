import type { RegistryImportEntry, RegistryImportResult } from '../types'

const isRecord = (value: unknown): value is Record<string, unknown> =>
  null !== value && 'object' === typeof value && !Array.isArray(value)

export const normalizeRegistry = (value: string): string => {
  if (
    !value.trim() ||
    value.includes('@') ||
    Array.from(value).some(
      (character) => 32 > character.charCodeAt(0) || 127 === character.charCodeAt(0),
    )
  ) {
    throw new Error('Enter a registry address.')
  }
  return value
}

export const decodeAuth = (value: string): { username: string; password: string } => {
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(value)) {
    throw new Error('The saved login is not valid base64. Enter the credentials below.')
  }
  let decoded: string
  try {
    const bytes = Uint8Array.from(atob(value), (character) => character.charCodeAt(0))
    decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    throw new Error('The saved login could not be read. Enter the credentials below.')
  }
  const separator = decoded.indexOf(':')
  if (0 > separator) {
    throw new Error('The saved login needs a username and password separator.')
  }
  return { username: decoded.slice(0, separator), password: decoded.slice(separator + 1) }
}

export const encodeAuth = (username: string, password: string): string =>
  btoa(
    Array.from(new TextEncoder().encode(`${username}:${password}`), (byte) =>
      String.fromCharCode(byte),
    ).join(''),
  )

export const parseDockerConfig = (text: string): RegistryImportResult => {
  let config: unknown
  try {
    config = JSON.parse(text)
  } catch {
    throw new Error('This file is not valid JSON.')
  }
  if (
    !isRecord(config) ||
    (undefined !== config.auths && !isRecord(config.auths)) ||
    (undefined !== config.credHelpers && !isRecord(config.credHelpers)) ||
    (undefined !== config.credsStore && 'string' !== typeof config.credsStore)
  ) {
    throw new Error('Choose a Docker config.json file with an auths object.')
  }

  const auths = isRecord(config.auths) ? config.auths : {}
  const helpers = isRecord(config.credHelpers) ? config.credHelpers : {}
  const credentialStore = 'string' === typeof config.credsStore ? config.credsStore : ''
  const addresses = new Set([...Object.keys(auths), ...Object.keys(helpers)])
  const entries: Array<RegistryImportEntry> = []

  for (const address of addresses) {
    const entry: RegistryImportEntry = {
      registry: address,
      label: '',
      auth: {},
      background_checks: false,
      selected: false,
      message: '',
    }
    try {
      normalizeRegistry(address)
    } catch (cause) {
      entry.message = cause instanceof Error ? cause.message : 'Enter a registry hostname.'
    }
    try {
      const auth = auths[address]
      if (undefined !== auth && !isRecord(auth)) {
        throw new Error('This login could not be read. Enter the credentials below.')
      }
      if (isRecord(auth)) {
        for (const [field, value] of Object.entries(auth)) {
          if ('string' !== typeof value) {
            entry.message =
              'A non-string field could not be imported. Review this login before saving.'
            continue
          }
          entry.auth[field] = value
        }
        entry.selected = 0 < Object.keys(auth).length
      }

      if (!entry.selected) {
        entry.message =
          helpers[address] || credentialStore
            ? 'This entry uses a credential helper and has no password in this file. Enter it below.'
            : 'No password is saved in this file. Enter one below.'
      }
    } catch (cause) {
      entry.message = cause instanceof Error ? cause.message : 'This login could not be read.'
    }
    entries.push(entry)
  }

  return { entries, credentialStore }
}
