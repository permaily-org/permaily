import { afterAll, beforeAll, describe, expect, setDefaultTimeout, test } from 'bun:test'
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { isAbsolute, join, relative, resolve, sep } from 'node:path'

// The port 25 check's instance (deploy/port25, ADR 0038): run.sh, alert.sh and
// on-failure.sh with the copies of verify-cron.sh and verify-production.sh in
// deploy/port25, all as the instance gets them from deploy/tofu/port25.tf
// (full-line comments taken out), with a fake aws (Secrets Manager and SNS)
// and a fake gh that serves one release, insists on the token and attests
// each file only for the release workflow on main. And the first boot
// (cloud-init.sh.tftpl, rendered as port25.tf renders it) with a download of
// gh that never ends or is not the pinned archive.

setDefaultTimeout(30_000)

const TOKEN = 'github_pat_11TESTONLY_0123456789abcdef'
const deploy = join(import.meta.dir, '../deploy')
const IDENTITY =
  '--repo permaily-org/permaily --signer-workflow permaily-org/permaily/.github/workflows/release.yml --source-ref refs/heads/main'
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
    'run.sh': join(deploy, 'port25/run.sh'),
    'alert.sh': join(deploy, 'port25/alert.sh'),
    'on-failure.sh': join(deploy, 'port25/on-failure.sh'),
    'verify-cron.sh': join(deploy, 'port25/verify-cron.sh'),
    'verify-production.sh': join(deploy, 'port25/verify-production.sh'),
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
        --message) m=$2; shift 2 ;;
        *) shift ;;
      esac
    done
    [[ -e "$FAKE/sns-hangs" ]] && exec sleep 600 >/dev/null 2>&1
    { printf '%s\\n' "$s"; if [[ $m == file://* ]]; then cat "\${m#file://}"; else printf '%s\\n' "$m"; fi; } > "$FAKE/published-$i"
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
  "attestation verify")
    [[ "\${*:4}" == "${IDENTITY}" ]] || { echo "not the release workflow's identity: \${*:4}" >&2; exit 1; }
    case $(basename "$3") in pcrs.json) a=pcrs ;; *) a=verifier ;; esac
    [[ -e "$FAKE/attested-$a" ]] || { echo "no attestation" >&2; exit 1; } ;;
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

