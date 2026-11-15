import { afterAll, beforeAll, describe, expect, setDefaultTimeout, test } from 'bun:test'
import { execFileSync } from 'node:child_process'
import { createHash, createPublicKey } from 'node:crypto'
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { createServer as createHTTPServer } from 'node:http'
import { connect, createServer as createTCPServer, type Server, type Socket } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// The operator's scripts are run against a fake enclave that answers as a
// given release does: the current one, or an older one that leaves fields out.
// They need bash, curl, jq and openssl, as they do on the operator's computer
// (openssl only here, to make the test certificates).
//
// The fake enclave serves TLS with key A, under a certificate of a test CA
// the scripts are told to trust (PERMAILY_CA_FILE). permaily-verify is
// replaced by a stub that reports what the real one reports after it attested
// the enclave: that key A is the one inside it. An impostor serves the same
// name with key B, under a certificate of the same CA: valid for every check
// but the key, like a second certificate a CA was made to issue.

// Some tests run a script dozens of times: more than the default 5 s on a busy runner.
setDefaultTimeout(30_000)

type Answer = { status: number; body: unknown }
let answers: Record<string, Answer> = {}
// asked is every request the fake enclave was sent: method, path and query.
let asked: string[] = []
// seen is every request either server was sent, whole: method, path, headers and body.
type Seen = { server: 'enclave' | 'impostor'; request: string }
let seen: Seen[] = []
let enclave: ReturnType<typeof Bun.serve>
let impostor: ReturnType<typeof Bun.serve>
let dir: string
// attested is sha256(SubjectPublicKeyInfo) of key A, in hex: what the verifier attests.
let attested: string

function openssl(...args: string[]) {
  execFileSync('openssl', args, { cwd: dir, stdio: 'pipe' })
}

// leaf makes a key and a certificate for 127.0.0.1, ::1, localhost, the dev
// names the tests use and two public names, signed by the test CA: the
// impostor's is valid for every name a script could be led to.
function leaf(name: string) {
  openssl(
    'req',
    '-newkey',
    'ec',
    '-pkeyopt',
    'ec_paramgen_curve:P-256',
    '-nodes',
    '-keyout',
    `${name}.key`,
    '-subj',
    '/CN=127.0.0.1',
    '-out',
    `${name}.csr`,
  )
  writeFileSync(
    join(dir, `${name}.ext`),
    'subjectAltName=IP:127.0.0.1,IP:::1,DNS:localhost,DNS:dev.localhost,DNS:dev.permaily.test,DNS:api.permaily.com,DNS:remote.example\nextendedKeyUsage=serverAuth\n',
  )
  openssl(
    'x509',
    '-req',
    '-in',
    `${name}.csr`,
    '-CA',
    'ca.pem',
    '-CAkey',
    'ca.key',
    '-CAcreateserial',
    '-days',
    '2',
    '-extfile',
    `${name}.ext`,
    '-out',
    `${name}.pem`,
  )
  return {
    cert: readFileSync(join(dir, `${name}.pem`), 'utf8'),
    key: readFileSync(join(dir, `${name}.key`), 'utf8'),
  }
}

function serve(name: Seen['server'], tls: { cert: string; key: string }) {
  return Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    tls,
    async fetch(request) {
      const url = new URL(request.url)
      const body = await request.text()
      seen.push({
        server: name,
        request: `${request.method} ${url.pathname}${url.search}\n${JSON.stringify([...request.headers])}\n${body}`,
      })
      if (name === 'impostor')
        return Response.json({ error: { message: 'thanks' } }, { status: 200 })
      asked.push(`${request.method} ${url.pathname}${url.search}`)
      const answer = answers[`${request.method} ${url.pathname}`]
      if (!answer) return Response.json({ error: { message: 'no such endpoint' } }, { status: 404 })
      return Response.json(answer.body, { status: answer.status })
    },
  })
}

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'operator-scripts-'))
  openssl(
    'req',
    '-x509',
    '-newkey',
    'ec',
    '-pkeyopt',
    'ec_paramgen_curve:P-256',
    '-nodes',
    '-keyout',
    'ca.key',
    '-subj',
    '/CN=Permaily test CA',
    '-days',
    '2',
    '-out',
    'ca.pem',
  )
  const a = leaf('a')
  const b = leaf('b')
  attested = createHash('sha256')
    .update(createPublicKey(a.key).export({ type: 'spki', format: 'der' }))
    .digest('hex')
  enclave = serve('enclave', a)
  impostor = serve('impostor', b)
  writeFileSync(join(dir, 'pcrs.json'), '{}')
  // The stub verifier: it records how it was run, and answers as permaily-verify -json does.
  writeFileSync(
    join(dir, 'permaily-verify'),
    `#!/usr/bin/env bash
printf '%s\\n' "$*" >> "${dir}/verify-runs"
url=\${!#}
if [[ -n \${STUB_VERIFY_FAILS:-} ]]; then
  jq -n --arg url "$url" '{ok: false, hosts: [{url: $url, ok: false, error: "PCR0 does not match"}]}'
  exit 1
fi
jq -n --arg url "$url" --arg spki "\${STUB_ATTESTED}" '{ok: true, hosts: [{url: $url, ok: true, tls_spki_sha256: $spki}]}'
`,
  )
  chmodSync(join(dir, 'permaily-verify'), 0o755)
})

afterAll(() => {
  enclave.stop(true)
  impostor.stop(true)
  rmSync(dir, { recursive: true, force: true })
})

// How a script is run: from another copy of the scripts (scripts), under
// bash -x (trace), or with something on standard input (input).
type How = { scripts?: string; trace?: boolean; input?: string }

async function runWith(env: Record<string, string>, script: string, ...args: string[]) {
  return runHow({}, env, script, ...args)
}

