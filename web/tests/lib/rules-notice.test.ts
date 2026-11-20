import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { RULES_NOTICE_VERSION, rulesNotice, rulesNoticeTitle } from '../../src/lib/rules-notice.ts'

// U32 (ADR 0040, "The rules' notice"): the notice a person agrees to before a model helps with their rules. Its own,
// apart from the model's notice, so a new version of it stops no connection.

/** The server's version of the rules' notice, declared once in the Go package at `dir`; anything else throws. */
function serverNoticeVersion(dir: string): string {
  const source = readdirSync(dir)
    .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .map((f) => readFileSync(join(dir, f), 'utf8'))
    .join('\n')
  const found = [...source.matchAll(/\bRulesNoticeVersion(?:\s+string)?\s*=\s*"([^"]+)"/g)]
  if (found.length !== 1) {
    throw new Error(`RulesNoticeVersion is declared ${found.length} times in ${dir}, not once`)
  }
  return found[0][1]
}

describe("the rules' notice", () => {
  // The app and the server ship in one image, so the notice agreed to is the version the server records: the
  // server's constant (`domain.RulesNoticeVersion`, ADR 0040's pull request 6) must be there, once, and be the same.
  it('is the version the service records', () => {
    expect(serverNoticeVersion(join(import.meta.dirname, '../../../internal/domain'))).toBe(
      RULES_NOTICE_VERSION,
    )
  })

  // The review of pull request 189, finding 4: a constant the test cannot find fails it, never skips the comparison.
  it('finding 4: fails when the server’s constant is missing or said twice', () => {
    const dir = mkdtempSync(join(tmpdir(), 'rules-notice-'))
    try {
      writeFileSync(join(dir, 'other.go'), 'package domain\n\nconst TermsVersion = "2026-01-01"\n')
      expect(() => serverNoticeVersion(dir)).toThrow(/RulesNoticeVersion/)
      writeFileSync(
        join(dir, 'rules.go'),
        'package domain\n\nconst RulesNoticeVersion string = "2026-11-21"\n',
      )
      expect(serverNoticeVersion(dir)).toBe('2026-11-21')
      writeFileSync(
        join(dir, 'more.go'),
        'package domain\n\nconst (\n\tRulesNoticeVersion = "x"\n)\n',
      )
      expect(() => serverNoticeVersion(dir)).toThrow(/RulesNoticeVersion/)
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
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
