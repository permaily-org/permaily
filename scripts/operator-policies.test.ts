import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

// The operators' two policies (deploy/tofu/operators/*.json.tftpl, ADR 0036)
// rendered as OpenTofu renders them and evaluated as IAM evaluates identity
// policies in one account, for the role session an IAM Identity Center
// permission set gives: an explicit Deny wins, then an Allow, otherwise an
// implicit deny. Only the condition operators the policies use are
// implemented; an unknown one fails the test rather than being ignored.
//
// Three kinds of test: what the two policies allow by themselves (least
// privilege); that each deny of the guard still denies, by the statement meant
// to, next to a policy that allows everything (what else the permission set
// may carry); and that weakening any of those denies (a statement removed or
// made an Allow, an action or a resource dropped, the whole guard deleted)
// fails the second kind. Plus that they fit a role session, and IAM's size
// limit on the rendered documents.

const dir = join(import.meta.dir, '../deploy/tofu')
const account = '111122223333'
const region = 'us-east-2'
// Identifiers as long as the real ones, for the size limit: a KMS key id is a
// UUID, an instance id 17 hexadecimal digits, the buckets carry the account id.
const vars: Record<string, string> = {
  account_id: account,
  region,
  state_key_arn: `arn:aws:kms:${region}:${account}:key/0123abcd-45ef-6789-abcd-ef0123456789`,
  artifacts_arn: `arn:aws:s3:::permaily-artifacts-${account}`,
  anchors_arn: `arn:aws:s3:::permaily-anchors-${account}`,
  anchors_staging_arn: `arn:aws:s3:::permaily-anchors-staging-${account}`,
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

const templateText = (name: string) =>
  readFileSync(join(dir, 'operators', `${name}.json.tftpl`), 'utf8')

// renderText does what templatefile does with these templates: ${name} is a
// variable, $${ is a literal ${. jsonencode(jsondecode(...)) then makes it
// compact, as JSON.stringify(JSON.parse(...)) does.
function renderText(name: string) {
  const out = templateText(name).replace(/\$\$\{|\$\{([a-z_]+)\}/g, (m, v: string | undefined) => {
    if (m === '$${') return '${'
    const value = vars[v as string]
    if (value === undefined) throw new Error(`no variable ${v} for ${name}`)
    return value
  })
  return JSON.stringify(JSON.parse(out))
}
const render = (name: string): Statement[] => JSON.parse(renderText(name)).Statement

const policies = {
  operate: render('operate'),
  guard: render('guard'),
}
const statements = Object.values(policies).flat()

// The role session of the permission set: aws:userid is the role's id and the
// session name, which IAM Identity Center sets to the user's sign-in name.
const userid = 'AROA0123456789EXAMPLE:operator-2'
const sessionRole = `arn:aws:iam::${account}:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_permaily-operator_0123456789abcdef`

type Request = {
  action: string
  resource: string
  // sessionId is the resource tag aws:ssmmessages:session-id of an SSM session.
  sessionId?: string
}

const guardPolicy = `arn:aws:iam::${account}:policy/permaily-operators-guard`
const user = `arn:aws:iam::${account}:user/permaily-operator-9`
const hostRole = `arn:aws:iam::${account}:role/permaily-enclave-host`
const releaseKey = `arn:aws:kms:${region}:${account}:key/fedcba98-7654-3210-fedc-ba9876543210`
const stateBucket = `arn:aws:s3:::permaily-tofu-state-${account}`
const stateObject = `${stateBucket}/permaily/terraform.tfstate`
const anchors = vars.anchors_arn as string
const anchorsStaging = vars.anchors_staging_arn as string
const table = `arn:aws:dynamodb:${region}:${account}:table/permaily-mail`
const session = (id: string) => `arn:aws:ssm:${region}:${account}:session/${id}`

const list = (v: string | string[] | undefined) =>
  v === undefined ? [] : Array.isArray(v) ? v : [v]

function glob(pattern: string, value: string, ignoreCase: boolean) {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*/g, '.*')
    .replace(/\?/g, '.')
  return new RegExp(`^${re}$`, ignoreCase ? 'i' : '').test(value)
}

// A role session has aws:userid and no aws:username; a policy variable that is
// not in the context makes IAM skip the statement, as here.
function substitute(pattern: string) {
  if (/\$\{(?!aws:userid\})/.test(pattern)) return undefined
  // ${aws:userid}, the policy variable, written so it is not a template string.
  return pattern.replaceAll(`\${aws:userid}`, userid)
}

function conditionHolds(op: string, key: string, want: string, req: Request) {
  const context: Record<string, string | undefined> = {
    'aws:userid': userid,
    'ssm:resourceTag/aws:ssmmessages:session-id': req.sessionId,
  }
  const got = context[key]
  const pattern = substitute(want)
  switch (op) {
    case 'StringLike':
      return got !== undefined && pattern !== undefined && glob(pattern, got, false)
    default:
      throw new Error(`condition operator ${op} is not implemented here`)
  }
}

function applies(s: Statement, req: Request) {
  const action = s.Action
    ? list(s.Action).some((a) => glob(a, req.action, true))
    : !list(s.NotAction).some((a) => glob(a, req.action, true))
  if (!action) return false
  const matches = (r: string) => {
    const p = substitute(r)
    return p !== undefined && glob(p, req.resource, false)
  }
  const resource = s.Resource ? list(s.Resource).some(matches) : !list(s.NotResource).some(matches)
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

// Another policy that allows everything: what IAM would add to the two if the
// permission set ever carried anything else (an AWS managed policy, an inline
// policy, another customer-managed one).
const everything: Statement = {
  Sid: 'AnotherPolicy',
  Effect: 'Allow',
  Action: '*',
  Resource: '*',
}

// Each deny of the guard, with requests it must deny. Written out by hand,
// never derived from the policies, so that a weakened policy cannot weaken its
// own test.
type Case = { sid: string; action: string; resource: string }
const held = (sid: string, requests: [action: string, resource: string][]): Case[] =>
  requests.map(([action, resource]) => ({ sid, action, resource }))
const onEach = (actions: string[], resources: string[]): [string, string][] =>
  actions.flatMap((a) => resources.map((r): [string, string] => [a, r]))

const heldCases: Case[] = [
  ...held(
    'NoKeyChanges',
    onEach(
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
        'kms:RotateKeyOnDemand',
        'kms:ImportKeyMaterial',
        'kms:DeleteImportedKeyMaterial',
        'kms:TagResource',
        'kms:UntagResource',
      ],
      [releaseKey],
    ),
  ),
  ...held('NoKeyChanges', [['kms:PutKeyPolicy', vars.state_key_arn as string]]),
  // Nothing in IAM: not the session's own role (its policies, its trust, its
  // boundary), not a user or a key of one, not the guard itself.
  ...held('NoIAMChanges', [
    ['iam:AddUserToGroup', `arn:aws:iam::${account}:group/any`],
    ['iam:AttachRolePolicy', sessionRole],
    ['iam:ChangePassword', user],
    ['iam:CreateUser', user],
    ['iam:CreateRole', `arn:aws:iam::${account}:role/any`],
    ['iam:CreateAccessKey', user],
    ['iam:CreateLoginProfile', user],
    ['iam:CreatePolicyVersion', guardPolicy],
    ['iam:CreateOpenIDConnectProvider', `arn:aws:iam::${account}:oidc-provider/example.com`],
    ['iam:DeactivateMFADevice', user],
    ['iam:DeletePolicy', guardPolicy],
    ['iam:DeleteRolePermissionsBoundary', sessionRole],
    ['iam:DetachRolePolicy', sessionRole],
    ['iam:DisableOrganizationsRootSessions', '*'],
    ['iam:EnableMFADevice', user],
    ['iam:PassRole', hostRole],
    ['iam:PutRolePolicy', sessionRole],
    ['iam:PutRolePermissionsBoundary', sessionRole],
    ['iam:RemoveUserFromGroup', `arn:aws:iam::${account}:group/any`],
    ['iam:ResetServiceSpecificCredential', user],
    ['iam:ResyncMFADevice', user],
    ['iam:SetDefaultPolicyVersion', guardPolicy],
    ['iam:TagRole', sessionRole],
    ['iam:UntagRole', sessionRole],
    ['iam:UpdateAssumeRolePolicy', sessionRole],
    ['iam:UpdateAssumeRolePolicy', hostRole],
    ['iam:UploadSSHPublicKey', user],
  ]),
  ...held('NoStateWrites', [
    ['s3:PutObject', stateObject],
    ['s3:PutObject', `${stateBucket}/another/key`],
    ['s3:DeleteObject', stateObject],
    ['s3:DeleteObjectVersion', stateObject],
    ['s3:DeleteBucket', stateBucket],
    ['s3:DeleteBucketPolicy', stateBucket],
    ['s3:PutBucketPolicy', stateBucket],
    ['s3:PutBucketVersioning', stateBucket],
    ['s3:PutLifecycleConfiguration', stateBucket],
  ]),
  // Both Object Lock buckets, production's and staging's: not deleted, not
  // reconfigured (no default retention, no lifecycle, no versioning change),
  // no object deleted or hidden behind a delete marker, no lock changed.
  ...held('KeepTheAnchors', [
    ...onEach(
      [
        's3:DeleteBucket',
        's3:DeleteBucketPolicy',
        's3:PutBucketObjectLockConfiguration',
        's3:PutBucketVersioning',
        's3:PutBucketPolicy',
        's3:PutLifecycleConfiguration',
      ],
      [anchors, anchorsStaging],
    ),
    ...onEach(
      [
        's3:DeleteObject',
        's3:DeleteObjectVersion',
        's3:PutObjectRetention',
        's3:PutObjectLegalHold',
        's3:BypassGovernanceRetention',
      ],
      [`${anchors}/commits/x`, `${anchorsStaging}/commits/x`],
    ),
  ]),
  ...held('NoTable', [
    ['dynamodb:GetItem', table],
    ['dynamodb:Query', table],
    ['dynamodb:Scan', table],
    ['dynamodb:PutItem', table],
    ['dynamodb:UpdateItem', table],
    ['dynamodb:DeleteItem', table],
    ['dynamodb:DeleteTable', table],
  ]),
]

// violations lists every held case that the statements, next to a policy that
// allows everything, do not deny explicitly by the statement meant to.
function violations(from: Statement[]) {
  const out: string[] = []
  for (const c of heldCases) {
    const d = evaluate({ action: c.action, resource: c.resource }, [...from, everything])
    if (d.decision !== 'explicit deny' || !d.denies.includes(c.sid)) {
      out.push(`${c.sid}: ${c.action} ${c.resource}`)
    }
  }
  return out
}

describe('what the two policies allow by themselves', () => {
  test('deploying and operating', () => {
    for (const r of [
      { action: 'ssm:SendCommand', resource: vars.instance_arn as string },
      { action: 'ssm:StartSession', resource: vars.instance_arn as string },
      { action: 's3:GetObject', resource: stateObject },
      { action: 'kms:Decrypt', resource: vars.state_key_arn as string },
      { action: 's3:PutObject', resource: `${vars.artifacts_arn}/artifacts/x` },
      {
        action: 'lambda:InvokeFunction',
        resource: `arn:aws:lambda:${region}:${account}:function:permaily-drift`,
      },
      { action: 's3:ListBucketVersions', resource: anchors },
    ]) {
      expect([r.action, decide(r)]).toEqual([r.action, 'allowed'])
    }
  })
  test('ending or resuming only their own SSM sessions', () => {
    for (const action of ['ssm:TerminateSession', 'ssm:ResumeSession']) {
      const own = { action, resource: session(`${userid}-0123456789abcdef0`), sessionId: userid }
      expect([action, decide(own)]).toEqual([action, 'allowed'])
      const other = {
        action,
        resource: session('AROAOTHER:owner-0a'),
        sessionId: 'AROAOTHER:owner',
      }
      expect([action, decide(other)]).toEqual([action, 'denied'])
      expect([action, decide({ action, resource: own.resource })]).toEqual([action, 'denied'])
    }
  })
  test('nothing the guard denies, nor writes elsewhere', () => {
    for (const c of heldCases) {
      expect([c.action, c.resource, decide(c)]).toEqual([c.action, c.resource, 'denied'])
    }
    for (const r of [
      { action: 'ec2:TerminateInstances', resource: vars.instance_arn as string },
      { action: 's3:GetObject', resource: `arn:aws:s3:::permaily-mail-${account}/x` },
      { action: 's3:PutObject', resource: `${anchors}/commits/x` },
      { action: 'secretsmanager:GetSecretValue', resource: '*' },
      { action: 'sts:AssumeRole', resource: hostRole },
    ]) {
      expect([r.action, decide(r)]).toEqual([r.action, 'denied'])
    }
  })
})

describe('they fit a role session of IAM Identity Center', () => {
  // A role session has no aws:username, so a statement that names it never
  // applies; and IAM Identity Center checks MFA at sign-in without setting
  // aws:MultiFactorAuthPresent in the session, so a deny without it would
  // deny everything.
  for (const name of Object.keys(policies)) {
    test(`${name} uses no aws:username and no aws:MultiFactorAuthPresent`, () => {
      expect(templateText(name)).not.toContain('aws:username')
      expect(templateText(name)).not.toContain('MultiFactorAuthPresent')
    })
  }
})

describe('the guard holds next to a policy that allows everything', () => {
  test('every held case is denied explicitly, by the statement meant to', () => {
    expect(violations(statements)).toEqual([])
  })
  test('the allow-everything policy allows what no deny covers (the evaluator is not denying everything)', () => {
    expect(
      evaluate({ action: 'ssm:SendCommand', resource: vars.instance_arn as string }, [
        ...statements,
        everything,
      ]).decision,
    ).toBe('allowed')
    expect(evaluate({ action: 'iam:TagRole', resource: sessionRole }, [everything]).decision).toBe(
      'allowed',
    )
  })
})

// mutants weakens the guard's denies one way at a time.
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
    if (s.NotAction || s.NotResource || s.Condition) {
      throw new Error(`no mutation for ${s.Sid}'s NotAction, NotResource or Condition`)
    }
  })
  return out
}

