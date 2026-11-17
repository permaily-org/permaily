import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { messageFor } from '../../src/pages/auth/messages.ts'
import { ok, refused, server } from '../helpers/server.ts'

/** The code a request fails with. */
async function failure(request: Promise<unknown>): Promise<string> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return api.errorCode(error)
  }
  throw new Error('the request did not fail')
}

const sha = 'a'.repeat(64)
const head = { n: 3, verdict: 'ok', sha256: sha, at: '2026-11-17T12:00:00Z' }

function walkOf(over: Record<string, unknown> = {}) {
  return {
    head,
    links: [
      {
        n: 3,
        verdict: 'ok',
        kind: 'receipts',
        at: '2026-11-17T12:00:00Z',
        prev: sha,
        receipts: [{ index: 0, id: 'rcp_x', sha256: sha, verdict: 'ok', receipt: { v: 2 } }],
      },
      { n: 2, verdict: 'lost' },
    ],
    next: { n: 2, index: 0 },
    ...over,
  }
}

function exportOf(over: Record<string, unknown> = {}) {
  return {
    v: 1,
    account: 'acc_ana',
    head,
    links: [{ n: 3, verdict: 'ok', doc: 'e30=' }],
    next: { n: 3, index: 0 },
    ...over,
  }
}

// ADR 0037, pull request 3: the calls the Activity screen (step 8) and the export are built on.
describe('the receipts’ calls', () => {
  it('reads a page of the walk, from the head or from where the page before ended', async () => {
    server.on('GET /api/v1/receipts', ok(walkOf()))
    const page = await api.receipts()
    expect(page.head).toEqual(head)
    expect(page.links.map((l) => [l.n, l.verdict, l.receipts.length])).toEqual([
      [3, 'ok', 1],
      [2, 'lost', 0],
    ])
    expect(page.done).toBe(false)
    server.on('GET /api/v1/receipts?before=2.0&limit=10', ok(walkOf({ links: [], done: true })))
    expect((await api.receipts({ before: page.next, limit: 10 })).done).toBe(true)

    for (const answer of [
      null,
      walkOf({ links: null }),
      walkOf({ head: null }),
      walkOf({ next: 'x' }),
    ]) {
      server.on('GET /api/v1/receipts', ok(answer))
      expect(await failure(api.receipts())).toBe('other')
    }
  })

  it('says each refusal of the chain in words', async () => {
    for (const [status, code] of [
      [400, 'invalid_before'],
      [503, 'receipts_unreadable'],
      [503, 'busy'],
    ] as const) {
      server.on('GET /api/v1/receipts', refused(status, code))
      expect(await failure(api.receipts())).toBe(code)
      expect(messageFor(code)).not.toBe(messageFor('other'))
    }
  })

  it('downloads every page of the export, one line a page, and the head the person notes', async () => {
    server.on('GET /api/v1/receipts/export', ok(exportOf()))
    server.on(
      'GET /api/v1/receipts/export?before=3.0',
      ok(
        exportOf({
          head: { ...head, n: 4 },
          links: [{ n: 2, verdict: 'lost' }],
          next: { n: 2, index: 0 },
        }),
      ),
    )
    server.on(
      'GET /api/v1/receipts/export?before=2.0',
      ok(
        exportOf({
          links: [{ n: 1, verdict: 'ok', doc: 'e30=' }],
          next: { n: 0, index: 0 },
          done: true,
        }),
      ),
    )
    const got = await api.exportReceipts()
    expect(got.head).toEqual(head)
    const lines = got.file.split('\n')
    expect(lines).toHaveLength(4)
    expect(lines[3]).toBe('')
    expect(lines.slice(0, 3).map((l) => JSON.parse(l).links[0].n)).toEqual([3, 2, 1])
    expect(JSON.parse(lines[0])).toEqual(exportOf())
  })

  it('refuses an export whose pages do not move on, or are not pages', async () => {
    server.on('GET /api/v1/receipts/export', ok(exportOf()))
    server.on('GET /api/v1/receipts/export?before=3.0', ok(exportOf()))
    expect(await failure(api.exportReceipts())).toBe('other')
    for (const answer of [
      null,
      exportOf({ links: 'x' }),
      exportOf({ next: null }),
      exportOf({ head: 3 }),
    ]) {
      server.on('GET /api/v1/receipts/export', ok(answer))
      expect(await failure(api.exportReceipts())).toBe('other')
    }
    server.on('GET /api/v1/receipts/export', refused(503, 'busy'))
    expect(await failure(api.exportReceipts())).toBe('busy')
  })
})
