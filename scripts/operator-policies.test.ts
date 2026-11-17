import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

// The operators' three policies (deploy/tofu/operators/*.json.tftpl, ADR 0036)
// rendered as OpenTofu renders them and evaluated as IAM evaluates identity
// policies in one account: an explicit Deny wins, then an Allow, otherwise an
// implicit deny. Only the condition operators the policies use are
// implemented; an unknown one fails the test rather than being ignored.
//
// Three kinds of test: what the three policies allow by themselves (least
// privilege); that each deny meant to hold whatever else is attached still
// denies, by the statement meant to, next to a policy that allows everything;
// and that weakening any of those denies (a statement removed, an action or a
// resource dropped, a condition loosened, the whole guard deleted) fails the
// second kind. Plus IAM's size limits on the rendered documents.

const dir = join(import.meta.dir, '../deploy/tofu')
const account = '111122223333'
const region = 'us-east-2'
// Identifiers as long as the real ones, for the size limits: a KMS key id is a
// UUID, an instance id 17 hexadecimal digits, the buckets carry the account id.
const vars: Record<string, string> = {
  account_id: account,
  region,
  mfa_enrollment_tag: 'permaily-mfa-enrollment',
  state_key_arn: `arn:aws:kms:${region}:${account}:key/0123abcd-45ef-6789-abcd-ef0123456789`,
  artifacts_arn: `arn:aws:s3:::permaily-artifacts-${account}`,
  anchors_arn: `arn:aws:s3:::permaily-anchors-${account}`,
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

// renderText does what templatefile does with these templates: ${name} is a
// variable, $${ is a literal ${. jsonencode(jsondecode(...)) then makes it
// compact, as JSON.stringify(JSON.parse(...)) does.
function renderText(name: string) {
  const text = readFileSync(join(dir, 'operators', `${name}.json.tftpl`), 'utf8')
  const out = text.replace(/\$\$\{|\$\{([a-z_]+)\}/g, (m, v: string | undefined) => {
    if (m === '$${') return '${'
    const value = vars[v as string]
    if (value === undefined) throw new Error(`no variable ${v} for ${name}`)
    return value
  })
  return JSON.stringify(JSON.parse(out))
}
const render = (name: string): Statement[] => JSON.parse(renderText(name)).Statement

const policies = {
  'require-mfa': render('require-mfa'),
  operate: render('operate'),
  guard: render('guard'),
}
const statements = Object.values(policies).flat()

type Request = {
  action: string
  resource: string
  // mfa is aws:MultiFactorAuthPresent: absent for a long-term access key.
  mfa?: boolean
  // enrolling sets the user's tag permaily-mfa-enrollment=open (the owner does).
  enrolling?: boolean
}

const user = 'permaily-operator-2'
const self = `arn:aws:iam::${account}:user/${user}`
const ownDevice = `arn:aws:iam::${account}:mfa/${user}`
const other = `arn:aws:iam::${account}:user/permaily-operator-3`
const otherDevice = `arn:aws:iam::${account}:mfa/permaily-operator-3`
const group = `arn:aws:iam::${account}:group/permaily-operators`
const guardPolicy = `arn:aws:iam::${account}:policy/permaily-operators-guard`
const role = `arn:aws:iam::${account}:role/permaily-enclave-host`
const releaseKey = `arn:aws:kms:${region}:${account}:key/fedcba98-7654-3210-fedc-ba9876543210`
const stateBucket = `arn:aws:s3:::permaily-tofu-state-${account}`
const stateObject = `${stateBucket}/permaily/terraform.tfstate`
const table = `arn:aws:dynamodb:${region}:${account}:table/permaily-mail`

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

type Decision = { decision: 'allowed' | 'explicit deny' | 'implicit deny'; denies: string[] }

// evaluate tells an explicit deny, and the statements that made it, from an
// implicit one.
function evaluate(req: Request, from: Statement[] = statements): Decision {
  const matching = from.filter((s) => applies(s, req))
  const denies = matching.filter((s) => s.Effect === 'Deny').map((s) => s.Sid)
  if (denies.length > 0) return { decision: 'explicit deny', denies }
  const allowed = matching.some((s) => s.Effect === 'Allow')
  return { decision: allowed ? 'allowed' : 'implicit deny', denies }
}

const decide = (req: Request) => (evaluate(req).decision === 'allowed' ? 'allowed' : 'denied')

// Another attachment that allows everything: what IAM would add to the three
// policies if anything else were ever attached to the group or the user.
const everything: Statement = {
  Sid: 'AnotherAttachment',
  Effect: 'Allow',
  Action: '*',
  Resource: '*',
}

const contexts = [undefined, false, true].flatMap((mfa) =>
  [false, true].map((enrolling) => ({ mfa, enrolling })),
)
const withoutMFA = contexts.filter((c) => c.mfa !== true)

// Each deny meant to hold whatever else is attached, with requests it must deny
// and the contexts it must deny them in. Written out by hand, never derived from
// the policies, so that a weakened policy cannot weaken its own test.
type Case = { sid: string; action: string; resource: string; in: typeof contexts }
const held = (
  sid: string,
  inContexts: typeof contexts,
  requests: [action: string, resource: string][],
): Case[] => requests.map(([action, resource]) => ({ sid, action, resource, in: inContexts }))

const guardCases: Case[] = [
  ...held(
    'NoKeyChanges',
    contexts,
    [
      'kms:CreateKey',
      'kms:PutKeyPolicy',
      'kms:ScheduleKeyDeletion',
      'kms:CancelKeyDeletion',
      'kms:CreateGrant',
      'kms:RetireGrant',
      'kms:RevokeGrant',
      'kms:EnableKey',
      'kms:DisableKey',
      'kms:CreateAlias',
      'kms:UpdateAlias',
      'kms:DeleteAlias',
      'kms:ReplicateKey',
      'kms:UpdateKeyDescription',
      'kms:UpdatePrimaryRegion',
      'kms:EnableKeyRotation',
      'kms:DisableKeyRotation',
      'kms:TagResource',
      'kms:UntagResource',
    ].map((a) => [a, releaseKey]),
  ),
  ...held('NoKeyChanges', contexts, [['kms:PutKeyPolicy', vars.state_key_arn as string]]),
  ...held('NoIAMButTheirOwn', contexts, [
    ['iam:AddUserToGroup', group],
    ['iam:AttachGroupPolicy', group],
    ['iam:ChangePassword', other],
    ['iam:CreateUser', `arn:aws:iam::${account}:user/permaily-operator-9`],
    ['iam:CreateAccessKey', other],
    ['iam:CreatePolicyVersion', guardPolicy],
    ['iam:CreateVirtualMFADevice', otherDevice],
    ['iam:DeactivateMFADevice', other],
    ['iam:DeleteVirtualMFADevice', otherDevice],
    ['iam:DeletePolicy', guardPolicy],
    ['iam:DetachGroupPolicy', group],
    ['iam:EnableMFADevice', other],
    ['iam:PassRole', role],
    ['iam:PutGroupPolicy', group],
    ['iam:RemoveUserFromGroup', group],
    ['iam:ResyncMFADevice', other],
    ['iam:SetDefaultPolicyVersion', guardPolicy],
    ['iam:TagUser', other],
    ['iam:UntagUser', other],
    ['iam:UpdateAssumeRolePolicy', role],
    ['iam:UpdateLoginProfile', other],
    ['iam:UploadSSHPublicKey', other],
  ]),
  // On themselves, with MFA or without, the enrollment open or closed: their
  // tags (the enrollment window), policies, groups and other credentials.
  ...held('OnThemselvesOnlyTheirCredentials', contexts, [
    ['iam:TagUser', self],
    ['iam:UntagUser', self],
    ['iam:PutUserPolicy', self],
    ['iam:AttachUserPolicy', self],
    ['iam:DeleteUserPolicy', self],
    ['iam:DetachUserPolicy', self],
    ['iam:PutUserPermissionsBoundary', self],
    ['iam:DeleteUserPermissionsBoundary', self],
    ['iam:CreateLoginProfile', self],
    ['iam:UpdateLoginProfile', self],
    ['iam:DeleteLoginProfile', self],
    ['iam:UpdateUser', self],
    ['iam:DeleteUser', self],
    ['iam:UploadSSHPublicKey', self],
    ['iam:UploadSigningCertificate', self],
    ['iam:CreateServiceSpecificCredential', self],
  ]),
  ...held('OnTheirDeviceOnlyCreateAndDelete', contexts, [
    ['iam:TagMFADevice', ownDevice],
    ['iam:UntagMFADevice', ownDevice],
  ]),
  ...held('NoStateWrites', contexts, [
    ['s3:PutObject', stateObject],
    ['s3:PutObject', `${stateBucket}/another/key`],
    ['s3:DeleteObject', stateObject],
    ['s3:DeleteObjectVersion', stateObject],
    ['s3:PutBucketPolicy', stateBucket],
    ['s3:PutBucketVersioning', stateBucket],
    ['s3:PutLifecycleConfiguration', stateBucket],
  ]),
  ...held('NoTable', contexts, [
    ['dynamodb:GetItem', table],
    ['dynamodb:Query', table],
    ['dynamodb:Scan', table],
    ['dynamodb:PutItem', table],
    ['dynamodb:UpdateItem', table],
    ['dynamodb:DeleteItem', table],
    ['dynamodb:DeleteTable', table],
  ]),
]

const mfaCases: Case[] = [
  // Without MFA, nothing but the password and (below) the first device.
  ...held('NothingElseWithoutMFA', withoutMFA, [
    ['ssm:SendCommand', vars.instance_arn as string],
    ['ssm:StartSession', vars.instance_arn as string],
    ['s3:GetObject', stateObject],
    ['kms:Decrypt', vars.state_key_arn as string],
    ['s3:PutObject', `${vars.artifacts_arn}/artifacts/x`],
    ['lambda:InvokeFunction', `arn:aws:lambda:${region}:${account}:function:permaily-drift`],
    ['ec2:TerminateInstances', vars.instance_arn as string],
    ['iam:CreateAccessKey', self],
    ['iam:DeactivateMFADevice', self],
    ['iam:DeleteVirtualMFADevice', ownDevice],
  ]),
  // Without MFA and the enrollment closed: no device of their own.
  ...held(
    'NoEnrollmentWithoutMFAUnlessOpen',
    withoutMFA.filter((c) => !c.enrolling),
    [
      ['iam:CreateVirtualMFADevice', ownDevice],
      ['iam:EnableMFADevice', self],
    ],
  ),
  // While enrolling, with MFA or without: no access key made or turned back on.
  ...held(
    'NoAccessKeyWhileEnrolling',
    contexts.filter((c) => c.enrolling),
    [
      ['iam:CreateAccessKey', self],
      ['iam:UpdateAccessKey', self],
    ],
  ),
]

const heldCases = [...guardCases, ...mfaCases]

// violations lists every held case that the statements, next to an attachment
// that allows everything, do not deny explicitly by the statement meant to.
function violations(from: Statement[]) {
  const out: string[] = []
  for (const c of heldCases) {
    for (const ctx of c.in) {
      const d = evaluate({ action: c.action, resource: c.resource, ...ctx }, [...from, everything])
      if (d.decision !== 'explicit deny' || !d.denies.includes(c.sid)) {
        out.push(`${c.sid}: ${c.action} ${c.resource} mfa=${ctx.mfa} enrolling=${ctx.enrolling}`)
      }
    }
  }
  return out
}

describe('what the three policies allow by themselves', () => {
  const enroll = [
    { action: 'iam:CreateVirtualMFADevice', resource: ownDevice },
    { action: 'iam:EnableMFADevice', resource: self },
  ]
  const operate = [
    { action: 'ssm:SendCommand', resource: vars.instance_arn as string },
    { action: 'ssm:StartSession', resource: vars.instance_arn as string },
    { action: 's3:GetObject', resource: stateObject },
    { action: 'kms:Decrypt', resource: vars.state_key_arn as string },
  ]
  const escalate = heldCases
    .filter((c) => c.in === contexts)
    .map((c) => ({ action: c.action, resource: c.resource }))

  test('a long-term key without MFA, enrollment closed: no device of its own, nothing else', () => {
    for (const r of enroll) {
      expect([r.action, evaluate(r).decision]).toEqual([r.action, 'explicit deny'])
    }
    for (const r of [...operate, ...escalate, { action: 'iam:CreateAccessKey', resource: self }]) {
      expect([r.action, r.resource, decide(r)]).toEqual([r.action, r.resource, 'denied'])
    }
  })
  test('the same with aws:MultiFactorAuthPresent false (a session made without MFA)', () => {
    for (const r of [...enroll, ...operate, { action: 'iam:CreateAccessKey', resource: self }]) {
      expect([r.action, decide({ ...r, mfa: false })]).toEqual([r.action, 'denied'])
    }
  })
  test('enrollment open, without MFA: the first device goes in, under their own name only', () => {
    for (const r of enroll) {
      expect([r.action, decide({ ...r, mfa: false, enrolling: true })]).toEqual([
        r.action,
        'allowed',
      ])
    }
    expect(
      decide({
        action: 'iam:CreateVirtualMFADevice',
        resource: `arn:aws:iam::${account}:mfa/other`,
        mfa: false,
        enrolling: true,
      }),
    ).toBe('denied')
  })
  test('enrollment open: no access key made or turned back on, nothing operated without MFA', () => {
    for (const mfa of [undefined, false, true]) {
      for (const action of ['iam:CreateAccessKey', 'iam:UpdateAccessKey']) {
        expect([action, mfa, decide({ action, resource: self, mfa, enrolling: true })]).toEqual([
          action,
          mfa,
          'denied',
        ])
      }
    }
    for (const r of [...escalate, ...operate]) {
      expect([r.action, decide({ ...r, mfa: false, enrolling: true })]).toEqual([
        r.action,
        'denied',
      ])
    }
  })
  test('with MFA, enrollment closed: they operate and manage their own credentials', () => {
    for (const r of operate) {
      expect([r.action, decide({ ...r, mfa: true })]).toEqual([r.action, 'allowed'])
    }
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
})

describe('the denies hold next to an attachment that allows everything', () => {
  test('every held case is denied explicitly, by the statement meant to', () => {
    expect(violations(statements)).toEqual([])
  })
  test('the allow-everything attachment allows what no deny covers (the evaluator is not denying everything)', () => {
    expect(
      evaluate({ action: 'ssm:SendCommand', resource: vars.instance_arn as string, mfa: true }, [
        ...statements,
        everything,
      ]).decision,
    ).toBe('allowed')
    expect(
      evaluate({ action: 'iam:TagUser', resource: self, mfa: true }, [everything]).decision,
    ).toBe('allowed')
  })
})

// mutants weakens the denies of the guard and of require-mfa one way at a time.
function mutants(): [string, Statement[]][] {
  const out: [string, Statement[]][] = []
  const base = structuredClone(statements)
  const mutate = (name: string, at: number, change: (s: Statement) => void) => {
    const copy = structuredClone(base)
    change(copy[at] as Statement)
    out.push([name, copy])
  }
  out.push(['the whole guard deleted', statements.filter((s) => !policies.guard.includes(s))])
  base.forEach((s, i) => {
    if (s.Effect !== 'Deny') return
    out.push([`${s.Sid} deleted`, base.filter((_, j) => j !== i)])
    mutate(`${s.Sid} made an Allow`, i, (m) => {
      m.Effect = 'Allow'
    })
    for (const field of ['Action', 'Resource'] as const) {
      list(s[field]).forEach((v, k) => {
        mutate(`${s.Sid} without ${field} ${v}`, i, (m) => {
          m[field] = list(m[field]).filter((_, j) => j !== k)
        })
      })
    }
    if (s.NotAction) {
      const added = ['*', ...heldCases.filter((c) => c.sid === s.Sid).map((c) => c.action)]
      for (const a of new Set(added)) {
        mutate(`${s.Sid} with NotAction ${a}`, i, (m) => {
          m.NotAction = [...list(m.NotAction), a]
        })
      }
    }
    if (s.NotResource) {
      for (const r of ['*', `arn:aws:iam::${account}:user/*`, `arn:aws:iam::${account}:*`]) {
        mutate(`${s.Sid} with NotResource ${r}`, i, (m) => {
          m.NotResource = [...list(m.NotResource), r]
        })
      }
    }
    for (const [op, keys] of Object.entries(s.Condition ?? {})) {
      for (const [key, want] of Object.entries(keys)) {
        const loosened: Record<string, [string, string]> = {
          BoolIfExists: ['Bool', want],
          Bool: ['Bool', want === 'false' ? 'true' : 'false'],
          StringNotEquals: ['StringEquals', want],
          StringEquals: ['StringNotEquals', want],
        }
        const [to, value] = loosened[op] ?? []
        if (!to) throw new Error(`no mutation for ${op}`)
        mutate(`${s.Sid}: ${op} ${key} as ${to} ${value}`, i, (m) => {
          const cond = m.Condition as Record<string, Record<string, string>>
          const rest = { ...cond[op] }
          delete rest[key]
          if (Object.keys(rest).length) cond[op] = rest
          else delete cond[op]
          cond[to as string] = { ...(cond[to as string] ?? {}), [key]: value as string }
        })
        if (op === 'BoolIfExists') {
          mutate(`${s.Sid}: ${op} ${key} flipped`, i, (m) => {
            const cond = m.Condition as Record<string, Record<string, string>>
            cond[op] = { ...cond[op], [key]: want === 'false' ? 'true' : 'false' }
          })
        }
      }
    }
  })
  return out
}

describe('weakening any deny that must hold fails the test above', () => {
  const all = mutants()
  test('there are mutants of every deny statement', () => {
    const sids = new Set(statements.filter((s) => s.Effect === 'Deny').map((s) => s.Sid))
    for (const sid of sids) expect(all.some(([name]) => name.startsWith(sid))).toBe(true)
    expect(all.length).toBeGreaterThan(80)
  })
  test('each mutant is caught', () => {
    const survivors = all.filter(([, s]) => violations(s).length === 0).map(([name]) => name)
    expect(survivors).toEqual([])
  })
})

// IAM counts a policy's size without whitespace: 6,144 characters for a
// managed policy, 5,120 for all of a group's inline policies together.
describe('IAM size limits', () => {
  const size = (name: string) => renderText(name).replace(/\s/g, '').length
  const tf = readFileSync(join(dir, 'operators.tf'), 'utf8')
  const resources = [
    ...tf.matchAll(/^resource "(aws_iam_[a-z_]+)" "([a-z_]+)" \{\n([\s\S]*?)^\}/gm),
  ].map(([, type, name, body]) => ({
    type,
    name,
    template: /operators\/([a-z-]+)\.json\.tftpl/.exec(body as string)?.[1],
  }))

  test('every template is installed once, as a managed policy or a group inline policy', () => {
    const templates = readdirSync(join(dir, 'operators'))
      .filter((f) => f.endsWith('.json.tftpl'))
      .map((f) => f.replace('.json.tftpl', ''))
      .sort()
    const installed = resources.filter((r) => r.template).map((r) => r.template)
    expect([...installed].sort()).toEqual(templates)
    for (const r of resources.filter((x) => x.template)) {
      expect([r.name, r.type]).toEqual([
        r.name,
        expect.stringMatching(/^aws_iam_(policy|group_policy)$/),
      ])
    }
  })
  test('each managed policy is within 6,144 characters', () => {
    for (const r of resources.filter((x) => x.type === 'aws_iam_policy' && x.template)) {
      const n = size(r.template as string)
      expect([r.template, n <= 6144, n]).toEqual([r.template, true, n])
    }
  })
  test("the group's inline policies are within 5,120 characters together", () => {
    const inline = resources.filter((x) => x.type === 'aws_iam_group_policy' && x.template)
    const total = inline.reduce((t, r) => t + size(r.template as string), 0)
    expect([total <= 5120, total]).toEqual([true, total])
  })
})
