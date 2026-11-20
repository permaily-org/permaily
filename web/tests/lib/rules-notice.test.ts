import { describe, expect, it } from 'vitest'
import { RULES_NOTICE_VERSION } from '../../src/lib/rules-notice.ts'

// ADR 0040, "The rules' notice": the version is a date, the server's RulesNoticeVersion, which a Go test compares with
// this one.
describe('the rules’ notice', () => {
  it('has a version that is a date', () => {
    expect(RULES_NOTICE_VERSION).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
