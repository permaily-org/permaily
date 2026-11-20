/**
 * The person's rules as the server answers them (ADR 0040, internal/contracts/rules.go), with made-up values only. A
 * test changes what it needs.
 */
import { RULES_NOTICE_VERSION } from '../../src/lib/rules-notice.ts'
import { defaultRule, sentence } from './connections.ts'

export const at = '2026-11-20T12:00:00Z'

/** The sentence of a rule's criteria: one formula, said in one line. */
export function ruleSentence(text: string) {
  return { lang: 'en', text, lead: [], formulas: [sentence(text)] }
}

/** A formula of one condition, as the server writes it back. */
export function formulaOf(variable: string, op: string, value?: string) {
  return {
    v: 1,
    all: [{ any: [{ var: variable, op, ...(value === undefined ? {} : { value }) }] }],
  }
}

/** A draft checked: canonical, its identifier and the server's words. */
export function checkOf(
  overrides: Record<string, unknown> = {},
  draft: Record<string, unknown> = {},
) {
  return {
    draft_id: 'rdr_home',
    draft: {
      name: 'Home address',
      criteria: [formulaOf('message', 'mentions', 'a home address')],
      effect: 'remove',
      removal: { shapes: ['phone_number'], topics: ['a home address'] },
      scope: { to: 'all' },
      on: true,
      ...draft,
    },
    sentence: ruleSentence('The message mentions “a home address”'),
    does: 'Takes out phone numbers and sentences about “a home address”, and the sender and attachments',
    for: 'Every connection',
    model: true,
    ...overrides,
  }
}

/** One of the person's rules. */
export function ruleOf(overrides: Record<string, unknown> = {}, check = checkOf()) {
  return {
    id: 'rul_home',
    version: 1,
    check,
    notice: RULES_NOTICE_VERSION,
    created_at: at,
    changed_at: at,
    ...overrides,
  }
}

/** A rule that holds back mail from a bank, for Trip App only. */
export const bankRule = ruleOf(
  { id: 'rul_bank', notice: undefined },
  checkOf(
    {
      draft_id: 'rdr_bank',
      sentence: ruleSentence('From bank.example'),
      does: 'Holds the email back',
      for: 'Only trips.example/client.json',
      clients: [
        {
          client_id: 'https://trips.example/client.json',
          shown: 'trips.example/client.json',
          name: 'Trip App',
        },
      ],
      model: false,
    },
    {
      name: 'Bank',
      criteria: [formulaOf('from_domain', 'is', 'bank.example')],
      effect: 'hold',
      removal: undefined,
      scope: { to: 'only', clients: ['https://trips.example/client.json'] },
    },
  ),
)

/** The account's rules: the default on, the rules given, the notice agreed unless `notice` is null. */
export function ruleListOf(
  rules: unknown[] = [],
  options: { on?: boolean; notice?: string | null; version?: number; max?: number } = {},
) {
  const notice = options.notice === undefined ? RULES_NOTICE_VERSION : options.notice
  return {
    default: { rule: defaultRule, on: options.on ?? true },
    rules,
    ...(notice === null ? {} : { notice: { version: notice, at } }),
    version: options.version ?? 3,
    max: options.max ?? 16,
  }
}