describe('weakening any deny of the guard fails the test above', () => {
  const all = mutants()
  test('there are mutants of every deny statement, and only the guard denies', () => {
    const sids = new Set(statements.filter((s) => s.Effect === 'Deny').map((s) => s.Sid))
    expect([...sids].sort()).toEqual(policies.guard.map((s) => s.Sid).sort())
    for (const sid of sids) expect(all.some(([name]) => name.startsWith(sid))).toBe(true)
    expect(all.length).toBeGreaterThan(60)
  })
  test('each mutant is caught', () => {
    const survivors = all.filter(([, s]) => violations(s).length === 0).map(([name]) => name)
    expect(survivors).toEqual([])
  })
})

// A permission set references a customer-managed policy by its name and path;
// IAM counts a managed policy's size without whitespace, 6,144 characters.
describe('installed as customer-managed policies a permission set can reference', () => {
  const tf = readFileSync(join(dir, 'operators.tf'), 'utf8')
  const resources = [
    ...tf.matchAll(/^resource "(aws_iam_[a-z_]+)" "([a-z_]+)" \{\n([\s\S]*?)^\}/gm),
  ].map(([, type, name, body]) => ({
    type,
    name,
    policyName: /^\s*name\s*=\s*"([^"]+)"/m.exec(body as string)?.[1],
    path: /^\s*path\s*=\s*"([^"]+)"/m.exec(body as string)?.[1],
    template: /operators\/([a-z-]+)\.json\.tftpl/.exec(body as string)?.[1],
  }))
  const operations = readFileSync(join(dir, '../../docs/operations.md'), 'utf8')

  test('every template is installed once, as a managed policy with a fixed name and path /', () => {
    const templates = readdirSync(join(dir, 'operators'))
      .filter((f) => f.endsWith('.json.tftpl'))
      .map((f) => f.replace('.json.tftpl', ''))
      .sort()
    expect(templates).toEqual(Object.keys(policies).sort())
    const installed = resources.filter((r) => r.template)
    expect(installed.map((r) => r.template).sort()).toEqual(templates)
    for (const r of installed) {
      expect([r.name, r.type, r.path, r.policyName]).toEqual([
        r.name,
        'aws_iam_policy',
        '/',
        `permaily-operators-${r.template}`,
      ])
    }
  })
  test('nothing the service control policy denies, and no IAM user', () => {
    expect(resources.map((r) => r.type).sort()).toEqual(['aws_iam_policy', 'aws_iam_policy'])
  })
  test('the runbook names each policy the permission set references', () => {
    for (const r of resources) expect(operations).toContain(`\`${r.policyName}\``)
  })
  test('each is within 6,144 characters', () => {
    for (const name of Object.keys(policies)) {
      const n = renderText(name).replace(/\s/g, '').length
      expect([name, n <= 6144, n]).toEqual([name, true, n])
    }
  })
})
