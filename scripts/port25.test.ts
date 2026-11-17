import { afterAll, beforeAll, describe, expect, setDefaultTimeout, test } from 'bun:test'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// The port 25 check's instance (deploy/port25, ADR 0038): run.sh, alert.sh and
// on-failure.sh with the real verify-cron.sh and verify-production.sh, all as
// the instance gets them from deploy/tofu/port25.tf (full-line comments taken
// out), with a fake aws (Secrets Manager and SNS) and a fake gh that serves one
// release and insists on the token.

setDefaultTimeout(30_000)

const TOKEN = 'github_pat_11TESTONLY_0123456789abcdef'
let dir: string
let bin: string
let root: string
let n = 0

// strip is port25.tf's: drop every line that starts with # but the first.
function strip(text: string) {
  return text
    .replace(/\n$/, '')
    .split('\n')
    .filter((line, i) => i === 0 || !line.startsWith('#'))
    .join('\n')
}

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'port25-'))
  bin = join(dir, 'bin')
  root = join(dir, 'opt')
  mkdirSync(bin)
  mkdirSync(root)
  const files: Record<string, string> = {
    'run.sh': join(import.meta.dir, '../deploy/port25/run.sh'),
    'alert.sh': join(import.meta.dir, '../deploy/port25/alert.sh'),
    'on-failure.sh': join(import.meta.dir, '../deploy/port25/on-failure.sh'),
    'verify-cron.sh': join(import.meta.dir, 'verify-cron.sh'),
    'verify-production.sh': join(import.meta.dir, 'verify-production.sh'),
  }
  for (const [name, path] of Object.entries(files)) {
    writeFileSync(join(root, name), `${strip(readFileSync(path, 'utf8'))}\n`, { mode: 0o755 })
  }
  // Every call's arguments go to $FAKE/argv, to show the token is never on a command line.
  writeFileSync(
    join(bin, 'aws'),
    `#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$FAKE/argv"
case "$1 $2" in
  "secretsmanager get-secret-value")
    [[ $* == *"--secret-id $PERMAILY_TOKEN_SECRET "* ]] || exit 3
    [[ -e "$FAKE/token" ]] || { echo "ResourceNotFoundException" >&2; exit 254; }
    cat "$FAKE/token" ;;
  "sns publish")
    i=$(find "$FAKE" -maxdepth 1 -name 'attempt-*' | wc -l); touch "$FAKE/attempt-$i"
    [[ -e "$FAKE/sns-fails" ]] && { echo "AuthorizationError" >&2; exit 254; }
    shift 2
    while [[ $# -gt 0 ]]; do
      case $1 in
        --topic-arn) [[ $2 == "$PERMAILY_TOPIC_ARN" ]] || exit 3; shift 2 ;;
        --subject) s=$2; shift 2 ;;
        --message) m=\${2#file://}; shift 2 ;;
        *) shift ;;
      esac
    done
    { printf '%s\\n' "$s"; cat "$m"; } > "$FAKE/published-$i"
    echo 00000000-0000-0000-0000-000000000000 ;;
  *) echo "unexpected aws $*" >&2; exit 2 ;;
esac
`,
    { mode: 0o755 },
  )
  writeFileSync(
    join(bin, 'gh'),
    `#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$FAKE/argv"
[[ \${GH_TOKEN:-} == "${TOKEN}" ]] || { echo "gh: no token" >&2; exit 4; }
case "$1 $2" in
  "release list") echo v0.2.0 ;;
  "release download")
    shift 3
    while [[ $# -gt 0 ]]; do case $1 in --dir) d=$2; shift 2 ;; *) shift ;; esac; done
    printf '{}' > "$d/pcrs.json"
    for a in amd64 arm64; do
      printf '#!/usr/bin/env bash\\necho "$*" > "%s/ran"\\necho "port 25: checked"\\nexit $(cat "%s/verifier-exit" 2>/dev/null || echo 0)\\n' "$FAKE" "$FAKE" > "$d/permaily-verify-linux-$a"
    done ;;
  "attestation verify") [[ -e "$FAKE/attested" ]] || { echo "no attestation" >&2; exit 1; } ;;
  *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
`,
    { mode: 0o755 },
  )
  writeFileSync(
    join(bin, 'journalctl'),
    '#!/usr/bin/env bash\necho "the journal of permaily-port25.service"\n',
    {
      mode: 0o755,
    },
  )
})

afterAll(() => rmSync(dir, { recursive: true, force: true }))

// fake is a directory for one test: the secret holds TOKEN, the release is attested.
function fake(opts: { token?: string | null; attested?: boolean } = {}) {
  const f = join(dir, `fake-${n++}`)
  mkdirSync(join(f, 'state'), { recursive: true })
  if (opts.token !== null) writeFileSync(join(f, 'token'), `${opts.token ?? TOKEN}\n`)
  if (opts.attested ?? true) writeFileSync(join(f, 'attested'), '')
  return f
}

function run(script: string, f: string, at = root) {
  const child = Bun.spawn(['bash', join(at, script)], {
    env: {
      PATH: `${bin}:${process.env.PATH ?? ''}`,
      HOME: '/nonexistent',
      FAKE: f,
      STATE_DIRECTORY: join(f, 'state'),
      AWS_REGION: 'us-east-2',
      PERMAILY_TOPIC_ARN: 'arn:aws:sns:us-east-2:000000000000:permaily-sigstore-deadline',
      PERMAILY_TOKEN_SECRET:
        'arn:aws:secretsmanager:us-east-2:000000000000:secret:permaily/port25/github-token',
    },
    stdin: 'ignore',
    stdout: 'pipe',
    stderr: 'pipe',
  })
  return Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]).then(([stdout, stderr, exit]) => ({ stdout, stderr, exit }))
}

