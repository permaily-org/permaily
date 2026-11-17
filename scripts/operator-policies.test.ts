import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

// The operators' three group policies (deploy/tofu/operators/*.json.tftpl,
// ADR 0036) rendered as OpenTofu renders them and evaluated as IAM evaluates
// identity policies in one account: an explicit Deny wins, then an Allow,
// otherwise denied. Only the condition operators the policies use are
// implemented; an unknown one fails the test rather than being ignored.

const account = '111122223333'
const region = 'us-east-2'
const vars: Record<string, string> = {
  account_id: account,
  region,
  mfa_enrollment_tag: 'permaily-mfa-enrollment',
  state_key_arn: `arn:aws:kms:${region}:${account}:key/state`,
  artifacts_arn: 'arn:aws:s3:::permaily-artifacts',
  anchors_arn: 'arn:aws:s3:::permaily-anchors',
  instance_arn: `arn:aws:ec2:${region}:${account}:instance/i-0123456789abcdef0`,
}

type Statement = {
  Sid: string
  Effect: 'Allow' | 'Deny'
  Action?: string | string[]
  NotAction?: string | string[]
  Resource?: string | string[]
  NotResource?: string | string[]
  Condition?: Record<string, Record<string, string>>
}

// render does what templatefile does with these templates: ${name} is a
// variable, $${ is a literal ${.
function render(name: string): Statement[] {
  const text = readFileSync(
    join(import.meta.dir, '../deploy/tofu/operators', `${name}.json.tftpl`),
    'utf8',
  )
  const out = text.replace(/\$\$\{|\$\{([a-z_]+)\}/g, (m, v: string | undefined) => {
    if (m === '$${') return '${'
    const value = vars[v as string]
    if (value === undefined) throw new Error(`no variable ${v} for ${name}`)
    return value
  })
  return JSON.parse(out).Statement
}

const statements = ['require-mfa', 'operate', 'guard'].flatMap(render)

type Request = {
  action: string
  resource: string
  // mfa is aws:MultiFactorAuthPresent: absent for a long-term access key.
  mfa?: boolean
  // enrolling sets the user's tag permaily-mfa-enrollment=open.
  enrolling?: boolean
}

const user = 'permaily-operator-2'
const self = `arn:aws:iam::${account}:user/${user}`
const ownDevice = `arn:aws:iam::${account}:mfa/${user}`

const list = (v: string | string[] | undefined) =>
  v === undefined ? [] : Array.isArray(v) ? v : [v]

function glob(pattern: string, value: string, ignoreCase: boolean) {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*/g, '.*')
    .replace(/\?/g, '.')
  return new RegExp(`^${re}$`, ignoreCase ? 'i' : '').test(value)
}

function substitute(pattern: string) {
  // ${aws:username}, the policy variable, written so it is not a template string.
  return pattern.replaceAll(`\${aws:username}`, user)
}

function conditionHolds(op: string, key: string, want: string, req: Request) {
  const context: Record<string, string | undefined> = {
    'aws:MultiFactorAuthPresent': req.mfa === undefined ? undefined : String(req.mfa),
    'aws:PrincipalTag/permaily-mfa-enrollment': req.enrolling ? 'open' : undefined,
    'aws:username': user,
  }
  const got = context[key]
  switch (op) {
    case 'Bool':
      return got !== undefined && got === want
    case 'BoolIfExists':
      return got === undefined || got === want
    case 'StringEquals':
      return got !== undefined && got === want
    case 'StringNotEquals':
      return got === undefined || got !== want
    default:
      throw new Error(`condition operator ${op} is not implemented here`)
  }
}

function applies(s: Statement, req: Request) {
  const action = s.Action
    ? list(s.Action).some((a) => glob(a, req.action, true))
    : !list(s.NotAction).some((a) => glob(a, req.action, true))
  if (!action) return false
  const resource = s.Resource
    ? list(s.Resource).some((r) => glob(substitute(r), req.resource, false))
    : !list(s.NotResource).some((r) => glob(substitute(r), req.resource, false))
  if (!resource) return false
  for (const [op, keys] of Object.entries(s.Condition ?? {})) {
    for (const [key, want] of Object.entries(keys)) {
      if (!conditionHolds(op, key, want, req)) return false
    }
  }
  return true
}

function decide(req: Request): 'allowed' | 'denied' {
  const matching = statements.filter((s) => applies(s, req))
  if (matching.some((s) => s.Effect === 'Deny')) return 'denied'
  return matching.some((s) => s.Effect === 'Allow') ? 'allowed' : 'denied'
}

