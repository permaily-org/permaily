/**
 * Codes as the server makes them, for invitations and for recovery: letters and digits of one alphabet,
 * shown in groups of four (XXXX-XXXX-XXXX). A person may type one with or without dashes, in any case.
 */

/** The server's alphabet: no 0, 1, i, l or o, which are taken for one another. */
const alphabet = '23456789abcdefghjkmnpqrstuvwxyz'
const group = 4

export type GroupedCode = {
  /** How the code looks when it is whole, for a placeholder. */
  shape: string
  /** Writes what a person typed the way the code is shown, as far as it goes. */
  format: (typed: string) => string
  /** The code as the server keeps it, or null when what was typed cannot be one. */
  normalize: (typed: string) => string | null
  /** How many letters and digits come before a position in what was typed. */
  charactersBefore: (typed: string, position: number) => number
}

/** The rules of a code with that many letters and digits. */
export function groupedCode(length: number): GroupedCode {
  /** What a person typed, without dashes, spaces or anything else that is not a letter or a digit. */
  const bare = (typed: string) => typed.replace(/[^0-9a-z]/gi, '').slice(0, length)
  const format = (typed: string) => {
    const groups = bare(typed)
      .toUpperCase()
      .match(new RegExp(`.{1,${group}}`, 'g'))
    return groups ? groups.join('-') : ''
  }
  return {
    shape: format('X'.repeat(length)),
    format,
    normalize: (typed) => {
      const code = typed.replace(/[\s-]/g, '').toLowerCase()
      if (code.length !== length) return null
      for (const c of code) if (!alphabet.includes(c)) return null
      return code
    },
    charactersBefore: (typed, position) => bare(typed.slice(0, position)).length,
  }
}

/** Where the caret belongs in the formatted text, given how many letters and digits were before it. */
export function caretAfter(formatted: string, characters: number): number {
  if (characters <= 0) return 0
  let seen = 0
  for (let i = 0; i < formatted.length; i++) {
    if (formatted[i] !== '-') seen++
    if (seen === characters) return i + 1
  }
  return formatted.length
}
