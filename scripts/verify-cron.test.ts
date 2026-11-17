import { afterAll, beforeAll, describe, expect, setDefaultTimeout, test } from 'bun:test'
import { spawn } from 'node:child_process'
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  utimesSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

// scripts/verify-production.sh and scripts/verify-cron.sh, run with a fake gh
// that serves two releases (v0.2.0, the latest, and v0.1.0). Each release's
// "verifier" is a script that records it ran and passes: whatever runs it
// without checking its provenance first would report success.

setDefaultTimeout(30_000)

let dir: string
let bin: string
let scripts: string

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), 'verify-cron-'))
  bin = join(dir, 'bin')
  scripts = join(dir, 'scripts')
  mkdirSync(bin)
  mkdirSync(scripts)
  for (const f of ['verify-production.sh', 'verify-cron.sh']) {
    copyFileSync(join(import.meta.dir, f), join(scripts, f))
  }
  // gh release list | release download | attestation verify, from $FAKE (a directory per test).
  writeFileSync(
    join(bin, 'gh'),
    `#!/usr/bin/env bash
case "$1 $2" in
  "release list") printf 'v0.2.0\\nv0.1.0\\n' ;;
  "release download")
    tag=$3; shift 3
    while [[ $# -gt 0 ]]; do case $1 in --dir) d=$2; shift 2 ;; *) shift ;; esac; done
    [[ -e "$FAKE/no-download-$tag" ]] && exit 1
    printf '{}' > "$d/pcrs.json"
    for a in amd64 arm64; do
      printf '#!/usr/bin/env bash\\necho ran >> "%s/ran-%s"\\nexit 0\\n' "$FAKE" "$tag" > "$d/permaily-verify-linux-$a"
    done ;;
  "attestation verify")
    case $3 in */v0.2.0/*) tag=v0.2.0 ;; */v0.1.0/*) tag=v0.1.0 ;; esac
    [[ -e "$FAKE/attested-$tag" ]] || { echo "no attestation for $3" >&2; exit 1; } ;;
  *) echo "unexpected gh $*" >&2; exit 2 ;;
esac
`,
    { mode: 0o755 },
  )
})

afterAll(() => {
  // A hung alert command left by a failing test: stop its process group.
  for (const f of readdirSync(dir)) {
    const pid = join(dir, f, 'alert-pid')
    if (existsSync(pid)) {
      try {
        process.kill(-Number(readFileSync(pid, 'utf8')), 'SIGKILL')
      } catch {}
    }
  }
  rmSync(dir, { recursive: true, force: true })
})

let n = 0
function fake(attested: string[]) {
  const f = join(dir, `fake-${n++}`)
  mkdirSync(f)
  for (const tag of attested) writeFileSync(join(f, `attested-${tag}`), '')
  return f
}

// alive is whether PID still runs (a zombie, killed and not yet reaped, does not).
function alive(pid: number) {
  try {
    process.kill(pid, 0)
  } catch {
    return false
  }
  try {
    return readFileSync(`/proc/${pid}/stat`, 'utf8').split(') ')[1]?.[0] !== 'Z'
  } catch {
    return false
  }
}

// gone waits up to 2 s for PID to stop running.
async function gone(pid: number) {
  for (let i = 0; i < 40 && alive(pid); i++) await Bun.sleep(50)
  return !alive(pid)
}

