import { expect, test } from 'bun:test'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import type { ChangelogRelease } from '../app/types'
import { loadChangelog } from '../app/utils/changelog'
import { readChangelog } from '../scripts/changelog'

const history: Array<ChangelogRelease> = [
  {
    tag: 'v1.0.0',
    commits: [{ sha: 'a'.repeat(40), date: '2026-01-01', subject: 'Add a feature' }],
  },
]

test('loads live history without requesting the bundle', async () => {
  const signal = new AbortController().signal
  let readBundle = false
  const result = await loadChangelog(
    signal,
    async () => history,
    async () => {
      readBundle = true
      return []
    },
  )
  expect(result).toEqual({ source: 'live', history })
  expect(readBundle).toBe(false)
})

test('falls back when the live feed is unreachable, empty or malformed', async () => {
  for (const readLive of [
    async () => {
      throw new Error('Offline')
    },
    async () => [],
    async () => [{ tag: 'Unreleased', commits: [{ sha: 'invalid' }] }],
  ]) {
    const result = await loadChangelog(new AbortController().signal, readLive, async () => history)
    expect(result).toEqual({ source: 'bundled', history })
  }
})

test('reports when neither source has changes', async () => {
  const result = await loadChangelog(
    new AbortController().signal,
    async () => ({ error: { code: 404 } }),
    async () => [{ tag: 'Unreleased', commits: [] }],
  )
  expect(result).toEqual({ source: 'unavailable', history: [] })
})

test('groups dev commits using release tags on master merges', () => {
  const dir = mkdtempSync(join(tmpdir(), 'dcompose-changelog-'))
  const directory = pathToFileURL(`${dir}/`)
  const git = (...args: Array<string>) =>
    execFileSync('git', ['-c', 'user.name=Test', '-c', 'user.email=test@example.test', ...args], {
      cwd: dir,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    }).trim()
  const commit = (subject: string) => {
    git('-c', 'commit.gpgsign=false', 'commit', '--allow-empty', '-m', subject)
    return git('rev-parse', 'HEAD')
  }

  try {
    expect(readChangelog('HEAD', directory)).toEqual([])

    git('init', '-b', 'master')
    const first = commit('Initial commit')
    git('tag', 'v0.1.0')
    git('switch', '-c', 'dev')
    const released = commit('Add π support')
    git('switch', 'master')
    git('merge', '--no-ff', 'dev', '-m', 'Merge release')
    git('tag', '--no-sign', '-a', 'v0.1.1', '-m', 'Release')
    const master = commit('Master-only change')
    git('switch', 'dev')
    const unreleased = commit('Dev-only change')

    const groups = (ref: string) =>
      readChangelog(ref, directory).map(({ tag, commits }) => [tag, commits.map(({ sha }) => sha)])
    expect(groups('HEAD')).toEqual([
      ['Unreleased', [unreleased]],
      ['v0.1.1', [released]],
      ['v0.1.0', [first]],
    ])
    expect(groups('master')).toEqual([
      ['Unreleased', [master]],
      ['v0.1.1', [released]],
      ['v0.1.0', [first]],
    ])
    expect(groups('v0.1.1')).toEqual([
      ['v0.1.1', [released]],
      ['v0.1.0', [first]],
    ])
    expect(readChangelog('HEAD', directory)[1]?.commits[0]?.subject).toBe('Add π support')
  } finally {
    rmSync(dir, { recursive: true })
  }
})