// fake is a directory for one test: the secret holds TOKEN, both files of the
// release are attested (attested names those that are).
function fake(opts: { token?: string | null; attested?: ('pcrs' | 'verifier')[] } = {}) {
  const f = join(dir, `fake-${n++}`)
  mkdirSync(join(f, 'state'), { recursive: true })
  if (opts.token !== null) writeFileSync(join(f, 'token'), `${opts.token ?? TOKEN}\n`)
  for (const a of opts.attested ?? ['pcrs', 'verifier']) writeFileSync(join(f, `attested-${a}`), '')
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
    // Both files, each against the release workflow on main of the repository.
    const verified = readFileSync(join(f, 'argv'), 'utf8')
      .split('\n')
      .filter((line) => line.startsWith('attestation verify '))
    expect(verified.map((line) => line.split(' ')[2]?.split('/').pop())).toEqual([
      'pcrs.json',
      expect.stringMatching(/^permaily-verify-linux-(amd64|arm64)$/),
    ])
    for (const line of verified) expect(line.split(' ').slice(3).join(' ')).toBe(IDENTITY)
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

  for (const [what, attested] of [
    ['neither file', []],
    ['the verifier', ['pcrs']],
    ['pcrs.json', ['verifier']],
  ] as const) {
    test(`a release whose provenance does not verify for ${what} is not run, and the failure is mailed`, async () => {
      const f = fake({ attested: [...attested] })
      const r = await run('run.sh', f)
      expect(r.exit).toBe(1)
      expect(existsSync(join(f, 'ran'))).toBe(false)
      expect(published(f)[0]).toContain('does not verify: not running it')
      tokenStaysPut(f)
    })
  }

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

// Everything port25.tf reads is under deploy/, which is all the drift check
// fetches (deploy/drift/drift.sh; scripts/tofu-validate.sh validates that
// layout), so the two verification scripts are copies: they stay scripts/'s.
describe("the port 25 check's configuration stays under deploy/", () => {
  test('the copies of verify-cron.sh and verify-production.sh are identical to scripts/', () => {
    for (const name of ['verify-cron.sh', 'verify-production.sh']) {
      const copy = join(deploy, 'port25', name)
      expect(readFileSync(copy, 'utf8')).toBe(readFileSync(join(import.meta.dir, name), 'utf8'))
      expect(statSync(copy).mode & 0o777).toBe(statSync(join(import.meta.dir, name)).mode & 0o777)
    }
  })

  test('every file deploy/tofu reads is in deploy/, with no dot file on its path', () => {
    const tofu = join(deploy, 'tofu')
    const read = readdirSync(tofu)
      .filter((name) => name.endsWith('.tf'))
      .flatMap((name) => [
        ...readFileSync(join(tofu, name), 'utf8').matchAll(/\$\{path\.module\}\/([^"]+)"/g),
      ])
      .map((m) => resolve(tofu, m[1] ?? ''))
    expect(read).toContain(join(deploy, 'port25', 'verify-cron.sh'))
    for (const path of read) {
      const inside = relative(deploy, path)
      expect(inside.startsWith('..') || isAbsolute(inside)).toBe(false)
      expect(inside.split(sep).some((part) => part.startsWith('.'))).toBe(false)
      expect(existsSync(path)).toBe(true)
    }
    expect(readFileSync(join(tofu, 'port25.tf'), 'utf8')).not.toMatch(
      /path\.(root|cwd)|\.\.\/\.\.\//,
    )
  })
})

// render is templatefile() of the first boot with these values; the template
// uses ${name} and nothing else of OpenTofu's template syntax.
function render(vars: Record<string, string>) {
  const template = readFileSync(join(deploy, 'port25/cloud-init.sh.tftpl'), 'utf8')
  expect(template).not.toMatch(/\$\$\{|%\{/)
  return template.replace(/\$\{([a-z0-9_]+)\}/g, (_, name: string) => {
    const value = vars[name]
    if (value === undefined) {
      throw new Error(`cloud-init.sh.tftpl uses ${name}, which the test does not set`)
    }
    return value
  })
}

describe("the port 25 check's first boot", () => {
  const tf = () => readFileSync(join(deploy, 'tofu/port25.tf'), 'utf8')
  const local = (name: string) =>
    tf().match(new RegExp(`${name}\\s*=\\s*"?([^"\\n]+)"?`))?.[1] ?? ''

  test('port25.tf bounds the download and the setup alert, and passes the template every value', () => {
    expect(Number(local('port25_download_timeout'))).toBeGreaterThan(0)
    expect(Number(local('port25_download_timeout'))).toBeLessThanOrEqual(900)
    expect(Number(local('port25_setup_alert_timeout'))).toBeLessThanOrEqual(120)
    const passed = [...tf().matchAll(/^\s+([a-z0-9_]+)\s+=/gm)].map((m) => m[1])
    const used = [
      ...readFileSync(join(deploy, 'port25/cloud-init.sh.tftpl'), 'utf8').matchAll(
        /\$\{([a-z0-9_]+)\}/g,
      ),
    ].map((m) => m[1])
    for (const name of new Set(used)) expect(passed).toContain(name)
  })

  // boot runs the rendered first boot as an unprivileged user, with a curl that
  // behaves as CURL says, and fakes of what would install anything.
  async function boot(f: string, mode: string, deadline = 2, alert = 2) {
    const fakes = join(f, 'bin')
    mkdirSync(fakes)
    writeFileSync(
      join(fakes, 'curl'),
      `#!/usr/bin/env bash
printf 'curl %s\\n' "$*" >> "$FAKE/argv"
while [[ $# -gt 0 ]]; do case $1 in -o) o=$2; shift 2 ;; *) shift ;; esac; done
case ${mode} in
  stalls) printf 'part of an archive' > "$o"; exec sleep 600 >/dev/null 2>&1 ;;
  ignores-term) printf 'part of an archive' > "$o"; trap '' TERM; while :; do sleep 0.2; done ;;
  other) printf 'not the pinned archive' > "$o" ;;
esac
`,
      { mode: 0o755 },
    )
    for (const name of ['install', 'useradd', 'systemctl', 'tar']) {
      writeFileSync(
        join(fakes, name),
        `#!/usr/bin/env bash\necho "${name} $*" >> "$FAKE/installed"\nexit 1\n`,
        {
          mode: 0o755,
        },
      )
    }
    const script = join(f, 'user-data.sh')
    writeFileSync(
      script,
      render({
        region: 'us-east-2',
        topic_arn: 'arn:aws:sns:us-east-2:000000000000:permaily-sigstore-deadline',
        token_secret:
          'arn:aws:secretsmanager:us-east-2:000000000000:secret:permaily/port25/github-token',
        gh_version: local('port25_gh_version'),
        gh_sha256: local('port25_gh_sha256'),
        download_timeout: String(deadline),
        alert_timeout: String(alert),
        service_unit: '',
        failed_unit: '',
        timer_unit: '',
        verify_cron: '',
        verify_production: '',
        run: '',
        alert: '',
        on_failure: '',
      }),
    )
    const started = Date.now()
    const child = Bun.spawn(['bash', script], {
      env: {
        PATH: `${fakes}:${bin}:${process.env.PATH ?? ''}`,
        HOME: '/nonexistent',
        FAKE: f,
        TMPDIR: f,
        PERMAILY_TOPIC_ARN: 'arn:aws:sns:us-east-2:000000000000:permaily-sigstore-deadline',
      },
      stdin: 'ignore',
      stdout: 'pipe',
      stderr: 'pipe',
    })
    const [, stderr, exit] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ])
    return { exit, stderr, seconds: (Date.now() - started) / 1000 }
  }

  for (const [mode, within] of [
    ['stalls', 2 + 5],
    ['ignores-term', 2 + 10 + 5],
  ] as const) {
    test(`a download that never ends (${mode}) fails the boot within its deadline, mails, and installs nothing`, async () => {
      const f = fake()
      const r = await boot(f, mode)
      expect(r.exit).not.toBe(0)
      expect(r.seconds).toBeLessThan(within)
      expect(published(f)).toEqual([
        expect.stringMatching(/^Permaily: the port 25 check could not be set up\n/),
      ])
      expect(existsSync(join(f, 'installed'))).toBe(false)
      const curl = readFileSync(join(f, 'argv'), 'utf8')
      for (const flag of ['--connect-timeout', '--max-time', '--speed-time', '--retry-max-time'])
        expect(curl).toContain(flag)
    })
  }

  test('an archive that is not the pinned one is not opened, and the boot mails', async () => {
    const f = fake()
    const r = await boot(f, 'other')
    expect(r.exit).not.toBe(0)
    expect(r.stderr).toContain('did NOT match')
    expect(published(f)).toEqual([expect.stringContaining('could not be set up')])
    expect(existsSync(join(f, 'installed'))).toBe(false)
  })

  test('a setup alert that hangs still lets the boot end, within its own deadline', async () => {
    const f = fake()
    writeFileSync(join(f, 'sns-hangs'), '')
    const r = await boot(f, 'stalls', 2, 2)
    expect(r.exit).not.toBe(0)
    expect(r.seconds).toBeLessThan(2 + 2 + 5 + 3)
    expect(attempts(f)).toBe(1)
    expect(existsSync(join(f, 'installed'))).toBe(false)
  })
})