async function runHow(how: How, env: Record<string, string>, script: string, ...args: string[]) {
  const bash = how.trace ? ['bash', '-x'] : ['bash']
  const child = Bun.spawn([...bash, join(how.scripts ?? import.meta.dir, script), ...args], {
    env: {
      PATH: process.env.PATH ?? '',
      HOME: '/nonexistent',
      PERMAILY_API: `https://127.0.0.1:${enclave.port}`,
      PERMAILY_ADMIN_TOKEN: 'test-token',
      PERMAILY_CA_FILE: join(dir, 'ca.pem'),
      PERMAILY_VERIFY: join(dir, 'permaily-verify'),
      PERMAILY_PCRS: join(dir, 'pcrs.json'),
      STUB_ATTESTED: attested,
      ...env,
    },
    stdin: how.input === undefined ? 'ignore' : new Blob([how.input]),
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

const run = (script: string, ...args: string[]) => runWith({}, script, ...args)

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
      [`POST /v1/admin/mailboxes/${mailbox}/pause`]: {
        status: 200,
        body: { ...paused, changed: true },
      },
      [`POST /v1/admin/mailboxes/${mailbox}/resume`]: {
        status: 200,
        body: {
          ...paused,
          state: 'receiving',
          paused_at: undefined,
          receiving_since: '2026-11-05T10:00:00Z',
          changed: true,
        },
      },
    }
    asked = []
    let out = await run('accounts.sh', 'mailbox', 'pause', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).toContain(`This will pause the mailbox ${mailbox}`)
    expect(out.stderr).toContain('Done. The mailbox now:')
    expect(out.stdout).toContain('paused since 2026-11-12 09:30 UTC')
    out = await run('accounts.sh', 'mailbox', 'resume', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).toContain('Done. The mailbox now:')
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

  test('a pause or a resume that changed nothing says so, and not that it was done', async () => {
    const receiving = {
      ...paused,
      state: 'receiving',
      paused_at: undefined,
      receiving_since: '2026-11-05T10:00:00Z',
    }
    answers = {
      [`POST /v1/admin/mailboxes/${mailbox}/pause`]: {
        status: 200,
        body: { ...paused, changed: false },
      },
      [`POST /v1/admin/mailboxes/${mailbox}/resume`]: {
        status: 200,
        body: { ...receiving, changed: false },
      },
    }
    let out = await run('accounts.sh', 'mailbox', 'pause', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).toContain('It was already paused: nothing was sent and nothing was counted.')
    expect(out.stderr).not.toContain('Done.')
    expect(out.stdout).toContain('paused since 2026-11-12 09:30 UTC')
    out = await run('accounts.sh', 'mailbox', 'resume', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).toContain('It was not paused: nothing was sent and nothing was counted.')
    expect(out.stderr).not.toContain('Done.')
    // An enclave of an earlier release does not say: nothing is claimed.
    answers = {
      [`POST /v1/admin/mailboxes/${mailbox}/pause`]: { status: 200, body: paused },
    }
    out = await run('accounts.sh', 'mailbox', 'pause', mailbox, '--yes')
    expect(out.exit).toBe(0)
    expect(out.stderr).not.toContain('Done.')
    expect(out.stderr).toContain('The mailbox now:')
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

  test('a failed lookup never prints the address it looked for', async () => {
    const address = 'k7m2Q9@inbox.permaily.com'
    // An older server without the route (404), and one that fails (500).
    for (const answer of [
      undefined,
      { status: 500, body: { error: { message: 'internal error' } } },
    ]) {
      answers = answer ? { 'GET /v1/admin/mailboxes': answer } : {}
      const { stdout, stderr, exit } = await run('accounts.sh', 'mailbox', 'find', address)
      expect(exit).toBe(1)
      expect(stderr).toContain(`HTTP ${answer?.status ?? 404}`)
      for (const shown of [address, encodeURIComponent(address), 'k7m2Q9', 'address=']) {
        expect(stdout + stderr).not.toContain(shown)
      }
    }
  })

  test('an identifier the server answers is checked before it is put in a URL', async () => {
    for (const id of [
      '../invitations?probe=',
      `${account}/../../invitations`,
      `${account}\n../x`,
      '',
    ]) {
      answers = {
        'GET /v1/admin/accounts': {
          status: 200,
          body: { accounts: [{ id, address: 'person@example.com' }], unreadable: 0 },
        },
      }
      for (const args of [
        ['mailboxes', 'person@example.com'],
        ['block', 'person@example.com', '--yes'],
      ]) {
        asked = []
        const { stderr, exit } = await run('accounts.sh', ...args)
        expect(exit).toBe(1)
        expect(stderr).toContain(
          'The enclave answered an account identifier that is not one. Nothing was done.',
        )
        expect(asked).toEqual(['GET /v1/admin/accounts?address=person%40example.com'])
      }
    }
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

describe('the token and secrets go only to the attested key', () => {
  const secret = 'client-secret-7f3a9c'
  const provider = {
    'PUT /v1/admin/providers/google': {
      status: 200,
      body: { provider: 'google', client_id: 'id-1', set_at: '2026-11-15T10:00:00Z' },
    },
  }
  const secretFile = () => {
    const file = join(dir, 'secret')
    writeFileSync(file, `${secret}\n`)
    return file
  }

  test('a secret reaches the enclave whose key was attested', async () => {
    answers = provider
    seen = []
    const { stdout, stderr, exit } = await run('provider.sh', 'set', 'google', 'id-1', secretFile())
    expect(exit).toBe(0)
    expect(stderr).toContain('google is on')
    expect(seen.map((s) => s.server)).toEqual(['enclave'])
    expect(seen[0].request).toContain(secret)
    expect(stdout + stderr).not.toContain(secret)
  })

  test('a server with another valid certificate for the name gets neither the secret nor the token', async () => {
    answers = provider
    const impostorAPI = { PERMAILY_API: `https://127.0.0.1:${impostor.port}` }
    for (const args of [
      ['provider.sh', 'set', 'google', 'id-1', secretFile()],
      ['provider.sh'],
      ['provider.sh', 'remove', 'google'],
      ['invite.sh'],
      ['invite.sh', 'list'],
      ['accounts.sh'],
      ['accounts.sh', 'stats'],
      ['accounts.sh', 'mailbox', 'pause', 'mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb', '--yes'],
    ]) {
      seen = []
      const { stdout, stderr, exit } = await runWith(
        impostorAPI,
        ...(args as [string, ...string[]]),
      )
      // The impostor saw no request at all: the connection ends at its key.
      expect({ args, seen }).toEqual({ args, seen: [] })
      expect({ args, exit: exit !== 0 }).toEqual({ args, exit: true })
      expect(stdout + stderr).not.toContain(secret)
      expect(stderr).toContain('nothing was sent')
    }
  })

  test('when verification fails, nothing is sent', async () => {
    answers = provider
    for (const args of [
      ['provider.sh', 'set', 'google', 'id-1', secretFile()],
      ['invite.sh'],
      ['accounts.sh'],
      ['handoff.sh', dir, dir],
      ['sigstore-trust-root.sh', '--send'],
    ]) {
      seen = []
      const { stderr, exit } = await runWith(
        { STUB_VERIFY_FAILS: '1' },
        ...(args as [string, ...string[]]),
      )
      expect({ args, exit: exit !== 0 }).toEqual({ args, exit: true })
      expect({ args, seen }).toEqual({ args, seen: [] })
      expect(stderr).toContain('PCR0 does not match')
      expect(stderr).toContain('Nothing was sent')
    }
  })

  test('without the measurements to verify against, nothing is sent', async () => {
    seen = []
    const { stderr, exit } = await runWith({ PERMAILY_PCRS: '' }, 'invite.sh')
    expect(exit).toBe(2)
    expect(seen).toEqual([])
    expect(stderr).toContain('PERMAILY_PCRS')
  })

  test('the verifier is asked about the enclave the requests go to, with the given measurements and CA', async () => {
    answers = { 'GET /v1/admin/invitations': { status: 200, body: { invitations: [] } } }
    rmSync(join(dir, 'verify-runs'), { force: true })
    const { exit } = await run('invite.sh', 'list')
    expect(exit).toBe(0)
    expect(readFileSync(join(dir, 'verify-runs'), 'utf8')).toBe(
      `-pcrs ${join(dir, 'pcrs.json')} -ca-file ${join(dir, 'ca.pem')} -json https://127.0.0.1:${enclave.port}\n`,
    )
  })

  test('an enclave in dev mode, which cannot be verified, is reached only on a name that is never production', async () => {
    answers = { 'GET /v1/admin/invitations': { status: 200, body: { invitations: [] } } }
    rmSync(join(dir, 'verify-runs'), { force: true })
    const dev = await runWith(
      { PERMAILY_DEV_UNVERIFIED: '1', STUB_VERIFY_FAILS: '1' },
      'invite.sh',
      'list',
    )
    expect(dev.exit).toBe(0)
    expect(dev.stderr).toContain('Not verified (PERMAILY_DEV_UNVERIFIED)')
    for (const api of [
      'https://api.permaily.com',
      'https://api.permaily.com:443/',
      'https://test.example.com',
      'https://localhost.example.com',
      'https://127.0.0.1.example.com',
    ]) {
      seen = []
      const out = await runWith(
        { PERMAILY_DEV_UNVERIFIED: '1', PERMAILY_API: api },
        'invite.sh',
        'list',
      )
      expect({ api, exit: out.exit }).toEqual({ api, exit: 2 })
      expect(out.stderr).toContain('Nothing was sent')
    }
    expect(() => readFileSync(join(dir, 'verify-runs'))).toThrow()
  })

  test('a plain http address is refused before anything is sent', async () => {
    seen = []
    const { exit } = await runWith(
      { PERMAILY_API: `http://127.0.0.1:${enclave.port}` },
      'accounts.sh',
    )
    expect(exit).toBe(2)
    expect(seen).toEqual([])
  })

  test('a redirect is not followed', async () => {
    answers = {
      'GET /v1/admin/invitations': { status: 307, body: { error: { message: 'moved' } } },
    }
    seen = []
    const { exit } = await run('invite.sh', 'list')
    expect(exit).toBe(1)
    expect(seen.length).toBe(1)
  })

  test('what an answer holds besides its fixed fields is never printed', async () => {
    // An answer that echoes the request back, with no error.message, and one that is not JSON.
    for (const body of [{ echo: secret, token: 'test-token' }, `${secret} test-token`]) {
      answers = { 'PUT /v1/admin/providers/google': { status: 400, body } }
      const out = await run('provider.sh', 'set', 'google', 'id-1', secretFile())
      expect(out.exit).toBe(1)
      expect(out.stdout + out.stderr).not.toContain(secret)
      expect(out.stdout + out.stderr).not.toContain('test-token')
      expect(out.stderr).toContain('HTTP 400')
      answers = { 'GET /v1/admin/accounts': { status: 500, body } }
      const listed = await run('accounts.sh')
      expect(listed.exit).toBe(1)
      expect(listed.stdout + listed.stderr).not.toContain('test-token')
    }
    // error.message is a fixed field, and is printed without control characters.
    answers = {
      'PUT /v1/admin/providers/google': {
        status: 400,
        body: { error: { code: 'bad_request', message: 'client_id is required\u001b[2J' } },
      },
    }
    const out = await run('provider.sh', 'set', 'google', 'id-1', secretFile())
    expect(out.stderr).toContain('client_id is required')
    expect(out.stderr).not.toContain('\u001b')
  })
})

test('the admin token goes only through attested.sh: enclave_curl --token or with_token, after attest', () => {
  const scripts = readdirSync(import.meta.dir).filter(
    (f) => f.endsWith('.sh') && f !== 'attested.sh',
  )
  const carriers = []
  for (const name of scripts) {
    const text = readFileSync(join(import.meta.dir, name), 'utf8')
    // No script makes the header or expands the token itself: under bash -x
    // that line would be traced.
    expect({
      name,
      itself: /Authorization|\$\{?token\b|PERMAILY_ADMIN_TOKEN:-/.test(
        text.replace(/^\s*#.*$/gm, ''),
      ),
    }).toEqual({ name, itself: false })
    if (!/enclave_curl --token|with_token /.test(text)) continue
    carriers.push(name)
    // One command a line: continuations joined. Every curl that carries it is enclave_curl.
    for (const command of text.replace(/\\\n/g, ' ').split('\n')) {
      if (!command.includes('--token')) continue
      expect({ name, command, pinned: /\benclave_curl --token\b/.test(command) }).toEqual({
        name,
        command,
        pinned: true,
      })
    }
    expect({
      name,
      sources:
        text.includes('source "$(dirname "$0")/attested.sh"') &&
        /^\s*load_token$/m.test(text) &&
        /^\s*attest$/m.test(text),
    }).toEqual({ name, sources: true })
  }
  expect(carriers.sort()).toEqual([
    'accounts.sh',
    'handoff.sh',
    'invite.sh',
    'provider.sh',
    'sigstore-trust-root.sh',
  ])
  // In attested.sh, the header is made in one place, behind the pin.
  const helper = readFileSync(join(import.meta.dir, 'attested.sh'), 'utf8')
  expect(helper.match(/Authorization: Bearer/g)?.length).toBe(1)
})

// Proxies that lead every connection to the impostor, as a hostile proxy, a
// hijacked DNS answer or a mistaken hosts entry would: each records where it
// was asked to connect.
let proxied: string[] = []
let httpProxy: ReturnType<typeof createHTTPServer>
let socksProxy: Server
const sockets = new Set<Socket>()

function toImpostor(socket: Socket, ready: () => void, head?: Buffer) {
  sockets.add(socket)
  const upstream = connect(impostor.port ?? 0, '127.0.0.1', () => {
    ready()
    if (head?.length) upstream.write(head)
    socket.pipe(upstream).pipe(socket)
  })
  sockets.add(upstream)
  socket.on('error', () => upstream.destroy())
  upstream.on('error', () => socket.destroy())
  socket.on('close', () => upstream.destroy())
}

async function listen(server: Server | ReturnType<typeof createHTTPServer>) {
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  return (server.address() as { port: number }).port
}

let proxies: Record<string, Record<string, string>> = {}

beforeAll(async () => {
  httpProxy = createHTTPServer()
  httpProxy.on('connect', (request, socket: Socket, head) => {
    proxied.push(`http ${request.url}`)
    toImpostor(socket, () => socket.write('HTTP/1.1 200 Connection Established\r\n\r\n'), head)
  })
  // SOCKS5 without authentication: enough for curl's socks5 and socks5h.
  socksProxy = createTCPServer((socket) => {
    sockets.add(socket)
    socket.once('data', () => {
      socket.write(Buffer.from([5, 0]))
      socket.once('data', (ask: Buffer) => {
        let host = `atyp ${ask[3]}`
        let at = 4
        if (ask[3] === 1) {
          host = [...ask.subarray(4, 8)].join('.')
          at = 8
        } else if (ask[3] === 3) {
          host = ask.subarray(5, 5 + ask[4]).toString()
          at = 5 + ask[4]
        } else if (ask[3] === 4) at = 20
        proxied.push(`socks ${host}:${ask.readUInt16BE(at)}`)
        toImpostor(socket, () => socket.write(Buffer.from([5, 0, 0, 1, 0, 0, 0, 0, 0, 0])))
      })
    })
  })
  const http = `http://127.0.0.1:${await listen(httpProxy)}`
  const socks = `127.0.0.1:${await listen(socksProxy)}`
  const none = { NO_PROXY: '', no_proxy: '' }
  proxies = {
    HTTPS_PROXY: { ...none, HTTPS_PROXY: http },
    https_proxy: { ...none, https_proxy: http },
    'ALL_PROXY socks5h': { ...none, ALL_PROXY: `socks5h://${socks}` },
    'all_proxy socks5': { ...none, all_proxy: `socks5://${socks}` },
  }
})

afterAll(async () => {
  for (const socket of sockets) socket.destroy()
  httpProxy.closeAllConnections()
  await new Promise((resolve) => httpProxy.close(resolve))
  await new Promise((resolve) => socksProxy.close(resolve))
})

describe('PERMAILY_API and the dev mode lead only where they say', () => {
  const list = { 'GET /v1/admin/invitations': { status: 200, body: { invitations: [] } } }

  test('PERMAILY_DEV_UNVERIFIED is 1 or unset; any other value is refused, and nothing is verified or sent', async () => {
    answers = list
    for (const value of ['0', 'false', 'no', 'true', 'yes', '01', ' 1', '1 ']) {
      seen = []
      rmSync(join(dir, 'verify-runs'), { force: true })
      const out = await runWith({ PERMAILY_DEV_UNVERIFIED: value }, 'invite.sh', 'list')
      expect({ value, exit: out.exit }).toEqual({ value, exit: 2 })
      expect({ value, seen }).toEqual({ value, seen: [] })
      expect(out.stderr).toContain('PERMAILY_DEV_UNVERIFIED')
      expect(out.stderr).toContain('Nothing was sent')
      expect(existsSync(join(dir, 'verify-runs'))).toBe(false)
    }
  })

  test('PERMAILY_API is https://host[:port] and nothing else, in both modes, before anything runs', async () => {
    answers = list
    const refused = [
      // userinfo, which curl takes for a name and a password, and connects past
      'https://localhost:unused@api.permaily.com',
      'https://localhost:password@remote.example',
      'https://localhost@api.permaily.com',
      'https://user@localhost',
      'https://[::1]:443@api.permaily.com',
      'https://[::1]@api.permaily.com',
      'https://127.0.0.1:1@remote.example',
      'https://localhost\\@api.permaily.com',
      // encoded delimiters, and anything percent-encoded
      'https://localhost%2f@api.permaily.com',
      'https://localhost%3a443',
      'https://localhost%40api.permaily.com',
      'https://api.permaily.com%2f.localhost',
      'https://%6cocalhost',
      // a path, a query or a fragment
      'https://localhost/path',
      'https://localhost/?q=1',
      'https://localhost?q=1',
      'https://localhost#f',
      'https://localhost//',
      // ports that are none
      'https://localhost:0',
      'https://localhost:65536',
      'https://localhost:',
      'https://localhost:443x',
      'https://localhost:+443',
      'https://localhost:0443',
      // spellings: case, a trailing dot, other IP forms
      'https://LOCALHOST',
      'https://Localhost',
      'https://localhost.',
      'https://dev.permaily.test.',
      'https://API.permaily.com',
      'HTTPS://localhost',
      'https://[::ffff:127.0.0.1]',
      'https://[0:0:0:0:0:0:0:1]',
      'https://2130706433',
      'https://0x7f.1',
      'https://127.1',
      'https://127.000.000.001',
      // no host, or not one
      'https://',
      'https://:443',
      'https://local host',
      'https://-localhost',
      'https://localhost..test',
    ]
    for (const mode of [{}, { PERMAILY_DEV_UNVERIFIED: '1' }]) {
      for (const api of refused) {
        seen = []
        proxied = []
        rmSync(join(dir, 'verify-runs'), { force: true })
        const out = await runWith(
          { ...mode, ...proxies.HTTPS_PROXY, PERMAILY_API: api },
          'invite.sh',
          'list',
        )
        const what = { mode, api }
        expect({ ...what, exit: out.exit }).toEqual({ ...what, exit: 2 })
        expect({ ...what, seen, proxied }).toEqual({ ...what, seen: [], proxied: [] })
        expect({ ...what, verified: existsSync(join(dir, 'verify-runs')) }).toEqual({
          ...what,
          verified: false,
        })
        expect(out.stderr).toContain('Nothing was sent')
      }
    }
  })

  test('an origin with a port or a trailing slash is verified and asked as one origin', async () => {
    answers = list
    for (const api of [
      `https://127.0.0.1:${enclave.port}/`,
      `https://localhost:${enclave.port}`,
      `https://localhost:${enclave.port}/`,
    ]) {
      seen = []
      asked = []
      rmSync(join(dir, 'verify-runs'), { force: true })
      const out = await runWith({ PERMAILY_API: api }, 'invite.sh', 'list')
      expect({ api, exit: out.exit }).toEqual({ api, exit: 0 })
      expect(asked).toEqual(['GET /v1/admin/invitations'])
      expect(readFileSync(join(dir, 'verify-runs'), 'utf8')).toEndWith(
        ` -json ${api.replace(/\/$/, '')}\n`,
      )
    }
  })

  test('in dev mode a request reaches only this machine, whatever DNS or the proxy variables say', async () => {
    answers = list
    // dev.permaily.test has no loopback answer in DNS (none at all, or a
    // resolver's own); the proxies lead to the impostor.
    for (const host of ['localhost', 'dev.localhost', 'dev.permaily.test', '127.0.0.1']) {
      for (const [name, proxy] of [['none', {}], ...Object.entries(proxies)] as const) {
        seen = []
        proxied = []
        const out = await runWith(
          {
            ...proxy,
            PERMAILY_DEV_UNVERIFIED: '1',
            STUB_VERIFY_FAILS: '1',
            PERMAILY_API: `https://${host}:${enclave.port}`,
          },
          'invite.sh',
          'list',
        )
        const what = { host, proxy: name }
        expect({ ...what, exit: out.exit, servers: seen.map((s) => s.server), proxied }).toEqual({
          ...what,
          exit: 0,
          servers: ['enclave'],
          proxied: [],
        })
      }
    }
  })

  test('in dev mode [::1] is reached directly, never through a proxy', async () => {
    let six: ReturnType<typeof Bun.serve> | undefined
    try {
      six = Bun.serve({
        port: 0,
        hostname: '::1',
        tls: {
          cert: readFileSync(join(dir, 'a.pem'), 'utf8'),
          key: readFileSync(join(dir, 'a.key'), 'utf8'),
        },
        fetch: () => Response.json({ invitations: [] }),
      })
    } catch {
      console.log('no IPv6 loopback here: [::1] is not exercised')
      return
    }
    try {
      for (const [name, proxy] of Object.entries(proxies)) {
        proxied = []
        seen = []
        const out = await runWith(
          { ...proxy, PERMAILY_DEV_UNVERIFIED: '1', PERMAILY_API: `https://[::1]:${six.port}` },
          'invite.sh',
          'list',
        )
        expect({ name, exit: out.exit, proxied, seen }).toEqual({
          name,
          exit: 0,
          proxied: [],
          seen: [],
        })
      }
    } finally {
      six.stop(true)
    }
  })
})

describe('the token and a secret are never traced', () => {
  const secret = 'client-secret-7f3a9c'

  test('under bash -x, neither the token nor a provider secret is printed', async () => {
    answers = {
      'GET /v1/admin/invitations': { status: 200, body: { invitations: [] } },
      'PUT /v1/admin/providers/google': {
        status: 200,
        body: { provider: 'google', client_id: 'id-1', set_at: '2026-11-15T10:00:00Z' },
      },
    }
    const file = join(dir, 'secret')
    writeFileSync(file, `${secret}\n`)
    for (const [how, args] of [
      [{ trace: true }, ['invite.sh', 'list']],
      [{ trace: true }, ['accounts.sh', 'stats']],
      [{ trace: true }, ['provider.sh', 'set', 'google', 'id-1', file]],
      [{ trace: true, input: `${secret}\n` }, ['provider.sh', 'set', 'google', 'id-1']],
      [{ trace: true }, ['provider.sh', 'remove', 'google']],
    ] as const) {
      seen = []
      const out = await runHow(how, {}, ...(args as unknown as [string, ...string[]]))
      const printed = out.stdout + out.stderr
      expect(out.stderr).toContain('+ ')
      expect({
        args,
        token: printed.includes('test-token'),
        secret: printed.includes(secret),
      }).toEqual({
        args,
        token: false,
        secret: false,
      })
      expect(seen.length).toBeGreaterThan(0)
    }
  })
})

// A copy of the repository with two releases: v0.1.0, built with Sigstore's
// TUF targets 12 compiled in, and the checkout after it, with targets 14. go
// is a stub that "builds" a verifier that knows only which targets were
// compiled into the source it was built from, and decides about the
// enclave's trust root as checkSigstore does (cmd/permaily-verify; the Go
// test TestTheVerifierMustBeTheRunningRelease shows the same with the real
// function and Sigstore's real metadata).
describe('the verifier is the running release', () => {
  let repo: string
  let bin: string
  // canon stands for https://github.com/permaily-org/permaily (git's url.<canon>.insteadOf).
  let canon: string
  // v010 is the commit of v0.1.0, the one its build provenance names.
  let v010: string

  const gitIn = (cwd: string, ...args: string[]) =>
    execFileSync('git', args, {
      cwd,
      stdio: 'pipe',
      env: {
        PATH: process.env.PATH ?? '',
        HOME: '/nonexistent',
        GIT_CONFIG_NOSYSTEM: '1',
        GIT_AUTHOR_NAME: 't',
        GIT_AUTHOR_EMAIL: 't@example.com',
        GIT_COMMITTER_NAME: 't',
        GIT_COMMITTER_EMAIL: 't@example.com',
      },
    }).toString()
  const git = (...args: string[]) => gitIn(repo, ...args)
  // attest says which commit the build provenance of a release's pcrs.json names (the gh fixture reads it).
  const attest = (tag: string, commit: string | null) => {
    const file = join(bin, 'attested', tag)
    if (commit === null) rmSync(file, { force: true })
    else writeFileSync(file, commit)
  }

  beforeAll(() => {
    repo = mkdtempSync(join(tmpdir(), 'operator-release-'))
    bin = join(repo, '..', `${repo.split('/').pop()}-bin`)
    mkdirSync(bin)
    for (const sub of ['scripts', 'pkg/provenance', 'cmd/permaily-verify']) {
      mkdirSync(join(repo, sub), { recursive: true })
    }
    for (const f of readdirSync(import.meta.dir).filter((f) => f.endsWith('.sh'))) {
      copyFileSync(join(import.meta.dir, f), join(repo, 'scripts', f))
    }
    const compiled = join(import.meta.dir, '../pkg/provenance')
    for (const f of [
      'sigstore_tuf_root.json',
      'sigstore_tuf_targets.json',
      'sigstore_trusted_root.json',
    ]) {
      copyFileSync(join(compiled, f), join(repo, 'pkg/provenance', f))
    }
    git('init', '-q', '-b', 'main')
    writeFileSync(join(repo, 'cmd/permaily-verify/compiled'), '12\n')
    git('add', '.')
    git('commit', '-q', '-m', 'v0.1.0')
    git('tag', 'v0.1.0')
    writeFileSync(join(repo, 'cmd/permaily-verify/compiled'), '14\n')
    git('commit', '-q', '-am', 'targets 14')
    git('tag', 'v0.2.0')
    v010 = git('rev-parse', 'v0.1.0^{commit}').trim()
    canon = join(repo, '..', `${repo.split('/').pop()}-canon`)
    execFileSync('git', ['clone', '-q', '--bare', repo, canon])
    mkdirSync(join(bin, 'attested'))
    attest('v0.1.0', v010)
    attest('v0.2.0', git('rev-parse', 'v0.2.0^{commit}').trim())

    // go build -C <source> ... -o <out> ./cmd/permaily-verify
    writeFileSync(
      join(bin, 'go'),
      `#!/usr/bin/env bash
src=. out=
while [[ $# -gt 0 ]]; do
  case $1 in -C) src=$2; shift ;; -o) out=$2; shift ;; esac
  shift
done
# STUB_GO_INTERRUPT: the run is stopped (SIGTERM) while it builds.
if [[ -n \${STUB_GO_INTERRUPT:-} ]]; then kill -TERM $PPID; exit 0; fi
# Source that is not a release's: a verifier that says yes, with the key it names, and leaves a mark.
if [[ -f $src/cmd/permaily-verify/hostile ]]; then
  printf '%s hostile\\n' "$(cd "$src" && pwd)" >> '${bin}/go-builds'
  key=$(cat "$src/cmd/permaily-verify/hostile")
  cat > "$out" <<HOSTILE
#!/usr/bin/env bash
touch '${bin}/hostile-ran'
jq -n --arg url "\\\${!#}" '{ok: true, hosts: [{url: \\$url, ok: true, tls_spki_sha256: "$key"}]}'
HOSTILE
  chmod +x "$out"
  exit 0
fi
own=$(cat "$src/cmd/permaily-verify/compiled")
printf '%s %s\\n' "$(cd "$src" && pwd)" "$own" >> '${bin}/go-builds'
cat > "$out" <<VERIFIER
#!/usr/bin/env bash
own=$own
printf '%s %s\\\\n' "\\$own" "\\$*" >> '${bin}/verifications'
url=\\\${!#}
if [[ \\$1 == -trust-update ]]; then echo '{"ok":true,"newer":false}'; exit 0; fi
# STUB_SIGSTORE: the enclave's targets, and "updated" when it was given a newer trust root.
read -r targets updated <<< "\\$STUB_SIGSTORE"
if [[ -z \\$updated ]] && (( targets != own )) || [[ -n \\$updated ]] && (( targets <= own )); then
  jq -n --arg url "\\$url" --arg e "trust root of TUF targets \\$targets does not follow the one of this release, targets \\$own" \\\\
    '{ok: false, hosts: [{url: \\$url, ok: false, error: \\$e}]}'
  exit 1
fi
jq -n --arg url "\\$url" --arg spki "\\$STUB_ATTESTED" '{ok: true, hosts: [{url: \\$url, ok: true, tls_spki_sha256: \\$spki}]}'
VERIFIER
chmod +x "$out"
`,
    )
    // gh: the release's pcrs.json (a release exists when attested/<tag> does), its
    // provenance (checked; with --format json, the commit attested/<tag> names), and its bundle.
    writeFileSync(
      join(bin, 'gh'),
      `#!/usr/bin/env bash
printf '%s\\n' "$*" >> '${bin}/gh-runs'
if [[ $1 == release && $2 == download ]]; then
  tag=$3
  [[ -f '${bin}/attested/'$tag ]] || { echo "release not found" >&2; exit 1; }
  while [[ $# -gt 0 ]]; do if [[ $1 == --dir ]]; then printf '{"PCR0":"%s"}' "$tag" > "$2/pcrs.json"; exit 0; fi; shift; done
elif [[ $1 == attestation && $2 == verify ]]; then
  [[ -z \${STUB_PROVENANCE_FAILS:-} ]] || { echo "no attestation matched the constraints" >&2; exit 1; }
  if [[ " $* " == *" --format json "* ]]; then
    commit=$(cat '${bin}/attested/'"$(jq -r .PCR0 "$3")")
    jq -n --arg c "$commit" '[{verificationResult: {signature: {certificate: {sourceRepositoryURI: "https://github.com/permaily-org/permaily", sourceRepositoryRef: "refs/heads/main", sourceRepositoryDigest: $c}}}}]'
  fi
  exit 0
elif [[ $1 == attestation && $2 == download ]]; then
  echo '{"dsseEnvelope":{"payload":"eyJwcmVkaWNhdGVUeXBlIjoiaHR0cHM6Ly9zbHNhLmRldi9wcm92ZW5hbmNlL3YxIn0="},"verificationMaterial":{"tlogEntries":[{}]}}' > fixture.jsonl
else exit 1; fi
`,
    )
    // curl: Sigstore's TUF repository is served from the compiled files; the rest is curl.
    const targets = JSON.parse(readFileSync(join(compiled, 'sigstore_tuf_targets.json'), 'utf8'))
      .signed.version
    const curl = execFileSync('bash', ['-c', 'command -v curl']).toString().trim()
    writeFileSync(
      join(bin, 'curl'),
      `#!/usr/bin/env bash
out= url=
for a in "$@"; do [[ $prev == -o ]] && out=$a; [[ $a == https://* ]] && url=$a; prev=$a; done
if [[ $url == https://tuf-repo-cdn.sigstore.dev/* ]]; then
  case $url in
    */timestamp.json) printf '%s' '{"signed":{"meta":{"snapshot.json":{"version":1}}}}' > "$out" ;;
    */1.snapshot.json) printf '%s' '{"signed":{"meta":{"targets.json":{"version":${targets}}}}}' > "$out" ;;
    */${targets}.targets.json) cp '${compiled}/sigstore_tuf_targets.json' "$out" ;;
    */targets/*.trusted_root.json) cp '${compiled}/sigstore_trusted_root.json' "$out" ;;
    *) exit 22 ;;
  esac
  exit 0
fi
exec '${curl}' "$@"
`,
    )
    for (const f of ['go', 'gh', 'curl']) chmodSync(join(bin, f), 0o755)
  })

  afterAll(() => {
    rmSync(repo, { recursive: true, force: true })
    rmSync(bin, { recursive: true, force: true })
    rmSync(canon, { recursive: true, force: true })
  })

  const flow = () => {
    const compiled = join(import.meta.dir, '../pkg/provenance')
    const root = JSON.parse(readFileSync(join(compiled, 'sigstore_tuf_root.json'), 'utf8')).signed
      .version
    answers = {
      'GET /status': { status: 200, body: { sigstore: { tuf_root_version: root } } },
      'POST /v1/admin/trust-root': {
        status: 200,
        body: {
          trusted_root_sha256: 'ab'.repeat(32),
          tuf_root_version: root,
          tuf_targets_version: 14,
        },
      },
      'POST /v1/admin/handoff': {
        status: 200,
        body: {
          image: 'ab'.repeat(48),
          commit: 'cd'.repeat(20),
          published_at: '2026-11-15T00:00:00Z',
          key_arn: 'arn:test:key',
          created: true,
        },
      },
      'GET /v1/admin/invitations': { status: 200, body: { invitations: [] } },
    }
    for (const f of ['go-builds', 'verifications', 'gh-runs', 'hostile-ran'])
      rmSync(join(bin, f), { force: true })
    return {
      PATH: `${bin}:${process.env.PATH}`,
      PERMAILY_VERIFY: '',
      GIT_CONFIG_COUNT: '1',
      GIT_CONFIG_KEY_0: `url.${canon}.insteadOf`,
      GIT_CONFIG_VALUE_0: 'https://github.com/permaily-org/permaily',
    }
  }
  const lines = (name: string) =>
    existsSync(join(bin, name)) ? readFileSync(join(bin, name), 'utf8').trim().split('\n') : []

  for (const [state, sigstore] of [
    ['with the trust root compiled into it', '12'],
    ['after it was given a newer trust root in place', '14 updated'],
  ] as const) {
    test(`a handoff from an older release, ${state}, is verified by that release's verifier, built from its tag`, async () => {
      const env = flow()
      seen = []
      const out = await runHow(
        { scripts: join(repo, 'scripts'), trace: true },
        { ...env, STUB_SIGSTORE: sigstore },
        'handoff.sh',
        'v0.1.0',
        'v0.2.0',
      )
      expect({ exit: out.exit, stderr: out.exit === 0 ? '' : out.stderr }).toEqual({
        exit: 0,
        stderr: '',
      })
      // Built once, from the tag's source in a worktree that is gone after.
      const builds = lines('go-builds')
      expect(builds.length).toBe(1)
      expect(builds[0]).toEndWith(' 12')
      expect(builds[0].startsWith(`${repo} `)).toBe(false)
      expect(git('worktree', 'list', '--porcelain').match(/^worktree /gm)?.length).toBe(1)
      // That one binary verified the enclave twice (handoff, then --send) and decided the trust root.
      const runs = lines('verifications')
      expect(runs.filter((r) => r.includes(' -json ')).length).toBe(2)
      expect(runs.every((r) => r.startsWith('12 '))).toBe(true)
      expect(runs.some((r) => r.includes('-trust-update'))).toBe(true)
      expect(seen.map((s) => s.request.split('\n')[0])).toEqual([
        'GET /status',
        'POST /v1/admin/trust-root',
        'POST /v1/admin/handoff',
      ])
      expect(out.stdout + out.stderr).not.toContain('test-token')
    })
  }

  test("the checkout's verifier is not taken for the running release's", async () => {
    // What happened before: the verifier built from the checkout (targets 14)
    // refuses a running release with targets 12, before and after its update.
    for (const sigstore of ['12', '14 updated']) {
      const env = flow()
      seen = []
      const checkout = join(bin, 'checkout-verify')
      execFileSync(join(bin, 'go'), ['build', '-C', repo, '-o', checkout, './cmd/permaily-verify'])
      const out = await runWith(
        { ...env, PERMAILY_VERIFY: checkout, STUB_SIGSTORE: sigstore },
        'invite.sh',
        'list',
      )
      expect({ sigstore, exit: out.exit }).toEqual({ sigstore, exit: 1 })
      expect(out.stderr).toContain('does not follow the one of this release')
      expect(seen).toEqual([])
    }
  })

  test('PERMAILY_RELEASE builds the verifier from that tag; PERMAILY_VERIFY, given, wins', async () => {
    let env = flow()
    let out = await runHow(
      { scripts: join(repo, 'scripts') },
      { ...env, PERMAILY_RELEASE: 'v0.1.0', PERMAILY_PCRS: '', STUB_SIGSTORE: '12' },
      'invite.sh',
      'list',
    )
    expect(out.exit).toBe(0)
    expect(lines('go-builds').map((b) => b.split(' ')[1])).toEqual(['12'])
    expect(lines('verifications')[0]).toStartWith('12 -release v0.1.0 ')
    // From the commit the release's build provenance names, checked against the repository, the workflow and main.
    expect(out.stderr).toContain(`permaily-verify built from commit ${v010}`)
    expect(lines('gh-runs').find((r) => r.startsWith('attestation verify '))).toEndWith(
      ' --repo permaily-org/permaily --signer-workflow permaily-org/permaily/.github/workflows/release.yml --source-ref refs/heads/main --deny-self-hosted-runners --format json',
    )
    // Measurements of your own build, and the verifier of the tag.
    env = flow()
    out = await runHow(
      { scripts: join(repo, 'scripts') },
      { ...env, PERMAILY_RELEASE: 'v0.1.0', STUB_SIGSTORE: '12' },
      'invite.sh',
      'list',
    )
    expect(out.exit).toBe(0)
    expect(lines('verifications')[0]).toStartWith(`12 -pcrs ${join(dir, 'pcrs.json')} `)
    // An explicit verifier is used as it is.
    env = flow()
    out = await runHow(
      { scripts: join(repo, 'scripts') },
      { ...env, PERMAILY_RELEASE: 'v0.1.0', PERMAILY_VERIFY: join(dir, 'permaily-verify') },
      'invite.sh',
      'list',
    )
    expect(out.exit).toBe(0)
    expect(lines('go-builds')).toEqual([])
  })

  test('PERMAILY_PCRS alone builds from the checkout, and says it must be the running release', async () => {
    const env = flow()
    const out = await runHow(
      { scripts: join(repo, 'scripts') },
      { ...env, STUB_SIGSTORE: '14' },
      'invite.sh',
      'list',
    )
    expect(out.exit).toBe(0)
    expect(lines('go-builds')).toEqual([`${repo} 14`])
    expect(out.stderr).toContain('must be the source of the release the enclave runs')
  })

  test('a release that is not a tag here, or not a release tag, is refused before anything is sent', async () => {
    for (const release of ['v9.9.9', '--upload-pack=x', 'v0.1.0^', 'main']) {
      const env = flow()
      seen = []
      const out = await runHow(
        { scripts: join(repo, 'scripts') },
        { ...env, PERMAILY_RELEASE: release, STUB_SIGSTORE: '12' },
        'invite.sh',
        'list',
      )
      expect({ release, exit: out.exit }).toEqual({ release, exit: 2 })
      expect(seen).toEqual([])
      expect(lines('verifications')).toEqual([])
      expect(out.stderr).toContain('Nothing was sent')
    }
  })
  // A tag says nothing about who made the commit it names: the verifier is built
  // only from the commit the release's build provenance names, fetched by that
  // commit from the repository itself. Each case: a source that is not the
  // release's, whose verifier would say yes to the impostor's key, under the
  // release's tag; PERMAILY_API is the impostor.
  describe('a tag that names another commit never builds the verifier', () => {
    let impostorKey: string
    let fork: string
    beforeAll(() => {
      impostorKey = createHash('sha256')
        .update(createPublicKey(readFileSync(join(dir, 'b.key'))).export({ type: 'spki', format: 'der' }))
        .digest('hex')
      fork = join(repo, '..', `${repo.split('/').pop()}-fork`)
    })
    afterAll(() => {
      for (const ref of ['refs/tags/v0.3.0']) {
        execFileSync('git', ['--git-dir', canon, 'update-ref', '-d', ref])
      }
    })
    const cleanup = () => {
      try {
        git('tag', '-d', 'v0.3.0')
      } catch {}
      try {
        git('remote', 'remove', 'origin')
      } catch {}
      try {
        execFileSync('git', ['--git-dir', canon, 'update-ref', '-d', 'refs/tags/v0.3.0'])
      } catch {}
      rmSync(fork, { recursive: true, force: true })
      attest('v0.3.0', null)
    }
    // hostile makes a commit (not checked out) whose verifier says yes to the impostor.
    const hostile = () => {
      const head = git('rev-parse', 'HEAD').trim()
      writeFileSync(join(repo, 'cmd/permaily-verify/hostile'), impostorKey)
      git('add', '.')
      git('commit', '-q', '-m', 'not a release')
      const sha = git('rev-parse', 'HEAD').trim()
      git('reset', '-q', '--hard', head)
      return sha
    }
    const origin = (tag: string) => {
      execFileSync('git', ['clone', '-q', '--bare', canon, fork])
      git('push', '-q', '-f', fork, `${tag}:refs/tags/v0.3.0`)
      git('remote', 'add', 'origin', fork)
    }
    const send = async () => {
      seen = []
      const secret = join(dir, 'tag-secret')
      writeFileSync(secret, 'provider-secret-of-the-tag-tests')
      return runHow(
        { scripts: join(repo, 'scripts') },
        {
          ...flow(),
          PERMAILY_RELEASE: 'v0.3.0',
          PERMAILY_API: `https://127.0.0.1:${impostor.port}`,
          STUB_SIGSTORE: '12',
        },
        'provider.sh',
        'set',
        'google',
        'id-1',
        secret,
      )
    }
    const nothingOfIt = (out: { stderr: string; exit: number }) => {
      expect(out.exit).not.toBe(0)
      expect(existsSync(join(bin, 'hostile-ran'))).toBe(false)
      expect(lines('go-builds').filter((b) => b.endsWith(' hostile'))).toEqual([])
      expect(seen).toEqual([])
      expect(out.stderr).toContain('othing was sent')
      expect(git('worktree', 'list', '--porcelain').match(/^worktree /gm)?.length).toBe(1)
    }

    test('the tag moved at the repository and at origin: refused, nothing built', async () => {
      try {
        const bad = hostile()
        attest('v0.3.0', v010)
        git('push', '-q', '-f', canon, `${bad}:refs/tags/v0.3.0`)
        origin(bad)
        const out = await send()
        nothingOfIt(out)
        expect(out.exit).toBe(2)
        expect(lines('go-builds')).toEqual([])
        expect(out.stderr).toContain(`the build provenance of v0.3.0 names ${v010}`)
      } finally {
        cleanup()
      }
    })

    test('a hostile tag already here is not used: the attested commit is built', async () => {
      try {
        const bad = hostile()
        attest('v0.3.0', v010)
        git('push', '-q', '-f', canon, `${v010}:refs/tags/v0.3.0`)
        git('tag', 'v0.3.0', bad)
        origin(v010)
        const out = await send()
        nothingOfIt(out)
        expect(lines('go-builds').map((b) => b.split(' ')[1])).toEqual(['12'])
        expect(out.stderr).toContain(`permaily-verify built from commit ${v010}`)
        expect(out.stderr).toContain('TLS key other than the one attested')
        expect(git('rev-parse', 'refs/tags/v0.3.0').trim()).toBe(bad)
      } finally {
        cleanup()
      }
    })

    test("a fork's origin is not where the source comes from: the commit is fetched from the repository", async () => {
      // The release's commit is only at the repository (canon), not here.
      const clone = join(repo, '..', `${repo.split('/').pop()}-clone`)
      try {
        const bad = hostile()
        execFileSync('git', ['clone', '-q', canon, clone])
        gitIn(clone, 'checkout', '-q', 'v0.1.0')
        writeFileSync(join(clone, 'released'), 'v0.3.0\n')
        gitIn(clone, 'add', '.')
        gitIn(clone, 'commit', '-q', '-m', 'v0.3.0')
        const released = gitIn(clone, 'rev-parse', 'HEAD').trim()
        gitIn(clone, 'push', '-q', 'origin', 'HEAD:refs/tags/v0.3.0')
        attest('v0.3.0', released)
        expect(() => git('cat-file', '-e', `${released}^{commit}`)).toThrow()
        origin(bad)
        const out = await send()
        nothingOfIt(out)
        expect(lines('go-builds').map((b) => b.split(' ')[1])).toEqual(['12'])
        expect(out.stderr).toContain(`permaily-verify built from commit ${released}`)
        expect(git('cat-file', '-t', released).trim()).toBe('commit')
        // No tag is written here: the next run reads the provenance again.
        expect(() => git('rev-parse', '-q', '--verify', 'refs/tags/v0.3.0')).toThrow()
      } finally {
        rmSync(clone, { recursive: true, force: true })
        cleanup()
      }
    })

    test('without authenticated provenance (no release, gh offline, a provenance that does not verify), nothing is built or sent', async () => {
      try {
        const bad = hostile()
        git('tag', 'v0.3.0', bad)
        git('push', '-q', '-f', canon, `${bad}:refs/tags/v0.3.0`)
        // No release v0.3.0: gh release download fails, as offline.
        let out = await send()
        nothingOfIt(out)
        expect(out.exit).toBe(2)
        expect(out.stderr).toContain('PERMAILY_VERIFY')
        expect(lines('go-builds')).toEqual([])
        // A release whose provenance does not verify.
        attest('v0.3.0', bad)
        seen = []
        out = await runHow(
          { scripts: join(repo, 'scripts') },
          {
            ...flow(),
            PERMAILY_RELEASE: 'v0.3.0',
            PERMAILY_API: `https://127.0.0.1:${impostor.port}`,
            STUB_PROVENANCE_FAILS: '1',
          },
          'invite.sh',
          'list',
        )
        nothingOfIt(out)
        expect(out.exit).toBe(2)
        expect(lines('go-builds')).toEqual([])
      } finally {
        cleanup()
      }
    })
  })

  test('a run stopped while it builds leaves no worktree registered', async () => {
    const env = flow()
    seen = []
    const out = await runHow(
      { scripts: join(repo, 'scripts') },
      { ...env, PERMAILY_RELEASE: 'v0.1.0', STUB_SIGSTORE: '12', STUB_GO_INTERRUPT: '1' },
      'invite.sh',
      'list',
    )
    expect(out.exit).not.toBe(0)
    expect(seen).toEqual([])
    expect(git('worktree', 'list', '--porcelain').match(/^worktree /gm)?.length).toBe(1)
  })
})
