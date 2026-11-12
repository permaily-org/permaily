import { describe, expect, it } from 'vitest'
import { caretAfter, groupedCode } from '../../src/lib/codes.ts'
import { destination } from '../../src/lib/destination.ts'
import { describeDevice } from '../../src/lib/device.ts'
import * as invitation from '../../src/lib/invitation.ts'
import { recoveryCode } from '../../src/lib/recoveryCode.ts'
import { actionClass, buttonClass, cx } from '../../src/lib/styles.ts'
import { applyTheme, readTheme, saveTheme } from '../../src/lib/theme.ts'
import { blockStorage } from '../helpers/app.tsx'

describe('a code in groups of four', () => {
  const code = groupedCode(12)

  it('is shown in capitals and groups, as far as it goes', () => {
    expect(code.shape).toBe('XXXX-XXXX-XXXX')
    expect(code.format('')).toBe('')
    expect(code.format('abc')).toBe('ABC')
    expect(code.format('abcd2')).toBe('ABCD-2')
    expect(code.format('abcd-2345 efgh')).toBe('ABCD-2345-EFGH')
    // What is neither a letter nor a digit is left out, and so is what is too much.
    expect(code.format(' ab_cd!2345efgh6789')).toBe('ABCD-2345-EFGH')
  })

  it('is read with or without dashes and spaces, in any case', () => {
    expect(code.normalize('ABCD-2345-EFGH')).toBe('abcd2345efgh')
    expect(code.normalize('abcd2345efgh')).toBe('abcd2345efgh')
    expect(code.normalize(' AbCd 2345 efgh ')).toBe('abcd2345efgh')
  })

  it('is not a code with another length, or with a character the server never writes', () => {
    expect(code.normalize('')).toBeNull()
    expect(code.normalize('abcd2345efg')).toBeNull()
    expect(code.normalize('abcd2345efgh2')).toBeNull()
    for (const never of ['0', '1', 'i', 'l', 'o', '_', '!', 'é']) {
      expect(code.normalize(`abcd2345efg${never}`)).toBeNull()
    }
  })

  it('knows where the caret belongs once the text is written in groups again', () => {
    expect(code.charactersBefore('AB-CD', 0)).toBe(0)
    expect(code.charactersBefore('AB-CD', 3)).toBe(2)
    expect(code.charactersBefore('AB-CD', 5)).toBe(4)
    expect(caretAfter('ABCD-2345', 0)).toBe(0)
    expect(caretAfter('ABCD-2345', 4)).toBe(4)
    expect(caretAfter('ABCD-2345', 5)).toBe(6)
    expect(caretAfter('ABCD-2345', 99)).toBe(9)
  })
})

describe('an invitation and a recovery code', () => {
  it('have twelve and sixteen characters', () => {
    expect(invitation.formatInvitation('abcd2345efgh6789')).toBe('ABCD-2345-EFGH')
    expect(invitation.normalizeInvitation('ABCD-2345-EFGH')).toBe('abcd2345efgh')
    expect(invitation.normalizeInvitation('ABCD-2345-EFGH-6789')).toBeNull()
    expect(invitation.charactersBefore('ABCD-2', 6)).toBe(5)
    expect(invitation.caretAfter('ABCD-2', 5)).toBe(6)
    expect(recoveryCode.shape).toBe('XXXX-XXXX-XXXX-XXXX')
    expect(recoveryCode.normalize('ABCD-2345-EFGH-6789')).toBe('abcd2345efgh6789')
    expect(recoveryCode.normalize('ABCD-2345-EFGH')).toBeNull()
  })
})

describe('where a person was going', () => {
  it('is a path inside the app, as the history carried it', () => {
    expect(destination({ from: '/rules' })).toBe('/rules')
    expect(destination({ from: '/settings?state=x#sign-in' })).toBe('/settings?state=x#sign-in')
    expect(destination({ from: '/sign-ins' })).toBe('/sign-ins')
  })

  it('is home for anything else', () => {
    for (const state of [
      null,
      undefined,
      'text',
      {},
      { from: 7 },
      { from: '' },
      { from: 'rules' },
      { from: 'https://evil.example/' },
      { from: '//evil.example/' },
      { from: 'javascript:alert(1)' },
      { from: '/sign-in' },
      { from: '/sign-up' },
      { from: '/sign-in#attempt=a&token=b' },
      { from: '/sign-up?invitation=abcd' },
      { from: '/sign-in/' },
    ]) {
      expect(destination(state)).toBe('/')
    }
  })
})

describe('what a browser is called', () => {
  it('is its name and its system, as far as they can be told', () => {
    const cases: Array<[string, string]> = [
      [
        'Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0',
        'Firefox on Linux',
      ],
      [
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15',
        'Safari on Mac',
      ],
      [
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0',
        'Edge on Windows',
      ],
      [
        'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/140.0 Mobile/15E148 Safari/604.1',
        'Chrome on iPhone',
      ],
      [
        'Mozilla/5.0 (Linux; Android 15) SamsungBrowser/28.0 Chrome/130.0 Safari/537.36',
        'Samsung Internet on Android',
      ],
      ['curl/8.0', 'Unknown device'],
      ['Mozilla/5.0 (X11; CrOS x86_64)', 'ChromeOS'],
      ['Something Firefox/1.0', 'Firefox'],
    ]
    for (const [userAgent, name] of cases) expect(describeDevice(userAgent)).toBe(name)
  })
})

describe('the theme', () => {
  it('is the only choice kept in storage, and only as light or dark', () => {
    expect(readTheme()).toBe('system')
    saveTheme('dark')
    expect({ ...localStorage }).toEqual({ 'permaily-theme': 'dark' })
    expect(readTheme()).toBe('dark')
    saveTheme('system')
    expect({ ...localStorage }).toEqual({})
    localStorage.setItem('permaily-theme', 'purple')
    expect(readTheme()).toBe('system')
  })

  it('is set on the page, or left to the system', () => {
    applyTheme('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    applyTheme('system')
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false)
  })

  it('lasts for the visit when storage is unavailable', () => {
    blockStorage('localStorage')
    expect(readTheme()).toBe('system')
    expect(() => saveTheme('dark')).not.toThrow()
    expect(() => saveTheme('system')).not.toThrow()
  })
})

describe('class names', () => {
  it('leave out what is not one, and keep a caller own position', () => {
    expect(cx('a', false, null, undefined, '', 'b')).toBe('a b')
    expect(buttonClass()).toContain('bg-ink')
    expect(buttonClass('danger', 'sm', 'mt-2')).toContain('mt-2')
    expect(actionClass()).toContain('relative')
    expect(actionClass('danger', 'sm', 'absolute top-0')).not.toContain(' relative')
  })
})
