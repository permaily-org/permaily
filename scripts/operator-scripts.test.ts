import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { join } from 'node:path'

// The operator's scripts are run against a fake enclave that answers as a
// given release does: the current one, or an older one that leaves fields out.
// They need bash, curl and jq, as they do on the operator's computer.

type Answer = { status: number; body: unknown }
let answers: Record<string, Answer> = {}
// asked is every request the fake enclave was sent: method, path and query.
let asked: string[] = []
let server: ReturnType<typeof Bun.serve>

beforeAll(() => {
  server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch(request) {
      const url = new URL(request.url)
      asked.push(`${request.method} ${url.pathname}${url.search}`)
      const answer = answers[`${request.method} ${url.pathname}`]
      if (!answer) return Response.json({ error: { message: 'no such endpoint' } }, { status: 404 })
      return Response.json(answer.body, { status: answer.status })
    },
  })
})

afterAll(() => server.stop(true))

async function run(script: string, ...args: string[]) {
  const child = Bun.spawn(['bash', join(import.meta.dir, script), ...args], {
    env: {
      PATH: process.env.PATH ?? '',
      HOME: '/nonexistent',
      PERMAILY_API: `http://127.0.0.1:${server.port}`,
      PERMAILY_ADMIN_TOKEN: 'test-token',
    },
    stdin: 'ignore',
    stdout: 'pipe',
    stderr: 'pipe',
  })
  const [stdout, stderr, exit] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ])
  return { stdout, stderr, exit }
}

describe('invite.sh', () => {
  test('prints the codes and their identifiers', async () => {
    answers = {
      'POST /v1/admin/invitations': {
        status: 200,
        body: {
          invitations: ['code-one', 'code-two'],
          ids: ['inv_one', 'inv_two'],
          expires_at: '2026-12-12T10:00:00Z',
        },
      },
    }
    const { stdout, stderr, exit } = await run('invite.sh', '2')
    expect(exit).toBe(0)
    expect(stdout).toBe('code-one\ncode-two\n')
    expect(stderr).toContain('inv_one\ninv_two\n')
  })

  test('against a release that gives no identifiers, prints the codes and says so', async () => {
    answers = {
      'POST /v1/admin/invitations': {
        status: 200,
        body: { invitations: ['code-one'], expires_at: '2026-12-12T10:00:00Z' },
      },
    }
    const { stdout, stderr, exit } = await run('invite.sh')
    expect(exit).toBe(0)
    expect(stdout).toBe('code-one\n')
    expect(stderr).toContain('gives no identifiers')
    expect(stderr).not.toContain('jq:')
  })
})

describe('accounts.sh', () => {
  const record = {
    id: 'acc_aaaaaaaaaaaaaaaaaaaaaaaaaa',
    state: 'active',
    address: 'person@example.com',
    name: '',
    language: 'en',
    created_at: '2026-11-01T10:00:00Z',
    address_verified_at: '2026-11-01T10:00:00Z',
    passkeys: 0,
    passkey_only: false,
    sessions: 1,
  }
  const line = (stdout: string, name: string) =>
    stdout.split('\n').find((l) => l.startsWith(`${name}:`)) ?? ''

  test('a person who is no admin, on the current release, is told plainly', async () => {
    answers = {
      [`GET /v1/admin/accounts/${record.id}`]: { status: 200, body: { ...record, providers: [] } },
    }
    const { stdout, exit } = await run('accounts.sh', record.id)
    expect(exit).toBe(0)
    expect(line(stdout, 'Admin')).toMatch(/:\s+no$/)
    expect(line(stdout, 'Terms')).toMatch(/:\s+none agreed$/)
  })

  test('an admin is named', async () => {
    answers = {
      [`GET /v1/admin/accounts/${record.id}`]: {
        status: 200,
        body: { ...record, providers: [], admin: 'super' },
      },
    }
    const { stdout } = await run('accounts.sh', record.id)
    expect(line(stdout, 'Admin')).toMatch(/the super admin$/)
  })

  test('an older release that says nothing of admins is not taken for "no"', async () => {
    answers = { [`GET /v1/admin/accounts/${record.id}`]: { status: 200, body: record } }
    const { stdout, exit } = await run('accounts.sh', record.id)
    expect(exit).toBe(0)
    expect(line(stdout, 'Admin')).toContain('not said by this server (an older release)')
    expect(line(stdout, 'Terms')).toContain('not said by this server (an older release)')
    expect(line(stdout, 'Providers')).toContain('not said by this server (an older release)')
  })

  test('an account says how many mailboxes it has, or that an older release does not say', async () => {
    answers = {
      [`GET /v1/admin/accounts/${record.id}`]: {
        status: 200,
        body: { ...record, providers: [], mailboxes: 2 },
      },
    }
    expect(line((await run('accounts.sh', record.id)).stdout, 'Mailboxes')).toMatch(/:\s+2$/)
    answers = { [`GET /v1/admin/accounts/${record.id}`]: { status: 200, body: record } }
    expect(line((await run('accounts.sh', record.id)).stdout, 'Mailboxes')).toContain(
      'not said by this server (an older release)',
    )
  })

  test('the list shows how many mailboxes each account has', async () => {
    answers = {
      'GET /v1/admin/accounts': {
        status: 200,
        body: { accounts: [{ ...record, providers: [], mailboxes: 3 }], unreadable: 0 },
      },
    }
    const { stdout, exit } = await run('accounts.sh')
    expect(exit).toBe(0)
    const [head, row] = stdout.trim().split('\n')
    expect(head).toMatch(/SESSIONS\s+MAILBOXES$/)
    expect(row).toMatch(/\s3$/)
  })
})

