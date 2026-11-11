/** Invitations as the server makes them: twelve characters, shown in groups of four (XXXX-XXXX-XXXX). */

/** The server's alphabet: no 0, 1, i, l or o, which are taken for one another. */
const alphabet = '23456789abcdefghjkmnpqrstuvwxyz'
const length = 12
const group = 4

/** What a person typed, without dashes, spaces or anything else that is not a letter or a digit. */
function bare(typed: string): string {
  return typed.replace(/[^0-9a-z]/gi, '').slice(0, length)
}

/** Writes what a person typed the way an invitation is shown, as far as it goes. */
export function formatInvitation(typed: string): string {
  const groups = bare(typed)
    .toUpperCase()
    .match(new RegExp(`.{1,${group}}`, 'g'))
  return groups ? groups.join('-') : ''
}

/** The invitation as the server keeps it, or null when what was typed cannot be one. */
export function normalizeInvitation(typed: string): string | null {
  const code = typed.replace(/[\s-]/g, '').toLowerCase()
  if (code.length !== length) return null
  for (const c of code) if (!alphabet.includes(c)) return null
  return code
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

/** How many letters and digits come before a position in what was typed. */
export function charactersBefore(typed: string, position: number): number {
  return bare(typed.slice(0, position)).length
}