function run(script: string, env: Record<string, string>, root = scripts) {
  const child = Bun.spawn(['bash', join(root, script)], {
    env: { PATH: `${bin}:${process.env.PATH ?? ''}`, HOME: '/nonexistent', ...env },
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

describe('verify-production.sh runs only a verifier whose provenance verifies', () => {
  test('neither release attested: no verifier runs, and it fails', async () => {
    const f = fake([])
    const r = await run('verify-production.sh', { FAKE: f })
    expect(r.exit).toBe(1)
    expect(existsSync(join(f, 'ran-v0.2.0'))).toBe(false)
    expect(existsSync(join(f, 'ran-v0.1.0'))).toBe(false)
    expect(r.stdout).toContain('RESULT: production does not verify')
  })

  test('only the previous release attested: only its verifier runs', async () => {
    const f = fake(['v0.1.0'])
    const r = await run('verify-production.sh', { FAKE: f })
    expect(r.exit).toBe(0)
    expect(existsSync(join(f, 'ran-v0.2.0'))).toBe(false)
    expect(existsSync(join(f, 'ran-v0.1.0'))).toBe(true)
    expect(r.stdout).toContain('production runs v0.1.0')
  })

  test('a download that fails runs nothing of that release', async () => {
    const f = fake(['v0.2.0', 'v0.1.0'])
    writeFileSync(join(f, 'no-download-v0.2.0'), '')
    const r = await run('verify-production.sh', { FAKE: f })
    expect(r.exit).toBe(0)
    expect(existsSync(join(f, 'ran-v0.2.0'))).toBe(false)
    expect(existsSync(join(f, 'ran-v0.1.0'))).toBe(true)
  })

  test('the latest attested: its verifier runs and passes', async () => {
    const f = fake(['v0.2.0', 'v0.1.0'])
    const r = await run('verify-production.sh', { FAKE: f })
    expect(r.exit).toBe(0)
    expect(existsSync(join(f, 'ran-v0.2.0'))).toBe(true)
    expect(existsSync(join(f, 'ran-v0.1.0'))).toBe(false)
  })
})

describe('verify-cron.sh', () => {
  // cronEnv is a state directory and an alert command that copies the report to alert.txt.
  function cronEnv(f: string, extra: Record<string, string> = {}) {
    return {
      FAKE: f,
      XDG_STATE_HOME: join(f, 'state'),
      PERMAILY_ALERT_CMD: `cat > '${join(f, 'alert.txt')}'`,
      ...extra,
    }
  }

  test('passes quietly when production verifies', async () => {
    const f = fake(['v0.2.0'])
    const r = await run('verify-cron.sh', cronEnv(f))
    expect(r.exit).toBe(0)
    expect(r.stdout).toBe('')
    expect(existsSync(join(f, 'alert.txt'))).toBe(false)
  })

  test('provenance rejected for both releases: fails and alerts, and runs no verifier', async () => {
    const f = fake([])
    const r = await run('verify-cron.sh', cronEnv(f))
    expect(r.exit).toBe(1)
    expect(existsSync(join(f, 'ran-v0.2.0'))).toBe(false)
    expect(existsSync(join(f, 'ran-v0.1.0'))).toBe(false)
    expect(readFileSync(join(f, 'alert.txt'), 'utf8')).toContain(
      'RESULT: production does not verify',
    )
  })

  // A copy of the scripts whose verify-production.sh hangs, with a child that would outlive it.
  function hanging() {
    const root = mkdtempSync(join(dir, 'hang-'))
    copyFileSync(join(import.meta.dir, 'verify-cron.sh'), join(root, 'verify-cron.sh'))
    writeFileSync(
      join(root, 'verify-production.sh'),
      `#!/usr/bin/env bash
sleep 600 &
echo $! > "$FAKE/child"
echo "downloading"
wait
`,
      { mode: 0o755 },
    )
    return root
  }

  test('a run that hangs is stopped at the deadline, with its children, and alerts', async () => {
    const f = fake([])
    const started = Date.now()
    const r = await run(
      'verify-cron.sh',
      cronEnv(f, { PERMAILY_VERIFY_TIMEOUT: '2', PERMAILY_VERIFY_GRACE: '1' }),
      hanging(),
    )
    expect(r.exit).toBe(1)
    expect(Date.now() - started).toBeLessThan(15_000)
    const alert = readFileSync(join(f, 'alert.txt'), 'utf8')
    expect(alert).toContain('downloading')
    expect(alert).toContain('did not finish within 2 s')
    const child = Number(readFileSync(join(f, 'child'), 'utf8'))
    expect(await gone(child)).toBe(true)
  })

  // holdLock takes the cron's lock in another process, as a run still going would.
  async function holdLock(f: string) {
    const state = join(f, 'state', 'permaily-verify-cron')
    mkdirSync(state, { recursive: true })
    const holder = spawn('flock', [join(state, 'lock'), 'sleep', '30'], { stdio: 'ignore' })
    for (let i = 0; i < 50; i++) {
      const probe = Bun.spawnSync(['flock', '-n', join(state, 'lock'), 'true'])
      if (probe.exitCode !== 0) break
      await Bun.sleep(50)
    }
    return { state, holder }
  }

  test('a run still going within its deadline: the next one leaves it be', async () => {
    const f = fake(['v0.2.0'])
    const { state, holder } = await holdLock(f)
    try {
      writeFileSync(join(state, 'started'), `${Math.floor(Date.now() / 1000)}\n`)
      const r = await run('verify-cron.sh', cronEnv(f))
      expect(r.exit).toBe(0)
      expect(r.stderr).toContain('still going')
      expect(existsSync(join(f, 'alert.txt'))).toBe(false)
    } finally {
      holder.kill()
    }
  })

  test('a lock held past the deadline is a failure, and alerts', async () => {
    const f = fake(['v0.2.0'])
    const { state, holder } = await holdLock(f)
    try {
      const old = Math.floor(Date.now() / 1000) - 7200
      writeFileSync(join(state, 'started'), `${old}\n`)
      utimesSync(join(state, 'started'), old, old)
      const r = await run('verify-cron.sh', cronEnv(f))
      expect(r.exit).toBe(1)
      expect(readFileSync(join(f, 'alert.txt'), 'utf8')).toContain('still holds the lock')
    } finally {
      holder.kill()
    }
  })

  test('a lock that cannot be taken at all is a failure, and alerts', async () => {
    const f = fake(['v0.2.0'])
    const state = join(f, 'state', 'permaily-verify-cron')
    mkdirSync(join(state, 'lock'), { recursive: true })
    const r = await run('verify-cron.sh', cronEnv(f))
    expect(r.exit).toBe(1)
    expect(readFileSync(join(f, 'alert.txt'), 'utf8')).toContain('lock')
  })

  // A copy of the scripts whose verify-production.sh counts its runs in
  // $FAKE/runs and passes once $FAKE/pass exists, failing before.
  function counting() {
    const root = mkdtempSync(join(dir, 'count-'))
    copyFileSync(join(import.meta.dir, 'verify-cron.sh'), join(root, 'verify-cron.sh'))
    writeFileSync(
      join(root, 'verify-production.sh'),
      `#!/usr/bin/env bash
echo run >> "$FAKE/runs"
[[ -e "$FAKE/pass" ]] && exit 0
echo "RESULT: production does not verify"
exit 1
`,
      { mode: 0o755 },
    )
    return root
  }
  const runs = (f: string) =>
    existsSync(join(f, 'runs')) ? readFileSync(join(f, 'runs'), 'utf8').split('\n').length - 1 : 0

  // An alert command that takes the report, then hangs ignoring TERM, with a
  // child; it records its own pid (its process group) and the child's.
  function hungAlert(f: string) {
    return `cat > '${join(f, 'alert.txt')}'; trap '' TERM; sleep 600 & echo $! > '${join(f, 'alert-child')}'; echo $$ > '${join(f, 'alert-pid')}'; wait`
  }
  const pidIn = (f: string, name: string) => Number(readFileSync(join(f, name), 'utf8'))
  const lockFree = (f: string) =>
    Bun.spawnSync(['flock', '-n', join(f, 'state', 'permaily-verify-cron', 'lock'), 'true'])
      .exitCode === 0

  test('an alert that hangs is stopped at its own deadline, and the next run verifies', async () => {
    const f = fake([])
    const root = counting()
    const env = cronEnv(f, {
      PERMAILY_ALERT_CMD: hungAlert(f),
      PERMAILY_ALERT_TIMEOUT: '1',
      PERMAILY_VERIFY_GRACE: '1',
    })
    const started = Date.now()
    const r = await run('verify-cron.sh', env, root)
    expect(r.exit).toBe(1)
    expect(Date.now() - started).toBeLessThan(10_000)
    expect(r.stdout).toContain('RESULT: production does not verify')
    expect(r.stderr).toContain('the alert command did not finish within 1 s')
    expect(readFileSync(join(f, 'alert.txt'), 'utf8')).toContain('production does not verify')
    expect(await gone(pidIn(f, 'alert-pid'))).toBe(true)
    expect(await gone(pidIn(f, 'alert-child'))).toBe(true)
    expect(lockFree(f)).toBe(true)
    writeFileSync(join(f, 'pass'), '')
    const next = await run('verify-cron.sh', env, root)
    expect(next.exit).toBe(0)
    expect(runs(f)).toBe(2)
  })

  test('the alert runs without the lock: a run while it hangs verifies', async () => {
    const f = fake([])
    const root = counting()
    const env = cronEnv(f, { PERMAILY_ALERT_CMD: hungAlert(f), PERMAILY_ALERT_TIMEOUT: '60' })
    const first = run('verify-cron.sh', env, root)
    for (let i = 0; i < 100 && !existsSync(join(f, 'alert-pid')); i++) await Bun.sleep(50)
    const pgid = pidIn(f, 'alert-pid')
    try {
      expect(alive(pgid)).toBe(true)
      expect(lockFree(f)).toBe(true)
      writeFileSync(join(f, 'pass'), '')
      const next = await run('verify-cron.sh', env, root)
      expect(next.exit).toBe(0)
      expect(next.stderr).not.toContain('still going')
      expect(runs(f)).toBe(2)
    } finally {
      process.kill(-pgid, 'SIGKILL')
    }
    const r = await first
    expect(r.exit).toBe(1)
    expect(r.stderr).toContain('the alert command failed')
  })

  test('a stale lock whose alert hangs: the run still ends within the alert deadline', async () => {
    const f = fake(['v0.2.0'])
    const { state, holder } = await holdLock(f)
    try {
      const old = Math.floor(Date.now() / 1000) - 7200
      writeFileSync(join(state, 'started'), `${old}\n`)
      const started = Date.now()
      const r = await run(
        'verify-cron.sh',
        cronEnv(f, {
          PERMAILY_ALERT_CMD: hungAlert(f),
          PERMAILY_ALERT_TIMEOUT: '1',
          PERMAILY_VERIFY_GRACE: '1',
        }),
      )
      expect(r.exit).toBe(1)
      expect(Date.now() - started).toBeLessThan(10_000)
      expect(r.stdout).toContain('still holds the lock')
      expect(r.stderr).toContain('the alert command did not finish within 1 s')
      expect(await gone(pidIn(f, 'alert-pid'))).toBe(true)
    } finally {
      holder.kill()
    }
  })

  test('an alert command that fails: still exit 1, and it says so', async () => {
    const f = fake([])
    const r = await run(
      'verify-cron.sh',
      cronEnv(f, { PERMAILY_ALERT_CMD: 'cat >/dev/null; exit 3' }),
    )
    expect(r.exit).toBe(1)
    expect(r.stdout).toContain('RESULT: production does not verify')
    expect(r.stderr).toContain('the alert command failed')
  })
})
