import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { privacyUrl, TERMS_VERSION, termsUrl } from '../../src/lib/terms.ts'

describe('the terms', () => {
  // The app and the server ship in one image: a sign-up names the version the server takes.
  it('match the version the service records', () => {
    const dir = join(import.meta.dirname, '../../../internal/domain')
    const source = readdirSync(dir)
      .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
      .map((f) => readFileSync(join(dir, f), 'utf8'))
      .join('\n')
    const match = source.match(/\bTermsVersion\s*(?:string\s*)?=\s*"([^"]+)"/)
    expect(match?.[1]).toBe(TERMS_VERSION)
  })

  it('match the version the site publishes', () => {
    const legal = readFileSync(
      join(import.meta.dirname, '../../../site/src/config/legal.ts'),
      'utf8',
    )
    expect(legal.match(/\bTERMS_VERSION\s*=\s*'([^']+)'/)?.[1]).toBe(TERMS_VERSION)
  })

  it('link the published pages', () => {
    expect(termsUrl).toBe('https://permaily.com/terms/')
    expect(privacyUrl).toBe('https://permaily.com/privacy/')
  })
})
