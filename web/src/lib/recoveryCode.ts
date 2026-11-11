/** Recovery codes as the server makes them: sixteen characters, shown in groups of four. */

import { groupedCode } from './codes.ts'

export const recoveryCode = groupedCode(16)
