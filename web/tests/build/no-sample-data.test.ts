import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

/*
  The app shows only what the server answers: no invented person, mailbox, connection, rule or count, on any screen a
  signed-in person, an admin or anyone reaching a route can see. The sample data the screens were drawn with is gone
  (docs/web.md, "Screens"). These are its words; none may come back into the app's sources or into what the build ships.
*/
const sampleWords = [
  // The sample person and her mailboxes.
  'Marta',
  'marta.ruiz',
  'estudioruiz',
  // The sample vendors and connections.
  'Trip App',
  'Ledgerly',
  'Inbox Tidy',
  'Mailwise',
  'Morning summary',
  'Bookkeeping',
  // The sample overview: its filter, its counts and what it said waited.
  'All mailboxes',
  'waiting for you',
  // The sample consent screens' routes.
  'consent/trip-app',
  'consent/claude',
  'consent/ledgerly',
  // The sample inbox's senders.
  'colegio-sanjose',
  'holded.com',
  'iberdrola',
]

const web = join(import.meta.dirname, '..', '..')

function filesUnder(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    return statSync(path).isDirectory() ? filesUnder(path) : [path]
  })
}

/** Each file under `dir` that holds one of the sample's words, with the words it holds. */
function sampleIn(dir: string, text: (path: string) => boolean): string[] {
  return filesUnder(dir)
    .filter(text)
    .flatMap((path) => {
      const content = readFileSync(path, 'utf8')
      const found = sampleWords.filter((word) => content.includes(word))
      return found.length ? [`${relative(web, path)}: ${found.join(', ')}`] : []
    })
}

describe('no sample data', () => {
  it('is in the app’s sources', () => {
    expect(sampleIn(join(web, 'src'), (path) => /\.(ts|tsx|css|html)$/.test(path))).toEqual([])
    expect(existsSync(join(web, 'src', 'data'))).toBe(false)
  })

  // The build is what the enclave serves (internal/webui). `make check-web` and CI's `web-unit` build before they test;
  // a test run without a build has nothing to look at, which CI never allows.
  const dist = join(web, 'dist')
  it.skipIf(!existsSync(dist) && !process.env.CI)('is in the production build', () => {
    expect(existsSync(join(dist, 'index.html'))).toBe(true)
    expect(sampleIn(dist, (path) => /\.(js|css|html|json|txt|svg)$/.test(path))).toEqual([])
  })
})
