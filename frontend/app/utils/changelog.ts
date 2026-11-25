import type { Changelog, ChangelogRelease } from '~/types'

function isRecord(value: unknown): value is Record<string, unknown> {
  return 'object' === typeof value && null !== value
}

function decodeHistory(value: unknown): Array<ChangelogRelease> {
  if (!Array.isArray(value)) {
    throw new Error('Invalid changelog')
  }
  return value.map((release: unknown) => {
    if (!isRecord(release) || 'string' !== typeof release.tag || !Array.isArray(release.commits)) {
      throw new Error('Invalid changelog release')
    }
    const commits = release.commits.map((commit: unknown) => {
      if (
        !isRecord(commit) ||
        'string' !== typeof commit.sha ||
        !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(commit.sha) ||
        'string' !== typeof commit.date ||
        'string' !== typeof commit.subject
      ) {
        throw new Error('Invalid changelog commit')
      }
      return { sha: commit.sha, date: commit.date, subject: commit.subject }
    })
    return { tag: release.tag, commits }
  })
}

export async function loadChangelog(
  signal: AbortSignal,
  readLive: () => Promise<unknown>,
  readBundled: () => Promise<unknown>,
): Promise<Changelog> {
  for (const [source, read] of [
    ['live', readLive],
    ['bundled', readBundled],
  ] as const) {
    signal.throwIfAborted()
    try {
      const history = decodeHistory(await read()).filter((release) => 0 < release.commits.length)
      signal.throwIfAborted()
      if (0 < history.length) {
        return { source, history }
      }
    } catch {
      signal.throwIfAborted()
    }
  }
  return { source: 'unavailable', history: [] }
}
