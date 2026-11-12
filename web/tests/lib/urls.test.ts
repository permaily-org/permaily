import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { parseDestination } from '../../src/pages/inbox/Link.tsx'

// ADR 0026, tests 24 and 25, in Node's WHATWG URL: the table Go writes (internal/mailhtml/testdata/urls.json).

type Row = {
  input: string
  kept: boolean
  url?: string
  protocol?: string
  hostname?: string
  port?: string
  host?: string
  ipv4?: boolean
}

const table: { urls: Row[] } = JSON.parse(
  readFileSync(resolve(__dirname, '../../../internal/mailhtml/testdata/urls.json'), 'utf8'),
)

describe('the URL agreement table', () => {
  it('24: every kept URL reads back as the server wrote it, and the link page takes it', () => {
    const kept = table.urls.filter((row) => row.kept)
    expect(kept.length).toBeGreaterThan(20)
    for (const row of kept) {
      const url = new URL(row.url as string)
      expect([url.href, url.protocol, url.hostname, url.port, url.host], row.input).toEqual([
        row.url,
        row.protocol,
        row.hostname,
        row.port ?? '',
        row.host,
      ])
      expect(
        parseDestination({
          link: '0.AAAAAAAAAAAAAAAA',
          url: row.url as string,
          host: row.host as string,
        })?.url.href,
      ).toBe(row.url)
    }
  })

  it('25: every refused numeric spelling is an IPv4 address to a browser', () => {
    const numeric = table.urls.filter((row) => row.ipv4)
    expect(numeric.length).toBeGreaterThanOrEqual(5)
    for (const row of numeric) {
      expect(row.kept).toBe(false)
      expect(new URL(row.input).hostname, row.input).toMatch(/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/)
    }
  })
})
