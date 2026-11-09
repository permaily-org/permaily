// Renders sample release notes with the configured preset, so an incompatible
// preset or semantic-release upgrade fails on the pull request instead of during
// a release on main.

import { readFileSync } from 'node:fs'
import { generateNotes } from '@semantic-release/release-notes-generator'

const config = JSON.parse(readFileSync(new URL('../.releaserc.json', import.meta.url), 'utf8'))
const [, options] = config.plugins.find(
  (p) => Array.isArray(p) && p[0] === '@semantic-release/release-notes-generator',
)

const commit = (message) => ({
  hash: '0123456789abcdef0123456789abcdef01234567',
  message,
  committerDate: '2026-11-09T00:00:00Z',
})

const notes = await generateNotes(options, {
  cwd: process.cwd(),
  env: process.env,
  options: { repositoryUrl: 'https://github.com/permaily-org/permaily' },
  commits: [commit('feat: sample feature'), commit('fix: sample fix')],
  lastRelease: { gitTag: 'v0.0.0', version: '0.0.0' },
  nextRelease: { gitTag: 'v0.1.0', version: '0.1.0' },
  logger: { log() {}, error: console.error },
})

if (!notes.includes('sample feature') || !notes.includes('sample fix')) {
  console.error(notes)
  throw new Error('release notes did not render the sample commits')
}
console.log('release notes check: ok')