const enroll = [
  { action: 'iam:CreateVirtualMFADevice', resource: ownDevice },
  { action: 'iam:EnableMFADevice', resource: self },
]
const operate = [
  { action: 'ssm:SendCommand', resource: vars.instance_arn as string },
  { action: 'ssm:StartSession', resource: vars.instance_arn as string },
  {
    action: 's3:GetObject',
    resource: `arn:aws:s3:::permaily-tofu-state-${account}/permaily/terraform.tfstate`,
  },
  { action: 'kms:Decrypt', resource: vars.state_key_arn as string },
]
const escalate = [
  { action: 'iam:TagUser', resource: self },
  { action: 'iam:UntagUser', resource: self },
  { action: 'iam:PutUserPolicy', resource: self },
  { action: 'iam:AttachUserPolicy', resource: self },
  { action: 'iam:AddUserToGroup', resource: `arn:aws:iam::${account}:group/permaily-operators` },
  { action: 'iam:CreateLoginProfile', resource: self },
  {
    action: 'iam:CreateVirtualMFADevice',
    resource: `arn:aws:iam::${account}:mfa/permaily-operator-3`,
  },
  {
    action: 'iam:DeleteVirtualMFADevice',
    resource: `arn:aws:iam::${account}:mfa/permaily-operator-3`,
  },
  { action: 'iam:EnableMFADevice', resource: `arn:aws:iam::${account}:user/permaily-operator-3` },
  { action: 'iam:CreateAccessKey', resource: `arn:aws:iam::${account}:user/permaily-operator-3` },
  { action: 'kms:PutKeyPolicy', resource: `arn:aws:kms:${region}:${account}:key/release` },
  { action: 'kms:ScheduleKeyDeletion', resource: `arn:aws:kms:${region}:${account}:key/release` },
  {
    action: 's3:PutObject',
    resource: `arn:aws:s3:::permaily-tofu-state-${account}/permaily/terraform.tfstate`,
  },
  { action: 'dynamodb:GetItem', resource: `arn:aws:dynamodb:${region}:${account}:table/permaily` },
]

describe('a long-term access key without MFA, enrollment closed (after onboarding)', () => {
  test('cannot enroll a device of its own', () => {
    for (const r of enroll) expect([r.action, decide(r)]).toEqual([r.action, 'denied'])
  })
  test('cannot operate, make another key or escalate', () => {
    for (const r of [...operate, ...escalate, { action: 'iam:CreateAccessKey', resource: self }]) {
      expect([r.action, r.resource, decide(r)]).toEqual([r.action, r.resource, 'denied'])
    }
  })
  test('the same with aws:MultiFactorAuthPresent false (a session made without MFA)', () => {
    for (const r of [...enroll, ...operate, { action: 'iam:CreateAccessKey', resource: self }]) {
      expect([r.action, decide({ ...r, mfa: false })]).toEqual([r.action, 'denied'])
    }
  })
})

describe('enrollment open (the owner tagged the user)', () => {
  test('signed in without MFA, the first device goes in, under their own name only', () => {
    for (const r of enroll)
      expect([r.action, decide({ ...r, mfa: false, enrolling: true })]).toEqual([
        r.action,
        'allowed',
      ])
    expect(
      decide({
        action: 'iam:CreateVirtualMFADevice',
        resource: `arn:aws:iam::${account}:mfa/other`,
        mfa: false,
        enrolling: true,
      }),
    ).toBe('denied')
  })
  test('no access key is made or turned back on, with MFA or without', () => {
    for (const mfa of [undefined, false, true]) {
      for (const action of ['iam:CreateAccessKey', 'iam:UpdateAccessKey']) {
        expect([action, mfa, decide({ action, resource: self, mfa, enrolling: true })]).toEqual([
          action,
          mfa,
          'denied',
        ])
      }
    }
  })
  test('they cannot close or keep it open themselves, nor operate without MFA', () => {
    for (const r of [...escalate, ...operate]) {
      expect([r.action, decide({ ...r, mfa: false, enrolling: true })]).toEqual([
        r.action,
        'denied',
      ])
    }
  })
})

describe('signed in with MFA, enrollment closed', () => {
  test('they operate', () => {
    for (const r of operate)
      expect([r.action, decide({ ...r, mfa: true })]).toEqual([r.action, 'allowed'])
  })
  test('they manage their own credentials and add a device', () => {
    for (const action of [
      'iam:CreateAccessKey',
      'iam:DeleteAccessKey',
      'iam:EnableMFADevice',
      'iam:DeactivateMFADevice',
    ]) {
      expect([action, decide({ action, resource: self, mfa: true })]).toEqual([action, 'allowed'])
    }
    expect(decide({ action: 'iam:CreateVirtualMFADevice', resource: ownDevice, mfa: true })).toBe(
      'allowed',
    )
  })
  test('the guard holds', () => {
    for (const r of escalate) {
      expect([r.action, r.resource, decide({ ...r, mfa: true })]).toEqual([
        r.action,
        r.resource,
        'denied',
      ])
    }
  })
})
