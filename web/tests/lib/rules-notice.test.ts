import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { RULES_NOTICE_VERSION, rulesNotice, rulesNoticeTitle } from '../../src/lib/rules-notice.ts'

// U32 (ADR 0040, "The rules' notice"): the notice a person agrees to before a model helps with their rules. Its own,
// apart from the model's notice, so a new version of it stops no connection.

describe("the rules' notice", () => {
  // The app and the server ship in one image, so the notice agreed to is the version the server records. The
  // server's constant is ADR 0040's pull request 1 (`domain.RulesNoticeVersion`); until it is there, nothing compares.
  it('is the version the service records, once the service has one', () => {
    const dir = join(import.meta.dirname, '../../../internal/domain')
    const source = readdirSync(dir)
      .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
      .map((f) => readFileSync(join(dir, f), 'utf8'))
      .join('\n')
    const server = source.match(/\bRulesNoticeVersion\s*=\s*"([^"]+)"/)?.[1]
    if (server !== undefined) expect(server).toBe(RULES_NOTICE_VERSION)
    expect(RULES_NOTICE_VERSION).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })

  // Its words are docs/web.md's, which say what is sent for words, for a rule a model decides and for taking out
  // topics, that the words are kept nowhere, what OpenAI may keep and how long a rule's decisions are kept.
  it('says what docs/web.md says, in its words', () => {
    const doc = readFileSync(join(import.meta.dirname, '../../../docs/web.md'), 'utf8')
    expect(doc).toContain(`### The rules' notice (version ${RULES_NOTICE_VERSION})`)
    expect(doc).toContain(`**${rulesNoticeTitle}**`)
    for (const paragraph of rulesNotice) expect(doc).toContain(`> ${paragraph}`)
    const words = rulesNotice.join(' ')
    for (const said of [
      'OpenAI',
      'those words and nothing else',
      'keeps the words nowhere',
      'a model decides',
      'sentence by sentence',
      'hidden',
      'never sends the sender, the recipients or attachments',
      'up to 30 days',
      'zero data retention',
      '365 days and one hour',
      'never what the text was',
    ])
      expect(words).toContain(said)
  })
})