describe('accounts.sh and mailboxes', () => {
  const account = 'acc_aaaaaaaaaaaaaaaaaaaaaaaaaa'
  const mailbox = 'mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb'
  const paused = {
    id: mailbox,
    account,
    state: 'paused',
    created_at: '2026-11-01T10:00:00Z',
    retention_days: 90,
    paused_at: '2026-11-12T09:30:00Z',
    verified: true,
  }

  test("an account's mailboxes: state and dates, never an address or a label", async () => {
    answers = {
      [`GET /v1/admin/accounts/${account}/mailboxes`]: {
        status: 200,
        body: {
          mailboxes: [
            paused,
            {
              id: 'mbx_cccccccccccccccccccccccccc',
              account,
              state: 'unreadable',
              created_at: '2026-11-02T10:00:00Z',
              retention_days: 0,
              verified: false,
            },
          ],
        },
      },
    }
    asked = []
    const { stdout, stderr, exit } = await run('accounts.sh', 'mailboxes', account)
    expect(exit).toBe(0)
    const rows = stdout.trim().split('\n')
    expect(rows[0]).toMatch(
      /^MAILBOX\s+STATE\s+CREATED \(UTC\)\s+KEEPS\s+RECEIVING SINCE\s+GOOGLE CONFIRMED$/,
    )
    expect(rows[1]).toContain(mailbox)
    expect(rows[1]).toContain('paused since 2026-11-12 09:30 UTC')
    expect(rows[1]).toContain('90 days')
    // A lost or forked mailbox (ADR 0026, "An unreadable mailbox"): its identifier, when it was added
    // and that it cannot be read, and no retention or Google confirmation, which its record would say.
    expect(rows[2]).toContain('mbx_cccccccccccccccccccccccccc')
    expect(rows[2]).toContain(
      'unreadable: what Permaily stores for it cannot be read; it accepts no mail and nobody can open it',
    )
    expect(rows[2]).toContain('2026-11-02 10:00')
    expect(rows[2]).not.toContain('0 days')
    expect(rows[2]).toMatch(/\s-\s+-\s+-$/)
    expect(stderr).toContain('2 mailbox(es) of')
    expect(asked).toEqual([`GET /v1/admin/accounts/${account}/mailboxes`])
  })

  test('an account named by its address is looked up first', async () => {
    answers = {
      'GET /v1/admin/accounts': {
        status: 200,
        body: { accounts: [{ id: account, address: 'person@example.com' }], unreadable: 0 },
      },
      [`GET /v1/admin/accounts/${account}/mailboxes`]: { status: 200, body: { mailboxes: [] } },
    }
    asked = []
    const { exit, stderr } = await run('accounts.sh', 'mailboxes', 'person@example.com')
    expect(exit).toBe(0)
    expect(stderr).toContain('0 mailbox(es)')
    expect(asked).toEqual([
      'GET /v1/admin/accounts?address=person%40example.com',
      `GET /v1/admin/accounts/${account}/mailboxes`,
    ])
  })

  test('finding a mailbox by an address shows whose it is, and only that', async () => {
    answers = { 'GET /v1/admin/mailboxes': { status: 200, body: { mailboxes: [paused] } } }
    asked = []
    const { stdout, exit } = await run(
      'accounts.sh',
      'mailbox',
      'find',
      'k7m2Q9@inbox.permaily.com',
    )
    expect(exit).toBe(0)
    expect(asked).toEqual(['GET /v1/admin/mailboxes?address=k7m2Q9%40inbox.permaily.com'])
    const line = (name: string) => stdout.split('\n').find((l) => l.startsWith(`${name}:`)) ?? ''
    expect(line('Mailbox')).toContain(mailbox)
    expect(line('Account')).toContain(account)
    expect(line('State')).toContain('paused since 2026-11-12 09:30 UTC')
    expect(line('Google confirmed')).toMatch(/yes$/)
    expect(stdout).not.toContain('inbox.permaily.com')
  })

  test('an address that leads to no mailbox says so and fails', async () => {
    answers = { 'GET /v1/admin/mailboxes': { status: 200, body: { mailboxes: [] } } }
    const { stderr, exit } = await run(
      'accounts.sh',
      'mailbox',
      'find',
      'nobody@inbox.permaily.com',
    )
    expect(exit).toBe(1)
    expect(stderr).toContain('No mailbox has that address')
  })

  test('pausing and resuming ask the enclave, after a yes', async () => {
    answers = {
      [`POST /v1/admin/mailboxes/${mailbox}/pause`]: { status: 200, body: paused },
      [`POST /v1/admin/mailboxes/${mailbox}/resume`]: {
        status: 200,
        body: {
          ...paused,
          state: 'receiving',
          paused_at: undefined,
          receiving_since: '2026-11-05T10:00:00Z',
        },
      },
    }
    asked = []
    let out = await run('accounts.sh', 'mailbox', 'pause', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).toContain(`This will pause the mailbox ${mailbox}`)
    expect(out.stdout).toContain('paused since 2026-11-12 09:30 UTC')
    out = await run('accounts.sh', 'mailbox', 'resume', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stdout).toMatch(/State:\s+receiving/)
    expect(asked).toEqual([
      `POST /v1/admin/mailboxes/${mailbox}/pause`,
      `POST /v1/admin/mailboxes/${mailbox}/resume`,
    ])
    // Without a yes (no terminal to answer), nothing is asked.
    asked = []
    out = await run('accounts.sh', 'mailbox', 'pause', mailbox)
    expect(out.exit).not.toBe(0)
    expect(asked).toEqual([])
  })

  test('what the enclave refuses is said, and fails', async () => {
    answers = {
      [`POST /v1/admin/mailboxes/${mailbox}/pause`]: {
        status: 429,
        body: {
          error: {
            code: 'too_many_requests',
            message: 'too many attempts; wait a while and try again',
          },
        },
      },
    }
    const { stderr, exit } = await run('accounts.sh', 'mailbox', 'pause', mailbox, '--yes')
    expect(exit).toBe(1)
    expect(stderr).toContain('too many attempts')
    expect(stderr).toContain('HTTP 429')
  })

  test('there is no command to disconnect, read or rename a mailbox, and a mailbox is named by its identifier', async () => {
    for (const args of [
      ['mailbox', 'disconnect', mailbox],
      ['mailbox', 'read', mailbox],
      ['mailbox', 'delete', mailbox],
      ['mailbox', 'pause', 'not-a-mailbox', '--yes'],
      ['mailbox', 'pause', `${mailbox}x`, '--yes'],
      ['mailbox', 'find', 'no-at-sign'],
      ['mailboxes'],
      ['mailboxes', account, '--yes'],
    ]) {
      asked = []
      const { exit } = await run('accounts.sh', ...args)
      expect(exit).toBe(2)
      expect(asked).toEqual([])
    }
  })
})
