import { afterAll, beforeAll, describe, expect, test } from 'bun:test'
import { execFileSync } from 'node:child_process'
import { createHash, createPublicKey } from 'node:crypto'
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
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

// leaf makes a key and a certificate for 127.0.0.1 and localhost, signed by the test CA.
function leaf(name: string) {
  openssl('req', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-keyout', `${name}.key`, '-subj', '/CN=127.0.0.1', '-out', `${name}.csr`)
  writeFileSync(join(dir, `${name}.ext`), 'subjectAltName=IP:127.0.0.1,DNS:localhost\nextendedKeyUsage=serverAuth\n')
  openssl('x509', '-req', '-in', `${name}.csr`, '-CA', 'ca.pem', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '2', '-extfile', `${name}.ext`, '-out', `${name}.pem`)
  return { cert: readFileSync(join(dir, `${name}.pem`), 'utf8'), key: readFileSync(join(dir, `${name}.key`), 'utf8') }
}

function serve(name: Seen['server'], tls: { cert: string; key: string }) {
  return Bun.serve({
    port: 0,
    hostname: '127.0.0.1',
    tls,
    async fetch(request) {
      const url = new URL(request.url)
      const body = await request.text()
      seen.push({ server: name, request: `${request.method} ${url.pathname}${url.search}\n${JSON.stringify([...request.headers])}\n${body}` })
      if (name === 'impostor') return Response.json({ error: { message: 'thanks' } }, { status: 200 })
      asked.push(`${request.method} ${url.pathname}${url.search}`)
      const answer = answers[`${request.method} ${url.pathname}`]
      if (!answer) return Response.json({ error: { message: 'no such endpoint' } }, { status: 404 })
      return Response.json(answer.body, { status: answer.status })
    },
  })
}

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'operator-scripts-'))
  openssl('req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-keyout', 'ca.key', '-subj', '/CN=Permaily test CA', '-days', '2', '-out', 'ca.pem')
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

async function runWith(env: Record<string, string>, script: string, ...args: string[]) {
  const child = Bun.spawn(['bash', join(import.meta.dir, script), ...args], {
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
      const { stdout, stderr, exit } = await runWith(impostorAPI, ...(args as [string, ...string[]]))
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
      const { stderr, exit } = await runWith({ STUB_VERIFY_FAILS: '1' }, ...(args as [string, ...string[]]))
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

  test('a plain http address is refused before anything is sent', async () => {
    seen = []
    const { exit } = await runWith({ PERMAILY_API: `http://127.0.0.1:${enclave.port}` }, 'accounts.sh')
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
