/** Invitations as the server makes them: twelve characters, shown in groups of four (XXXX-XXXX-XXXX). */

import { groupedCode } from './codes.ts'

export { caretAfter } from './codes.ts'

const invitation = groupedCode(12)

/** Writes what a person typed the way an invitation is shown, as far as it goes. */
export const formatInvitation = invitation.format

/** The invitation as the server keeps it, or null when what was typed cannot be one. */
export const normalizeInvitation = invitation.normalize

/** How many letters and digits come before a position in what was typed. */
export const charactersBefore = invitation.charactersBefore
