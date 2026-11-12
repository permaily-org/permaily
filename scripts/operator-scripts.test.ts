import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { join } from 'node:path'

// The operator's scripts are run against a fake enclave that answers as a
// given release does: the current one, or an older one that leaves fields out.
// They need bash, curl and jq, as they do on the operator's computer.

type Answer = { status: number; body: unknown }
let answers: Record<string, Answer> = {}
let server: ReturnType<typeof Bun.serve>

beforeAll(() => {
  server = Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    fetch(request) {
      const url = new URL(request.url)
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
})
