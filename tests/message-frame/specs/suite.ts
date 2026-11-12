import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { Page, TestInfo } from '@playwright/test'

const here = dirname(fileURLToPath(import.meta.url))

/** The hostile corpus of internal/mailhtml/testdata/hostile, by name. */
export const corpus = readdirSync(join(here, '../../../internal/mailhtml/testdata/hostile'))
  .filter((f) => f.endsWith('.html'))
  .map((f) => f.slice(0, -'.html'.length))
  .sort()

/** The URL agreement table Go writes (tests 24 and 25). */
export const urlTable: {
  urls: Array<{
    input: string
    kept: boolean
    url?: string
    protocol?: string
    hostname?: string
    port?: string
    host?: string
    ipv4?: boolean
  }>
} = JSON.parse(readFileSync(join(here, '../../../internal/mailhtml/testdata/urls.json'), 'utf8'))

export type Seen = {
  host: string
  method: string
  path: string
  session: boolean
  device: boolean
  referer?: string
  dest?: string
  site?: string
  status?: number
}

/** What reached the app, the internet listener (through the proxy), the proxy's CONNECTs, and the resolver. */
export type Requests = { app: Seen[]; internet: Seen[]; connects: string[]; lookups: string[] }

/** What reached the app and the internet since the last reset. */
export async function requests(page: Page): Promise<Requests> {
  return (await page.request.get('/test/requests')).json()
}

export async function reset(page: Page) {
  await page.request.get('/test/reset')
}

/** Signs in: the app's cookies, set by the app's host. */
export async function signIn(page: Page) {
  await page.goto('/test/session')
}

export async function cases(
  page: Page,
): Promise<{ box: string; cases: Array<{ Name: string; Message: string }> }> {
  return (await page.request.get('/test/cases')).json()
}

/** Records an observation of the release gate (docs/acceptance.md) for this browser. */
export function observe(info: TestInfo, what: string, value: unknown) {
  const line = `${info.project.name} ${info.project.use.browserName ?? ''}: ${what}: ${JSON.stringify(value)}`
  info.annotations.push({ type: 'observed', description: line })
  console.log(`OBSERVED ${line}`)
}

/** Records dialogs a page or its frames open: a script that ran would open one. */
export function watchDialogs(page: Page): string[] {
  const seen: string[] = []
  page.on('dialog', (d) => {
    seen.push(d.message())
    void d.dismiss()
  })
  return seen
}