function published(f: string) {
  return readdirSync(f)
    .filter((name) => name.startsWith('published-'))
    .sort()
    .map((name) => readFileSync(join(f, name), 'utf8'))
}

function attempts(f: string) {
  return readdirSync(f).filter((name) => name.startsWith('attempt-')).length
}

// The token is in no command line and in no mail, whatever happened.
function tokenStaysPut(f: string) {
  const argv = existsSync(join(f, 'argv')) ? readFileSync(join(f, 'argv'), 'utf8') : ''
  expect(argv).not.toContain('github_pat_')
  for (const mail of published(f)) expect(mail).not.toContain('github_pat_')
}

describe('the port 25 check on its instance', () => {
  test('a passing run runs the attested verifier with -require-smtp and mails nothing', async () => {
    const f = fake()
    const r = await run('run.sh', f)
    expect(r.exit).toBe(0)
    expect(r.stdout).toBe('')
    expect(readFileSync(join(f, 'ran'), 'utf8')).toContain('-require-smtp')
    expect(attempts(f)).toBe(0)
    tokenStaysPut(f)
  })

  test('a failed verification mails its report once, and the failure unit adds nothing', async () => {
    const f = fake()
    writeFileSync(join(f, 'verifier-exit'), '1')
    const r = await run('run.sh', f)
    expect(r.exit).toBe(1)
    const mail = published(f)
    expect(mail).toHaveLength(1)
    expect(mail[0]).toStartWith('Permaily: the port 25 check failed\n')
    expect(mail[0]).toContain('port 25: checked')
    expect(mail[0]).toContain('RESULT: production does not verify')
    expect((await run('on-failure.sh', f)).exit).toBe(0)
    expect(attempts(f)).toBe(1)
    tokenStaysPut(f)
  })

  test('a release whose provenance does not verify is not run, and the failure is mailed', async () => {
    const f = fake({ attested: false })
    const r = await run('run.sh', f)
    expect(r.exit).toBe(1)
    expect(existsSync(join(f, 'ran'))).toBe(false)
    expect(published(f)[0]).toContain('does not verify: not running it')
    tokenStaysPut(f)
  })

  test('a token that cannot be read runs nothing and is mailed', async () => {
    const f = fake({ token: null })
    const r = await run('run.sh', f)
    expect(r.exit).toBe(1)
    expect(readFileSync(join(f, 'argv'), 'utf8')).not.toContain('release')
    expect(published(f)).toEqual([
      expect.stringContaining('the GitHub token could not be read from Secrets Manager'),
    ])
    expect((await run('on-failure.sh', f)).exit).toBe(0)
    expect(attempts(f)).toBe(1)
  })

  test('a secret that is not a fine-grained token runs nothing and is mailed, without the secret', async () => {
    const f = fake({ token: 'ghp_classicTokenWithScopes0123456789' })
    const r = await run('run.sh', f)
    expect(r.exit).toBe(1)
    expect(readFileSync(join(f, 'argv'), 'utf8')).not.toContain('release')
    const mail = published(f)
    expect(mail).toEqual([expect.stringContaining('does not hold a fine-grained GitHub token')])
    expect(mail[0]).not.toContain('ghp_')
    expect(r.stdout + r.stderr).not.toContain('ghp_')
  })

  test('an alert SNS refused leaves the failure unit to mail, with the journal', async () => {
    const f = fake()
    writeFileSync(join(f, 'verifier-exit'), '1')
    writeFileSync(join(f, 'sns-fails'), '')
    const r = await run('run.sh', f)
    expect(r.exit).toBe(1)
    expect(r.stderr).toContain('the alert command failed')
    expect(published(f)).toEqual([])
    rmSync(join(f, 'sns-fails'))
    expect((await run('on-failure.sh', f)).exit).toBe(0)
    const mail = published(f)
    expect(mail).toHaveLength(1)
    expect(mail[0]).toStartWith("Permaily: the port 25 check's run failed\n")
    expect(mail[0]).toContain('the journal of permaily-port25.service')
    tokenStaysPut(f)
  })

  test('a run that ends without its alert (a crash, systemd stopping it) is mailed by the failure unit', async () => {
    const f = fake()
    writeFileSync(join(f, 'state', 'alerted'), '')
    // The marker of an earlier run's alert does not silence this one.
    const crashed = join(dir, 'crashed')
    mkdirSync(crashed)
    for (const name of ['run.sh', 'alert.sh', 'on-failure.sh']) {
      writeFileSync(join(crashed, name), readFileSync(join(root, name)), { mode: 0o755 })
    }
    writeFileSync(join(crashed, 'verify-cron.sh'), '#!/usr/bin/env bash\nexit 3\n', { mode: 0o755 })
    expect((await run('run.sh', f, crashed)).exit).toBe(3)
    expect(published(f)).toEqual([])
    expect((await run('on-failure.sh', f)).exit).toBe(0)
    expect(published(f)).toEqual([expect.stringContaining('failed without mailing its report')])
  })
})
