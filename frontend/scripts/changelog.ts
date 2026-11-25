import { execFileSync } from 'node:child_process'
import { existsSync, writeFileSync } from 'node:fs'
import process from 'node:process'
import { fileURLToPath } from 'node:url'
import type { ChangelogCommit, ChangelogRelease } from '../app/types'

const root = new URL('../../', import.meta.url)
const releaseTag = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/

export function readChangelog(ref = 'HEAD', directory = root): Array<ChangelogRelease> {
  if (!existsSync(new URL('.git', directory))) {
    return []
  }

  const git = (...args: Array<string>) =>
    execFileSync('git', ['--no-replace-objects', ...args], {
      cwd: directory,
      encoding: 'utf8',
      maxBuffer: Infinity,
    })
  const tags = git('-c', 'versionsort.suffix=-', 'tag', '--sort=-version:refname', '--list', 'v*')
    .split('\n')
    .filter((tag) => releaseTag.test(tag))
  const fields = git(
    'log',
    '--topo-order',
    '--no-merges',
    '--no-show-signature',
    '-z',
    '--format=%H%x00%as%x00%s',
    ref,
    '--',
  ).split('\0')
  const commits = new Map<string, ChangelogCommit>()
  for (let i = 0; i + 2 < fields.length; i += 3) {
    const sha = fields[i]!
    const date = fields[i + 1]!
    const subject = fields[i + 2]!
    commits.set(sha, { sha, date, subject })
  }

  const releases: Array<ChangelogRelease> = []
  const tagRefs = tags.map((tag) => `refs/tags/${tag}`)
  // Release tags can point to master merges absent from dev. Match their commits, not their tips.
  for (const [index, tag] of tags.entries()) {
    const published = git(
      'rev-list',
      '--topo-order',
      `refs/tags/${tag}`,
      '--not',
      ...tagRefs.slice(index + 1),
    )
    const entries: Array<ChangelogCommit> = []
    for (const sha of published.trim().split('\n')) {
      const commit = commits.get(sha)
      if (commit) {
        entries.push(commit)
        commits.delete(sha)
      }
    }
    if (0 < entries.length) {
      releases.push({ tag, commits: entries })
    }
  }
  if (0 < commits.size) {
    releases.unshift({ tag: 'Unreleased', commits: [...commits.values()] })
  }
  return releases
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [output, ref] = process.argv.slice(2)
  if (!output || !existsSync(new URL('.git', root))) {
    throw new Error('Usage from a Git checkout: changelog.ts <output path> [ref]')
  }
  writeFileSync(output, `${JSON.stringify(readChangelog(ref))}\n`)
}
